//go:build cgo && production

package gui

import (
	"context"
	"io/fs"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// Tests replace the native boundary without opening a desktop window.
var runWindow = wails.Run
var quitWindow = runtime.Quit

func showWindow(ctx context.Context, page page) error {
	frontend, err := fs.Sub(assets, "frontend")
	if err != nil {
		return err
	}
	window := newWindowContext(ctx, page)
	defer window.Close()
	done := make(chan struct{})
	defer close(done)
	err = runWindow(&options.App{
		Title: "Ouroboros",
		Width: 1180, Height: 780,
		MinWidth: 360, MinHeight: 260,
		AssetServer: &assetserver.Options{Assets: frontend},
		Bind:        []interface{}{window},
		// Cancellation must work even if the frontend never reaches DOM readiness.
		OnStartup: func(app context.Context) {
			// A tool that asked for a new build closes the window, and the
			// process this one makes room for opens it again, see Restart.
			window.OnRestart(func() { quitWindow(app) })
			go func() {
				select {
				case <-ctx.Done():
					quitWindow(app)
				case <-done:
				}
			}()
		},
	})
	if err != nil {
		return err
	}
	if exe, going, ok := window.Restart(); ok {
		return handOver(exe, going)
	}
	return ctx.Err()
}
