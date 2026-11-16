//go:build cgo && production

package gui

import (
	"context"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// Tests replace the native boundary without opening a desktop window.
var runWindow = wails.Run
var quitWindow = runtime.Quit
var emitWindow = runtime.EventsEmit

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
		AssetServer: &assetserver.Options{Assets: frontend, Handler: pictures{dir: p.dir.Pictures()}.handler()},
		Bind:        []interface{}{window},
		// Cancellation must work even if the frontend never reaches DOM readiness.
		OnStartup: func(app context.Context) {
			window.onEmit(func(events []Event) { emitWindow(app, "ouro:events", events) })
			window.onEnded(func() { emitWindow(app, "ouro:ended") })
			// A restart does not close the window: the page is told, and opens
			// the thread on the node that runs anew by itself. The window goes
			// on with its own build of the front end until it is closed.
			window.onRestart(func() { emitWindow(app, "ouro:restarting") })
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
	return ctx.Err()
}
