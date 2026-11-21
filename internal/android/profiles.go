package android

import (
	"fmt"
	"runtime"
)

// Profile is a screen preset. W and H are always the portrait size.
type Profile struct {
	ID     string  `json:"id"`
	Kind   string  `json:"kind"` // phone, tablet, foldable, custom
	NameTH string  `json:"nameTh"`
	NameEN string  `json:"nameEn"`
	W      int     `json:"w"`
	H      int     `json:"h"`
	DPI    int     `json:"dpi"`
	Inch   float64 `json:"inch"`
}

// Profiles lists the built-in device presets.
func Profiles() []Profile {
	return []Profile{
		{"phone-compact", "phone", "มือถือจอเล็ก", "Compact phone", 720, 1280, 320, 4.7},
		{"phone-hd", "phone", "มือถือ HD+", "HD+ phone", 720, 1600, 280, 6.5},
		{"phone-fhd", "phone", "มือถือ Full HD", "Full HD phone", 1080, 1920, 420, 5.5},
		{"phone-fhdp", "phone", "มือถือ Full HD+", "Full HD+ phone", 1080, 2400, 420, 6.5},
		{"phone-gaming", "phone", "มือถือเกมมิ่ง", "Gaming phone", 1080, 2460, 400, 6.8},
		{"phone-qhd", "phone", "มือถือ Quad HD+", "Quad HD+ phone", 1440, 3120, 560, 6.8},
		{"tablet-7", "tablet", "แท็บเล็ต 7 นิ้ว", "7-inch tablet", 800, 1280, 213, 7},
		{"tablet-8", "tablet", "แท็บเล็ต 8.7 นิ้ว", "8.7-inch tablet", 1200, 1920, 260, 8.7},
		{"tablet-10", "tablet", "แท็บเล็ต 10.5 นิ้ว", "10.5-inch tablet", 1200, 2000, 240, 10.5},
		{"tablet-11", "tablet", "แท็บเล็ต 11 นิ้ว", "11-inch tablet", 1600, 2560, 320, 11},
		{"tablet-13", "tablet", "แท็บเล็ต 13 นิ้ว", "13-inch tablet", 2064, 2752, 320, 13},
		{"fold-inner", "foldable", "จอพับ (กางออก)", "Foldable (open)", 1768, 2208, 420, 7.6},
		{"fold-outer", "foldable", "จอพับ (พับอยู่)", "Foldable (closed)", 904, 2316, 420, 6.2},
	}
}

// FindProfile resolves an ID, or builds a validated custom profile.
func FindProfile(id string, w, h, dpi int) (Profile, error) {
	if id == "custom" {
		if w > h {
			w, h = h, w
		}
		if w < 320 || h < 480 || h > 4096 || dpi < 120 || dpi > 640 {
			return Profile{}, fmt.Errorf("profile: custom size out of range")
		}
		return Profile{ID: "custom", Kind: "custom", NameTH: "กำหนดเอง", NameEN: "Custom", W: w, H: h, DPI: dpi}, nil
	}
	for _, p := range Profiles() {
		if p.ID == id {
			return p, nil
		}
	}
	return Profiles()[3], nil
}

// Tuning is the resource budget for the virtual device.
type Tuning struct {
	Cores  int `json:"cores"`
	RAMMB  int `json:"ramMb"`
	HeapMB int `json:"heapMb"`
}

func clamp(v, lo, hi int) int {
	if hi < lo {
		hi = lo
	}
	return max(lo, min(v, hi))
}

// Tune maps a performance mode to resources based on this computer.
func Tune(mode string) Tuning {
	threads, total := runtime.NumCPU(), TotalRAMMB()
	if total <= 0 {
		total = 8192
	}
	switch mode {
	case "eco":
		return Tuning{Cores: clamp(threads/4, 2, 2), RAMMB: clamp(total/5, 2048, 2048), HeapMB: 192}
	case "max":
		return Tuning{Cores: clamp(threads-2, 2, 8), RAMMB: clamp(total/2, 2048, 8192), HeapMB: 512}
	}
	return Tuning{Cores: clamp(threads/2, 2, 6), RAMMB: clamp(total/4, 2048, 4096), HeapMB: 256}
}
