// Package apkinfo reads metadata from APK files and split bundles
// (.xapk, .apks, .apkm) offline, without aapt or a running device.
// All input is treated as untrusted.
package apkinfo

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/shogo82148/androidbinary"
)

const (
	maxManifest = 16 << 20
	maxTable    = 128 << 20
	maxIcon     = 768 << 10
	maxBundle   = 32 << 30
)

// ErrInvalid marks a file that is not a readable APK or bundle.
var ErrInvalid = errors.New("invalid apk")

var (
	pkgRe  = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(\.[A-Za-z][A-Za-z0-9_]*)+$`)
	fileRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	abiRe  = regexp.MustCompile(`(?:^|[._])(arm64[_-]v8a|armeabi[_-]v7a|armeabi|x86_64|x86)(?:\.apk$|[._])`)
)

// Info describes an application package.
type Info struct {
	Package     string   `json:"package"`
	Label       string   `json:"label"`
	VersionName string   `json:"versionName"`
	MinSDK      int      `json:"minSdk"`
	TargetSDK   int      `json:"targetSdk"`
	Orientation string   `json:"orientation"`
	ABIs        []string `json:"abis"`
	Icon        string   `json:"icon"`
	Size        int64    `json:"size"`
	Bundle      bool     `json:"bundle"`
}

// Prepared is a package ready to install.
type Prepared struct {
	Info Info
	APKs []string
	OBBs []string
	tmp  string
}

// Close removes any temporary files.
func (p *Prepared) Close() {
	if p.tmp != "" {
		os.RemoveAll(p.tmp)
	}
}

// ValidPackage reports whether s is safe to use as an Android package name.
func ValidPackage(s string) bool { return len(s) <= 255 && pkgRe.MatchString(s) }

// FilterSplits removes ABI splits the device cannot run.
func FilterSplits(apks, deviceABIs []string) []string {
	ok := map[string]bool{}
	for _, a := range deviceABIs {
		ok[strings.ReplaceAll(a, "-", "_")] = true
	}
	var out []string
	for _, f := range apks {
		m := abiRe.FindStringSubmatch(strings.ToLower(filepath.Base(f)))
		if m == nil || ok[strings.ReplaceAll(m[1], "-", "_")] {
			out = append(out, f)
		}
	}
	return out
}

// Prepare inspects path and unpacks it when it is a bundle.
func Prepare(file, tmpRoot string) (*Prepared, error) {
	st, err := os.Stat(file)
	if err != nil {
		return nil, err
	}
	zr, err := zip.OpenReader(file)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	defer zr.Close()

	if findEntry(&zr.Reader, "AndroidManifest.xml") != nil {
		info, err := parse(&zr.Reader)
		if err != nil {
			return nil, err
		}
		info.Size = st.Size()
		return &Prepared{Info: *info, APKs: []string{file}}, nil
	}
	return unpackBundle(&zr.Reader, st.Size(), tmpRoot)
}

func unpackBundle(zr *zip.Reader, size int64, tmpRoot string) (*Prepared, error) {
	if err := os.MkdirAll(tmpRoot, 0o755); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(tmpRoot, "bundle-")
	if err != nil {
		return nil, err
	}
	p := &Prepared{tmp: dir}
	fail := func(e error) (*Prepared, error) { p.Close(); return nil, e }

	var total uint64
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		name := path.Base(strings.ReplaceAll(f.Name, `\`, "/"))
		low := strings.ToLower(name)
		isAPK, isOBB := strings.HasSuffix(low, ".apk"), strings.HasSuffix(low, ".obb")
		if (!isAPK && !isOBB) || !fileRe.MatchString(name) {
			continue
		}
		if total += f.UncompressedSize64; total > maxBundle {
			return fail(fmt.Errorf("%w: bundle too large", ErrInvalid))
		}
		dst := filepath.Join(dir, name)
		if _, err := os.Stat(dst); err == nil {
			continue
		}
		if err := extract(f, dst); err != nil {
			return fail(err)
		}
		if isAPK {
			p.APKs = append(p.APKs, dst)
		} else {
			p.OBBs = append(p.OBBs, dst)
		}
	}
	if len(p.APKs) == 0 {
		return fail(fmt.Errorf("%w: no apk in bundle", ErrInvalid))
	}
	sort.Strings(p.APKs)

	info, err := parseBase(p.APKs)
	if err != nil {
		return fail(err)
	}
	info.Size, info.Bundle = size, true
	// ABI libraries live in config splits rather than the base APK.
	seen := map[string]bool{}
	for _, a := range info.ABIs {
		seen[a] = true
	}
	for _, f := range p.APKs {
		if m := abiRe.FindStringSubmatch(strings.ToLower(filepath.Base(f))); m != nil {
			a := strings.ReplaceAll(m[1], "_", "-")
			if a == "x86-64" {
				a = "x86_64"
			}
			if !seen[a] {
				seen[a] = true
				info.ABIs = append(info.ABIs, a)
			}
		}
	}
	sort.Strings(info.ABIs)
	p.Info = *info
	return p, nil
}

func extract(f *zip.File, dst string) error {
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, io.LimitReader(rc, int64(f.UncompressedSize64)))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}

// parseBase finds the split-less APK in a bundle.
func parseBase(apks []string) (*Info, error) {
	ordered := append([]string(nil), apks...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return baseRank(ordered[i]) > baseRank(ordered[j])
	})
	var last error = ErrInvalid
	for _, f := range ordered {
		zr, err := zip.OpenReader(f)
		if err != nil {
			last = err
			continue
		}
		info, split, err := parseWithSplit(&zr.Reader)
		zr.Close()
		if err != nil {
			last = err
			continue
		}
		if split == "" {
			return info, nil
		}
	}
	return nil, fmt.Errorf("%w: no base apk: %v", ErrInvalid, last)
}

func baseRank(f string) int64 {
	n := strings.ToLower(filepath.Base(f))
	var r int64
	if n == "base.apk" {
		r = 1 << 62
	}
	if st, err := os.Stat(f); err == nil {
		r += st.Size()
	}
	return r
}

type manifest struct {
	Package     androidbinary.String `xml:"package,attr"`
	Split       androidbinary.String `xml:"split,attr"`
	VersionName androidbinary.String `xml:"http://schemas.android.com/apk/res/android versionName,attr"`
	SDK         struct {
		Min    androidbinary.String `xml:"http://schemas.android.com/apk/res/android minSdkVersion,attr"`
		Target androidbinary.String `xml:"http://schemas.android.com/apk/res/android targetSdkVersion,attr"`
	} `xml:"uses-sdk"`
	App struct {
		Label      androidbinary.String `xml:"http://schemas.android.com/apk/res/android label,attr"`
		Icon       androidbinary.String `xml:"http://schemas.android.com/apk/res/android icon,attr"`
		Activities []activity           `xml:"activity"`
		Aliases    []activity           `xml:"activity-alias"`
	} `xml:"application"`
}

type activity struct {
	Name        androidbinary.String `xml:"http://schemas.android.com/apk/res/android name,attr"`
	Target      androidbinary.String `xml:"http://schemas.android.com/apk/res/android targetActivity,attr"`
	Orientation androidbinary.String `xml:"http://schemas.android.com/apk/res/android screenOrientation,attr"`
	Filters     []struct {
		Actions []struct {
			Name androidbinary.String `xml:"http://schemas.android.com/apk/res/android name,attr"`
		} `xml:"action"`
		Categories []struct {
			Name androidbinary.String `xml:"http://schemas.android.com/apk/res/android name,attr"`
		} `xml:"category"`
	} `xml:"intent-filter"`
}

func parse(zr *zip.Reader) (*Info, error) {
	info, _, err := parseWithSplit(zr)
	return info, err
}

func parseWithSplit(zr *zip.Reader) (info *Info, split string, err error) {
	// The binary XML decoder can panic on hostile input.
	defer func() {
		if r := recover(); r != nil {
			info, split, err = nil, "", fmt.Errorf("%w: %v", ErrInvalid, r)
		}
	}()

	raw, err := readEntry(findEntry(zr, "AndroidManifest.xml"), maxManifest)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	var table *androidbinary.TableFile
	if e := findEntry(zr, "resources.arsc"); e != nil {
		if b, err := readEntry(e, maxTable); err == nil {
			table, _ = androidbinary.NewTableFile(bytes.NewReader(b))
		}
	}
	xf, err := androidbinary.NewXMLFile(bytes.NewReader(raw))
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	var m manifest
	thai := &androidbinary.ResTableConfig{Language: [2]uint8{'t', 'h'}}
	if err := xf.Decode(&m, table, thai); err != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrInvalid, err)
	}

	pkg, _ := m.Package.String()
	if !ValidPackage(pkg) {
		return nil, "", fmt.Errorf("%w: bad package name", ErrInvalid)
	}
	split, _ = m.Split.String()

	out := &Info{Package: pkg, ABIs: abisOf(zr), Orientation: orientationOf(&m)}
	out.VersionName = text(m.VersionName)
	out.MinSDK = number(m.SDK.Min)
	out.TargetSDK = number(m.SDK.Target)
	if out.Label = text(m.App.Label); out.Label == "" {
		out.Label = pkg
	}
	out.Icon = iconOf(zr, xf, &m, table)
	return out, split, nil
}

func text(v androidbinary.String) string {
	s, err := v.String()
	if err != nil || androidbinary.IsResID(s) {
		return ""
	}
	return strings.TrimSpace(s)
}

func number(v androidbinary.String) int {
	s, err := v.String()
	if err != nil {
		return 0
	}
	n, err := strconv.ParseInt(strings.TrimSpace(s), 0, 32)
	if err != nil {
		return 0
	}
	return int(n)
}

func abisOf(zr *zip.Reader) []string {
	seen := map[string]bool{}
	for _, f := range zr.File {
		p := strings.Split(f.Name, "/")
		if len(p) >= 3 && p[0] == "lib" && strings.HasSuffix(f.Name, ".so") {
			seen[p[1]] = true
		}
	}
	out := make([]string, 0, len(seen))
	for a := range seen {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

// orientationOf derives the natural orientation from the manifest. The launcher
// activity counts more because it is what the user sees first; sensor-driven
// values are ignored because they follow the panel.
func orientationOf(m *manifest) string {
	portrait, landscape := 0, 0
	// Values are android.content.pm.ActivityInfo.SCREEN_ORIENTATION_*.
	tally := func(v, weight int) {
		switch v {
		case 1, 7, 9, 12: // portrait, sensorPortrait, reversePortrait, userPortrait
			portrait += weight
		case 0, 6, 8, 11: // landscape, sensorLandscape, reverseLandscape, userLandscape
			landscape += weight
		}
	}
	targets := map[string]bool{}
	for _, a := range m.App.Aliases {
		if isLauncher(a) {
			if t := text(a.Target); t != "" {
				targets[t] = true
			}
		}
	}
	for _, a := range m.App.Activities {
		s, err := a.Orientation.String()
		if err != nil {
			continue
		}
		v, err := strconv.ParseInt(strings.TrimSpace(s), 0, 32)
		if err != nil {
			continue
		}
		w := 1
		if isLauncher(a) || targets[text(a.Name)] {
			w = 8
		}
		tally(int(v), w)
	}
	switch {
	case portrait > landscape:
		return "portrait"
	case landscape > portrait:
		return "landscape"
	}
	return ""
}

func isLauncher(a activity) bool {
	for _, f := range a.Filters {
		main, launcher := false, false
		for _, x := range f.Actions {
			main = main || text(x.Name) == "android.intent.action.MAIN"
		}
		for _, x := range f.Categories {
			n := text(x.Name)
			launcher = launcher || n == "android.intent.category.LAUNCHER" || n == "android.intent.category.LEANBACK_LAUNCHER"
		}
		if main && launcher {
			return true
		}
	}
	return false
}

func isBitmap(n string) bool {
	switch strings.ToLower(path.Ext(n)) {
	case ".png", ".webp", ".jpg", ".jpeg":
		return true
	}
	return false
}

func iconOf(zr *zip.Reader, _ *androidbinary.XMLFile, m *manifest, table *androidbinary.TableFile) string {
	var ref string
	if table != nil {
		ref, _ = m.App.Icon.WithTableFile(table).WithResTableConfig(&androidbinary.ResTableConfig{Density: 640}).String()
	}
	if ref == "" || androidbinary.IsResID(ref) {
		ref, _ = m.App.Icon.String()
	}
	for depth := 0; depth < 4 && ref != "" && !androidbinary.IsResID(ref); depth++ {
		if isBitmap(ref) {
			if uri := dataURI(findEntry(zr, ref)); uri != "" {
				return uri
			}
			break
		}
		next := adaptiveForeground(zr, ref, table)
		if next == "" || next == ref {
			break
		}
		ref = next
	}
	return guessIcon(zr)
}

// adaptiveForeground follows an <adaptive-icon> or wrapper drawable to a bitmap.
func adaptiveForeground(zr *zip.Reader, file string, table *androidbinary.TableFile) string {
	raw, err := readEntry(findEntry(zr, file), maxManifest)
	if err != nil {
		return ""
	}
	xf, err := androidbinary.NewXMLFile(bytes.NewReader(raw))
	if err != nil {
		return ""
	}
	cfg := &androidbinary.ResTableConfig{Density: 640}
	dec := xml.NewDecoder(xf.Reader())
	const ns = "http://schemas.android.com/apk/res/android"
	var fallback string
	inForeground := false
	for {
		tok, err := dec.Token()
		if err != nil {
			return fallback
		}
		switch el := tok.(type) {
		case xml.StartElement:
			if el.Name.Local == "foreground" {
				inForeground = true
			}
			for _, at := range el.Attr {
				if at.Name.Space != ns || (at.Name.Local != "drawable" && at.Name.Local != "src") {
					continue
				}
				v := resolve(at.Value, table, cfg)
				if v == "" {
					continue
				}
				if inForeground {
					return v
				}
				if fallback == "" {
					fallback = v
				}
			}
		case xml.EndElement:
			if el.Name.Local == "foreground" {
				inForeground = false
			}
		}
	}
}

func resolve(v string, table *androidbinary.TableFile, cfg *androidbinary.ResTableConfig) string {
	if !androidbinary.IsResID(v) {
		return v
	}
	if table == nil {
		return ""
	}
	id, err := androidbinary.ParseResID(v)
	if err != nil {
		return ""
	}
	r, err := table.GetResource(id, cfg)
	if err != nil {
		return ""
	}
	s, _ := r.(string)
	return s
}

func guessIcon(zr *zip.Reader) string {
	type cand struct {
		f     *zip.File
		score float64
	}
	var list []cand
	for _, f := range zr.File {
		n := strings.ToLower(f.Name)
		if !strings.HasPrefix(n, "res/") || !isBitmap(n) || f.UncompressedSize64 > maxIcon {
			continue
		}
		if !strings.Contains(n, "ic_launcher") && !strings.Contains(n, "app_icon") && !strings.Contains(n, "/icon") {
			continue
		}
		s := float64(f.UncompressedSize64)
		for k, w := range map[string]float64{"xxxhdpi": 1.6, "_round": .6, "_background": .1, "_monochrome": .1} {
			if strings.Contains(n, k) {
				s *= w
			}
		}
		list = append(list, cand{f, s})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].score > list[j].score })
	for _, c := range list {
		if uri := dataURI(c.f); uri != "" {
			return uri
		}
	}
	return ""
}

func dataURI(f *zip.File) string {
	if f == nil || f.UncompressedSize64 > maxIcon {
		return ""
	}
	b, err := readEntry(f, maxIcon)
	if err != nil || len(b) < 32 {
		return ""
	}
	var mime string
	switch {
	case bytes.HasPrefix(b, []byte{0x89, 'P', 'N', 'G'}):
		mime = "image/png"
	case len(b) > 12 && string(b[:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		mime = "image/webp"
	case bytes.HasPrefix(b, []byte{0xFF, 0xD8, 0xFF}):
		mime = "image/jpeg"
	default:
		return ""
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(b)
}

func findEntry(zr *zip.Reader, name string) *zip.File {
	for _, f := range zr.File {
		if f.Name == name {
			return f
		}
	}
	return nil
}

func readEntry(f *zip.File, limit int64) ([]byte, error) {
	if f == nil {
		return nil, errors.New("missing entry")
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, errors.New("entry too large")
	}
	return b, nil
}
