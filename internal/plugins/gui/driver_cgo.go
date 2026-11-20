//go:build cgo && production

package gui

import (
	"context"
	"os"

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

// framesEnv makes WebKitGTK hand the frames of the page to the window through
// shared memory instead of as buffers of the GPU.
const framesEnv = "WEBKIT_DMABUF_RENDERER_FORCE_SHM"

// shareFrames is what lets the webview compose its page where wails alone
// would not let it. The window itself then never draws with the GPU: it was
// that which ended a window on NVIDIA under Wayland with "Error 71 (Protocol
// error)", and why wails leaves the GPU to the webview never. The page is
// composed with the GPU all the same, and the finished frame alone is copied.
// WebKit reads this once, before its first webview, so it is said before the
// window is made; an owner who said otherwise keeps what they said.
func shareFrames() {
	if _, set := os.LookupEnv(framesEnv); !set {
		_ = os.Setenv(framesEnv, "1")
	}
}

func showWindow(ctx context.Context, p *profile, name string) error {
	frontend, err := frontendFiles()
	if err != nil {
		return err
	}
	window := newWindowContext(ctx, p, name)
	defer window.Close()
	done := make(chan struct{})
	defer close(done)
	shareFrames()
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
			// The webview composes its page and does not paint it in software.
			// A see-through webview painted in software is wiped whole at
			// every paint by WebKitGTK 2.54, which then paints anew only what
			// changed (https://bugs.webkit.org/show_bug.cgi?id=324543): all
			// but the last change went clear, and what was painted twice
			// showed twice as dense. Composed, every frame is whole. Wails
			// hands its OnDemand to WebKit as "always" and its Always as "on
			// demand", which WebKit takes for the same and warns about, so it
			// is OnDemand that says always here; see shareFrames for what
			// keeps that from ending the window.
			WebviewGpuPolicy: linux.WebviewGpuPolicyOnDemand,
		},
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
