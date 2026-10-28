package gui

import (
	"context"
	"strings"
	"testing"
	"time"

	"ouroboros/internal/config"
	"ouroboros/internal/home"
	"ouroboros/internal/identity"
	"ouroboros/internal/memory"
	"ouroboros/plugin"
)

// memoryWindow is a window over one profile whose owner keeps its
// configuration and memories in a temporary home.
func memoryWindow(t *testing.T) (*Window, home.Dir) {
	t.Helper()
	dir := home.Dir(t.TempDir())
	interval := "24h"
	if err := dir.SaveConfig(config.Config{Access: "access", Model: "model", Name: "node", PrivateKey: identity.Generate().Private(), MemoryInterval: &interval}); err != nil {
		t.Fatal(err)
	}
	w := newWindow(page{choices: []choice{{"node", "id"}}, profiles: []*Profile{{owner: plugin.OwnerOf(dir, nil)}}})
	t.Cleanup(w.Close)
	return w, dir
}

// The memory methods of the window read and change the selected profile.
func TestWindowMemory(t *testing.T) {
	ctx := context.Background()
	w, dir := memoryWindow(t)
	if interval, err := w.MemoryInterval(0); err != nil || interval != "24h" {
		t.Fatalf("interval %q, %v", interval, err)
	}
	holds, err := w.SetMemoryInterval(0, "1h")
	if err != nil || holds == "" {
		t.Fatalf("set interval %q, %v", holds, err)
	}
	if interval, err := w.MemoryInterval(0); err != nil || interval != "1h" {
		t.Fatalf("interval after set %q, %v", interval, err)
	}
	store, err := memory.Open(dir.History())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	e, _, err := store.Remember(ctx, "", "drink", "oolong tea", "owner", false, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := store.Apply(ctx, memory.Change{Action: "archive", ID: e.ID, Reason: "no longer holds"})
	if err != nil {
		t.Fatal(err)
	}
	changes, err := w.MemoryChanges(0, 10)
	if err != nil || len(changes) != 1 || changes[0].ID != rec.ID || changes[0].Action != "archive" {
		t.Fatalf("changes %+v, %v", changes, err)
	}
	if err := w.UndoMemoryChange(0, rec.ID); err != nil {
		t.Fatal(err)
	}
	changes, err = w.MemoryChanges(0, 10)
	if err != nil || len(changes) != 1 || !changes[0].Undone {
		t.Fatalf("changes after undo %+v, %v", changes, err)
	}
	if err := w.UndoMemoryChange(0, rec.ID); err == nil {
		t.Fatal("the same change was undone twice")
	}
}

// An index the window has no profile for, and a profile without its owner,
// are refused by every memory method.
func TestWindowMemoryInvalidProfile(t *testing.T) {
	w, _ := memoryWindow(t)
	invalid := "invalid GUI profile index"
	for _, profile := range []int{-1, 1} {
		if _, err := w.MemoryInterval(profile); err == nil || !strings.Contains(err.Error(), invalid) {
			t.Fatalf("MemoryInterval(%d): %v", profile, err)
		}
		if _, err := w.SetMemoryInterval(profile, "1h"); err == nil || !strings.Contains(err.Error(), invalid) {
			t.Fatalf("SetMemoryInterval(%d): %v", profile, err)
		}
		if _, err := w.MemoryChanges(profile, 10); err == nil || !strings.Contains(err.Error(), invalid) {
			t.Fatalf("MemoryChanges(%d): %v", profile, err)
		}
		if err := w.UndoMemoryChange(profile, 1); err == nil || !strings.Contains(err.Error(), invalid) {
			t.Fatalf("UndoMemoryChange(%d): %v", profile, err)
		}
	}
	noOwner := newWindow(page{choices: []choice{{"none", ""}}, profiles: []*Profile{{}}})
	t.Cleanup(noOwner.Close)
	unowned := "has no owner access"
	if _, err := noOwner.MemoryInterval(0); err == nil || !strings.Contains(err.Error(), unowned) {
		t.Fatalf("MemoryInterval without owner: %v", err)
	}
	if _, err := noOwner.SetMemoryInterval(0, "1h"); err == nil || !strings.Contains(err.Error(), unowned) {
		t.Fatalf("SetMemoryInterval without owner: %v", err)
	}
	if _, err := noOwner.MemoryChanges(0, 10); err == nil || !strings.Contains(err.Error(), unowned) {
		t.Fatalf("MemoryChanges without owner: %v", err)
	}
	if err := noOwner.UndoMemoryChange(0, 1); err == nil || !strings.Contains(err.Error(), unowned) {
		t.Fatalf("UndoMemoryChange without owner: %v", err)
	}
}
