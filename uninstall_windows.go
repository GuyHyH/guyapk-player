//go:build windows

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"guyapk/internal/android"
	"guyapk/internal/winsetup"
)

// handleCLI runs the uninstaller when requested and reports whether it did.
func handleCLI() bool {
	mode := ""
	for _, a := range os.Args[1:] {
		switch a {
		case "--uninstall":
			mode = "ask"
		case "--uninstall-silent":
			mode = "silent"
		case "--uninstall-silent-all":
			mode = "silent-all"
		}
	}
	if mode == "" {
		return false
	}
	uninstall(mode)
	return true
}

func uninstall(mode string) {
	const title = "ถอนการติดตั้ง GuyAPK"
	removeData := mode == "silent-all"
	if mode == "ask" {
		switch winsetup.MessageBox(
			"ต้องการลบแอปที่ติดตั้ง ข้อมูลในแอป และระบบ Android ที่ GuyAPK ดาวน์โหลดไว้ด้วยหรือไม่?\n\n"+
				"ใช่ = ลบทั้งหมด (กู้คืนไม่ได้)\nไม่ = ลบเฉพาะโปรแกรม เก็บข้อมูลไว้\n\n"+
				"Also delete installed apps, their data and the Android system GuyAPK downloaded?\n"+
				"Yes = delete everything (cannot be undone)   No = keep data",
			title, winsetup.MBYesNoCancel|winsetup.MBIconQuest) {
		case winsetup.IDYes:
			removeData = true
		case winsetup.IDNo:
		default:
			return
		}
	}

	data, derr := winsetup.DataDir()
	if derr == nil {
		sdk := android.Locate(filepath.Join(data, "sdk"))
		if sdk.HasTools() {
			m := &android.Manager{SDK: sdk}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			if m.Device().Online(ctx) {
				m.Stop(ctx)
			}
			cancel()
		}
	}

	err := winsetup.Unregister()
	if removeData && derr == nil && filepath.Base(data) == "GuyAPK" {
		os.RemoveAll(data)
	}
	// Only ever remove the folder this installer created.
	if exe, e := os.Executable(); e == nil && strings.EqualFold(filepath.Dir(exe), winsetup.InstallDir()) {
		if e := winsetup.RemoveAfterExit(filepath.Dir(exe)); e != nil && err == nil {
			err = e
		}
	}
	if mode != "ask" {
		return
	}
	if err != nil {
		winsetup.MessageBox("ถอนการติดตั้งไม่สมบูรณ์: "+err.Error(), title, winsetup.MBOK|winsetup.MBIconWarn)
		return
	}
	winsetup.MessageBox("ถอนการติดตั้ง GuyAPK เรียบร้อยแล้ว\nGuyAPK has been removed.", title, winsetup.MBOK|winsetup.MBIconInfo)
}
