package session

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRelativeProjectFromWorkspace(t *testing.T) {
	s := New(&fakeAgent{})
	defer s.Close()
	s.Workspace = t.TempDir()
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
	root, one, two := t.TempDir(), t.TempDir(), t.TempDir()
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
