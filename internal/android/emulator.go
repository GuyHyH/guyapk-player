package android

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Spec is everything that defines a virtual device boot.
type Spec struct {
	Profile   Profile
	Landscape bool
	Tuning    Tuning
	Image     Image
}

// Fingerprint changes when a setting needs a restart. Orientation is excluded
// because it can change live.
func (s Spec) Fingerprint() string {
	return fmt.Sprintf("%dx%d@%d/%d/%d/%s", s.Profile.W, s.Profile.H, s.Profile.DPI,
		s.Tuning.Cores, s.Tuning.RAMMB, s.Image.Path)
}

// Manager owns the emulator process.
type Manager struct {
	SDK     SDK
	AVDHome string
	LogPath string

	mu     sync.Mutex
	cmd    *exec.Cmd
	exited chan struct{}
}

// Device returns the handle for GuyAPK's instance.
func (m *Manager) Device() Device { return Device{m.SDK, Serial} }

// WriteAVD writes the device definition. User data in the AVD is preserved, so
// installed apps follow the user across screen sizes.
func (m *Manager) WriteAVD(s Spec) error {
	dir := filepath.Join(m.AVDHome, AVDName+".avd")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	pointer := fmt.Sprintf("avd.ini.encoding=UTF-8\npath=%s\npath.rel=avd/%s.avd\ntarget=android-%d\n",
		dir, AVDName, s.Image.API)
	if err := writeAtomic(filepath.Join(m.AVDHome, AVDName+".ini"), pointer); err != nil {
		return err
	}
	arch := "x86_64"
	if s.Image.ABI == "arm64-v8a" {
		arch = "arm64"
	}
	yn := func(b bool) string {
		if b {
			return "yes"
		}
		return "no"
	}
	orient := "portrait"
	w, h := s.Profile.W, s.Profile.H
	if s.Landscape {
		orient = "landscape"
	}
	lines := []string{
		"PlayStore.enabled=" + yn(s.Image.Tag == "google_apis_playstore"),
		"abi.type=" + s.Image.ABI,
		"avd.ini.encoding=UTF-8",
		"disk.dataPartition.size=16G",
		"fastboot.chosenSnapshotFile=",
		"fastboot.forceChosenSnapshotBoot=no",
		"fastboot.forceColdBoot=no",
		"fastboot.forceFastBoot=yes",
		"hw.accelerometer=yes",
		"hw.audioInput=yes",
		"hw.audioOutput=yes",
		"hw.battery=yes",
		"hw.camera.back=none",
		"hw.camera.front=none",
		"hw.cpu.arch=" + arch,
		"hw.cpu.ncore=" + strconv.Itoa(s.Tuning.Cores),
		"hw.dPad=no",
		"hw.device.manufacturer=GuyAPK",
		"hw.device.name=guyapk",
		"hw.gps=yes",
		"hw.gpu.enabled=yes",
		"hw.gpu.mode=host",
		"hw.gyroscope=yes",
		"hw.initialOrientation=" + orient,
		"hw.keyboard=yes",
		"hw.lcd.density=" + strconv.Itoa(NearestDensity(s.Profile.DPI)),
		"hw.lcd.height=" + strconv.Itoa(h),
		"hw.lcd.width=" + strconv.Itoa(w),
		"hw.mainKeys=no",
		"hw.ramSize=" + strconv.Itoa(s.Tuning.RAMMB),
		"hw.sdCard=no",
		"hw.sensors.orientation=yes",
		"hw.sensors.proximity=yes",
		"hw.trackBall=no",
		"image.sysdir.1=" + s.Image.Path,
		"runtime.network.latency=none",
		"runtime.network.speed=full",
		"showDeviceFrame=no",
		"skin.dynamic=yes",
		fmt.Sprintf("skin.name=%dx%d", w, h),
		"skin.path=_no_skin",
		"tag.display=" + s.Image.Tag,
		"tag.id=" + s.Image.Tag,
		"vm.heapSize=" + strconv.Itoa(s.Tuning.HeapMB),
	}
	return writeAtomic(filepath.Join(dir, "config.ini"), strings.Join(lines, "\n")+"\n")
}

// bootDensities are the only LCD densities the emulator accepts at boot.
var bootDensities = []int{120, 140, 160, 180, 213, 240, 280, 320, 340, 360, 400, 420, 440, 480, 560, 640}

// NearestDensity maps any DPI to a bootable one. The exact value is applied
// after boot with wm density.
func NearestDensity(dpi int) int {
	best := bootDensities[0]
	for _, d := range bootDensities {
		if abs(d-dpi) < abs(best-dpi) {
			best = d
		}
	}
	return best
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func writeAtomic(path, content string) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (m *Manager) spawn(gpu string) (chan struct{}, error) {
	args := []string{"-avd", AVDName, "-port", strconv.Itoa(ConsolePort),
		"-gpu", gpu, "-no-boot-anim", "-netfast"}
	cmd := exec.Command(m.SDK.Emulator(), args...)
	cmd.Dir = filepath.Dir(m.SDK.Emulator())
	cmd.Env = append(os.Environ(),
		"ANDROID_SDK_ROOT="+m.SDK.Root,
		"ANDROID_HOME="+m.SDK.Root,
		"ANDROID_AVD_HOME="+m.AVDHome,
	)
	hideConsole(cmd)
	if logf, err := os.Create(m.LogPath); err == nil {
		cmd.Stdout, cmd.Stderr = logf, logf
		defer logf.Close()
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	done := make(chan struct{})
	m.mu.Lock()
	m.cmd, m.exited = cmd, done
	m.mu.Unlock()
	go func() {
		cmd.Wait()
		close(done)
	}()
	return done, nil
}

// Boot starts the device and waits until Android is usable. If the host GPU
// path crashes early it retries once with the emulator's automatic renderer.
func (m *Manager) Boot(ctx context.Context, s Spec, step func(string)) error {
	if err := m.WriteAVD(s); err != nil {
		return fmt.Errorf("avd: %w", err)
	}
	run(ctx, 15*time.Second, m.SDK.ADB(), "start-server")

	var err error
	for _, gpu := range []string{"host", "auto"} {
		step("launch")
		var done chan struct{}
		if done, err = m.spawn(gpu); err != nil {
			return fmt.Errorf("boot: %w", err)
		}
		err = m.waitBoot(ctx, done, step)
		if err == nil || !errors.Is(err, errExitedEarly) {
			break
		}
	}
	if err != nil {
		return err
	}
	step("tune")
	m.tune(ctx, s)
	return nil
}

var errExitedEarly = errors.New("boot:exited")

func (m *Manager) waitBoot(ctx context.Context, done chan struct{}, step func(string)) error {
	d := m.Device()
	start := time.Now()
	deadline := start.Add(6 * time.Minute)
	online := false
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
			// The launcher can exit after handing off to the VM process.
			if !d.Online(ctx) && time.Since(start) < 90*time.Second {
				return fmt.Errorf("%w: %s", errExitedEarly, m.logTail())
			}
		case <-time.After(700 * time.Millisecond):
		}
		if !online {
			if online = d.Online(ctx); online {
				step("android")
			}
			continue
		}
		if d.Booted(ctx) {
			return nil
		}
	}
	return errors.New("boot:timeout")
}

func (m *Manager) logTail() string {
	b, err := os.ReadFile(m.LogPath)
	if err != nil {
		return ""
	}
	var keep []string
	for _, line := range strings.Split(string(b), "\n") {
		if strings.Contains(line, "adb public key") {
			continue
		}
		if strings.HasPrefix(line, "qemu:") || strings.Contains(line, "ERROR") || strings.Contains(line, "PANIC") {
			keep = append(keep, strings.TrimSpace(line))
		}
	}
	return tail(strings.Join(keep, " | "), 500)
}

// tune applies settings that make the virtual device feel faster.
func (m *Manager) tune(ctx context.Context, s Spec) {
	d := m.Device()
	// The override persists in user data, so it must be cleared for exact matches.
	if s.Profile.DPI == NearestDensity(s.Profile.DPI) {
		d.Shell(ctx, 5*time.Second, "wm", "density", "reset")
	} else {
		d.Shell(ctx, 5*time.Second, "wm", "density", strconv.Itoa(s.Profile.DPI))
	}
	for _, kv := range [][3]string{
		{"global", "window_animation_scale", "0.5"},
		{"global", "transition_animation_scale", "0.5"},
		{"global", "animator_duration_scale", "0.5"},
		{"system", "screen_off_timeout", "1800000"},
		{"global", "stay_on_while_plugged_in", "7"},
		{"secure", "show_ime_with_hard_keyboard", "1"},
	} {
		d.Shell(ctx, 5*time.Second, "settings", "put", kv[0], kv[1], kv[2])
	}
	d.SetLandscape(ctx, s.Landscape)
}

// Stop shuts the device down, saving a Quick Boot snapshot.
func (m *Manager) Stop(ctx context.Context) error {
	d := m.Device()
	if d.Online(ctx) {
		d.adb(ctx, 45*time.Second, "emu", "kill")
	}
	m.mu.Lock()
	cmd, done := m.cmd, m.exited
	m.mu.Unlock()
	deadline := time.Now().Add(40 * time.Second)
	for time.Now().Before(deadline) && d.Online(ctx) {
		time.Sleep(400 * time.Millisecond)
	}
	if cmd != nil && done != nil {
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			cmd.Process.Kill()
		}
	}
	m.mu.Lock()
	m.cmd, m.exited = nil, nil
	m.mu.Unlock()
	return nil
}
