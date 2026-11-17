//go:build cgo && production

package gui

import (
	"context"
	"errors"
	"io/fs"
	"testing"
	"time"

	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/linux"

	"ouroboros/plugin"
)

func TestNativeWindow(t *testing.T) {
	oldRun, oldQuit, oldRepaint := runWindow, quitWindow, repaintWindow
	t.Cleanup(func() { runWindow, quitWindow, repaintWindow = oldRun, oldQuit, oldRepaint })
	p := &profile{sessions: &fakeSessions{}}
	quit := make(chan struct{}, 1)
	quitWindow = func(context.Context) { quit <- struct{}{} }
	painted := make(chan [4]uint8, 1)
	repaintWindow = func(_ context.Context, r, g, b, a uint8) { painted <- [4]uint8{r, g, b, a} }
	runWindow = func(o *options.App) error {
		if o.Width != 1180 || o.Height != 780 || len(o.Bind) != 1 {
			t.Fatalf("options: %+v", o)
		}
		// The window is made see-through from its first moment, always: the
		// page decides how much of its ground it paints.
		if o.Linux == nil || !o.Linux.WindowIsTranslucent {
			t.Fatalf("window is not translucent: %+v", o.Linux)
		}
		// A Linux of our own would change the GPU policy wails chose alone.
		if o.Linux.WebviewGpuPolicy != linux.WebviewGpuPolicyNever {
			t.Fatalf("webview gpu policy: %v", o.Linux.WebviewGpuPolicy)
		}
		if o.BackgroundColour == nil || o.BackgroundColour.A != 0 {
			t.Fatalf("background colour: %+v", o.BackgroundColour)
		}
		if state := o.Bind[0].(*Window).InitialState(); state.Profile != "Luna" || state.Thread != "owner/"+plugin.ThreadID {
			t.Fatalf("binding: %+v", state)
		}
		for _, name := range []string{"index.html", "app.js", "app.css"} {
			if _, err := fs.ReadFile(o.AssetServer.Assets, name); err != nil {
				t.Fatal(err)
			}
		}
		o.OnStartup(context.Background())
		// The page asks for this after it painted another ground: the window
		// paints its whole face anew, and what it paints is clear, as it is.
		o.Bind[0].(*Window).Repaint()
		select {
		case got := <-painted:
			if got != [4]uint8{0, 0, 0, 0} {
				t.Fatalf("repaint colour: %v", got)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the page could not ask the window to paint anew")
		}
		return nil
	}
	if err := showWindow(context.Background(), p, "Luna"); err != nil {
		t.Fatal(err)
	}

	// No DOM-ready event: a stalled frontend must not prevent cancellation.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runWindow = func(o *options.App) error {
		o.OnStartup(context.Background())
		cancel()
		select {
		case <-quit:
		case <-time.After(5 * time.Second):
			t.Fatal("cancellation did not close the window")
		}
		return nil
	}
	if err := showWindow(ctx, p, "Luna"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled: %v", err)
	}

	sentinel := errors.New("native startup failed")
	runWindow = func(*options.App) error { return sentinel }
	if err := showWindow(context.Background(), p, "Luna"); !errors.Is(err, sentinel) {
		t.Fatalf("startup: %v", err)
	}
}

type emitted struct {
	name string
	data []interface{}
}

// What the thread tells reaches the page as Wails events. A restart is told
// too, and the window stays: the page opens the thread on the new node itself.
func TestNativeWindowStreamsAndRestarts(t *testing.T) {
	oldEmit, oldRun, oldQuit := emitWindow, runWindow, quitWindow
	t.Cleanup(func() { emitWindow, runWindow, quitWindow = oldEmit, oldRun, oldQuit })
	events := make(chan emitted, 8)
	emitWindow = func(_ context.Context, name string, data ...interface{}) {
		events <- emitted{name, data}
	}
	quit := make(chan struct{}, 1)
	quitWindow = func(context.Context) { quit <- struct{}{} }
	live := make(chan plugin.SessionEvent, 2)
	p := &profile{sessions: &fakeSessions{live: live}}
	runWindow = func(o *options.App) error {
		o.OnStartup(context.Background())
		w := o.Bind[0].(*Window)
		if _, err := w.Open(); err != nil {
			t.Fatal(err)
		}
		live <- plugin.SessionEvent{Kind: plugin.SessionAnswer, Text: "done"}
		select {
		case got := <-events:
			if got.name != "ouro:events" {
				t.Fatalf("event %q", got.name)
			}
			if len(got.data) != 1 {
				t.Fatalf("payload: %+v", got.data)
			}
			batch, ok := got.data[0].([]Event)
			if !ok || len(batch) != 1 || batch[0].Text != "done" {
				t.Fatalf("batch: %+v", got.data[0])
			}
		case <-time.After(5 * time.Second):
			t.Fatal("what the thread told did not reach the page")
		}
		// Every restart is told, also the one after a told restart: the page
		// reopens the thread on the node that runs anew each time.
		for _, exe := range []string{"/new/ouro", "/other/ouro"} {
			live <- plugin.SessionEvent{Kind: plugin.SessionRestart, Text: exe}
			select {
			case got := <-events:
				if got.name != "ouro:restarting" {
					t.Fatalf("event %q", got.name)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("the restart for %s was not announced", exe)
			}
		}
		select {
		case <-quit:
			t.Fatal("a restart closed the window")
		case <-time.After(50 * time.Millisecond):
		}
		return nil
	}
	if err := showWindow(context.Background(), p, "Luna"); err != nil {
		t.Fatalf("restart: %v", err)
	}
}
