package history

import (
	"slices"
	"testing"
)

// A conversation of the owner marked to go on is returned once, and the
// mark goes with the conversation when it is deleted.
func TestGoOn(t *testing.T) {
	dir := t.TempDir()
	a := Open(dir)
	if err := a.GoOn("nowhere"); err == nil {
		t.Error("a mark of no session")
	}
	for _, id := range []string{"owner/s1", "owner/s2"} {
		if err := a.GoOn(id); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := a.GoingOn(); err != nil || !slices.Equal(got, []string{"owner/s1", "owner/s2"}) {
		t.Fatalf("going on: %v, %v", got, err)
	}
	if got, err := a.GoingOn(); err != nil || len(got) != 0 {
		t.Fatalf("again: %v, %v", got, err)
	}

	a.GoOn("owner/s3")
	if err := a.Append(Who{Session: "s3"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := a.Delete("owner/s3"); err != nil {
		t.Fatal(err)
	}
	if got, _ := a.GoingOn(); len(got) != 0 {
		t.Fatalf("a deleted session goes on: %v", got)
	}

	// A failed consumption rolls back all marks.
	a.GoOn("owner/s4")
	db, err := a.database()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("CREATE TRIGGER keep_mark BEFORE UPDATE OF goon ON history_sessions WHEN old.goon=1 AND new.goon=0 BEGIN SELECT RAISE(ABORT,'broken'); END"); err != nil {
		t.Fatal(err)
	}
	if got, err := a.GoingOn(); err == nil || len(got) != 0 {
		t.Fatalf("failed consume: %v %v", got, err)
	}
	if _, err = db.Exec("DROP TRIGGER keep_mark"); err != nil {
		t.Fatal(err)
	}
	if got, err := a.GoingOn(); err != nil || !slices.Equal(got, []string{"owner/s4"}) {
		t.Fatalf("retry: %v %v", got, err)
	}
}
