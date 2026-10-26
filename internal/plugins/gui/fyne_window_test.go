//go:build cgo

package gui

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"

	"ouroboros/plugin"
)

// fyneDesktop puts the test driver in place of the desktop: the window runs
// until it is closed, as on the desktop.
func fyneDesktop(t *testing.T) {
	t.Helper()
	oldApp, oldRun := newFyneApp, showAndRun
	t.Cleanup(func() { newFyneApp, showAndRun = oldApp, oldRun })
	application := newLoopApp(t)
	newFyneApp = func() fyne.App { return application }
	showAndRun = func(w fyne.Window) {
		closed := make(chan struct{})
		fyne.DoAndWait(func() {
			w.SetOnClosed(func() { close(closed) })
			w.Show()
		})
		<-closed
	}
}

// installed is a build in place of the running one, see watchExecutable.
func installed(t *testing.T) string {
	t.Helper()
	oldExe, oldSelf, oldEvery := executable, selfImage, watchInterval
	t.Cleanup(func() { executable, selfImage, watchInterval = oldExe, oldSelf, oldEvery })
	exe := filepath.Join(t.TempDir(), "ouro")
	if err := os.WriteFile(exe, []byte("old build"), 0o755); err != nil {
		t.Fatal(err)
	}
	executable = func() (string, error) { return exe, nil }
	selfImage = ""
	watchInterval = 5 * time.Millisecond
	return exe
}

func install(t *testing.T, exe string) {
	t.Helper()
	if err := os.WriteFile(exe+".new", []byte("new build"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(exe+".new", exe); err != nil {
		t.Fatal(err)
	}
}

// The window closes with its context and says so.
func TestFyneWindowClosesWithContext(t *testing.T) {
	fyneDesktop(t)
	installed(t)
	sessions := newE2ESessions()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- showWindow(ctx, page{choices: []choice{{name: "herox", id: "herox"}}, profiles: []*Profile{{sessions: sessions}}})
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("closed with %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the window stays open")
	}
}

// A new build replaces the window: it hands over the profile, the session
// and what was written and not sent.
func TestFyneWindowHandsOverToNewBuild(t *testing.T) {
	fyneDesktop(t)
	exe := installed(t)
	sessions := newE2ESessions()
	opened := make(chan struct{})
	open := sessions.watch
	sessions.watch = func(ctx context.Context, id string) ([]plugin.SessionEvent, <-chan plugin.SessionEvent, error) {
		defer func() {
			select {
			case <-opened:
			default:
				close(opened)
			}
		}()
		return open(ctx, id)
	}
	done := make(chan error, 1)
	go func() {
		done <- showWindow(context.Background(), page{
			choices:  []choice{{name: "herox", id: "herox"}},
			profiles: []*Profile{{sessions: sessions}},
			drafts:   map[string]string{"0:owner/s1": "half a thought"},
		})
	}()
	<-opened
	time.Sleep(100 * time.Millisecond)
	install(t, exe)
	var err error
	select {
	case err = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the window ignores the new build")
	}
	r, ok := errors.AsType[*plugin.Restarting](err)
	if !ok || r.Exe != exe {
		t.Fatalf("closed with %v", err)
	}
	going := handedOver(t, r)
	if going.Profile != "herox" || going.Session != "owner/"+plugin.ThreadID || going.Drafts["0:owner/s1"] != "half a thought" {
		t.Fatalf("handed over %+v", going)
	}
}

// A window replaced before it opened a session goes on with the thread.
func TestFyneWindowHandsOverTheThreadBeforeItOpened(t *testing.T) {
	fyneDesktop(t)
	exe := installed(t)
	sessions := newE2ESessions()
	asked := make(chan struct{})
	sessions.open = func(ctx context.Context) (bool, error) {
		close(asked)
		<-ctx.Done()
		return false, ctx.Err()
	}
	done := make(chan error, 1)
	go func() {
		done <- showWindow(context.Background(), page{choices: []choice{{name: "herox", id: "herox"}}, profiles: []*Profile{{sessions: sessions}}})
	}()
	<-asked
	install(t, exe)
	var err error
	select {
	case err = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the window ignores the new build")
	}
	r, ok := errors.AsType[*plugin.Restarting](err)
	if !ok {
		t.Fatalf("closed with %v", err)
	}
	if going := handedOver(t, r); going.Session != "owner/"+plugin.ThreadID {
		t.Fatalf("handed over %+v", going)
	}
}

func handedOver(t *testing.T, r *plugin.Restarting) resume {
	t.Helper()
	for _, e := range r.Env {
		if v, ok := strings.CutPrefix(e, resumeEnv+"="); ok {
			var going resume
			if err := json.Unmarshal([]byte(v), &going); err != nil {
				t.Fatal(err)
			}
			return going
		}
	}
	t.Fatal("nothing handed over")
	return resume{}
}

// The window opens in the size it closed with, the first time in its
// default, and never below the smallest it allows.
func TestFyneWindowKeepsItsSize(t *testing.T) {
	application := newLoopApp(t)
	if got := windowSize(application); got != fyne.NewSize(1180, 780) {
		t.Fatalf("first size %v", got)
	}
	keepWindowSize(application, fyne.Size{})
	keepWindowSize(application, fyne.NewSize(900, 640))
	if got := windowSize(application); got != fyne.NewSize(900, 640) {
		t.Fatalf("kept size %v", got)
	}
	keepWindowSize(application, fyne.NewSize(100, 100))
	if got := windowSize(application); got != fyne.NewSize(360, 420) {
		t.Fatalf("a tiny size opens as %v", got)
	}
}
