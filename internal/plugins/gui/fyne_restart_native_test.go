//go:build cgo && linux

package gui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"ouroboros/plugin"
)

// The real desktop loop must run on the process's main goroutine, not the
// testing package's worker goroutine. Only the disposable helper takes this path.
func init() {
	if os.Getenv("OURO_TEST_FYNE_PHASE") == "" {
		return
	}
	runtime.LockOSThread()
	if err := fyneRestartFixture(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func fyneRestartFixture() error {
	phase := os.Getenv("OURO_TEST_FYNE_PHASE")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	window := page{choices: []choice{{name: "fixture", id: "fixture"}}, session: "owner/s1", drafts: map[string]string{"0:owner/s1": "unsent text"}}
	if phase == "reopened" {
		going, err := resumed()
		if err != nil || going == nil || going.Profile != "fixture" || going.Session != "owner/s1" || going.Drafts["0:owner/s1"] != "unsent text" {
			return fmt.Errorf("lost handover: %+v %v", going, err)
		}
	}
	replace := func() error {
		data, err := os.ReadFile(exe)
		if err != nil {
			return err
		}
		if err := os.WriteFile(exe+".new", append(data, '\n'), 0755); err != nil {
			return err
		}
		return os.Rename(exe+".new", exe)
	}
	if phase == "before" {
		if err := replace(); err != nil {
			return err
		}
	}
	window.profiles = []*Profile{{sessions: &testSessions{
		list: func(context.Context) ([]plugin.SessionInfo, error) { return []plugin.SessionInfo{{ID: "s1"}}, nil },
		watch: func(context.Context, string) ([]plugin.SessionEvent, <-chan plugin.SessionEvent, error) {
			go func() {
				time.Sleep(250 * time.Millisecond)
				if phase == "reopened" {
					cancel()
					return
				}
				if phase != "before" {
					if err := replace(); err != nil {
						panic(err)
					}
				}
			}()
			return nil, make(chan plugin.SessionEvent), nil
		}}}}
	err = showFyneWindow(ctx, window)
	if phase == "reopened" {
		if !errors.Is(err, context.Canceled) {
			return fmt.Errorf("reopened window: %v", err)
		}
		fmt.Println("native window reopened with session and draft")
		return nil
	}
	r, ok := errors.AsType[*plugin.Restarting](err)
	if !ok {
		return fmt.Errorf("window did not restart after executable replacement: %v", err)
	}
	env := make([]string, 0, len(r.Env))
	for _, e := range r.Env {
		if !strings.HasPrefix(e, "OURO_TEST_FYNE_PHASE=") {
			env = append(env, e)
		}
	}
	env = append(env, "OURO_TEST_FYNE_PHASE=reopened")
	return syscall.Exec(r.Exe, os.Args, env)
}

// TestFyneInstalledBuildNative exercises the real desktop loop in a disposable
// subprocess. Run under xvfb-run with OURO_TEST_FYNE_NATIVE=1; normal unit tests
// must not open windows on the owner's desktop.
func TestFyneInstalledBuildNative(t *testing.T) {
	if os.Getenv("OURO_TEST_FYNE_NATIVE") != "1" {
		t.Skip("requires opt-in and a desktop or Xvfb")
	}
	for _, phase := range []string{"first", "before"} {
		t.Run(phase, func(t *testing.T) { testFyneInstalledBuildNative(t, phase) })
	}
}

func testFyneInstalledBuildNative(t *testing.T, phase string) {
	dir := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	in, err := os.Open(exe)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	helper := filepath.Join(dir, "fyne-restart-test")
	out, err := os.OpenFile(helper, os.O_CREATE|os.O_WRONLY, 0755)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.Copy(out, in)
	if err = errors.Join(err, out.Close()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, helper)
	cmd.Env = append(x11Environ(), "OURO_TEST_FYNE_PHASE="+phase, "XDG_CONFIG_HOME="+dir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("native restart failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "native window reopened with session and draft") {
		t.Fatalf("no reopened window: %s", output)
	}
}
