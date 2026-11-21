package main

import (
	"embed"
	"log"
	"runtime"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	if handleCLI() {
		return
	}
	app := NewApp()
	err := wails.Run(&options.App{
		Title:            "GuyAPK",
		Width:            1360,
		Height:           860,
		MinWidth:         1080,
		MinHeight:        700,
		Frameless:        runtime.GOOS != "darwin",
		BackgroundColour: &options.RGBA{R: 12, G: 10, B: 9, A: 255},
		AssetServer:      &assetserver.Options{Assets: assets},
		OnStartup:        app.startup,
		OnBeforeClose:    app.beforeClose,
		OnShutdown:       app.shutdown,
		Bind:             []any{app},
		DragAndDrop:      &options.DragAndDrop{EnableFileDrop: true},
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId:               "th.guyapk.player.single-instance",
			OnSecondInstanceLaunch: app.onSecondInstance,
		},
		Windows: &windows.Options{
			WebviewIsTransparent: false,
			WindowIsTranslucent:  false,
			Theme:                windows.Dark,
		},
		Mac: &mac.Options{
			TitleBar:   mac.TitleBarHiddenInset(),
			Appearance: mac.NSAppearanceNameDarkAqua,
			OnFileOpen: app.openFile,
			About: &mac.AboutInfo{
				Title:   "GuyAPK",
				Message: "เล่นแอป Android บนคอมพิวเตอร์ · Run Android apps on your computer",
			},
		},
	})
	if err != nil {
		log.Fatal(err)
	}
}
