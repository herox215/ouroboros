package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	store "ouroboros/internal/memory"
)

// The Changes tool lists what the archivist changed, the Undo tool takes one
// change back, and a change is undone once.
func TestChangesAndUndoTools(t *testing.T) {
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	e, err := s.Create(ctx, "", "drink", "oolong", "owner")
	if err != nil {
		t.Fatal(err)
	}
	rec, err := s.Apply(ctx, store.Change{Action: "rewrite", ID: e.ID, Content: "oolong tea", Reason: "more precise"})
	if err != nil {
		t.Fatal(err)
	}
	var changes []store.ChangeRecord
	if err := json.Unmarshal([]byte(call(t, s, ctx, "owner", Changes, `{"limit":10}`)), &changes); err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || changes[0].ID != rec.ID || changes[0].Action != "rewrite" || changes[0].Reason != "more precise" || changes[0].Undone {
		t.Fatalf("the changes: %+v", changes)
	}
	if out := call(t, s, ctx, "owner", Undo, fmt.Sprintf(`{"id":%d}`, rec.ID)); out != "Change undone." {
		t.Fatalf("undo: %q", out)
	}
	if got, err := s.Read(ctx, "", e.ID); err != nil || got.Content != "oolong" {
		t.Fatalf("not as it was: %+v, %v", got, err)
	}
	if out := call(t, s, ctx, "owner", Undo, fmt.Sprintf(`{"id":%d}`, rec.ID)); !strings.HasPrefix(out, "error:") {
		t.Fatalf("undo twice: %q", out)
	}
	changes = nil
	if err := json.Unmarshal([]byte(call(t, s, ctx, "owner", Changes, `{}`)), &changes); err != nil {
		t.Fatal(err)
	}
	if len(changes) != 1 || !changes[0].Undone {
		t.Fatalf("the changes after undo: %+v", changes)
	}
}
