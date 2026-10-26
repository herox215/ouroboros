package history

import (
	"slices"
	"testing"
)

// An answer on its way is kept until it ends or an image of ouro ends with
// it, and what is left over is consumed once, as the sessions that go on.
func TestAnsweringMarks(t *testing.T) {
	dir := t.TempDir()
	a := Open(dir)
	if got, err := a.Interrupted(); err != nil || len(got) != 0 {
		t.Fatalf("interrupted before any answer: %v, %v", got, err)
	}
	if err := a.BeginAnswer("nowhere"); err == nil {
		t.Error("a mark of no session")
	}
	for _, id := range []string{"owner/s1", "owner/s2"} {
		if err := a.BeginAnswer(id); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.EndAnswer("owner/s1"); err != nil {
		t.Fatal(err)
	}
	if got, err := a.Interrupted(); err != nil || !slices.Equal(got, []string{"owner/s2"}) {
		t.Fatalf("interrupted: %v, %v", got, err)
	}
	if got, err := a.Interrupted(); err != nil || len(got) != 0 {
		t.Fatalf("again: %v, %v", got, err)
	}

	// A failed consumption leaves the mark, as one that cannot be read.
	a.BeginAnswer("owner/s3")
	db, err := a.database()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("CREATE TRIGGER keep_answer BEFORE UPDATE OF answering ON history_sessions WHEN old.answering=1 AND new.answering=0 BEGIN SELECT RAISE(ABORT,'broken'); END"); err != nil {
		t.Fatal(err)
	}
	if got, err := a.Interrupted(); err == nil || len(got) != 0 {
		t.Fatalf("failed consume: %v %v", got, err)
	}
	if _, err = db.Exec("DROP TRIGGER keep_answer"); err != nil {
		t.Fatal(err)
	}
	if got, err := a.Interrupted(); err != nil || !slices.Equal(got, []string{"owner/s3"}) {
		t.Fatalf("retry: %v %v", got, err)
	}

	// An archive from before the mark was kept gets its column back.
	if _, err = db.Exec("ALTER TABLE history_sessions DROP COLUMN answering"); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := Open(dir)
	defer reopened.Close()
	if err := reopened.BeginAnswer("owner/s4"); err != nil {
		t.Fatalf("mark in a migrated archive: %v", err)
	}
	if got, err := reopened.Interrupted(); err != nil || !slices.Equal(got, []string{"owner/s4"}) {
		t.Fatalf("interrupted after migration: %v %v", got, err)
	}
}

// A mark does not bring a conversation back that the owner deleted.
func TestMarkKeepsADeletedConversationDeleted(t *testing.T) {
	a := Open(t.TempDir())
	defer a.Close()
	for _, step := range []func(string) error{a.BeginAnswer, a.EndAnswer} {
		if err := step("owner/gone"); err != nil {
			t.Fatal(err)
		}
	}
	db, err := a.database()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("UPDATE history_sessions SET deleted=1 WHERE id='owner/gone'"); err != nil {
		t.Fatal(err)
	}
	if err = a.BeginAnswer("owner/gone"); err != nil {
		t.Fatal(err)
	}
	if got, err := a.Interrupted(); err != nil || len(got) != 0 {
		t.Fatalf("interrupted: %v, %v", got, err)
	}
}
