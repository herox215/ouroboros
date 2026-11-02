package plugin_test

import (
	"context"
	"testing"

	"ouroboros/internal/config"
	"ouroboros/internal/home"
	"ouroboros/internal/memory"
)

// The changes the archivist made are listed, the newest first, and one of
// them can be taken back once: what it changed is as it was. A change taken
// back twice fails.
func TestOwnerListsAndUndoesMemoryChanges(t *testing.T) {
	o, dir, _ := built(t, config.Config{Name: "alpha", Model: "m", BaseURL: "http://x/v1"})
	ctx := context.Background()
	store, err := memory.Open(home.Dir(dir).History())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	e, err := store.Create(ctx, "", "drink", "oolong", "owner")
	if err != nil {
		t.Fatal(err)
	}
	rec, err := store.Apply(ctx, memory.Change{Action: "rewrite", ID: e.ID, Content: "oolong tea", Reason: "more precise"})
	if err != nil {
		t.Fatal(err)
	}
	changes, err := o.MemoryChanges(ctx, 10)
	if err != nil || len(changes) != 1 || changes[0].ID != rec.ID || changes[0].Action != "rewrite" || changes[0].Reason != "more precise" {
		t.Fatalf("the changes: %+v, %v", changes, err)
	}
	if err := o.UndoMemoryChange(ctx, rec.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Read(ctx, "", e.ID); err != nil || got.Content != "oolong" {
		t.Fatalf("not as it was: %+v, %v", got, err)
	}
	if err := o.UndoMemoryChange(ctx, rec.ID); err == nil {
		t.Fatal("a change was taken back twice")
	}
}
