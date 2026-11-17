//go:build cgo && production

package gui

import (
	"context"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/linux"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// Tests replace the native boundary without opening a desktop window.
var runWindow = wails.Run
var quitWindow = runtime.Quit
var emitWindow = runtime.EventsEmit
var repaintWindow = runtime.WindowSetBackgroundColour

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
		// A window is transparent only from the moment it is made, so it is
		// made see-through for good and the page decides how much of its
		// ground it paints: the options hold how see-through it is, and a
		// change of them needs no new window. The whole background colour is
		// clear as well, so that no part of the window behind the webview is
		// painted; the page paints everything that has to be read.
		BackgroundColour: &options.RGBA{R: 0, G: 0, B: 0, A: 0},
		Linux: &linux.Options{
			WindowIsTranslucent: true,
			// A non-nil Linux changes what wails would do alone, which is to
			// leave the GPU to the webview never: it says so here as well.
			WebviewGpuPolicy: linux.WebviewGpuPolicyNever,
		},
		// Cancellation must work even if the frontend never reaches DOM readiness.
		OnStartup: func(app context.Context) {
			window.onEmit(func(events []Event) { emitWindow(app, "ouro:events", events) })
			window.onEnded(func() { emitWindow(app, "ouro:ended") })
			// A restart does not close the window: the page is told, and opens
			// the thread on the node that runs anew by itself. The window goes
			// on with its own build of the front end until it is closed.
			window.onRestart(func() { emitWindow(app, "ouro:restarting") })
			// The page paints the ground of the window itself, and a see-through
			// ground is drawn only where the webview thinks something changed:
			// the page asks for this after it changed how see-through the ground
			// is. Setting the background colour again is what makes wails paint
			// the webview and the box behind it anew, and both stay clear, as
			// the colour given is clear.
			window.onRepaint(func() { repaintWindow(app, 0, 0, 0, 0) })
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
