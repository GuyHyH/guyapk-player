// Package android locates and drives the Android SDK tools: adb, the emulator,
// virtual device definitions and system images.
package android

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// AVDName is GuyAPK's private virtual device.
	AVDName = "GuyAPK"
	// ConsolePort is the last slot adb scans, which keeps it clear of emulators
	// the user starts from Android Studio (they count up from 5554).
	ConsolePort = 5584
	// Serial is the adb serial for ConsolePort.
	Serial = "emulator-5584"
)

// SDK is an Android SDK root directory.
type SDK struct{ Root string }

func exe(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// ADB returns the adb path.
func (s SDK) ADB() string { return filepath.Join(s.Root, "platform-tools", exe("adb")) }

// Emulator returns the emulator path.
func (s SDK) Emulator() string { return filepath.Join(s.Root, "emulator", exe("emulator")) }

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// HasTools reports whether adb and the emulator are present.
func (s SDK) HasTools() bool { return isFile(s.ADB()) && isFile(s.Emulator()) }

// Ready reports whether the SDK can boot a device on this computer.
func (s SDK) Ready() bool { return s.HasTools() && len(s.Images()) > 0 }

// HostABI is the system image ABI that runs without CPU translation here.
func HostABI() string {
	if runtime.GOARCH == "arm64" {
		return "arm64-v8a"
	}
	return "x86_64"
}

// Image is an installed system image.
type Image struct {
	API  int    `json:"api"`
	Tag  string `json:"tag"`
	ABI  string `json:"abi"`
	Path string `json:"path"` // relative to the SDK root, with a trailing slash
}

var apiDirRe = regexp.MustCompile(`^android-(\d+)`)

func tagRank(t string) int {
	switch t {
	case "google_apis_playstore":
		return 3
	case "google_apis":
		return 2
	case "default":
		return 1
	}
	return 0
}

// Images lists usable images for this CPU, best first.
func (s SDK) Images() []Image {
	root := filepath.Join(s.Root, "system-images")
	apis, _ := os.ReadDir(root)
	var out []Image
	for _, a := range apis {
		m := apiDirRe.FindStringSubmatch(a.Name())
		if m == nil || !a.IsDir() {
			continue
		}
		api, _ := strconv.Atoi(m[1])
		tags, _ := os.ReadDir(filepath.Join(root, a.Name()))
		for _, t := range tags {
			if tagRank(t.Name()) == 0 {
				continue
			}
			abi := HostABI()
			if isFile(filepath.Join(root, a.Name(), t.Name(), abi, "system.img")) {
				out = append(out, Image{api, t.Name(), abi,
					"system-images/" + a.Name() + "/" + t.Name() + "/" + abi + "/"})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].API != out[j].API {
			return out[i].API > out[j].API
		}
		return tagRank(out[i].Tag) > tagRank(out[j].Tag)
	})
	return out
}

// Locate prefers an SDK already on this computer, so nothing is downloaded
// twice. When none is usable it returns the managed location for provisioning.
func Locate(managed string) SDK {
	home, _ := os.UserHomeDir()
	candidates := []string{managed, os.Getenv("ANDROID_HOME"), os.Getenv("ANDROID_SDK_ROOT")}
	switch runtime.GOOS {
	case "windows":
		candidates = append(candidates, filepath.Join(os.Getenv("LOCALAPPDATA"), "Android", "Sdk"))
	case "darwin":
		candidates = append(candidates, filepath.Join(home, "Library", "Android", "sdk"))
	}
	for _, c := range candidates {
		if c != "" && (SDK{c}).Ready() {
			return SDK{c}
		}
	}
	return SDK{managed}
}

// run executes an SDK tool with a timeout and returns trimmed combined output.
func run(ctx context.Context, timeout time.Duration, bin string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	hideConsole(cmd)
	b, err := cmd.CombinedOutput()
	out := strings.TrimSpace(string(b))
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return out, fmt.Errorf("timeout: %s %s", filepath.Base(bin), first(args))
		}
		return out, fmt.Errorf("%w: %s", err, tail(out, 400))
	}
	return out, nil
}

func first(a []string) string {
	if len(a) > 0 {
		return a[0]
	}
	return ""
}

func tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

var accelRe = regexp.MustCompile(`(WHPX|HVF|AEHD|GVM|HAXM|KVM|Hypervisor\.Framework)`)

// CheckAccel reports the hardware accelerator, or "" when none is usable.
func (s SDK) CheckAccel(ctx context.Context) string {
	if !isFile(s.Emulator()) {
		return ""
	}
	out, _ := run(ctx, 25*time.Second, s.Emulator(), "-accel-check")
	if !strings.Contains(strings.ToLower(out), "usable") {
		return ""
	}
	// The tool can print a stale Hyper-V warning ahead of the working backend.
	for _, line := range strings.Split(out, "\n") {
		if i := strings.Index(line, "usable"); i >= 0 {
			if m := accelRe.FindAllString(line[:i], -1); len(m) > 0 {
				return m[len(m)-1]
			}
		}
	}
	if runtime.GOOS == "darwin" {
		return "HVF"
	}
	return "HW"
}
