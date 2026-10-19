package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRelativeProjectFromWorkspace(t *testing.T) {
	s := New(&fakeAgent{})
	defer s.Close()
	s.Workspace = canonicalTempDir(t)
	project := filepath.Join(s.Workspace, "project")
	if err := os.Mkdir(project, 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDirectory(context.Background(), "s1", "project"); err != nil {
		t.Fatal(err)
	}
	if got := directoryAt(t, s, "s1"); got != project {
		t.Fatalf("got %q", got)
	}
}

func TestDirectorySaveFailureDoesNotChangeSession(t *testing.T) {
	root, one, two := t.TempDir(), canonicalTempDir(t), canonicalTempDir(t)
	s, _, _ := archived(t, &fakeAgent{}, root, time.Now())
	defer s.Close()
	ctx := context.Background()
	if err := s.SetDirectory(ctx, "s1", one); err != nil {
		t.Fatal(err)
	}
	s.archive.Archive = unavailableArchive(t)
	if err := s.SetDirectory(ctx, "s1", two); err == nil {
		t.Fatal("save failure not reported")
	}
	if got := directoryAt(t, s, "s1"); got != one {
		t.Fatalf("failed change applied: %q", got)
	}
}

// An initial directory that cannot be resolved is refused and leaves new
// sessions without one, as is any once the Service is closed.
func TestInitialDirectoryRefused(t *testing.T) {
	s := New(&fakeAgent{})
	defer s.Close()
	t.Setenv("HOME", "")
	for _, path := range []string{filepath.Join(t.TempDir(), "missing"), "~/project"} {
		if err := s.SetInitialDirectory(path); err == nil {
			t.Errorf("%s was taken", path)
		}
	}
	id, err := s.New(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := directoryAt(t, s, id); got != "" {
		t.Fatalf("new session in %q", got)
	}
	s.Close()
	if err := s.SetInitialDirectory(t.TempDir()); !errors.Is(err, ErrStopped) {
		t.Fatalf("after the close: %v", err)
	}
}

// A relative initial directory is taken from the working directory, and is
// refused once that is gone.
func TestInitialDirectoryWithoutWorkingDirectory(t *testing.T) {
	gone := t.TempDir()
	t.Chdir(gone)
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	s := service(t, &fakeAgent{})
	if err := s.SetInitialDirectory("project"); err == nil {
		t.Fatal("taken without a working directory")
	}
}

// A session gets the initial directory also when the archive cannot keep
// it, which is logged.
func TestInitialDirectoryThatCannotBeKept(t *testing.T) {
	initial := canonicalTempDir(t)
	broken := filepath.Join(t.TempDir(), "history")
	if err := os.WriteFile(broken, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s, _, l := archived(t, &fakeAgent{}, broken, time.Now())
	if err := s.SetInitialDirectory(initial); err != nil {
		t.Fatal(err)
	}
	if got := directoryAt(t, s, "s1"); got != initial {
		t.Fatalf("directory %q", got)
	}
	if !l.has("the directory of the session s1 cannot be kept") {
		t.Fatalf("log %q", l.lines)
	}
}
