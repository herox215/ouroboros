package update

import (
	"context"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
)

func TestRevisionFromBuildInfo(t *testing.T) {
	info := &debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "vcs", Value: "git"}, {Key: "vcs.revision", Value: "abc"}}}
	if got := revision(info); got != "abc" {
		t.Fatalf("revision = %q", got)
	}
	if got := revision(&debug.BuildInfo{}); got != "" {
		t.Fatalf("revision of a build without commit = %q", got)
	}
	if got := revision(nil); got != "" {
		t.Fatalf("revision without build information = %q", got)
	}
	if got := Revision(); got != "" {
		t.Fatalf("a test binary carries no commit, got %q", got)
	}
}

// A replacement that fails leaves the old binary as it was and no temporary
// file behind: the node can still be started and updated again.
func TestReplaceFailureKeepsOldBinary(t *testing.T) {
	write := func(path, content string) string {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
			t.Fatal(err)
		}
		return path
	}
	src := write(filepath.Join(t.TempDir(), "built"), "new")
	exe := write(filepath.Join(t.TempDir(), "ouro"), "old")
	unchanged := func(name string) {
		t.Helper()
		if got, err := os.ReadFile(exe); err != nil || string(got) != "old" {
			t.Errorf("%s: binary = %q, %v", name, got, err)
		}
	}

	// The build produced no binary.
	if err := replace(filepath.Join(t.TempDir(), "missing"), exe); err == nil {
		t.Error("missing build: want an error")
	}
	unchanged("missing build")
	if _, err := os.Stat(exe + ".new"); err == nil {
		t.Error("missing build: left a temporary file")
	}

	// The temporary file next to the binary cannot be written.
	if err := os.Mkdir(exe+".new", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := replace(src, exe); err == nil {
		t.Error("blocked temporary file: want an error")
	}
	unchanged("blocked temporary file")

	os.Remove(exe + ".new")

	// The binary before cannot be kept: a directory that is not empty is
	// where it would go.
	if err := os.MkdirAll(filepath.Join(Previous(exe), "keep"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := replace(src, exe); err == nil {
		t.Error("blocked previous: want an error")
	}
	unchanged("blocked previous")
	if _, err := os.Stat(exe + ".new"); err == nil {
		t.Error("blocked previous: left a temporary file")
	}
	os.RemoveAll(Previous(exe))

	// The new binary cannot take the place of the old one: the old one is
	// put back.
	renames := 0
	rename = func(from, to string) error {
		if renames++; renames == 2 {
			return os.ErrPermission
		}
		return os.Rename(from, to)
	}
	t.Cleanup(func() { rename = os.Rename })
	if err := replace(src, exe); err == nil {
		t.Error("blocked binary: want an error")
	}
	unchanged("blocked binary")
	if _, err := os.Stat(exe + ".new"); err == nil {
		t.Error("blocked binary: left a temporary file")
	}
}

// Back installs the binary before the last replacement again, and a second
// Back the one after it.
func TestBack(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "ouro")
	if err := Back(exe); err != ErrNoPrevious {
		t.Fatalf("without a binary before: %v", err)
	}
	os.WriteFile(exe, []byte("v1"), 0o755)
	built := filepath.Join(dir, "built")
	os.WriteFile(built, []byte("v2"), 0o755)
	if err := replace(built, exe); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"v1", "v2"} {
		if err := Back(exe); err != nil {
			t.Fatal(err)
		}
		if got, _ := os.ReadFile(exe); string(got) != want {
			t.Fatalf("binary %q, want %q", got, want)
		}
	}
	// What cannot be looked at is an error of its own.
	os.Chmod(dir, 0o200)
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	if err := Back(exe); err == nil || err == ErrNoPrevious {
		t.Errorf("unreadable: %v", err)
	}
}

// A command that fails without saying why is reported with its own error.
func TestRunWithoutOutput(t *testing.T) {
	_, err := Updater{}.run(context.Background(), "", filepath.Join(t.TempDir(), "missing"))
	if err == nil || !strings.Contains(err.Error(), "no such file") {
		t.Fatalf("got %v", err)
	}
}
