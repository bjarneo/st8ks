package main

import (
	"embed"
	"fmt"
	"os"

	"github.com/go-logr/logr"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/linux"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
	"k8s.io/klog/v2"

	"st8ks/internal/shellenv"
)

//go:embed all:frontend/dist
var assets embed.FS

// version is set at build time with -ldflags "-X main.version=v1.2.3".
var version = "dev"

func main() {
	flags := parseFlags(os.Args[1:])
	if flags.Version {
		fmt.Println("st8ks " + version)
		return
	}
	if os.Getenv("ST8KS_DEBUG") == "" {
		// client-go logs every watch retry. The UI shows connection state.
		klog.SetLogger(logr.Discard())
	}
	if shellenv.Needed() {
		shellenv.Load()
	}

	app := NewApp(flags)
	err := wails.Run(&options.App{
		Title:            "st8ks",
		Width:            1440,
		Height:           900,
		MinWidth:         960,
		MinHeight:        600,
		BackgroundColour: &options.RGBA{R: 17, G: 18, B: 20, A: 255},
		AssetServer: &assetserver.Options{
			Assets:     assets,
			Middleware: app.middleware,
		},
		// ST8KS_HIDDEN starts without a window, for testing in a browser
		// against wails dev.
		StartHidden:              os.Getenv("ST8KS_HIDDEN") != "",
		OnStartup:                app.startup,
		OnShutdown:               app.shutdown,
		EnableDefaultContextMenu: true,
		Bind:                     []interface{}{app},
		Mac: &mac.Options{
			TitleBar:   mac.TitleBarHiddenInset(),
			Appearance: mac.DefaultAppearance,
			About:      &mac.AboutInfo{Title: "st8ks", Message: "A Kubernetes desktop client."},
		},
		Linux: &linux.Options{
			ProgramName:      "st8ks",
			WebviewGpuPolicy: linux.WebviewGpuPolicyOnDemand,
		},
		Windows: &windows.Options{
			Theme:                windows.SystemDefault,
			WebviewIsTransparent: false,
			WindowIsTranslucent:  false,
		},
	})
	if err != nil {
		println("Error:", err.Error())
		os.Exit(1)
	}
}
