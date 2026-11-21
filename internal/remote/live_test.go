package remote

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"guyapk/internal/android"
)

type liveCount struct {
	w, h, cfg, keys, delta, bytes int
}

// TestLiveStream needs a device already running on GuyAPK's port:
//
//	$env:GUYAPK_LIVE_STREAM="1"; go test ./internal/remote -run TestLiveStream -v
func TestLiveStream(t *testing.T) {
	if os.Getenv("GUYAPK_LIVE_STREAM") == "" {
		t.Skip("GUYAPK_LIVE_STREAM not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	sdk := android.Locate(filepath.Join(t.TempDir(), "sdk"))
	d := android.Device{SDK: sdk, Serial: android.Serial}
	if !d.Online(ctx) {
		t.Fatal("device is not running")
	}
	cache := envOr("GUYAPK_LIVE_CACHE", t.TempDir())
	src := &Scrcpy{Device: d, CacheDir: cache, LogFile: os.Getenv("GUYAPK_LIVE_LOG")}

	d.Shell(ctx, 5*time.Second, "am", "force-stop", "com.android.settings")
	d.Shell(ctx, 5*time.Second, "input", "keyevent", "3")
	time.Sleep(600 * time.Millisecond)

	start := time.Now()
	st, err := src.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	collect := func(dur time.Duration, until func(liveCount) bool) liveCount {
		var c liveCount
		end := time.After(dur)
		for {
			select {
			case p, ok := <-st.Packets():
				if !ok {
					t.Fatal("stream ended")
				}
				switch p.Kind {
				case KindSession:
					c.w, c.h = p.W, p.H
				case KindConfig:
					c.cfg++
				case KindKey:
					c.keys++
				case KindDelta:
					c.delta++
				}
				c.bytes += len(p.Data)
				if until != nil && until(c) {
					return c
				}
			case <-end:
				return c
			}
		}
	}
	first := collect(10*time.Second, func(c liveCount) bool { return c.keys > 0 })
	t.Logf("first picture after %v, %dx%d", time.Since(start).Round(time.Millisecond), first.w, first.h)
	if first.w == 0 || first.cfg == 0 || first.keys == 0 {
		t.Fatalf("no usable video: %+v", first)
	}

	// Real tap, sent through the stream, on a control found on screen.
	bounds := findText(ctx, t, d, "Settings")
	nx, ny := uint16(bounds[0]*65535/1200), uint16(bounds[1]*65535/1920)
	st.Touch(touchDown, 0, nx, ny)
	collect(70*time.Millisecond, nil)
	st.Touch(touchUp, 0, nx, ny)
	var f string
	for i := 0; i < 12 && !strings.Contains(f, "settings"); i++ {
		collect(500*time.Millisecond, nil)
		f = focus(ctx, d)
	}
	if !strings.Contains(f, "settings") {
		t.Fatalf("tap did not open Settings: %s", f)
	}
	t.Log("tap through stream opened Settings")

	// A sustained drag scrolls the list and measures video throughput.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for r := 0; r < 5; r++ {
			from, to := 50000, 16000
			if r%2 == 1 {
				from, to = to, from
			}
			st.Touch(touchDown, 0, 32768, uint16(from))
			const steps = 40
			for i := 1; i <= steps; i++ {
				time.Sleep(12 * time.Millisecond)
				st.Touch(touchMove, 0, 32768, uint16(from+(to-from)*i/steps))
			}
			st.Touch(touchUp, 0, 32768, uint16(to))
			time.Sleep(60 * time.Millisecond)
		}
	}()
	began := time.Now()
	c := collect(3*time.Second, nil)
	<-done
	secs := time.Since(began).Seconds()
	t.Logf("during drag: %.1f fps, %.1f Mbit/s", float64(c.keys+c.delta)/secs, float64(c.bytes)*8/secs/1e6)
	if c.keys+c.delta < 15 {
		t.Errorf("too few frames during drag: %+v", c)
	}

	// Rotation must start a new session with swapped dimensions.
	go d.SetLandscape(ctx, true)
	rot := collect(9*time.Second, func(c liveCount) bool { return c.w != 0 && c.keys > 0 })
	t.Logf("after rotate: %dx%d", rot.w, rot.h)
	if rot.w != first.h || rot.h != first.w || rot.keys == 0 {
		t.Errorf("rotation session = %+v, want %dx%d", rot, first.h, first.w)
	}
	go d.SetLandscape(ctx, false)
	back := collect(9*time.Second, func(c liveCount) bool { return c.w != 0 && c.keys > 0 })
	t.Logf("after rotating back: %dx%d", back.w, back.h)
	if back.w != first.w || back.h != first.h {
		t.Errorf("second rotation session = %+v", back)
	}

	if err := st.RequestKey(); err != nil {
		t.Fatalf("key request: %v", err)
	}
	if k := collect(5*time.Second, func(c liveCount) bool { return c.keys > 0 }); k.keys == 0 {
		t.Error("requested key frame did not arrive")
	}
	d.Shell(ctx, 5*time.Second, "input", "keyevent", "3")
}

func focus(ctx context.Context, d android.Device) string {
	out, _ := d.Shell(ctx, 6*time.Second, "dumpsys activity activities | grep -m1 topResumedActivity")
	return strings.TrimSpace(out)
}

// findText returns the centre of the first on-screen element with this label.
func findText(ctx context.Context, t *testing.T, d android.Device, label string) [2]int {
	t.Helper()
	if _, err := d.Shell(ctx, 20*time.Second, "uiautomator", "dump", "/sdcard/ui.xml"); err != nil {
		t.Fatal(err)
	}
	xml, err := d.Shell(ctx, 6*time.Second, "cat", "/sdcard/ui.xml")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`text="` + label + `"[^>]*?bounds="\[(\d+),(\d+)\]\[(\d+),(\d+)\]"`)
	m := re.FindStringSubmatch(xml)
	if m == nil {
		t.Fatalf("%q not on screen", label)
	}
	n := func(i int) int { v, _ := strconv.Atoi(m[i]); return v }
	return [2]int{(n(1) + n(3)) / 2, (n(2) + n(4)) / 2}
}
