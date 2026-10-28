package memory

import (
	"context"
	"testing"
	"time"
)

// Two memories that say the same are candidates, are merged in one change,
// and the change can be undone.
func TestArchivistCandidatesMergeUndo(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	a, _, err := s.Remember(ctx, "", "minerva-zugang", "Zugang zu minerva über SSH als root.", "session", false, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := s.Remember(ctx, "", "minerva-ssh-access", "Zugang zu minerva per SSH als root.", "session", false, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	pairs, err := s.Pairs(ctx, 10)
	if err != nil || len(pairs) != 1 {
		t.Fatalf("pairs: %+v, %v", pairs, err)
	}
	candidates, err := s.Candidates(ctx, time.Time{}, 0, 10)
	if err != nil || len(candidates) != 2 {
		t.Fatalf("candidates: %+v, %v", candidates, err)
	}
	rec, err := s.Apply(ctx, Change{
		Action: "merge", IDs: []string{a.ID, b.ID}, Key: "minerva-ssh-access",
		Content: "Zugang zu minerva per SSH als root.", Reason: "both say the same about the access",
	})
	if err != nil {
		t.Fatal(err)
	}
	current, err := s.Current(ctx, 10)
	if err != nil || len(current) != 1 || current[0].Key != "minerva-ssh-access" {
		t.Fatalf("after merge: %+v, %v", current, err)
	}
	changes, err := s.Changes(ctx, 10)
	if err != nil || len(changes) != 1 || changes[0].Action != "merge" || changes[0].Reason == "" {
		t.Fatalf("log: %+v, %v", changes, err)
	}
	if err := s.UndoChange(ctx, rec.ID); err != nil {
		t.Fatal(err)
	}
	current, err = s.Current(ctx, 10)
	if err != nil || len(current) != 2 {
		t.Fatalf("after undo: %+v, %v", current, err)
	}
}

// A rewrite may not say more than its source: the change fails whole, and
// nothing of it is logged.
func TestArchivistRefusesToSayMore(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	e, _, err := s.Remember(ctx, "", "k", "short", "session", false, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(ctx, Change{Action: "rewrite", ID: e.ID, Content: string(make([]byte, 4096)), Reason: "longer"}); err == nil {
		t.Fatal("a rewrite that says more than its source was applied")
	}
	changes, err := s.Changes(ctx, 10)
	if err != nil || len(changes) != 0 {
		t.Fatalf("log after a refused change: %+v, %v", changes, err)
	}
}

// What was examined in its version is no candidate again; a changed one is.
func TestArchivistExaminesOncePerVersion(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	e, _, err := s.Remember(ctx, "", "k", "a memory of a state", "session", false, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetMeta(ctx, "archivist/seen/"+e.ID, "1"); err != nil {
		t.Fatal(err)
	}
	candidates, err := s.Candidates(ctx, time.Now(), 0, 10)
	if err != nil || len(candidates) != 0 {
		t.Fatalf("examined memory is a candidate again: %+v, %v", candidates, err)
	}
	marker := time.Now().Add(-time.Hour)
	if _, _, err := s.Remember(ctx, "", "k", "a memory of another state", "session", false, time.Time{}); err != nil {
		t.Fatal(err)
	}
	candidates, err = s.Candidates(ctx, marker, 0, 10)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("changed memory is no candidate: %+v, %v", candidates, err)
	}
}
