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

	// The new binary cannot take the place of what is there: a directory
	// that is not empty.
	dir := filepath.Join(t.TempDir(), "ouro")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(dir, "keep"), "old")
	if err := replace(src, dir); err == nil {
		t.Error("directory: want an error")
	}
	if _, err := os.Stat(filepath.Join(dir, "keep")); err != nil {
		t.Errorf("directory: %v", err)
	}
	if _, err := os.Stat(dir + ".new"); err == nil {
		t.Error("directory: left a temporary file")
	}
}

// A command that fails without saying why is reported with its own error.
func TestRunWithoutOutput(t *testing.T) {
	_, err := Updater{}.run(context.Background(), "", filepath.Join(t.TempDir(), "missing"))
	if err == nil || !strings.Contains(err.Error(), "no such file") {
		t.Fatalf("got %v", err)
	}
}
