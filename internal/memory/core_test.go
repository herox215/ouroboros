package memory

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"ouroboros/internal/storage"
)

// Core memories are global, listed the oldest first, and together have at
// most MaxCore characters: a create or an update that would make them more
// fails and changes nothing.
func TestCoreBudget(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	half := strings.Repeat("é", MaxCore/2)
	first, err := s.CreateCore(ctx, "style", half, "owner")
	if err != nil || !first.Core || first.Project != "" {
		t.Fatalf("core: %+v, %v", first, err)
	}
	second, err := s.CreateCore(ctx, "name", half, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateCore(ctx, "more", "x", "owner"); !errors.Is(err, ErrCoreFull) {
		t.Fatalf("over the budget: %v", err)
	}
	if _, err := s.Update(ctx, "", second.ID, 1, "name", half+"x", "owner"); !errors.Is(err, ErrCoreFull) {
		t.Fatalf("an update over the budget: %v", err)
	}
	if _, err := s.Create(ctx, "", "plain", strings.Repeat("x", MaxCore), "owner"); err != nil {
		t.Fatalf("a memory that is no core one counts: %v", err)
	}
	if got, err := s.Read(ctx, "", second.ID); err != nil || got != second {
		t.Fatalf("a refused update changed it: %+v, %v", got, err)
	}
	core, err := s.Core(ctx)
	if err != nil || len(core) != 2 || core[0].ID != first.ID || core[1].ID != second.ID {
		t.Fatalf("core: %+v, %v", core, err)
	}
	if hits, err := s.Search(ctx, "", "global", "", 10); err != nil || len(hits) != 3 || !hits[2].Core {
		t.Fatalf("search tells core ones: %+v, %v", hits, err)
	}
	stopped, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.Core(stopped); !errors.Is(err, context.Canceled) {
		t.Fatalf("stopped: %v", err)
	}
}

// Remember creates a memory, or replaces the one of the same key in its
// scope, and Undo takes either back: what was replaced comes back, what was
// new is forgotten.
func TestRememberAndUndo(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	e, was, err := s.Remember(ctx, "/p", "build", "make build", "chat", false, time.Time{})
	if err != nil || was.ID != "" || e.Version != 1 || e.Project != "/p" || e.Source != "chat" {
		t.Fatalf("new: %+v, %+v, %v", e, was, err)
	}
	again, was, err := s.Remember(ctx, "/p", "build", "make check", "later", false, time.Time{})
	if err != nil || was != e || again.ID != e.ID || again.Version != 2 || again.Content != "make check" || again.Created != e.Created {
		t.Fatalf("replaced: %+v, %+v, %v", again, was, err)
	}
	if err := s.Undo(ctx, again, was); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Read(ctx, "/p", e.ID); err != nil || got.Content != "make build" || got.Source != "chat" || got.Version != 3 {
		t.Fatalf("undone: %+v, %v", got, err)
	}
	if err := s.Undo(ctx, again, was); !errors.Is(err, ErrConflict) {
		t.Fatalf("undo of what changed since: %v", err)
	}
	if err := s.Undo(ctx, Entry{ID: "missing", Version: 1}, was); !errors.Is(err, ErrNotFound) {
		t.Fatalf("undo of what is not there: %v", err)
	}
	if err := s.Undo(ctx, e, Entry{}); !errors.Is(err, ErrConflict) {
		t.Fatalf("forgetting what changed since: %v", err)
	}
	got, _ := s.Read(ctx, "/p", e.ID)
	if err := s.Undo(ctx, got, Entry{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read(ctx, "/p", e.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("forgotten: %v", err)
	}
}

// A core memory Remember keeps is global and must fit; one it replaces may
// become a core one, and what does not fit changes nothing.
func TestRememberCore(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, _, err := s.Remember(ctx, "/p", "k", "v", "chat", true, time.Time{}); err == nil {
		t.Fatal("a core memory of a project")
	}
	if _, _, err := s.Remember(ctx, "", "", "v", "chat", false, time.Time{}); err == nil {
		t.Fatal("remembered without a key")
	}
	if _, _, err := s.Remember(ctx, "", "big", strings.Repeat("x", MaxCore+1), "chat", true, time.Time{}); !errors.Is(err, ErrCoreFull) {
		t.Fatalf("too big for the core: %v", err)
	}
	e, _, err := s.Remember(ctx, "", "style", "short answers", "chat", false, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Remember(ctx, "", "style", strings.Repeat("x", MaxCore+1), "chat", true, time.Time{}); !errors.Is(err, ErrCoreFull) {
		t.Fatalf("a replacement too big for the core: %v", err)
	}
	core, _, err := s.Remember(ctx, "", "style", "very short answers", "chat", true, time.Time{})
	if err != nil || !core.Core {
		t.Fatalf("made a core one: %+v, %v", core, err)
	}
	if err := s.Undo(ctx, core, e); err != nil {
		t.Fatal(err)
	}
	if list, err := s.Core(ctx); err != nil || len(list) != 0 {
		t.Fatalf("undone, it is no core one again: %+v, %v", list, err)
	}
}

// What the database fails at fails Remember, Undo and the budget, and
// changes nothing.
func TestCoreFailures(t *testing.T) {
	dir := t.TempDir()
	broken := errors.New("disk gone")
	f := &faults{}
	s, err := New(faultDB(t, dir, f))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	e, _, err := s.Remember(ctx, "", "style", "short", "chat", false, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	f.query = func(query string) error {
		if strings.Contains(query, "AND key=?") {
			return broken
		}
		return nil
	}
	if _, _, err := s.Remember(ctx, "", "style", "long", "chat", false, time.Time{}); !errors.Is(err, broken) {
		t.Fatalf("a lookup that failed: %v", err)
	}
	f.query = func(query string) error {
		if strings.Contains(query, "sum(length") {
			return broken
		}
		return nil
	}
	if _, err := s.CreateCore(ctx, "name", "Lu", "chat"); !errors.Is(err, broken) {
		t.Fatalf("a budget that cannot be told: %v", err)
	}
	f.query = nil
	if _, err := s.db.Exec(`CREATE TRIGGER keep BEFORE UPDATE ON memories BEGIN SELECT RAISE(ABORT,'kept'); END;`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Remember(ctx, "", "style", "long", "chat", false, time.Time{}); err == nil || !strings.Contains(err.Error(), "kept") {
		t.Fatalf("a replacement that failed: %v", err)
	}
	if err := s.Undo(ctx, e, e); err == nil || !strings.Contains(err.Error(), "kept") {
		t.Fatalf("an undo that failed: %v", err)
	}
	if got, err := s.Read(ctx, "", e.ID); err != nil || got != e {
		t.Fatalf("failures changed it: %+v, %v", got, err)
	}
}

// A database of a version before core memories gets the column, and its
// memories are no core ones; one whose column cannot be told or added is no
// store.
func TestCoreMigration(t *testing.T) {
	dir := t.TempDir()
	db, err := storage.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE memories (
 id TEXT PRIMARY KEY, project TEXT NOT NULL, key TEXT NOT NULL,
 content TEXT NOT NULL, version INTEGER NOT NULL, created TEXT NOT NULL,
 updated TEXT NOT NULL, source TEXT NOT NULL, UNIQUE(project,key));
 INSERT INTO memories VALUES('old','','drink','tea',1,'t','t','owner');`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.Read(context.Background(), "", "old"); err != nil || got.Core || got.Content != "tea" {
		t.Fatalf("an old memory: %+v, %v", got, err)
	}
	s.Close()

	broken := errors.New("disk gone")
	for _, f := range []*faults{
		{query: func(query string) error {
			if strings.Contains(query, "pragma_table_info") {
				return broken
			}
			return nil
		}},
		{exec: func(query string, err error) error {
			if strings.Contains(query, "ALTER TABLE") {
				return broken
			}
			return err
		}},
	} {
		dir := t.TempDir()
		db := faultDB(t, dir, &faults{})
		if _, err := db.Exec("CREATE TABLE memories (id TEXT PRIMARY KEY)"); err != nil {
			t.Fatal(err)
		}
		if _, err := New(faultDB(t, dir, f)); !errors.Is(err, broken) {
			t.Errorf("a column that cannot be told or added: %v", err)
		}
	}
}
