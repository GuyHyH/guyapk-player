package android

import (
	"archive/zip"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	repoBase   = "https://dl.google.com/android/repository/"
	repoIndex  = repoBase + "repository2-3.xml"
	imageBase  = repoBase + "sys-img/google_apis/"
	imageIndex = imageBase + "sys-img2-3.xml"
	// DefaultAPI is the Android version installed when none is present.
	DefaultAPI = 34
)

// Package is one downloadable SDK component.
type Package struct {
	Path string `json:"path"`
	URL  string `json:"url"`
	SHA1 string `json:"-"`
	Size int64  `json:"size"`
}

// Plan is what must be downloaded, and the licence that covers it.
type Plan struct {
	Packages []Package `json:"packages"`
	Total    int64     `json:"total"`
	License  string    `json:"license"`
}

type repoXML struct {
	Channels []struct {
		ID   string `xml:"id,attr"`
		Name string `xml:",chardata"`
	} `xml:"channel"`
	Licenses []struct {
		ID   string `xml:"id,attr"`
		Text string `xml:",chardata"`
	} `xml:"license"`
	Packages []struct {
		Path    string `xml:"path,attr"`
		Channel struct {
			Ref string `xml:"ref,attr"`
		} `xml:"channelRef"`
		License struct {
			Ref string `xml:"ref,attr"`
		} `xml:"uses-license"`
		Archives []struct {
			Size     int64  `xml:"complete>size"`
			Checksum string `xml:"complete>checksum"`
			URL      string `xml:"complete>url"`
			OS       string `xml:"host-os"`
			Arch     string `xml:"host-arch"`
		} `xml:"archives>archive"`
	} `xml:"remotePackage"`
}

func hostNames() (string, string) {
	o := map[string]string{"windows": "windows", "darwin": "macosx"}[runtime.GOOS]
	a := map[string]string{"amd64": "x64", "arm64": "aarch64"}[runtime.GOARCH]
	return o, a
}

func fetchRepo(ctx context.Context, url string) (*repoXML, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("repository: HTTP %d", res.StatusCode)
	}
	var r repoXML
	if err := xml.NewDecoder(io.LimitReader(res.Body, 16<<20)).Decode(&r); err != nil {
		return nil, err
	}
	return &r, nil
}

var httpClient = &http.Client{Transport: &http.Transport{
	Proxy:                 http.ProxyFromEnvironment,
	TLSHandshakeTimeout:   20 * time.Second,
	ResponseHeaderTimeout: 45 * time.Second,
}}

// pick selects the stable archive of a package for this host.
func pick(r *repoXML, pkgPath, base string) (Package, string, bool) {
	stable := "channel-0"
	for _, c := range r.Channels {
		if strings.TrimSpace(c.Name) == "stable" {
			stable = c.ID
		}
	}
	hostOS, hostArch := hostNames()
	for _, p := range r.Packages {
		if p.Path != pkgPath || p.Channel.Ref != stable {
			continue
		}
		for _, a := range p.Archives {
			if (a.OS == "" || a.OS == hostOS) && (a.Arch == "" || a.Arch == hostArch) {
				return Package{pkgPath, base + a.URL, a.Checksum, a.Size}, p.License.Ref, true
			}
		}
	}
	return Package{}, "", false
}

// BuildPlan works out which components this SDK root still needs.
func BuildPlan(ctx context.Context, s SDK) (*Plan, error) {
	repo, err := fetchRepo(ctx, repoIndex)
	if err != nil {
		return nil, err
	}
	plan := &Plan{}
	licenses := map[string]bool{}
	add := func(r *repoXML, path, base string) error {
		p, lic, ok := pick(r, path, base)
		if !ok {
			return fmt.Errorf("repository: %s not available for this computer", path)
		}
		plan.Packages = append(plan.Packages, p)
		plan.Total += p.Size
		licenses[lic] = true
		return nil
	}
	if !isFile(s.ADB()) {
		if err := add(repo, "platform-tools", repoBase); err != nil {
			return nil, err
		}
	}
	if !isFile(s.Emulator()) {
		if err := add(repo, "emulator", repoBase); err != nil {
			return nil, err
		}
	}
	var imgRepo *repoXML
	if len(s.Images()) == 0 {
		if imgRepo, err = fetchRepo(ctx, imageIndex); err != nil {
			return nil, err
		}
		path := fmt.Sprintf("system-images;android-%d;google_apis;%s", DefaultAPI, HostABI())
		if err := add(imgRepo, path, imageBase); err != nil {
			return nil, err
		}
	}
	var text []string
	for _, r := range []*repoXML{repo, imgRepo} {
		if r == nil {
			continue
		}
		for _, l := range r.Licenses {
			if licenses[l.ID] {
				text = append(text, strings.TrimSpace(l.Text))
				delete(licenses, l.ID)
			}
		}
	}
	plan.License = strings.Join(text, "\n\n----------\n\n")
	return plan, nil
}

// Progress reports provisioning state.
type Progress struct {
	Stage string `json:"stage"` // download, verify, extract, done
	Path  string `json:"path"`
	Done  int64  `json:"done"`
	Total int64  `json:"total"`
}

// Provision downloads, verifies and unpacks a plan. Interrupted downloads resume.
func Provision(ctx context.Context, s SDK, plan *Plan, report func(Progress)) error {
	if err := os.MkdirAll(filepath.Join(s.Root, ".cache"), 0o755); err != nil {
		return err
	}
	// The emulator refuses an SDK root without a platforms directory.
	if err := os.MkdirAll(filepath.Join(s.Root, "platforms"), 0o755); err != nil {
		return err
	}
	if free, err := FreeDisk(s.Root); err == nil && free < uint64(plan.Total)*3 {
		return fmt.Errorf("disk: need %d free, have %d", uint64(plan.Total)*3, free)
	}
	var finished int64
	for _, p := range plan.Packages {
		base := finished
		tick := func(n int64) {
			report(Progress{"download", p.Path, base + n, plan.Total})
		}
		file := filepath.Join(s.Root, ".cache", p.SHA1+".zip")
		if err := download(ctx, p, file, tick); err != nil {
			return fmt.Errorf("download %s: %w", p.Path, err)
		}
		report(Progress{"extract", p.Path, base + p.Size, plan.Total})
		dst := filepath.Join(s.Root, filepath.FromSlash(strings.ReplaceAll(p.Path, ";", "/")))
		if err := unpack(ctx, file, dst); err != nil {
			return fmt.Errorf("extract %s: %w", p.Path, err)
		}
		os.Remove(file)
		finished += p.Size
	}
	report(Progress{"done", "", plan.Total, plan.Total})
	return nil
}

func download(ctx context.Context, p Package, file string, tick func(int64)) error {
	if ok, _ := verify(file, p.SHA1); ok {
		tick(p.Size)
		return nil
	}
	part := file + ".part"
	var have int64
	if st, err := os.Stat(part); err == nil && st.Size() <= p.Size {
		have = st.Size()
	}
	if have < p.Size {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.URL, nil)
		if err != nil {
			return err
		}
		if have > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", have))
		}
		res, err := httpClient.Do(req)
		if err != nil {
			return err
		}
		defer res.Body.Close()
		flags := os.O_CREATE | os.O_WRONLY
		switch res.StatusCode {
		case http.StatusPartialContent:
			flags |= os.O_APPEND
		case http.StatusOK:
			have = 0
			flags |= os.O_TRUNC
		default:
			return fmt.Errorf("HTTP %d", res.StatusCode)
		}
		out, err := os.OpenFile(part, flags, 0o644)
		if err != nil {
			return err
		}
		err = copyProgress(out, res.Body, have, tick)
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
	ok, err := verify(part, p.SHA1)
	if err != nil {
		return err
	}
	if !ok {
		os.Remove(part)
		return errors.New("checksum mismatch")
	}
	return os.Rename(part, file)
}

func copyProgress(w io.Writer, r io.Reader, n int64, tick func(int64)) error {
	buf := make([]byte, 1<<20)
	last := time.Now()
	for {
		c, err := r.Read(buf)
		if c > 0 {
			if _, werr := w.Write(buf[:c]); werr != nil {
				return werr
			}
			n += int64(c)
			if time.Since(last) > 200*time.Millisecond {
				tick(n)
				last = time.Now()
			}
		}
		if err == io.EOF {
			tick(n)
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func verify(file, want string) (bool, error) {
	f, err := os.Open(file)
	if err != nil {
		return false, err
	}
	defer f.Close()
	h := sha1.New()
	if _, err := io.Copy(h, f); err != nil {
		return false, err
	}
	return strings.EqualFold(hex.EncodeToString(h.Sum(nil)), want), nil
}

// unpack extracts an SDK archive, dropping its single top-level directory, into
// a staging directory that is swapped in only once complete.
func unpack(ctx context.Context, archive, dst string) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer zr.Close()
	staging := dst + ".staging"
	os.RemoveAll(staging)
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return err
	}
	root := filepath.Clean(staging) + string(os.PathSeparator)
	for _, f := range zr.File {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := strings.ReplaceAll(f.Name, `\`, "/")
		i := strings.IndexByte(name, '/')
		if i < 0 || i == len(name)-1 {
			continue
		}
		target := filepath.Join(staging, filepath.FromSlash(name[i+1:]))
		if !strings.HasPrefix(target, root) {
			return fmt.Errorf("unsafe path in archive: %s", f.Name)
		}
		if err := writeEntry(f, target, root); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	return os.Rename(staging, dst)
}

func writeEntry(f *zip.File, target, root string) error {
	mode := f.Mode()
	if mode.IsDir() {
		return os.MkdirAll(target, 0o755)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	if mode&os.ModeSymlink != 0 {
		// macOS emulator archives contain library symlinks.
		b, err := io.ReadAll(io.LimitReader(rc, 4096))
		if err != nil {
			return err
		}
		link := string(b)
		resolved := filepath.Clean(filepath.Join(filepath.Dir(target), link))
		if filepath.IsAbs(link) || !strings.HasPrefix(resolved+string(os.PathSeparator), root) {
			return fmt.Errorf("unsafe link in archive: %s", f.Name)
		}
		return os.Symlink(link, target)
	}
	perm := os.FileMode(0o644)
	if mode&0o111 != 0 {
		perm = 0o755
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, rc)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}
