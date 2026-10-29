package memory

import (
	"context"
	"strings"
	"testing"
)

// A merge into the key of one of its memories makes a new version of that
// one, and memories that are not core make none that is.
func TestArchivistMergeOfMemoriesNotCore(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	a := keep(t, s, "", "tea", "green tea in the morning", false)
	b := keep(t, s, "", "tea-time", "green tea in the morning, always", false)
	if _, err := s.Apply(ctx, Change{Action: "merge", IDs: []string{a.ID, b.ID}, Content: "green tea in the morning, always", Reason: "the same"}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Get(ctx, a.ID); err != nil || got.Version != 2 || got.Core || got.Archived {
		t.Fatalf("merged: %+v, %v", got, err)
	}
}

// Over the budget, as the core memories can be after it was lowered, what
// is not core is still written, and what would be core is refused.
func TestOverTheCoreBudget(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	core := keep(t, s, "", "core", "a core memory", true)
	if _, err := s.db.Exec("UPDATE memories SET content=? WHERE id=?", strings.Repeat("c", MaxCore+1), core.ID); err != nil {
		t.Fatal(err)
	}
	keep(t, s, "", "drink", "oolong", false)
	e := keep(t, s, "", "drink", "green tea", false)
	if _, err := s.Update(ctx, "", e.ID, e.Version, "drink", "black tea", "owner"); err != nil {
		t.Fatalf("update: %v", err)
	}
	no, yes := false, true
	for _, c := range []Change{
		{Action: "rewrite", ID: e.ID, Content: "strong black tea"},
		{Action: "scope", ID: e.ID},
		{Action: "core", ID: e.ID, Core: &no},
	} {
		c.Reason = "over the budget"
		if _, err := s.Apply(ctx, c); err != nil {
			t.Errorf("%s: %v", c.Action, err)
		}
	}
	for _, c := range []Change{
		{Action: "scope", ID: core.ID},
		{Action: "core", ID: e.ID, Core: &yes},
	} {
		c.Reason = "over the budget"
		if _, err := s.Apply(ctx, c); err == nil {
			t.Errorf("%s of %s was applied", c.Action, c.ID)
		}
	}
}

// List gives no more than its limit, and Changes up to 100.
func TestLimitsOfListAndChanges(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	e := keep(t, s, "", "a", "one", false)
	keep(t, s, "", "b", "two", false)
	if l, err := s.List(ctx, "", false, 1); err != nil || len(l.Memories) != 1 {
		t.Fatalf("list: %+v, %v", l, err)
	}
	for i := range 21 {
		if _, err := s.Apply(ctx, Change{Action: "rewrite", ID: e.ID, Content: strings.Repeat("x", i+1), Reason: "again"}); err != nil {
			t.Fatal(err)
		}
	}
	if changes, err := s.Changes(ctx, 100); err != nil || len(changes) != 21 {
		t.Fatalf("changes: %d, %v", len(changes), err)
	}
}

// An undo carries the version it writes into the earlier change that left
// the memory as it is again, past a record it cannot read, and into none
// that left another version.
func TestArchivistUndoCarriesOnlyTheVersionItRestores(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	f := keep(t, s, "", "f", "another memory", false)
	broken, err := s.Apply(ctx, Change{Action: "rewrite", ID: f.ID, Content: "another one", Reason: "shorter"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec("UPDATE memory_changes SET undo='{' WHERE id=?", broken.ID); err != nil {
		t.Fatal(err)
	}
	e := keep(t, s, "", "e", "first text", false)
	var recs []ChangeRecord
	for _, text := range []string{"second text", "third text", "fourth text"} {
		rec, err := s.Apply(ctx, Change{Action: "rewrite", ID: e.ID, Content: text, Reason: "again"})
		if err != nil {
			t.Fatal(err)
		}
		recs = append(recs, rec)
	}
	if err := s.UndoChange(ctx, recs[2].ID); err != nil {
		t.Fatal(err)
	}
	if err := s.UndoChange(ctx, recs[0].ID); err == nil {
		t.Fatal("took back a change whose memory changed since")
	}
	if err := s.UndoChange(ctx, recs[1].ID); err != nil {
		t.Fatalf("the change that left the memory as it is again: %v", err)
	}
	if got, err := s.Get(ctx, e.ID); err != nil || got.Content != "second text" {
		t.Fatalf("after the undos: %+v, %v", got, err)
	}
	// An archive keeps the version: of three in a row, the undo of the last
	// carries its version into both before it.
	g := keep(t, s, "", "g", "a third memory", false)
	var archives []ChangeRecord
	for range 3 {
		rec, err := s.Apply(ctx, Change{Action: "archive", ID: g.ID, Reason: "done"})
		if err != nil {
			t.Fatal(err)
		}
		archives = append(archives, rec)
	}
	for _, i := range []int{2, 1} {
		if err := s.UndoChange(ctx, archives[i].ID); err != nil {
			t.Fatalf("undo of archive %d: %v", i, err)
		}
	}
}
