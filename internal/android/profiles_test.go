package android

import (
	"slices"
	"testing"
)

func TestNearestDensity(t *testing.T) {
	for in, want := range map[int]int{260: 240, 270: 280, 420: 420, 100: 120, 700: 640} {
		if got := NearestDensity(in); got != want && !(in == 260 && got == 280) {
			t.Errorf("NearestDensity(%d) = %d, want %d", in, got, want)
		}
	}
	for _, p := range Profiles() {
		if !slices.Contains(bootDensities, NearestDensity(p.DPI)) {
			t.Errorf("%s has no bootable density", p.ID)
		}
		if p.W >= p.H {
			t.Errorf("%s must be stored in portrait", p.ID)
		}
	}
}

func TestRotationFromGravity(t *testing.T) {
	for _, c := range []struct {
		x, y float64
		want int
	}{{0, 9.8, 0}, {9.8, 0, 1}, {0, -9.8, 2}, {-9.8, 0, 3}, {.4, 9.7, 0}, {9.7, -.4, 1}} {
		if got := rotationFromGravity(c.x, c.y); got != c.want {
			t.Errorf("rotationFromGravity(%v,%v) = %d, want %d", c.x, c.y, got, c.want)
		}
	}
}

func TestCustomProfile(t *testing.T) {
	p, err := FindProfile("custom", 1920, 1080, 300)
	if err != nil || p.W != 1080 || p.H != 1920 {
		t.Fatalf("got %+v, %v", p, err)
	}
	for _, c := range [][3]int{{100, 800, 300}, {800, 5000, 300}, {800, 1280, 90}, {800, 1280, 900}} {
		if _, err := FindProfile("custom", c[0], c[1], c[2]); err == nil {
			t.Errorf("expected rejection for %v", c)
		}
	}
}

func TestTuneStaysInBounds(t *testing.T) {
	for _, mode := range []string{"eco", "balanced", "max", "unknown"} {
		u := Tune(mode)
		if u.Cores < 2 || u.Cores > 8 || u.RAMMB < 2048 || u.RAMMB > 8192 {
			t.Errorf("%s out of bounds: %+v", mode, u)
		}
	}
}
