//go:build cgo && production

package gui

import (
	"context"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// Tests replace the native boundary without opening a desktop window.
var runWindow = wails.Run
var quitWindow = runtime.Quit
var emitWindow = runtime.EventsEmit

// restartEffectDuration is how long the page has to paint that ouro starts
// anew before the process is replaced: a stalled frontend must not block it.
const restartEffectDuration = 700 * time.Millisecond

func showWindow(ctx context.Context, p *profile, name string) error {
	frontend, err := frontendFiles()
	if err != nil {
		return err
	}
	window := newWindowContext(ctx, p, name)
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
			window.onEmit(func(events []Event) { emitWindow(app, "ouro:events", events) })
			window.onEnded(func() { emitWindow(app, "ouro:ended") })
			window.onRestart(func() {
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
	if exe, ok := window.pendingRestart(); ok {
		return handOver(exe)
	}
	return ctx.Err()
}
