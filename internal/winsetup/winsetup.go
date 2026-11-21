//go:build windows

// Package winsetup performs the per-user Windows install: no administrator
// rights, no system-wide changes, and it never takes over the default .apk app.
package winsetup

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	appName    = "GuyAPK"
	progID     = "GuyAPK.Package"
	uninstKey  = `Software\Microsoft\Windows\CurrentVersion\Uninstall\GuyAPK`
	classesKey = `Software\Classes\`
	noWindow   = 0x08000000

	MBOK          = 0x0
	MBOKCancel    = 0x1
	MBYesNoCancel = 0x3
	MBIconError   = 0x10
	MBIconQuest   = 0x20
	MBIconWarn    = 0x30
	MBIconInfo    = 0x40
	IDOK          = 1
	IDCancel      = 2
	IDYes         = 6
	IDNo          = 7
)

var extensions = []string{".apk", ".xapk", ".apks", ".apkm"}

// MessageBox shows a native dialog.
func MessageBox(text, title string, flags uint32) int {
	t, _ := windows.UTF16PtrFromString(text)
	c, _ := windows.UTF16PtrFromString(title)
	r, _ := windows.MessageBox(0, t, c, flags)
	return int(r)
}

// InstallDir is the per-user program location.
func InstallDir() string {
	return filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", appName)
}

// DataDir holds settings, the library and any downloaded Android runtime.
func DataDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, appName), nil
}

func shortcutPaths() ([]string, error) {
	desktop, err := windows.KnownFolderPath(windows.FOLDERID_Desktop, 0)
	if err != nil {
		return nil, err
	}
	programs, err := windows.KnownFolderPath(windows.FOLDERID_Programs, 0)
	if err != nil {
		return nil, err
	}
	return []string{filepath.Join(desktop, appName+".lnk"), filepath.Join(programs, appName+".lnk")}, nil
}

// CreateShortcuts adds Desktop and Start menu entries. Paths travel through the
// environment so that no value is ever parsed as PowerShell code.
func CreateShortcuts(exe string) error {
	links, err := shortcutPaths()
	if err != nil {
		return err
	}
	const script = `$s=New-Object -ComObject WScript.Shell;$l=$s.CreateShortcut($env:GUY_LNK);` +
		`$l.TargetPath=$env:GUY_EXE;$l.WorkingDirectory=$env:GUY_DIR;$l.IconLocation=$env:GUY_EXE+',0';` +
		`$l.Description='GuyAPK';$l.Save()`
	for _, link := range links {
		cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script)
		cmd.Env = append(os.Environ(), "GUY_LNK="+link, "GUY_EXE="+exe, "GUY_DIR="+filepath.Dir(exe))
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: noWindow}
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("shortcut %s: %w: %s", link, err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

func setStrings(root registry.Key, path string, values map[string]string) error {
	k, _, err := registry.CreateKey(root, path, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	for name, v := range values {
		if err := k.SetStringValue(name, v); err != nil {
			return err
		}
	}
	return nil
}

// Register adds the Apps & Features entry and an "Open with" handler. It leaves
// the user's default app for .apk files unchanged.
func Register(exe, version string, sizeKB uint32) error {
	root := registry.CURRENT_USER
	if err := setStrings(root, uninstKey, map[string]string{
		"DisplayName":     appName,
		"DisplayVersion":  version,
		"Publisher":       appName,
		"DisplayIcon":     exe + ",0",
		"InstallLocation": filepath.Dir(exe),
		"UninstallString": `"` + exe + `" --uninstall`,
	}); err != nil {
		return err
	}
	k, _, err := registry.CreateKey(root, uninstKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	k.SetDWordValue("EstimatedSize", sizeKB)
	k.SetDWordValue("NoModify", 1)
	k.SetDWordValue("NoRepair", 1)
	k.Close()

	if err := setStrings(root, classesKey+progID, map[string]string{"": "Android Package (GuyAPK)"}); err != nil {
		return err
	}
	if err := setStrings(root, classesKey+progID+`\DefaultIcon`, map[string]string{"": exe + ",0"}); err != nil {
		return err
	}
	if err := setStrings(root, classesKey+progID+`\shell\open\command`, map[string]string{"": `"` + exe + `" "%1"`}); err != nil {
		return err
	}
	for _, ext := range extensions {
		if err := setStrings(root, classesKey+ext+`\OpenWithProgids`, map[string]string{progID: ""}); err != nil {
			return err
		}
	}
	notifyAssoc()
	return nil
}

func notifyAssoc() {
	const shcneAssocChanged, shcnfIDList = 0x08000000, 0
	windows.NewLazySystemDLL("shell32.dll").NewProc("SHChangeNotify").
		Call(shcneAssocChanged, shcnfIDList, 0, uintptr(unsafe.Pointer(nil)))
}

func deleteTree(root registry.Key, path string) error {
	k, err := registry.OpenKey(root, path, registry.ENUMERATE_SUB_KEYS)
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	names, _ := k.ReadSubKeyNames(-1)
	k.Close()
	for _, n := range names {
		if err := deleteTree(root, path+`\`+n); err != nil {
			return err
		}
	}
	return registry.DeleteKey(root, path)
}

// Unregister removes everything Register and CreateShortcuts added.
func Unregister() error {
	root := registry.CURRENT_USER
	var first error
	keep := func(err error) {
		if err != nil && first == nil {
			first = err
		}
	}
	keep(deleteTree(root, uninstKey))
	keep(deleteTree(root, classesKey+progID))
	for _, ext := range extensions {
		if k, err := registry.OpenKey(root, classesKey+ext+`\OpenWithProgids`, registry.SET_VALUE); err == nil {
			if err := k.DeleteValue(progID); err != nil && !errors.Is(err, registry.ErrNotExist) {
				keep(err)
			}
			k.Close()
		}
	}
	if links, err := shortcutPaths(); err == nil {
		for _, l := range links {
			if err := os.Remove(l); err != nil && !os.IsNotExist(err) {
				keep(err)
			}
		}
	} else {
		keep(err)
	}
	notifyAssoc()
	return first
}

// RemoveAfterExit deletes dir shortly after this process ends, because a
// running program cannot delete its own file. The path is passed by
// environment variable so it is never parsed as part of the command.
func RemoveAfterExit(dir string) error {
	cmd := exec.Command("cmd.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: noWindow,
		CmdLine:       `cmd.exe /d /c ping -n 3 127.0.0.1 >nul & rmdir /s /q "%GUY_RM%"`,
	}
	cmd.Env = append(os.Environ(), "GUY_RM="+dir)
	cmd.Dir = os.TempDir()
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Process.Release()
	time.Sleep(50 * time.Millisecond)
	return nil
}
