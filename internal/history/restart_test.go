package history

import (
	"errors"
	"reflect"
	"testing"

	"ouroboros/internal/openai"
)

func TestDurableRestartCompletion(t *testing.T) {
	dir := t.TempDir()
	a := Open(dir)
	t.Cleanup(func() { a.Close() })
	if err := a.Append(Who{Session: "trigger"}, []openai.Message{{Role: "user", Content: "build"}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := a.PendingRestart("owner/trigger", "old-image", "restart-id", "/new/ouro"); err != nil {
		t.Fatal(err)
	}
	if got, err := a.CompleteRestarts("old-image"); err != nil || len(got) != 0 {
		t.Fatalf("same process completed: %v, %v", got, err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	a = Open(dir)
	if got, err := a.CompleteRestarts("old-image"); err != nil || len(got) != 0 {
		t.Fatalf("reopen completed: %v, %v", got, err)
	}
	completed, err := a.CompleteRestarts("new-image")
	if err != nil {
		t.Fatal(err)
	}
	r := completed["owner/trigger"]
	if len(completed) != 1 || r.Role != Restarted || r.PresentationID != "restart-id" || r.Content != "/new/ouro" || r.Seq != 3 || r.Answer != 1 {
		t.Fatalf("completion: %+v", completed)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	a = Open(dir)
	for _, process := range []string{"new-image", "third-image"} {
		if got, err := a.CompleteRestarts(process); err != nil || len(got) != 0 {
			t.Fatalf("duplicate: %v, %v", got, err)
		}
	}
	records, err := a.Records("owner/trigger")
	if err != nil || len(records) != 3 || !reflect.DeepEqual(records[2], r) {
		t.Fatalf("records: %+v, %v", records, err)
	}
	if err := a.Append(Who{Session: "trigger"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	records, err = a.Records("owner/trigger")
	if err != nil || records[3].Seq != 4 || records[3].Answer != 2 {
		t.Fatalf("append after completion: %+v, %v", records, err)
	}
}

func TestRestartCompletionRollbackKeepsIntent(t *testing.T) {
	a := Open(t.TempDir())
	defer a.Close()
	if err := a.PendingRestart("owner/trigger", "old", "stable-id", "binary"); err != nil {
		t.Fatal(err)
	}
	// Fail after inserting the completion, at the final consumption step.
	exec(t, a, "CREATE TRIGGER reject_restart_consumption BEFORE DELETE ON history_restarts BEGIN SELECT RAISE(ABORT,'test failure'); END")
	if _, err := a.CompleteRestarts("new"); err == nil {
		t.Fatal("completion ignored failure")
	}
	exec(t, a, "DROP TRIGGER reject_restart_consumption")
	got, err := a.CompleteRestarts("new")
	if err != nil || len(got) != 1 {
		t.Fatalf("intent lost: %v, %v", got, err)
	}
	records, err := a.Records("owner/trigger")
	if err != nil || len(records) != 1 || records[0].PresentationID != "stable-id" {
		t.Fatalf("duplicate or missing completion: %+v, %v", records, err)
	}
}

func TestDeletedRestartDoesNotComplete(t *testing.T) {
	a := Open(t.TempDir())
	defer a.Close()
	if err := a.PendingRestart("owner/deleted", "old", "id", "binary"); err != nil {
		t.Fatal(err)
	}
	if err := a.Delete("owner/deleted"); err != nil && !errors.Is(err, ErrNotArchived) {
		t.Fatal(err)
	}
	if got, err := a.CompleteRestarts("new"); err != nil || len(got) != 0 {
		t.Fatalf("deleted completed: %v, %v", got, err)
	}
	if err := a.PendingRestart("peer-key/session", "old", "id", "binary"); err == nil {
		t.Fatal("peer restart accepted")
	}
}
