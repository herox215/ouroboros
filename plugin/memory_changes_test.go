package plugin_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"ouroboros/internal/config"
	"ouroboros/internal/home"
	"ouroboros/internal/memory"
)

// The memory dialog lists the changes of the archivist, the newest first,
// and one of them can be taken back once: what it changed is as it was. A
// change taken back twice, or a profile without a store, fails.
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
	logFile := home.Dir(dir).Service().LogFile
	if logged, _ := os.ReadFile(logFile); !strings.Contains(string(logged), "the owner took back a change of the archivist:") {
		t.Fatalf("the log lacks the undo: %s", logged)
	}
	if err := o.UndoMemoryChange(ctx, rec.ID); err == nil {
		t.Fatal("took back a change twice")
	}
	changes, err = o.MemoryChanges(ctx, 10)
	if err != nil || len(changes) != 1 || !changes[0].Undone {
		t.Fatalf("the changes after undo: %+v, %v", changes, err)
	}
	// The undo that failed is not logged as one.
	logged, _ := os.ReadFile(logFile)
	if n := strings.Count(string(logged), "the owner took back a change of the archivist:"); n != 1 {
		t.Fatalf("%d undos in the log: %s", n, logged)
	}

	bad, badDir, _ := built(t, config.Config{Name: "beta", Model: "m", BaseURL: "http://x/v1"})
	if err := os.WriteFile(home.Dir(badDir).History(), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := bad.MemoryChanges(ctx, 10); err == nil {
		t.Fatal("listed changes without a store")
	}
	if err := bad.UndoMemoryChange(ctx, rec.ID); err == nil {
		t.Fatal("took back a change without a store")
	}
}

// The interval of the archivist is read and set through the settings: an
// invalid interval is refused and changes nothing, and a configuration that
// cannot be read fails both.
