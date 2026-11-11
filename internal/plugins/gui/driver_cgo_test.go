//go:build cgo && production

package gui

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/wailsapp/wails/v2/pkg/options"

	"ouroboros/plugin"
)

func TestNativeWindow(t *testing.T) {
	oldRun, oldQuit := runWindow, quitWindow
	t.Cleanup(func() { runWindow, quitWindow = oldRun, oldQuit })
	p := &profile{sessions: &fakeSessions{}}
	quit := make(chan struct{}, 1)
	quitWindow = func(context.Context) { quit <- struct{}{} }
	runWindow = func(o *options.App) error {
		if o.Width != 1180 || o.Height != 780 || len(o.Bind) != 1 {
			t.Fatalf("options: %+v", o)
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

// What the thread tells reaches the page as Wails events; a restart closes the
// window for the build it names.
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
		live <- plugin.SessionEvent{Kind: plugin.SessionRestart, Text: "/new/ouro"}
		select {
		case got := <-events:
			if got.name != "ouro:restarting" {
				t.Fatalf("event %q", got.name)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("the restart was not announced")
		}
		select {
		case <-quit:
			t.Fatal("the window closed before the effect could paint")
		default:
		}
		select {
		case <-quit:
		case <-time.After(5 * time.Second):
			t.Fatal("a restart did not close the window")
		}
		return nil
	}
	r, ok := errors.AsType[*plugin.Restarting](showWindow(context.Background(), p, "Luna"))
	if !ok || r.Exe != "/new/ouro" {
		t.Fatalf("restart: %v", r)
	}
	if !slices.Equal(r.Env, os.Environ()) {
		t.Fatalf("env: %v", r.Env)
	}
}
