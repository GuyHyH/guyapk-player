package android

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Device talks to one running Android instance through adb.
type Device struct {
	SDK    SDK
	Serial string
}

// InstallError carries the package manager's failure code.
type InstallError struct{ Code string }

func (e *InstallError) Error() string { return "install:" + e.Code }

var (
	installRe      = regexp.MustCompile(`(INSTALL_[A-Z_]+)`)
	rotationRe     = regexp.MustCompile(`(?:mRotation=|mCurrentRotation=|rotation=)(?:ROTATION_)?(0|90|180|270|1|2|3)\b`)
	rotationLineRe = regexp.MustCompile(`(?m)^\s*mRotation=([0-3])\s+mDeferredRotationPauseCount`)
	safePkgRe      = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(\.[A-Za-z][A-Za-z0-9_]*)+$`)
	accelRe2       = regexp.MustCompile(`acceleration\s*[=:]\s*(-?[\d.]+(?:e-?\d+)?):(-?[\d.]+(?:e-?\d+)?):`)
)

func (d Device) adb(ctx context.Context, t time.Duration, args ...string) (string, error) {
	return run(ctx, t, d.SDK.ADB(), append([]string{"-s", d.Serial}, args...)...)
}

// Run executes an adb command for this device and returns its output.
func (d Device) Run(ctx context.Context, t time.Duration, args ...string) (string, error) {
	return d.adb(ctx, t, args...)
}

// Command prepares a long-running adb command for this device.
func (d Device) Command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, d.SDK.ADB(), append([]string{"-s", d.Serial}, args...)...)
	hideConsole(cmd)
	return cmd
}

// Shell runs a command on the device.
func (d Device) Shell(ctx context.Context, t time.Duration, cmd ...string) (string, error) {
	return d.adb(ctx, t, append([]string{"shell"}, cmd...)...)
}

// Online reports whether adb can reach the device.
func (d Device) Online(ctx context.Context) bool {
	out, err := d.adb(ctx, 4*time.Second, "get-state")
	return err == nil && strings.TrimSpace(out) == "device"
}

// Booted reports whether Android finished starting.
func (d Device) Booted(ctx context.Context) bool {
	out, err := d.Shell(ctx, 5*time.Second, "getprop", "sys.boot_completed")
	return err == nil && strings.TrimSpace(out) == "1"
}

// ABIs lists the CPU architectures the device can run, including translated ones.
func (d Device) ABIs(ctx context.Context) []string {
	out, err := d.Shell(ctx, 5*time.Second, "getprop", "ro.product.cpu.abilist")
	if err != nil || out == "" {
		return nil
	}
	return strings.Split(strings.TrimSpace(out), ",")
}

// Install installs one APK or a set of splits, then copies any OBB data.
func (d Device) Install(ctx context.Context, pkg string, apks, obbs []string) error {
	if !safePkgRe.MatchString(pkg) {
		return errors.New("install:INVALID_PACKAGE")
	}
	args := []string{"install", "-r", "-t", "-g"}
	if len(apks) > 1 {
		args[0] = "install-multiple"
	}
	out, err := d.adb(ctx, 15*time.Minute, append(args, apks...)...)
	if m := installRe.FindString(out); m != "" {
		return &InstallError{m}
	}
	if err != nil {
		return fmt.Errorf("install: %w", err)
	}
	if len(obbs) > 0 {
		dir := "/sdcard/Android/obb/" + pkg
		if _, err := d.Shell(ctx, 15*time.Second, "mkdir", "-p", dir); err != nil {
			return fmt.Errorf("obb: %w", err)
		}
		for _, o := range obbs {
			if _, err := d.adb(ctx, 30*time.Minute, "push", o, dir+"/"+filepath.Base(o)); err != nil {
				return fmt.Errorf("obb: %w", err)
			}
		}
	}
	return nil
}

// Launch opens the app's launcher activity.
func (d Device) Launch(ctx context.Context, pkg string) error {
	if !safePkgRe.MatchString(pkg) {
		return errors.New("launch: invalid package")
	}
	out, err := d.Shell(ctx, 10*time.Second, "cmd", "package", "resolve-activity", "--brief",
		"-c", "android.intent.category.LAUNCHER", pkg)
	if err == nil {
		lines := strings.Split(strings.TrimSpace(out), "\n")
		comp := strings.TrimSpace(lines[len(lines)-1])
		if strings.HasPrefix(comp, pkg+"/") || strings.Contains(comp, "/") {
			res, err := d.Shell(ctx, 15*time.Second, "am", "start", "-n", comp)
			if err == nil && !strings.Contains(res, "Error:") {
				return nil
			}
		}
	}
	res, err := d.Shell(ctx, 15*time.Second, "monkey", "-p", pkg, "-c", "android.intent.category.LAUNCHER", "1")
	if err != nil || strings.Contains(res, "No activities found") {
		return errors.New("launch:no_activity")
	}
	return nil
}

// Uninstall removes a package.
func (d Device) Uninstall(ctx context.Context, pkg string) error {
	if !safePkgRe.MatchString(pkg) {
		return errors.New("uninstall: invalid package")
	}
	out, err := d.adb(ctx, time.Minute, "uninstall", pkg)
	if err != nil && !strings.Contains(out, "DELETE_FAILED_INTERNAL_ERROR") {
		return fmt.Errorf("uninstall: %w", err)
	}
	return nil
}

// Key sends an Android key event.
func (d Device) Key(ctx context.Context, code int) error {
	_, err := d.Shell(ctx, 5*time.Second, "input", "keyevent", strconv.Itoa(code))
	return err
}

// Screenshot captures the screen as PNG.
func (d Device) Screenshot(ctx context.Context) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, d.SDK.ADB(), "-s", d.Serial, "exec-out", "screencap", "-p")
	hideConsole(cmd)
	b, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	if !bytes.HasPrefix(b, []byte{0x89, 'P', 'N', 'G'}) {
		return nil, errors.New("screenshot: invalid image")
	}
	return b, nil
}

// Rotation returns the display rotation as 0..3 quarter turns.
func (d Device) Rotation(ctx context.Context) (int, error) {
	out, err := d.Shell(ctx, 6*time.Second, "dumpsys", "window", "displays")
	if err != nil {
		return 0, err
	}
	return parseRotation(out)
}

// parseRotation reads the main display's rotation. Screen streaming adds a
// virtual display that is listed first and stays at rotation 0, so the report
// must be narrowed to display 0 before looking for the value.
func parseRotation(out string) (int, error) {
	section := out
	if i := strings.Index(out, "Display: mDisplayId=0"); i >= 0 {
		section = out[i:]
		if j := strings.Index(section[1:], "Display: mDisplayId="); j >= 0 {
			section = section[:j+1]
		}
	}
	m := rotationLineRe.FindStringSubmatch(section)
	if m == nil {
		m = rotationRe.FindStringSubmatch(section)
	}
	if m == nil {
		return 0, errors.New("rotation: unknown")
	}
	switch m[1] {
	case "90", "1":
		return 1, nil
	case "180", "2":
		return 2, nil
	case "270", "3":
		return 3, nil
	}
	return 0, nil
}

// WindowRotation reads the emulator window's rotation (0..3) from its virtual
// accelerometer, which the console rotate command turns with the window.
func (d Device) WindowRotation(ctx context.Context) (int, error) {
	out, err := d.adb(ctx, 5*time.Second, "emu", "sensor", "get", "acceleration")
	if err != nil {
		return 0, err
	}
	m := accelRe2.FindStringSubmatch(out)
	if m == nil {
		return 0, errors.New("rotate: no accelerometer reading")
	}
	x, _ := strconv.ParseFloat(m[1], 64)
	y, _ := strconv.ParseFloat(m[2], 64)
	return rotationFromGravity(x, y), nil
}

func rotationFromGravity(x, y float64) int {
	if math.Abs(y) >= math.Abs(x) {
		if y >= 0 {
			return 0
		}
		return 2
	}
	if x >= 0 {
		return 1
	}
	return 3
}

// SetLandscape puts the window and the Android display in the same orientation.
// Both must move together: if only Android turns, the picture appears sideways
// in the window. Apps' own orientation requests are ignored so the user's choice
// always wins; an app locked to the other orientation is letterboxed upright.
func (d Device) SetLandscape(ctx context.Context, landscape bool) error {
	d.Shell(ctx, 5*time.Second, "settings", "put", "system", "accelerometer_rotation", "0")
	d.Shell(ctx, 5*time.Second, "wm", "set-ignore-orientation-request", "true")

	// The console can only step the window a quarter turn, and ignores a step
	// sent while the last one is animating. So turn once and let Android follow
	// whichever way the window now faces, which keeps the picture upright.
	target := 0
	if landscape {
		target = 1
	}
	win, err := d.WindowRotation(ctx)
	if err == nil && (win%2 == 1) != landscape {
		before := win
		if _, err = d.adb(ctx, 6*time.Second, "emu", "rotate"); err == nil {
			deadline := time.Now().Add(2500 * time.Millisecond)
			for win == before && time.Now().Before(deadline) {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(40 * time.Millisecond):
				}
				if n, rerr := d.WindowRotation(ctx); rerr == nil {
					win = n
				}
			}
		}
	}
	// The desktop window is best effort. If it did not respond, Android must
	// still rotate so that a connected iPad gets the right picture.
	if err == nil && (win%2 == 1) == landscape {
		target = win
	}
	if _, err := d.Shell(ctx, 5*time.Second, "wm", "user-rotation", "lock", strconv.Itoa(target)); err != nil {
		return fmt.Errorf("rotate: %w", err)
	}
	deadline := time.Now().Add(6 * time.Second)
	for {
		if r, err := d.Rotation(ctx); err == nil && r == target {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("rotate: display did not change")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(120 * time.Millisecond):
		}
	}
}
