package desktop

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	model "ouroboros/internal/desktop"
	"ouroboros/plugin"
)

func TestTrayAggregate(t *testing.T) {
	a := &Profile{source: &testSessions{activity: plugin.Activity{Running: 2}}}
	b := &Profile{source: &testSessions{activity: plugin.Activity{Running: 1, Waiting: 1}}}
	Together([]*Profile{a, b})
	if a.controller != b.controller {
		t.Fatal("profiles do not share tray")
	}
	got := a.controller.sample(context.Background())
	if got.State != "waiting" || got.Running != 3 || got.Waiting != 1 {
		t.Fatal(got)
	}
	b.source = &testSessions{activity: plugin.Activity{Running: 1}}
	if got := a.controller.sample(context.Background()); got.State != "working" || got.Running != 3 || got.Waiting != 0 {
		t.Fatal(got)
	}
	a.source, b.source = &testSessions{}, &testSessions{}
	if got := a.controller.sample(context.Background()); got.State != "idle" {
		t.Fatal(got)
	}
	b.source = &testSessions{err: errors.New("unavailable")}
	if got := a.controller.sample(context.Background()); got.State != "error" {
		t.Fatal(got)
	}
	b.source = nil
	if got := a.controller.sample(context.Background()); got.State != "error" {
		t.Fatal(got)
	}
	// A profile without a source still leaves the counts of the others.
	a.source, b.source = nil, &namedSessions{testSessions: testSessions{activity: plugin.Activity{Running: 2}}, names: []string{"Research"}}
	if got := a.controller.sample(context.Background()); got.State != "error" || got.Running != 2 || len(got.Sessions) != 1 {
		t.Fatal(got)
	}
}

func TestTrayLifetime(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("Linux or macOS node adapter")
	}
	p := &Profile{log: func(err error) { t.Errorf("tray error: %v", err) }}
	q := &Profile{}
	Together([]*Profile{p, q})
	c := p.controller
	started, ended := make(chan struct{}), make(chan struct{})
	var runs atomic.Int32
	c.run = func(ctx context.Context, _ func(context.Context) model.Status, _ func()) error {
		runs.Add(1)
		close(started)
		<-ctx.Done()
		close(ended)
		return ctx.Err()
	}
	p.start()
	q.start()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("not started")
	}
	p.stop()
	select {
	case <-ended:
	default:
		t.Fatal("stop returned before the tray ended")
	}
	q.stop()
	if runs.Load() != 1 {
		t.Fatal(runs.Load())
	}
}

func TestTrayOpenCoalesces(t *testing.T) {
	p := &Profile{log: func(err error) { t.Errorf("open: %v", err) }}
	Together([]*Profile{p})
	c := p.controller
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	c.launch = func() error { calls.Add(1); close(entered); <-release; return nil }
	c.open()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("not opened")
	}
	c.open()
	if calls.Load() != 1 {
		t.Fatal("opened duplicate GUI")
	}
	close(release)
}

// A profile that serves alone gets a tray of its own. Without a session bus
// that tray fails, and the log of the node says why.
func TestTrayAlone(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux node adapter")
	}
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path="+filepath.Join(t.TempDir(), "missing"))
	logged := make(chan error, 1)
	p := &Profile{log: func(err error) { logged <- err }}
	p.start()
	if p.controller == nil || len(p.controller.profiles) != 1 || p.controller.profiles[0] != p {
		t.Fatalf("controller: %+v", p.controller)
	}
	select {
	case err := <-logged:
		if !strings.Contains(err.Error(), "tray: connect session bus") {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the failure was not logged")
	}
	p.stop()
}

// A profile that serves alone gets a tray of its own, with only its sessions.
func TestTrayAloneOwnsOne(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("Linux or macOS node adapter")
	}
	old := runTray
	t.Cleanup(func() { runTray = old })
	sampled := make(chan model.Status, 1)
	runTray = func(ctx context.Context, sample func(context.Context) model.Status, _ func()) error {
		sampled <- sample(ctx)
		<-ctx.Done()
		return ctx.Err()
	}
	p := &Profile{source: &testSessions{activity: plugin.Activity{Running: 1}}, log: func(err error) { t.Errorf("tray error: %v", err) }}
	p.start()
	select {
	case got := <-sampled:
		if got.State != "working" || got.Running != 1 {
			t.Fatal(got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("not started")
	}
	if p.controller == nil || len(p.controller.profiles) != 1 || p.controller.profiles[0] != p {
		t.Fatalf("controller: %+v", p.controller)
	}
	p.stop()
}

// Nothing to stop: never started, and in no tray. No profiles share no tray.
func TestTrayNothing(t *testing.T) {
	(&Profile{}).stop()
	Together(nil)
}

// Errors of the tray go to the log of the first profile, except a stop.
func TestTrayReport(t *testing.T) {
	var logged []error
	p := &Profile{log: func(err error) { logged = append(logged, err) }}
	Together([]*Profile{p, {log: func(err error) { t.Errorf("second profile logged %v", err) }}})
	failure := errors.New("launch failed")
	for _, err := range []error{nil, context.Canceled, fmt.Errorf("tray: %w", context.Canceled), failure} {
		p.controller.report(err)
	}
	if len(logged) != 1 || logged[0] != failure {
		t.Fatalf("logged: %v", logged)
	}
	(&controller{}).report(failure) // no profile to log it: dropped
}

// Opening starts this binary again with the command gui and reports what
// went wrong: no binary, or one that failed.
func TestLaunchGUI(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("no sh")
	}
	old := executable
	t.Cleanup(func() { executable = old })
	gone := errors.New("no executable")
	executable = func() (string, error) { return "", gone }
	if err := launchGUI(); err != gone {
		t.Fatalf("no executable: %v", err)
	}
	// sh runs the script named by its argument: a script named gui stands in
	// for the GUI, in the working directory.
	dir := t.TempDir()
	t.Chdir(dir)
	executable = func() (string, error) { return sh, nil }
	if err := os.WriteFile("gui", []byte(`echo "$0" > launched`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := launchGUI(); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "launched")); err != nil || string(got) != "gui\n" {
		t.Fatalf("launched: %q %v", got, err)
	}
	if err := os.WriteFile("gui", []byte("exit 3"), 0o644); err != nil {
		t.Fatal(err)
	}
	if exit, ok := errors.AsType[*exec.ExitError](launchGUI()); !ok || exit.ExitCode() != 3 {
		t.Fatalf("failed GUI: %v", exit)
	}
}
