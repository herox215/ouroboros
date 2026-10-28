package memory

import (
	"context"
	"strings"
	"testing"
	"time"
)

// The changes of the archivist are listed with action, reason and whether
// they were undone; one is undone once, and a second undo is refused.
func TestArchivistChangesListAndUndo(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	e, _, err := s.Remember(ctx, "", "tea", "oolong", "session", false, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := s.Apply(ctx, Change{Action: "rewrite", ID: e.ID, Content: "sencha", Reason: "the owner changed their tea"})
	if err != nil {
		t.Fatal(err)
	}
	changes, err := s.Changes(ctx, 10)
	if err != nil || len(changes) != 1 {
		t.Fatalf("changes: %+v, %v", changes, err)
	}
	if c := changes[0]; c.ID != rec.ID || c.Action != "rewrite" || c.Reason != "the owner changed their tea" || c.Undone {
		t.Fatalf("listed as %+v", c)
	}
	if err := s.UndoChange(ctx, rec.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Get(ctx, e.ID); err != nil || got.Content != "oolong" {
		t.Fatalf("after the undo: %+v, %v", got, err)
	}
	changes, err = s.Changes(ctx, 10)
	if err != nil || len(changes) != 1 || !changes[0].Undone {
		t.Fatalf("undone not shown: %+v, %v", changes, err)
	}
	if err := s.UndoChange(ctx, rec.ID); err == nil || !strings.Contains(err.Error(), "undone already") {
		t.Fatalf("a second undo: %v", err)
	}
}

// Undoing a change to a core memory restores what goes into the next prompt.
func TestArchivistUndoCore(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	e, err := s.CreateCore(ctx, "who", "Lucas", "session")
	if err != nil {
		t.Fatal(err)
	}
	rec, err := s.Apply(ctx, Change{Action: "rewrite", ID: e.ID, Content: "Lucas, the owner", Reason: "shorter"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UndoChange(ctx, rec.ID); err != nil {
		t.Fatal(err)
	}
	core, err := s.Core(ctx)
	if err != nil || len(core) != 1 || core[0].Content != "Lucas" {
		t.Fatalf("core memories: %+v, %v", core, err)
	}
}

// An undo refuses to write over a memory that changed since the change, and
// names that memory instead.
func TestArchivistUndoRefusesNewerVersion(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	e, _, err := s.Remember(ctx, "", "tea", "oolong", "session", false, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := s.Apply(ctx, Change{Action: "rewrite", ID: e.ID, Content: "sencha", Reason: "shorter"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update(ctx, "", e.ID, 2, "tea", "matcha", "owner"); err != nil {
		t.Fatal(err)
	}
	err = s.UndoChange(ctx, rec.ID)
	if err == nil || !strings.Contains(err.Error(), "tea") || !strings.Contains(err.Error(), "changed since the change") {
		t.Fatalf("undo over a later edit: %v", err)
	}
	if got, err := s.Get(ctx, e.ID); err != nil || got.Content != "matcha" {
		t.Fatalf("the later edit was overwritten: %+v, %v", got, err)
	}
	changes, err := s.Changes(ctx, 10)
	if err != nil || len(changes) != 1 || changes[0].Undone {
		t.Fatalf("a refused undo marked the change: %+v, %v", changes, err)
	}
}
