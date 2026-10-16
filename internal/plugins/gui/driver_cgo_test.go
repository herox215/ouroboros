//go:build cgo && production

package gui

import (
	"context"
	"errors"
	"io/fs"
	"slices"
	"testing"
	"time"

	"github.com/wailsapp/wails/v2/pkg/options"

	"ouroboros/plugin"
)

func TestNativeWindow(t *testing.T) {
	oldRun, oldQuit := runWindow, quitWindow
	t.Cleanup(func() { runWindow, quitWindow = oldRun, oldQuit })
	p := page{choices: []choice{{"Luna", "aaa"}}}
	quit := make(chan struct{}, 1)
	quitWindow = func(context.Context) { quit <- struct{}{} }
	runWindow = func(o *options.App) error {
		if o.Width != 1180 || o.Height != 780 || len(o.Bind) != 1 {
			t.Fatalf("options: %+v", o)
		}
		if state := o.Bind[0].(*Window).InitialState(); state.Profiles[0].ID != "aaa" {
			t.Fatalf("binding: %+v", state)
		}
		for _, name := range []string{"index.html", "app.js", "style.css"} {
			if _, err := fs.ReadFile(o.AssetServer.Assets, name); err != nil {
				t.Fatal(err)
			}
		}
		o.OnStartup(context.Background())
		return nil
	}
	if err := showWindow(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// No DOM-ready event: a stalled frontend must not prevent cancellation.
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
	if err := showWindow(ctx, p); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled: %v", err)
	}
	sentinel := errors.New("native startup failed")
	runWindow = func(*options.App) error { return sentinel }
	if err := showWindow(context.Background(), p); !errors.Is(err, sentinel) {
		t.Fatalf("startup: %v", err)
	}
}

// A tool that asks for a new build closes the window, and the process that
// takes its place opens it again with the profile and the session it showed.
func TestNativeWindowRestart(t *testing.T) {
	oldRun, oldQuit := runWindow, quitWindow
	t.Cleanup(func() { runWindow, quitWindow = oldRun, oldQuit })
	live := make(chan plugin.SessionEvent, 1)
	p := page{
		choices: []choice{{name: "Luna", id: "aaa"}},
		profiles: []*Profile{{sessions: &testSessions{watch: func(context.Context, string) ([]plugin.SessionEvent, <-chan plugin.SessionEvent, error) {
			return nil, live, nil
		}}}},
	}
	quit := make(chan struct{}, 1)
	quitWindow = func(context.Context) { quit <- struct{}{} }
	runWindow = func(o *options.App) error {
		o.OnStartup(context.Background())
		w := o.Bind[0].(*Window)
		snapshot, err := w.OpenSession(0, "owner/s1")
		if err != nil {
			t.Fatal(err)
		}
		live <- plugin.SessionEvent{Kind: plugin.SessionRestart, Text: "/new/ouro"}
		w.PollSession(snapshot.Token)
		select {
		case <-quit:
		case <-time.After(5 * time.Second):
			t.Fatal("a restart did not close the window")
		}
		return nil
	}
	r, ok := errors.AsType[*plugin.Restarting](showWindow(context.Background(), p))
	if !ok || r.Exe != "/new/ouro" {
		t.Fatalf("restart: %v", r)
	}
	if !slices.Contains(r.Env, resumeEnv+`={"profile":"aaa","session":"owner/s1"}`) {
		t.Fatalf("env: %v", r.Env)
	}
}
