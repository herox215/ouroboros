package plugin_test

import (
	"context"
	"os"
	"path/filepath"
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
	if err := o.UndoMemoryChange(ctx, rec.ID); err == nil {
		t.Fatal("took back a change twice")
	}
	changes, err = o.MemoryChanges(ctx, 10)
	if err != nil || len(changes) != 1 || !changes[0].Undone {
		t.Fatalf("the changes after undo: %+v, %v", changes, err)
	}
	logged, _ := os.ReadFile(home.Dir(dir).Service().LogFile)
	if !strings.Contains(string(logged), "the owner took back a change of the archivist:") {
		t.Fatalf("the log lacks the undo: %s", logged)
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
func TestMemoryInterval(t *testing.T) {
	o, dir, _ := built(t, config.Config{Name: "alpha", Model: "m"})
	if got, err := o.MemoryInterval(); err != nil || got != "off" {
		t.Fatalf("absent: %q, %v", got, err)
	}
	holds, err := o.SetMemoryInterval("24h")
	if err != nil || holds == "" {
		t.Fatalf("set 24h: %q, %v", holds, err)
	}
	if got, err := o.MemoryInterval(); err != nil || got != "24h" {
		t.Fatalf("after set: %q, %v", got, err)
	}
	cfg, err := config.Load(dir)
	if err != nil || cfg.MemoryInterval == nil || *cfg.MemoryInterval != "24h" {
		t.Fatalf("saved: %+v, %v", cfg.MemoryInterval, err)
	}
	if _, err := o.SetMemoryInterval("soon"); err == nil || !strings.Contains(err.Error(), "duration") {
		t.Fatalf("an invalid interval: %v", err)
	}
	if cfg, err = config.Load(dir); err != nil || cfg.MemoryInterval == nil || *cfg.MemoryInterval != "24h" {
		t.Fatalf("a refused interval changed the configuration: %+v, %v", cfg.MemoryInterval, err)
	}
	logged, _ := os.ReadFile(home.Dir(dir).Service().LogFile)
	if !strings.Contains(string(logged), "the owner changed the settings: the interval of the memory archivist") {
		t.Fatalf("the log lacks the setting: %s", logged)
	}

	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := o.MemoryInterval(); err == nil {
		t.Fatal("read the interval of a broken configuration")
	}
	if _, err := o.SetMemoryInterval("48h"); err == nil {
		t.Fatal("set the interval of a broken configuration")
	}
}
