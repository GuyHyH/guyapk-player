//go:build windows

// Command installer is the small self-contained GuyAPK setup program.
package main

import (
	"bytes"
	_ "embed"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ulikunitz/xz"

	"guyapk/internal/winsetup"
)

//go:embed payload.xz
var payload []byte

//go:embed version.txt
var version string

const title = "ติดตั้ง GuyAPK"

func main() {
	silent := false
	for _, a := range os.Args[1:] {
		if strings.EqualFold(a, "/S") {
			silent = true
		}
	}
	v := strings.TrimSpace(version)
	if !silent {
		msg := fmt.Sprintf("ติดตั้ง GuyAPK %s สำหรับผู้ใช้นี้?\nไม่ต้องใช้สิทธิ์ผู้ดูแลระบบ\n\nInstall GuyAPK %s for this user?\nNo administrator rights needed.", v, v)
		if winsetup.MessageBox(msg, title, winsetup.MBOKCancel|winsetup.MBIconQuest) != winsetup.IDOK {
			return
		}
	}
	exe, err := install(v)
	if err != nil {
		if silent {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		winsetup.MessageBox("ติดตั้งไม่สำเร็จ กรุณาปิด GuyAPK แล้วลองอีกครั้ง\nInstallation failed. Close GuyAPK and try again.\n\n"+err.Error(),
			title, winsetup.MBOK|winsetup.MBIconError)
		os.Exit(1)
	}
	if !silent {
		exec.Command(exe).Start()
	}
}

func install(version string) (string, error) {
	dir := winsetup.InstallDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	exe := filepath.Join(dir, "GuyAPK.exe")
	tmp := exe + ".new"
	defer os.Remove(tmp)

	r, err := xz.NewReader(bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	f, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	n, err := io.Copy(f, r)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	if err := os.Rename(tmp, exe); err != nil {
		return "", err
	}
	if err := winsetup.Register(exe, version, uint32(n/1024)); err != nil {
		return "", err
	}
	if err := winsetup.CreateShortcuts(exe); err != nil {
		return "", err
	}
	return exe, nil
}
