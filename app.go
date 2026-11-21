package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/runtime"

	"guyapk/internal/android"
	"guyapk/internal/apkinfo"
	"guyapk/internal/remote"
	"guyapk/internal/store"
)

// Version is set at build time.
var Version = "1.1.0"

// State is the device status shown in the UI.
type State struct {
	Phase     string `json:"phase"` // idle, booting, ready, stopping
	Step      string `json:"step"`
	Landscape bool   `json:"landscape"`
	Restart   bool   `json:"restart"` // chosen screen differs from the running one
	Error     string `json:"error"`
}

// Env describes this computer's readiness.
type Env struct {
	Ready    bool   `json:"ready"`
	SDKPath  string `json:"sdkPath"`
	Image    string `json:"image"`
	API      int    `json:"api"`
	ABI      string `json:"abi"`
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	Threads  int    `json:"threads"`
	RAMGB    int    `json:"ramGb"`
	Version  string `json:"version"`
	DataPath string `json:"dataPath"`
}

// Boot is the initial payload for the UI.
type Boot struct {
	Settings store.Settings    `json:"settings"`
	Profiles []android.Profile `json:"profiles"`
	Apps     []store.App       `json:"apps"`
	Env      Env               `json:"env"`
	State    State             `json:"state"`
	Pending  string            `json:"pending"`
}

// App is the API bound to the frontend.
type App struct {
	ctx     context.Context
	dataDir string
	store   *store.Store
	sdk     android.SDK
	emu     *android.Manager

	life        sync.Mutex // serialises boot, stop and install
	mu          sync.Mutex
	state       State
	running     string // fingerprint of the running device
	plan        *android.Plan
	pendingFile string
	provCancel  context.CancelFunc
	remote      *remote.Server
}

// NewApp prepares storage.
func NewApp() *App {
	base, err := os.UserConfigDir()
	if err != nil {
		base = os.TempDir()
	}
	dir := filepath.Join(base, "GuyAPK")
	st, err := store.Open(dir)
	if err != nil {
		panic(err)
	}
	a := &App{dataDir: dir, store: st, state: State{Phase: "idle"}}
	a.refreshSDK()
	for _, arg := range os.Args[1:] {
		if isPackageFile(arg) {
			a.pendingFile = arg
			break
		}
	}
	return a
}

func isPackageFile(p string) bool {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".apk", ".xapk", ".apks", ".apkm":
		return true
	}
	return false
}

func (a *App) refreshSDK() {
	a.sdk = android.Locate(filepath.Join(a.dataDir, "sdk"))
	avd := filepath.Join(a.dataDir, "avd")
	os.MkdirAll(avd, 0o755)
	a.emu = &android.Manager{SDK: a.sdk, AVDHome: avd, LogPath: filepath.Join(a.dataDir, "emulator.log")}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	go a.adopt()
	go a.watchDevice()
}

// watchDevice notices when the emulator dies on its own, so neither the desktop
// nor a connected iPad keeps showing a device that is gone.
func (a *App) watchDevice() {
	tick := time.NewTicker(4 * time.Second)
	defer tick.Stop()
	misses := 0
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-tick.C:
		}
		a.mu.Lock()
		ready := a.state.Phase == "ready"
		a.mu.Unlock()
		if !ready || a.emu.Device().Online(a.ctx) {
			misses = 0
			continue
		}
		if misses++; misses < 2 {
			continue
		}
		misses = 0
		a.mu.Lock()
		a.running = ""
		a.mu.Unlock()
		a.setState(func(s *State) {
			if s.Phase == "ready" {
				s.Phase, s.Step, s.Error, s.Restart = "idle", "", "device_lost", false
			}
		})
	}
}

// adopt reconnects to a device left running by a previous session.
func (a *App) adopt() {
	if !a.sdk.HasTools() {
		return
	}
	d := a.emu.Device()
	if d.Online(a.ctx) && d.Booted(a.ctx) {
		r, _ := d.Rotation(a.ctx)
		a.mu.Lock()
		if spec, err := a.spec(false); err == nil {
			a.running = spec.Fingerprint()
		}
		a.mu.Unlock()
		a.setState(func(s *State) { s.Phase, s.Landscape = "ready", r%2 == 1 })
	}
}

func (a *App) openFile(p string) {
	if a.ctx == nil {
		a.mu.Lock()
		a.pendingFile = p
		a.mu.Unlock()
		return
	}
	runtime.EventsEmit(a.ctx, "file", p)
}

func (a *App) onSecondInstance(d options.SecondInstanceData) {
	runtime.WindowUnminimise(a.ctx)
	runtime.Show(a.ctx)
	for _, arg := range d.Args {
		if isPackageFile(arg) {
			a.openFile(arg)
			return
		}
	}
}

func (a *App) beforeClose(ctx context.Context) bool {
	a.mu.Lock()
	phase := a.state.Phase
	a.mu.Unlock()
	if phase != "ready" && phase != "booting" {
		return false
	}
	// Hide immediately so closing feels instant while the snapshot saves.
	runtime.WindowHide(ctx)
	return false
}

func (a *App) shutdown(ctx context.Context) {
	if a.provCancel != nil {
		a.provCancel()
	}
	a.mu.Lock()
	rs := a.remote
	a.mu.Unlock()
	if rs != nil {
		rs.Disable()
	}
	if a.sdk.HasTools() && a.emu.Device().Online(context.Background()) {
		c, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		a.emu.Stop(c)
	}
}

func (a *App) setState(f func(*State)) {
	a.mu.Lock()
	f(&a.state)
	s := a.state
	a.mu.Unlock()
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "state", s)
	}
}

func (a *App) env() Env {
	e := Env{
		Ready: a.sdk.Ready(), SDKPath: a.sdk.Root, OS: goruntime.GOOS, Arch: goruntime.GOARCH,
		Threads: goruntime.NumCPU(), RAMGB: (android.TotalRAMMB() + 512) / 1024,
		Version: Version, DataPath: a.dataDir, ABI: android.HostABI(),
	}
	if imgs := a.sdk.Images(); len(imgs) > 0 {
		e.Image, e.API = imgs[0].Tag, imgs[0].API
	}
	return e
}

// Bootstrap returns everything the UI needs to render.
func (a *App) Bootstrap() Boot {
	a.mu.Lock()
	state, pending := a.state, a.pendingFile
	a.pendingFile = ""
	a.mu.Unlock()
	return Boot{a.store.Settings(), android.Profiles(), a.store.Apps(), a.env(), state, pending}
}

// CheckAccel reports the hardware accelerator in use.
func (a *App) CheckAccel() string { return a.sdk.CheckAccel(a.ctx) }

// Tuning previews the resources for a performance mode.
func (a *App) Tuning(mode string) android.Tuning { return android.Tune(mode) }

// SaveSettings stores preferences and flags a needed restart.
func (a *App) SaveSettings(v store.Settings) (store.Settings, error) {
	saved, err := a.store.SaveSettings(v)
	if err != nil {
		return saved, err
	}
	a.mu.Lock()
	running := a.running
	a.mu.Unlock()
	if spec, err := a.spec(false); err == nil {
		a.setState(func(s *State) { s.Restart = s.Phase == "ready" && running != "" && running != spec.Fingerprint() })
	}
	return saved, nil
}

func (a *App) spec(landscape bool) (android.Spec, error) {
	s := a.store.Settings()
	p, err := android.FindProfile(s.Profile, s.CustomW, s.CustomH, s.CustomDPI)
	if err != nil {
		return android.Spec{}, err
	}
	imgs := a.sdk.Images()
	if len(imgs) == 0 {
		return android.Spec{}, errors.New("no_sdk")
	}
	return android.Spec{Profile: p, Landscape: landscape, Tuning: android.Tune(s.Perf), Image: imgs[0]}, nil
}

func (a *App) wantLandscape(app *store.App) bool {
	s := a.store.Settings()
	switch s.Orientation {
	case "portrait":
		return false
	case "landscape":
		return true
	}
	if app != nil {
		if app.Override != "" {
			return app.Override == "landscape"
		}
		if app.Orientation != "" {
			return app.Orientation == "landscape"
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.state.Landscape
}

// ensure boots the device if needed. Callers hold a.life.
func (a *App) ensure(landscape bool) error {
	if !a.sdk.Ready() {
		return errors.New("no_sdk")
	}
	spec, err := a.spec(landscape)
	if err != nil {
		return err
	}
	d := a.emu.Device()
	if d.Online(a.ctx) && d.Booted(a.ctx) {
		a.mu.Lock()
		same := a.running == spec.Fingerprint()
		a.mu.Unlock()
		if same {
			return nil
		}
		a.setState(func(s *State) { s.Phase, s.Step, s.Error = "stopping", "", "" })
		a.emu.Stop(a.ctx)
	}
	a.setState(func(s *State) { s.Phase, s.Step, s.Error, s.Restart = "booting", "prepare", "", false })
	if err := a.emu.Boot(a.ctx, spec, func(step string) {
		a.setState(func(s *State) { s.Step = step })
	}); err != nil {
		a.emu.Stop(a.ctx)
		a.setState(func(s *State) { s.Phase, s.Step, s.Error = "idle", "", errCode(err) })
		return err
	}
	a.mu.Lock()
	a.running = spec.Fingerprint()
	a.mu.Unlock()
	a.setState(func(s *State) { s.Phase, s.Step, s.Landscape, s.Restart = "ready", "", landscape, false })
	return nil
}

func errCode(err error) string {
	var ie *android.InstallError
	if errors.As(err, &ie) {
		return ie.Error()
	}
	msg := err.Error()
	switch {
	case msg == "no_sdk", strings.HasPrefix(msg, "launch:"):
		return msg
	case strings.HasPrefix(msg, "boot:timeout"):
		return "boot:timeout"
	case strings.HasPrefix(msg, "boot:exited"):
		return "boot:exited"
	case strings.HasPrefix(msg, "disk:"):
		return "disk"
	case errors.Is(err, apkinfo.ErrInvalid):
		return "bad_apk"
	}
	return "generic"
}

func fail(err error) error { return errors.New(errCode(err)) }

// StartDevice boots the selected screen, or restarts into it.
func (a *App) StartDevice() error {
	a.life.Lock()
	defer a.life.Unlock()
	if err := a.ensure(a.wantLandscape(nil)); err != nil {
		return fail(err)
	}
	return nil
}

// StopDevice shuts down and saves a snapshot for a fast next start.
func (a *App) StopDevice() error {
	a.life.Lock()
	defer a.life.Unlock()
	a.setState(func(s *State) { s.Phase, s.Step = "stopping", "" })
	err := a.emu.Stop(a.ctx)
	a.mu.Lock()
	a.running = ""
	a.mu.Unlock()
	a.setState(func(s *State) { s.Phase, s.Step, s.Restart = "idle", "", false })
	if err != nil {
		return fail(err)
	}
	return nil
}

// SetOrientation rotates a running device, or records the choice for next boot.
func (a *App) SetOrientation(mode string) (bool, error) {
	s := a.store.Settings()
	s.Orientation = mode
	if _, err := a.store.SaveSettings(s); err != nil {
		return false, fail(err)
	}
	a.mu.Lock()
	ready, current := a.state.Phase == "ready", a.state.Landscape
	a.mu.Unlock()
	if !ready {
		return current, nil
	}
	if mode == "auto" {
		return current, nil
	}
	want := mode == "landscape"
	if err := a.emu.Device().SetLandscape(a.ctx, want); err != nil {
		return current, fail(err)
	}
	a.setState(func(st *State) { st.Landscape = want })
	return want, nil
}

// Rotate flips the running device between portrait and landscape.
func (a *App) Rotate() (bool, error) {
	a.mu.Lock()
	want, ready := !a.state.Landscape, a.state.Phase == "ready"
	a.mu.Unlock()
	if !ready {
		return !want, errors.New("not_running")
	}
	if err := a.emu.Device().SetLandscape(a.ctx, want); err != nil {
		return !want, fail(err)
	}
	a.setState(func(st *State) { st.Landscape = want })
	return want, nil
}

var keyCodes = map[string]int{"home": 3, "back": 4, "recents": 187, "volup": 24, "voldown": 25, "power": 26}

// Key sends a navigation key.
func (a *App) Key(name string) error {
	code, ok := keyCodes[name]
	if !ok {
		return errors.New("generic")
	}
	if err := a.emu.Device().Key(a.ctx, code); err != nil {
		return fail(err)
	}
	return nil
}

// Screenshot saves the screen to the Pictures folder and returns the path.
func (a *App) Screenshot() (string, error) {
	b, err := a.emu.Device().Screenshot(a.ctx)
	if err != nil {
		return "", fail(err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fail(err)
	}
	dir := filepath.Join(home, "Pictures", "GuyAPK")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fail(err)
	}
	p := filepath.Join(dir, "GuyAPK-"+time.Now().Format("20060102-150405")+".png")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		return "", fail(err)
	}
	return p, nil
}

// PickPackage opens a file dialog.
func (a *App) PickPackage() (string, error) {
	return runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title:   "APK",
		Filters: []runtime.FileFilter{{DisplayName: "Android (*.apk, *.xapk, *.apks, *.apkm)", Pattern: "*.apk;*.xapk;*.apks;*.apkm"}},
	})
}

// Inspect reads a package without installing it.
func (a *App) Inspect(path string) (*apkinfo.Info, error) {
	p, err := apkinfo.Prepare(path, filepath.Join(a.dataDir, "tmp"))
	if err != nil {
		return nil, fail(err)
	}
	defer p.Close()
	return &p.Info, nil
}

func (a *App) progress(step string) {
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "install", step)
	}
}

// Install boots if necessary, installs a package and optionally opens it.
func (a *App) Install(path string, launch bool) (*store.App, error) {
	a.life.Lock()
	defer a.life.Unlock()

	a.progress("read")
	p, err := apkinfo.Prepare(path, filepath.Join(a.dataDir, "tmp"))
	if err != nil {
		return nil, fail(err)
	}
	defer p.Close()

	entry := store.App{
		Package: p.Info.Package, Label: p.Info.Label, Version: p.Info.VersionName, Icon: p.Info.Icon,
		Orientation: p.Info.Orientation, ABIs: p.Info.ABIs, Size: p.Info.Size, InstalledAt: time.Now().Unix(),
	}
	if old, ok := a.store.App(p.Info.Package); ok {
		entry.Override, entry.LastPlayed = old.Override, old.LastPlayed
	}
	landscape := a.wantLandscape(&entry)

	a.progress("boot")
	if err := a.ensure(landscape); err != nil {
		return nil, fail(err)
	}
	d := a.emu.Device()
	apks := p.APKs
	if len(apks) > 1 {
		if abis := d.ABIs(a.ctx); len(abis) > 0 {
			apks = apkinfo.FilterSplits(apks, abis)
		}
	}
	a.progress("install")
	if err := d.Install(a.ctx, p.Info.Package, apks, p.OBBs); err != nil {
		return nil, fail(err)
	}
	if err := a.store.PutApp(entry); err != nil {
		return nil, fail(err)
	}
	if launch {
		a.progress("launch")
		if err := a.open(&entry, landscape); err != nil {
			return &entry, fail(err)
		}
	}
	a.progress("done")
	return &entry, nil
}

func (a *App) open(app *store.App, landscape bool) error {
	d := a.emu.Device()
	if err := d.SetLandscape(a.ctx, landscape); err == nil {
		a.setState(func(s *State) { s.Landscape = landscape })
	}
	if err := d.Launch(a.ctx, app.Package); err != nil {
		return err
	}
	app.LastPlayed = time.Now().Unix()
	return a.store.PutApp(*app)
}

// Launch opens an installed app, booting first if necessary.
func (a *App) Launch(pkg string) error {
	a.life.Lock()
	defer a.life.Unlock()
	app, ok := a.store.App(pkg)
	if !ok {
		return errors.New("generic")
	}
	landscape := a.wantLandscape(&app)
	if err := a.ensure(landscape); err != nil {
		return fail(err)
	}
	if err := a.open(&app, landscape); err != nil {
		return fail(err)
	}
	return nil
}

// SetAppOrientation stores a per-app orientation: "", portrait or landscape.
func (a *App) SetAppOrientation(pkg, mode string) error {
	app, ok := a.store.App(pkg)
	if !ok {
		return errors.New("generic")
	}
	if mode != "" && mode != "portrait" && mode != "landscape" {
		return errors.New("generic")
	}
	app.Override = mode
	return a.store.PutApp(app)
}

// Uninstall removes an app from the device and the library.
func (a *App) Uninstall(pkg string) error {
	a.life.Lock()
	defer a.life.Unlock()
	d := a.emu.Device()
	if a.sdk.HasTools() && d.Online(a.ctx) {
		if err := d.Uninstall(a.ctx, pkg); err != nil {
			return fail(err)
		}
	}
	return a.store.RemoveApp(pkg)
}

// Apps returns the library.
func (a *App) Apps() []store.App { return a.store.Apps() }

// RuntimePlan fetches what must be downloaded and the licence text.
func (a *App) RuntimePlan() (*android.Plan, error) {
	ctx, cancel := context.WithTimeout(a.ctx, 45*time.Second)
	defer cancel()
	managed := android.SDK{Root: filepath.Join(a.dataDir, "sdk")}
	plan, err := android.BuildPlan(ctx, managed)
	if err != nil {
		return nil, errors.New("network")
	}
	a.mu.Lock()
	a.plan = plan
	a.mu.Unlock()
	return plan, nil
}

// InstallRuntime downloads the plan. accepted must reflect the user's own
// agreement to the licence shown in the UI.
func (a *App) InstallRuntime(accepted bool) error {
	if !accepted {
		return errors.New("license")
	}
	a.mu.Lock()
	plan := a.plan
	if plan == nil || a.provCancel != nil {
		a.mu.Unlock()
		return errors.New("generic")
	}
	ctx, cancel := context.WithCancel(a.ctx)
	a.provCancel = cancel
	a.mu.Unlock()
	defer func() {
		cancel()
		a.mu.Lock()
		a.provCancel = nil
		a.mu.Unlock()
	}()

	managed := android.SDK{Root: filepath.Join(a.dataDir, "sdk")}
	err := android.Provision(ctx, managed, plan, func(p android.Progress) {
		runtime.EventsEmit(a.ctx, "provision", p)
	})
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled):
			return errors.New("cancelled")
		case strings.HasPrefix(err.Error(), "disk:"):
			return errors.New("disk")
		}
		return fmt.Errorf("network")
	}
	a.refreshSDK()
	return nil
}

// CancelRuntime stops a running download; it can resume later.
func (a *App) CancelRuntime() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.provCancel != nil {
		a.provCancel()
	}
}

// OpenDataFolder shows GuyAPK's data folder in the file manager.
func (a *App) OpenDataFolder() error {
	tool := "open"
	if goruntime.GOOS == "windows" {
		tool = "explorer.exe"
	}
	if err := exec.Command(tool, a.dataDir).Start(); err != nil {
		return errors.New("generic")
	}
	return nil
}
