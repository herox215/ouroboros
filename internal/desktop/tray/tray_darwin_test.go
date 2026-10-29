//go:build darwin && cgo

package tray

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"ouroboros/internal/desktop"
)

func TestNativeModel(t *testing.T) {
	s := desktop.Status{State: "waiting", Running: 2, Waiting: 1, Sessions: []string{"A session"}}
	got := nativeModel(s)
	if got.Label != s.Label() || !got.Attention || got.Running != 2 || len(got.Sessions) != 1 || got.Sessions[0] != "A session" {
		t.Fatal(got)
	}
	if nativeModel(desktop.Status{State: "idle"}).Attention {
		t.Fatal("idle needs no attention")
	}
}

func TestRunCallbacksAndCancellation(t *testing.T) {
	if Run(context.Background(), nil, nil) == nil {
		t.Fatal("accepted nil callbacks")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := Run(ctx, func(context.Context) desktop.Status { t.Fatal("sampled canceled context"); return desktop.Status{} }, func() { t.Fatal("opened UI") })
	if err != nil {
		t.Fatal(err)
	}
}

// fakeHelper makes Run start the shell script instead of the menu-bar helper,
// and send a fresh status at the interval.
func fakeHelper(t *testing.T, every time.Duration, script string) {
	t.Helper()
	oldCommand, oldInterval := helperCommand, interval
	t.Cleanup(func() { helperCommand, interval = oldCommand, oldInterval })
	helperCommand = func(ctx context.Context) (*exec.Cmd, error) {
		return exec.CommandContext(ctx, "/bin/sh", "-c", script), nil
	}
	interval = every
}

func idle(context.Context) desktop.Status { return desktop.Status{State: "idle"} }

// The helper is this binary again, with the one argument of the helper.
func TestHelperCommandIsThisBinary(t *testing.T) {
	cmd, err := helperCommand(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	exe, _ := os.Executable()
	if cmd.Path != exe || len(cmd.Args) != 2 || cmd.Args[1] != "--ouro-internal-macos-tray" {
		t.Fatalf("helper: %s %q", cmd.Path, cmd.Args)
	}
	// It gets a fresh status every two seconds.
	if interval != 2*time.Second {
		t.Fatalf("interval %s", interval)
	}
}

// Without a helper to start, Run fails and says why.
func TestRunWithoutHelper(t *testing.T) {
	oldCommand := helperCommand
	t.Cleanup(func() { helperCommand = oldCommand })
	gone := errors.New("no executable")
	helperCommand = func(ctx context.Context) (*exec.Cmd, error) { return nil, gone }
	if err := Run(context.Background(), idle, func() {}); err != gone {
		t.Fatalf("no executable: %v", err)
	}
	// Pipes that cannot be made: stdin or stdout is taken already.
	helperCommand = func(ctx context.Context) (*exec.Cmd, error) {
		cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "exit 0")
		cmd.Stdin = strings.NewReader("")
		return cmd, nil
	}
	if err := Run(context.Background(), idle, func() {}); err == nil || !strings.Contains(err.Error(), "Stdin already set") {
		t.Fatalf("stdin taken: %v", err)
	}
	helperCommand = func(ctx context.Context) (*exec.Cmd, error) {
		cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "exit 0")
		cmd.Stdout = io.Discard
		return cmd, nil
	}
	if err := Run(context.Background(), idle, func() {}); err == nil || !strings.Contains(err.Error(), "Stdout already set") {
		t.Fatalf("stdout taken: %v", err)
	}
	helperCommand = func(ctx context.Context) (*exec.Cmd, error) {
		return exec.CommandContext(ctx, filepath.Join(t.TempDir(), "missing")), nil
	}
	if err := Run(context.Background(), idle, func() {}); err == nil || !strings.Contains(err.Error(), "tray: start macOS helper") {
		t.Fatalf("missing helper: %v", err)
	}
}

// The helper gets one status per line, again and again, until it goes away.
// A helper that goes away on its own is an error, with or without its exit
// status.
func TestRunFeedsHelperUntilItExits(t *testing.T) {
	// Status after status: the helper says it has read three.
	fakeHelper(t, time.Millisecond, `read a; read b; read c; echo open; exec sleep 10`)
	ctx, cancel := context.WithCancel(context.Background())
	var samples atomic.Int32
	sample := func(context.Context) desktop.Status { samples.Add(1); return desktop.Status{State: "idle"} }
	if err := Run(ctx, sample, cancel); err != nil {
		t.Fatal(err)
	}
	if samples.Load() < 3 {
		t.Fatalf("sampled %d times", samples.Load())
	}
	// The next status would come only in an hour: the helper is gone before.
	fakeHelper(t, time.Hour, `read first; exit 0`)
	err := Run(context.Background(), idle, func() { t.Error("opened UI") })
	if err == nil || err.Error() != "tray: macOS helper: helper exited" {
		t.Fatalf("helper exited: %v", err)
	}
	fakeHelper(t, time.Hour, `read first; exit 3`)
	err = Run(context.Background(), idle, func() {})
	if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 3 {
		t.Fatalf("helper failed: %v", err)
	}
}

// "open" from the helper opens the UI. Cancellation ends the helper and Run,
// without an error.
func TestRunOpensAndStops(t *testing.T) {
	fakeHelper(t, time.Hour, `read first; echo other; echo open; exec sleep 10`)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	opened := make(chan struct{})
	result := make(chan error, 1)
	go func() { result <- Run(ctx, idle, func() { close(opened) }) }()
	select {
	case <-opened:
	case <-time.After(10 * time.Second):
		t.Fatal("not opened")
	}
	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not stop")
	}
}

// A helper that no longer reads its status is an error, unless the tray was
// being stopped anyway.
func TestRunHelperStopsReading(t *testing.T) {
	for _, stopping := range []bool{false, true} {
		fakeHelper(t, time.Hour, `exec 0<&-; echo open; exec sleep 10`)
		ctx, cancel := context.WithCancel(context.Background())
		closed := make(chan struct{})
		sample := func(context.Context) desktop.Status {
			<-closed
			if stopping {
				cancel()
			}
			return desktop.Status{State: "idle"}
		}
		err := Run(ctx, sample, func() { close(closed) })
		cancel()
		if stopping && err != nil {
			t.Fatalf("stopping: %v", err)
		}
		if !stopping && (err == nil || !strings.Contains(err.Error(), "tray: update macOS helper") || !errors.Is(err, syscall.EPIPE)) {
			t.Fatalf("helper stopped reading: %v", err)
		}
	}
}
