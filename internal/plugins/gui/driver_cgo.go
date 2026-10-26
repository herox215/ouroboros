//go:build cgo && production

package gui

import (
	"context"
	"embed"
	"io/fs"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

//go:embed frontend/*
var assets embed.FS

// Tests replace the native boundary without opening a desktop window.
var runWindow = wails.Run
var quitWindow = runtime.Quit
var emitWindow = runtime.EventsEmit

const restartEffectDuration = 700 * time.Millisecond

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
			// Give the frontend time to paint the transition before replacing
			// the process. A stalled frontend must not block the restart.
			window.OnRestart(func() {
				emitWindow(app, "ouro:restarting")
				go func() {
					timer := time.NewTimer(restartEffectDuration)
					defer timer.Stop()
					select {
					case <-timer.C:
						quitWindow(app)
					case <-done:
					case <-ctx.Done():
					}
				}()
			})
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
