package history

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSQLMetadataOnlyDelete(t *testing.T) {
	a := Open(t.TempDir())
	defer a.Close()
	if err := a.SetTitle("owner/draft", "Draft"); err != nil {
		t.Fatal(err)
	}
	if err := a.SetDirectory("owner/draft", ""); err != nil {
		t.Fatal(err)
	}
	if err := a.GoOn("owner/draft"); err != nil {
		t.Fatal(err)
	}
	if err := a.Delete("owner/draft"); !errors.Is(err, ErrNotArchived) {
		t.Fatalf("metadata-only delete: %v", err)
	}
	if title, err := a.Title("owner/draft"); err != nil || title != "" {
		t.Fatalf("title: %q %v", title, err)
	}
	if _, ok, err := a.Directory("owner/draft"); err != nil || ok {
		t.Fatalf("directory: %v %v", ok, err)
	}
	if ids, err := a.GoingOn(); err != nil || len(ids) != 0 {
		t.Fatalf("goon: %v %v", ids, err)
	}
}

func TestSQLMetadataWriteFailure(t *testing.T) {
	a := Open(t.TempDir())
	defer a.Close()
	if err := a.SetTitle("owner/draft", "Before"); err != nil {
		t.Fatal(err)
	}
	db, err := a.database()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("CREATE TRIGGER keep_title BEFORE UPDATE OF title ON history_sessions BEGIN SELECT RAISE(ABORT,'broken'); END"); err != nil {
		t.Fatal(err)
	}
	if err = a.SetTitle("owner/draft", "After"); err == nil {
		t.Fatal("write succeeded")
	}
	if title, err := a.Title("owner/draft"); err != nil || title != "Before" {
		t.Fatalf("title after failure: %q %v", title, err)
	}
}

// The model of a conversation is kept beside its effort, and a new one is
// marked in the archive where the context of the new model starts.
func TestModel(t *testing.T) {
	a := at(t)
	id := Owner(owner.Session)
	if model, ok, err := a.Model(id); err != nil || ok || model != "" {
		t.Fatalf("none: %q, %v, %v", model, ok, err)
	}
	if err := a.NoteModel(id, "gpt-4o"); err != nil {
		t.Fatal(err)
	}
	if model, ok, err := a.Model(id); err != nil || !ok || model != "gpt-4o" {
		t.Fatalf("model: %q, %v, %v", model, ok, err)
	}
	records, err := a.Records(id)
	if err != nil || len(records) != 1 || records[0].Role != ModelChanged || records[0].Content != "gpt-4o" {
		t.Fatalf("records: %+v, %v", records, err)
	}
	// A conversation needs not be archived to keep its model.
	if err := a.NoteModel("owner/fresh", "small"); err != nil {
		t.Fatal(err)
	}
	if model, ok, err := a.Model("owner/fresh"); err != nil || !ok || model != "small" {
		t.Fatalf("unarchived: %q, %v, %v", model, ok, err)
	}
	// What names no conversation fails both to keep and to note a model.
	for _, id := range []string{"chat-1", "owner/../x"} {
		if err := a.SetModel(id, "m"); err == nil || !strings.Contains(err.Error(), "names no session") {
			t.Errorf("set %q: %v", id, err)
		}
		if err := a.NoteModel(id, "m"); err == nil || !strings.Contains(err.Error(), "names no session") {
			t.Errorf("note %q: %v", id, err)
		}
		if _, _, err := a.Model(id); err == nil || !strings.Contains(err.Error(), "names no session") {
			t.Errorf("get %q: %v", id, err)
		}
	}
}

// A note of the model that fails on the way is not kept, whichever of its
// writes fails.
func TestNoteModelFailsAtEveryWrite(t *testing.T) {
	for name, c := range map[string]struct {
		sql   string
		clock time.Time
	}{
		"session":  {sql: "CREATE TRIGGER broken BEFORE UPDATE ON history_sessions WHEN new.model=old.model BEGIN SELECT RAISE(ABORT,'broken'); END"},
		"sequence": {sql: "ALTER TABLE history_records DROP COLUMN answer"},
		// A clock no JSON time can hold.
		"time":    {clock: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)},
		"message": {sql: broken("BEFORE INSERT ON history_records")},
		"size":    {sql: broken("BEFORE UPDATE OF archived ON history_sessions")},
	} {
		t.Run(name, func(t *testing.T) {
			a := Open(t.TempDir())
			defer a.Close()
			id := "owner/noted"
			if err := a.SetModel(id, "m"); err != nil {
				t.Fatal(err)
			}
			if c.sql != "" {
				exec(t, a, c.sql)
			}
			if !c.clock.IsZero() {
				a.now = func() time.Time { return c.clock }
			}
			if err := a.NoteModel(id, "m2"); err == nil {
				t.Fatal("the model was noted")
			}
		})
	}
}
