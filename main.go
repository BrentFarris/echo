package main

import (
	"context"
	"embed"
	"os"

	"github.com/brent/echo/internal/services"
	"github.com/brent/echo/internal/webserver"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/linux"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
)

//go:embed all:frontend/dist
var assets embed.FS

//go:embed build/appicon.png
var appIcon []byte

func main() {
	// Headless mode serves the web UI from a plain browser without starting the
	// Wails desktop window. It is selected with `-headless` and never touches
	// the desktop path below.
	if hasHeadlessFlag(os.Args[1:]) {
		os.Exit(runHeadless())
	}

	app := NewApp()
	webAccess := webserver.New(app.System, assets)
	services.SetWebAccessController(app.System, webAccess)

	err := wails.Run(&options.App{
		Title:     "Echo",
		Width:     1200,
		Height:    780,
		MinWidth:  900,
		MinHeight: 620,
		AssetServer: &assetserver.Options{
			Assets:     assets,
			Handler:    app.System.WorkspaceIconHandler(),
			Middleware: app.System.WorkspaceIconMiddleware,
		},
		BackgroundColour:         &options.RGBA{R: 18, G: 18, B: 20, A: 1},
		EnableDefaultContextMenu: true,
		DragAndDrop: &options.DragAndDrop{
			EnableFileDrop: true,
		},
		OnStartup: func(ctx context.Context) {
			app.startup(ctx)
			_, _ = webAccess.ApplyWebAccessSettings(app.System.LoadState().WebAccess)
		},
		OnShutdown: func(ctx context.Context) {
			_ = webAccess.Shutdown(ctx)
			app.shutdown(ctx)
		},
		Linux: &linux.Options{
			Icon: appIcon,
		},
		Mac: &mac.Options{
			About: &mac.AboutInfo{
				Title: "Echo",
				Icon:  appIcon,
			},
		},
		Bind: []interface{}{
			app.System,
		},
	})
	if err != nil {
		println("Error:", err.Error())
	}
}
