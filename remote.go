package main

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"strings"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"guyapk/internal/remote"
)

// remoteHost is what a paired iPad may do. It is a separate type so that none
// of these methods are exposed to the desktop frontend binding.
type remoteHost struct{ a *App }

func (h remoteHost) RemoteStatus() remote.Status {
	h.a.mu.Lock()
	defer h.a.mu.Unlock()
	s := h.a.state
	return remote.Status{Phase: s.Phase, Step: s.Step, Landscape: s.Landscape}
}

func (h remoteHost) RemoteApps() []remote.AppInfo {
	apps := h.a.store.Apps()
	out := make([]remote.AppInfo, 0, len(apps))
	for _, x := range apps {
		out = append(out, remote.AppInfo{Package: x.Package, Label: x.Label, Icon: x.Icon})
	}
	return out
}

func (h remoteHost) RemoteStart() error            { return h.a.StartDevice() }
func (h remoteHost) RemoteLaunch(pkg string) error { return h.a.Launch(pkg) }
func (h remoteHost) RemoteRotate() (bool, error)   { return h.a.Rotate() }

func (h remoteHost) RemoteKey(name string) error {
	switch name {
	case "back", "home", "recents", "volup", "voldown":
		return h.a.Key(name)
	}
	return errors.New("generic")
}

type remoteSource struct{ a *App }

func (s remoteSource) Open(ctx context.Context) (remote.Stream, error) {
	src := &remote.Scrcpy{Device: s.a.emu.Device(), CacheDir: filepath.Join(s.a.dataDir, "remote")}
	return src.Open(ctx)
}

// remoteBind is empty in release builds. Automated tests set it at link time
// to stay on loopback.
var remoteBind string

func (a *App) remoteServer() *remote.Server {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.remote == nil {
		a.remote = remote.New(remote.Options{
			Addr:   net.ParseIP(remoteBind),
			Dir:    filepath.Join(a.dataDir, "remote"),
			Host:   remoteHost{a},
			Source: remoteSource{a},
			OnChange: func() {
				if a.ctx != nil {
					runtime.EventsEmit(a.ctx, "remote", a.remoteServer().Info())
				}
			},
		})
	}
	return a.remote
}

// RemoteInfo reports the iPad remote status.
func (a *App) RemoteInfo() remote.Info { return a.remoteServer().Info() }

// RemoteEnable starts or stops listening on the local network.
func (a *App) RemoteEnable(on bool) (remote.Info, error) {
	s := a.remoteServer()
	if !on {
		s.Disable()
		return s.Info(), nil
	}
	info, err := s.Enable()
	if err != nil {
		for _, code := range []string{"remote:no_lan", "remote:port"} {
			if strings.HasPrefix(err.Error(), code) {
				return info, errors.New(code)
			}
		}
		return info, errors.New("generic")
	}
	return info, nil
}

// RemoteNewPIN replaces the pairing PIN.
func (a *App) RemoteNewPIN() remote.Info { return a.remoteServer().NewPIN() }

// RemoteUnpairAll revokes every paired iPad.
func (a *App) RemoteUnpairAll() (remote.Info, error) {
	info, err := a.remoteServer().UnpairAll()
	if err != nil {
		return info, errors.New("generic")
	}
	return info, nil
}
