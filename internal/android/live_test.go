package android

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"guyapk/internal/apkinfo"
)

// TestLive boots a real device. It runs only with GUYAPK_LIVE_APK set to an APK:
//
//	$env:GUYAPK_LIVE_APK="C:\path\app.apk"; go test ./internal/android -run TestLive -v -timeout 20m
func TestLive(t *testing.T) {
	apk := os.Getenv("GUYAPK_LIVE_APK")
	if apk == "" {
		t.Skip("GUYAPK_LIVE_APK not set")
	}
	ctx := context.Background()
	work := t.TempDir()
	if v := os.Getenv("GUYAPK_LIVE_HOME"); v != "" {
		work = v
	}
	sdk := Locate(filepath.Join(work, "sdk"))
	if !sdk.Ready() {
		t.Fatal("no usable SDK")
	}
	t.Logf("sdk=%s accel=%s image=%+v", sdk.Root, sdk.CheckAccel(ctx), sdk.Images()[0])

	avd := filepath.Join(work, "avd")
	if err := os.MkdirAll(avd, 0o755); err != nil {
		t.Fatal(err)
	}
	m := &Manager{SDK: sdk, AVDHome: avd, LogPath: filepath.Join(work, "emulator.log")}
	profile, _ := FindProfile("tablet-8", 0, 0, 0)
	spec := Spec{Profile: profile, Tuning: Tune("balanced"), Image: sdk.Images()[0]}

	start := time.Now()
	if err := m.Boot(ctx, spec, func(s string) { t.Logf("%6.1fs step=%s", time.Since(start).Seconds(), s) }); err != nil {
		t.Fatalf("boot: %v", err)
	}
	defer m.Stop(ctx)
	t.Logf("booted in %.1fs", time.Since(start).Seconds())

	d := m.Device()
	size, _ := d.Shell(ctx, 5*time.Second, "wm", "size")
	density, _ := d.Shell(ctx, 5*time.Second, "wm", "density")
	t.Logf("%s | %s | abis=%v", size, density, d.ABIs(ctx))

	p, err := apkinfo.Prepare(apk, filepath.Join(work, "tmp"))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	defer p.Close()
	t.Logf("apk: %s %q v%s min=%d target=%d orient=%q abis=%v icon=%d", p.Info.Package, p.Info.Label,
		p.Info.VersionName, p.Info.MinSDK, p.Info.TargetSDK, p.Info.Orientation, p.Info.ABIs, len(p.Info.Icon))

	if err := d.Install(ctx, p.Info.Package, p.APKs, p.OBBs); err != nil {
		t.Fatalf("install: %v", err)
	}
	if err := d.Launch(ctx, p.Info.Package); err != nil {
		t.Fatalf("launch: %v", err)
	}
	time.Sleep(2 * time.Second)
	focus, _ := d.Shell(ctx, 6*time.Second, "dumpsys", "window", "|", "grep", "-m1", "mCurrentFocus")
	t.Logf("focus: %s", focus)

	if p.Info.Orientation != "landscape" {
		t.Errorf("manifest orientation = %q, want landscape", p.Info.Orientation)
	}
	capture := os.Getenv("GUYAPK_LIVE_CAPTURE")
	for i, landscape := range []bool{true, false, true, false} {
		s := time.Now()
		if err := d.SetLandscape(ctx, landscape); err != nil {
			t.Fatalf("rotate landscape=%v: %v", landscape, err)
		}
		r, _ := d.Rotation(ctx)
		w, _ := d.WindowRotation(ctx)
		t.Logf("landscape=%v display=%d window=%d in %dms", landscape, r, w, time.Since(s).Milliseconds())
		if (r%2 == 1) != landscape || r != w {
			t.Fatalf("rotation mismatch: landscape=%v display=%d window=%d", landscape, r, w)
		}
		if capture != "" && i < 2 {
			time.Sleep(1500 * time.Millisecond)
			out, err := exec.Command("powershell.exe", "-NoProfile", "-ExecutionPolicy", "Bypass", "-File", capture,
				fmt.Sprintf("rot-%d.png", i)).CombinedOutput()
			t.Logf("capture %d: %v %s", i, err, out)
		}
	}
	shot, err := d.Screenshot(ctx)
	if err != nil {
		t.Fatalf("screenshot: %v", err)
	}
	if out := os.Getenv("GUYAPK_LIVE_SHOT"); out != "" {
		os.WriteFile(out, shot, 0o644)
	}
	t.Logf("screenshot %d bytes", len(shot))
	if err := d.Uninstall(ctx, p.Info.Package); err != nil {
		t.Fatalf("uninstall: %v", err)
	}

	// Shutting down while in landscape must not break rotation after Quick Boot.
	if err := d.SetLandscape(ctx, true); err != nil {
		t.Fatalf("rotate before restart: %v", err)
	}
	if err := m.Stop(ctx); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := m.Boot(ctx, spec, func(string) {}); err != nil {
		t.Fatalf("second boot: %v", err)
	}
	for _, landscape := range []bool{true, false} {
		if err := d.SetLandscape(ctx, landscape); err != nil {
			t.Fatalf("rotate after restart landscape=%v: %v", landscape, err)
		}
		r, _ := d.Rotation(ctx)
		w, _ := d.WindowRotation(ctx)
		t.Logf("after restart landscape=%v display=%d window=%d", landscape, r, w)
		if (r%2 == 1) != landscape || r != w {
			t.Fatalf("rotation after restart: landscape=%v display=%d window=%d", landscape, r, w)
		}
	}
}
