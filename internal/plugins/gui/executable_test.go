package gui

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWatchInstalledExecutable(t *testing.T) {
	for _, action := range []string{"unchanged", "identical", "missing", "replaced", "not executable", "unreadable"} {
		t.Run(action, func(t *testing.T) {
			exe := filepath.Join(t.TempDir(), "ouro")
			install := func(text string, mode os.FileMode) {
				t.Helper()
				stage := exe + ".new"
				if err := os.WriteFile(stage, []byte(text), mode); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(stage, exe); err != nil {
					t.Fatal(err)
				}
			}
			install("old build", 0755)
			image, err := executableImage(exe)
			if err != nil {
				t.Fatal(err)
			}
			switch action {
			case "identical":
				install("old build", 0755)
			case "missing":
				if err := os.Remove(exe); err != nil {
					t.Fatal(err)
				}
			case "replaced":
				install("new build", 0755)
			case "not executable":
				install("new build", 0644)
			case "unreadable":
				install("new build", 0111)
			}
			ticks := make(chan time.Time, 1)
			ticks <- time.Now()
			close(ticks)
			w := newWindow(page{choices: []choice{{id: "profile"}}})
			defer w.Close()
			leaves := 0
			w.OnRestart(func() { leaves++ })
			watchImage(context.Background(), exe, image, ticks, w.restart)
			got, going, ok := w.Restart()
			if action != "replaced" {
				if ok || leaves != 0 {
					t.Fatalf("unexpected restart: %s, closes %d", got, leaves)
				}
				return
			}
			// No session was opened and no SessionRestart event was received.
			if !ok || got != exe || leaves != 1 || going.Profile != "profile" {
				t.Fatalf("restart = %q %+v %v, closes %d", got, going, ok, leaves)
			}
		})
	}
}

func TestExecutableWatcherStopsWithWindow(t *testing.T) {
	exe := filepath.Join(t.TempDir(), "ouro")
	if err := os.WriteFile(exe, []byte("build"), 0755); err != nil {
		t.Fatal(err)
	}
	image, err := executableImage(exe)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	watchImage(ctx, exe, image, make(chan time.Time), func(string) { called = true })
	if called {
		t.Fatal("closed window restarted")
	}
}

func TestExecutableImageErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := executableImage(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("a missing build has an image")
	}
	if _, err := executableImage(dir); err == nil {
		t.Fatal("a directory has an image")
	}
}

// watchExecutable watches the installed build until it changes, and gives up
// where it cannot tell which build runs.
func TestWatchExecutable(t *testing.T) {
	defer func(e func() (string, error), self string, every time.Duration) {
		executable, selfImage, watchInterval = e, self, every
	}(executable, selfImage, watchInterval)
	watchInterval = time.Millisecond
	dir := t.TempDir()
	exe := filepath.Join(dir, "ouro")
	if err := os.WriteFile(exe, []byte("old build"), 0755); err != nil {
		t.Fatal(err)
	}

	executable = func() (string, error) { return "", os.ErrNotExist }
	watchExecutable(context.Background(), func(string) { t.Fatal("no build, no change") })

	executable = func() (string, error) { return exe + " (deleted)", nil }
	selfImage = filepath.Join(dir, "gone")
	watchExecutable(context.Background(), func(string) { t.Fatal("no image, no change") })

	selfImage = ""
	executable = func() (string, error) { return exe, nil }
	changed := make(chan string, 1)
	go watchExecutable(context.Background(), func(path string) { changed <- path })
	time.Sleep(20 * time.Millisecond)
	if err := os.WriteFile(exe+".new", []byte("new build"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(exe+".new", exe); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-changed:
		if got != exe {
			t.Fatalf("changed %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the new build went unnoticed")
	}

	// On Linux the image of the running build is read through /proc, and
	// the install path loses the suffix the kernel adds to a replaced one.
	selfImage = exe
	executable = func() (string, error) { return exe + " (deleted)", nil }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	watchExecutable(ctx, func(string) { t.Fatal("a closed window changes nothing") })
}
