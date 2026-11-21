// Package store persists settings and the app library as JSON.
package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// Settings are the user's preferences.
type Settings struct {
	Lang        string `json:"lang"`
	Profile     string `json:"profile"`
	Orientation string `json:"orientation"` // auto, portrait, landscape
	Perf        string `json:"perf"`        // eco, balanced, max
	CustomW     int    `json:"customW"`
	CustomH     int    `json:"customH"`
	CustomDPI   int    `json:"customDpi"`
	AutoLaunch  bool   `json:"autoLaunch"`
}

// Defaults has Thai as the primary language.
func Defaults() Settings {
	return Settings{Lang: "th", Profile: "phone-fhdp", Orientation: "auto", Perf: "balanced",
		CustomW: 1080, CustomH: 2400, CustomDPI: 420, AutoLaunch: true}
}

// App is an installed application.
type App struct {
	Package     string   `json:"package"`
	Label       string   `json:"label"`
	Version     string   `json:"version"`
	Icon        string   `json:"icon"`
	Orientation string   `json:"orientation"`
	Override    string   `json:"override"`
	ABIs        []string `json:"abis"`
	Size        int64    `json:"size"`
	InstalledAt int64    `json:"installedAt"`
	LastPlayed  int64    `json:"lastPlayed"`
}

// Store is safe for concurrent use.
type Store struct {
	dir      string
	mu       sync.Mutex
	settings Settings
	apps     map[string]App
}

// Open loads the store, tolerating missing or damaged files.
func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	s := &Store{dir: dir, settings: Defaults(), apps: map[string]App{}}
	if b, err := os.ReadFile(filepath.Join(dir, "settings.json")); err == nil {
		v := Defaults()
		if json.Unmarshal(b, &v) == nil {
			s.settings = sanitize(v)
		}
	}
	if b, err := os.ReadFile(filepath.Join(dir, "apps.json")); err == nil {
		var list []App
		if json.Unmarshal(b, &list) == nil {
			for _, a := range list {
				s.apps[a.Package] = a
			}
		}
	}
	return s, nil
}

func sanitize(v Settings) Settings {
	d := Defaults()
	if v.Lang != "th" && v.Lang != "en" {
		v.Lang = d.Lang
	}
	if v.Orientation != "auto" && v.Orientation != "portrait" && v.Orientation != "landscape" {
		v.Orientation = d.Orientation
	}
	if v.Perf != "eco" && v.Perf != "balanced" && v.Perf != "max" {
		v.Perf = d.Perf
	}
	if v.Profile == "" {
		v.Profile = d.Profile
	}
	return v
}

func (s *Store) write(name string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	p := filepath.Join(s.dir, name)
	if err := os.WriteFile(p+".tmp", b, 0o644); err != nil {
		return err
	}
	return os.Rename(p+".tmp", p)
}

// Settings returns the current settings.
func (s *Store) Settings() Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settings
}

// SaveSettings validates and persists settings.
func (s *Store) SaveSettings(v Settings) (Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.settings = sanitize(v)
	return s.settings, s.write("settings.json", s.settings)
}

// Apps returns the library, most recently used first.
func (s *Store) Apps() []App {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]App, 0, len(s.apps))
	for _, a := range s.apps {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool {
		ai, aj := max(out[i].LastPlayed, out[i].InstalledAt), max(out[j].LastPlayed, out[j].InstalledAt)
		if ai != aj {
			return ai > aj
		}
		return out[i].Package < out[j].Package
	})
	return out
}

// App looks up one application.
func (s *Store) App(pkg string) (App, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.apps[pkg]
	return a, ok
}

func (s *Store) flushApps() error {
	list := make([]App, 0, len(s.apps))
	for _, a := range s.apps {
		list = append(list, a)
	}
	return s.write("apps.json", list)
}

// PutApp adds or replaces an application.
func (s *Store) PutApp(a App) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.apps[a.Package] = a
	return s.flushApps()
}

// RemoveApp deletes an application.
func (s *Store) RemoveApp(pkg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.apps, pkg)
	return s.flushApps()
}
