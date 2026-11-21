package remote

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"guyapk/internal/android"
)

// TestHarness runs the real server for browser tests until the file named by
// GUYAPK_HARNESS_DIR/stop appears.
//
//	GUYAPK_HARNESS=live    a real device, plain HTTP on loopback
//	GUYAPK_HARNESS=replay  a recorded stream, real TLS on GUYAPK_HARNESS_IP
func TestHarness(t *testing.T) {
	mode := os.Getenv("GUYAPK_HARNESS")
	if mode == "" {
		t.Skip("GUYAPK_HARNESS not set")
	}
	dir := os.Getenv("GUYAPK_HARNESS_DIR")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	opt := Options{Dir: filepath.Join(dir, "state")}
	var counter *touchCounter
	switch mode {
	case "live":
		sdk := android.Locate(filepath.Join(dir, "sdk"))
		avd := filepath.Join(dir, "avd")
		os.MkdirAll(avd, 0o755)
		m := &android.Manager{SDK: sdk, AVDHome: avd, LogPath: filepath.Join(dir, "emulator.log")}
		profile, _ := android.FindProfile(envOr("GUYAPK_HARNESS_PROFILE", "tablet-8"), 0, 0, 0)
		spec := android.Spec{Profile: profile, Tuning: android.Tune("balanced"), Image: sdk.Images()[0]}
		if err := m.Boot(ctx, spec, func(string) {}); err != nil {
			t.Fatal(err)
		}
		defer m.Stop(context.Background())
		d := m.Device()
		var src Source = &Scrcpy{Device: d, CacheDir: envOr("GUYAPK_LIVE_CACHE", filepath.Join(dir, "cache"))}
		if rec := os.Getenv("GUYAPK_HARNESS_RECORD"); rec != "" {
			src = &Scrcpy{Device: d, CacheDir: envOr("GUYAPK_LIVE_CACHE", filepath.Join(dir, "cache")), MaxSize: 960, BitRate: 1_500_000}
			src = &recorder{Source: src, file: rec, limit: 450 << 10}
		}
		opt.Source, opt.Host, opt.Addr, opt.Plain = src, &liveHost{d: d}, net.IPv4(127, 0, 0, 1), true
	case "replay":
		packets, err := loadRecording(envOr("GUYAPK_HARNESS_FIXTURE", filepath.Join("testdata", "sample.bin")))
		if err != nil {
			t.Fatal(err)
		}
		ip := net.ParseIP(os.Getenv("GUYAPK_HARNESS_IP"))
		if ip == nil {
			t.Fatal("GUYAPK_HARNESS_IP not set")
		}
		counter = &touchCounter{}
		opt.Source, opt.Host, opt.Addr = replaySource{packets, counter}, replayHost{}, ip.To4()
		opt.Plain = os.Getenv("GUYAPK_HARNESS_PLAIN") != ""
	default:
		t.Fatalf("unknown mode %q", mode)
	}

	s := New(opt)
	info, err := s.Enable()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Disable()
	publish := func() {
		v := map[string]any{"setup": info.SetupURL, "app": info.AppURL, "pin": info.PIN, "ca": filepath.Join(opt.Dir, "ca.crt")}
		if counter != nil {
			v["touches"] = counter.n.Load()
		}
		b, _ := json.Marshal(v)
		os.WriteFile(filepath.Join(dir, "harness.tmp"), b, 0o644)
		os.Rename(filepath.Join(dir, "harness.tmp"), filepath.Join(dir, "harness.json"))
	}
	for {
		publish()
		if _, err := os.Stat(filepath.Join(dir, "stop")); err == nil {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

type liveHost struct {
	d android.Device
	l atomic.Bool
}

func (h *liveHost) RemoteStatus() Status { return Status{Phase: "ready", Landscape: h.l.Load()} }
func (h *liveHost) RemoteApps() []AppInfo {
	return []AppInfo{{Package: "com.android.settings", Label: "การตั้งค่า"}}
}
func (h *liveHost) RemoteStart() error { return nil }
func (h *liveHost) RemoteLaunch(pkg string) error {
	return h.d.Launch(context.Background(), pkg)
}
func (h *liveHost) RemoteRotate() (bool, error) {
	want := !h.l.Load()
	if err := h.d.SetLandscape(context.Background(), want); err != nil {
		return !want, err
	}
	h.l.Store(want)
	return want, nil
}
func (h *liveHost) RemoteKey(name string) error {
	code := map[string]int{"back": 4, "home": 3, "recents": 187}[name]
	return h.d.Key(context.Background(), code)
}

type replayHost struct{}

func (replayHost) RemoteStatus() Status { return Status{Phase: "ready"} }
func (replayHost) RemoteApps() []AppInfo {
	return []AppInfo{{Package: "th.guyapk.demo", Label: "GuyAPK Demo"}, {Package: "com.example.reader", Label: "นิยายออนไลน์"}}
}
func (replayHost) RemoteStart() error          { return nil }
func (replayHost) RemoteLaunch(string) error   { return nil }
func (replayHost) RemoteRotate() (bool, error) { return false, nil }
func (replayHost) RemoteKey(string) error      { return nil }

type touchCounter struct{ n atomic.Int64 }

// recorder saves the first part of a live stream as a test fixture.
type recorder struct {
	Source
	file  string
	limit int
	once  sync.Once
}

type recStream struct {
	Stream
	ch chan Packet
}

func (r *recStream) Packets() <-chan Packet { return r.ch }

func (r *recorder) Open(ctx context.Context) (Stream, error) {
	st, err := r.Source.Open(ctx)
	if err != nil {
		return nil, err
	}
	out := &recStream{st, make(chan Packet, 32)}
	var f *os.File
	r.once.Do(func() { f, _ = os.Create(r.file) })
	go func() {
		defer close(out.ch)
		written, keys := 0, 0
		for p := range st.Packets() {
			if f != nil {
				if p.Kind == KindKey {
					keys++
				}
				// Stop at a key frame so the clip can loop cleanly.
				if written > r.limit && p.Kind == KindKey {
					f.Close()
					f = nil
				} else {
					var h [19]byte
					h[0] = byte(p.Kind)
					binary.BigEndian.PutUint64(h[1:], p.PTS)
					binary.BigEndian.PutUint16(h[9:], uint16(p.W))
					binary.BigEndian.PutUint16(h[11:], uint16(p.H))
					binary.BigEndian.PutUint32(h[13:], uint32(len(p.Data)))
					f.Write(h[:17])
					f.Write(p.Data)
					written += 17 + len(p.Data)
				}
			}
			select {
			case out.ch <- p:
			case <-ctx.Done():
				if f != nil {
					f.Close()
				}
				return
			}
		}
		if f != nil {
			f.Close()
		}
	}()
	return out, nil
}

func loadRecording(file string) ([]Packet, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := bufio.NewReader(f)
	var out []Packet
	for {
		var h [17]byte
		if _, err := io.ReadFull(r, h[:]); err == io.EOF {
			return out, nil
		} else if err != nil {
			return nil, err
		}
		p := Packet{
			Kind: Kind(h[0]), PTS: binary.BigEndian.Uint64(h[1:]),
			W: int(binary.BigEndian.Uint16(h[9:])), H: int(binary.BigEndian.Uint16(h[11:])),
		}
		p.Data = make([]byte, binary.BigEndian.Uint32(h[13:]))
		if _, err := io.ReadFull(r, p.Data); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
}

type replaySource struct {
	packets []Packet
	touches *touchCounter
}

type replayStream struct {
	ch      chan Packet
	touches *touchCounter
	stop    chan struct{}
	once    sync.Once
}

func (r replaySource) Open(context.Context) (Stream, error) {
	s := &replayStream{ch: make(chan Packet, 8), touches: r.touches, stop: make(chan struct{})}
	go func() {
		defer close(s.ch)
		for {
			var last uint64
			for _, p := range r.packets {
				if (p.Kind == KindKey || p.Kind == KindDelta) && last != 0 && p.PTS > last {
					d := time.Duration(p.PTS-last) * time.Microsecond
					if d > 100*time.Millisecond {
						d = 100 * time.Millisecond
					}
					select {
					case <-time.After(d):
					case <-s.stop:
						return
					}
				}
				if p.Kind == KindKey || p.Kind == KindDelta {
					last = p.PTS
				}
				select {
				case s.ch <- p:
				case <-s.stop:
					return
				}
			}
		}
	}()
	return s, nil
}

func (r *replayStream) Packets() <-chan Packet { return r.ch }
func (r *replayStream) Touch(uint8, uint8, uint16, uint16) error {
	r.touches.n.Add(1)
	return nil
}
func (r *replayStream) RequestKey() error { return nil }
func (r *replayStream) Close() error      { r.once.Do(func() { close(r.stop) }); return nil }
