package memory

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"ouroboros/internal/storage"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// A memory is added to a topic, which a new name creates and which compares
// case-insensitively; the same key overwrites, and the version before is in
// the history.
func TestAddLoadForget(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	e, was, err := s.Add(ctx, "vorteile.net · Maestro", "runner", "maestro test runs the flows", "chat", time.Time{})
	if err != nil || was.ID != "" || e.Topic != "vorteile.net · Maestro" || e.Version != 1 {
		t.Fatalf("add: %+v, %+v, %v", e, was, err)
	}
	again, was, err := s.Add(ctx, "  VORTEILE.NET · maestro ", "runner", "maestro test flows/ runs them", "chat", time.Time{})
	if err != nil || again.ID != e.ID || again.Version != 2 || again.Topic != e.Topic || was.Content != e.Content {
		t.Fatalf("overwrite: %+v, %+v, %v", again, was, err)
	}
	if _, _, err := s.Add(ctx, "", "tea", "oolong", "chat", time.Time{}); err != nil {
		t.Fatal(err)
	}
	topics, err := s.Topics(ctx)
	if err != nil || len(topics) != 2 || topics[0] != (Topic{"general", 1}) && topics[1] != (Topic{"general", 1}) {
		t.Fatalf("topics: %+v, %v", topics, err)
	}
	if got, err := s.Load(ctx, "vorteile.net · maestro", "", 0); err != nil || len(got) != 1 || got[0].Content != again.Content {
		t.Fatalf("load: %+v, %v", got, err)
	}
	// Any word of a search matches.
	if got, err := s.Load(ctx, "vorteile.net · maestro", "nothing flows", 0); err != nil || len(got) != 1 || got[0].Used != 0 {
		t.Fatalf("search: %+v, %v", got, err)
	}
	if got, _ := s.Read(ctx, e.ID); got.Used != 1 {
		t.Fatalf("a hit is not counted: %+v", got)
	}
	if got, err := s.Load(ctx, General, "flows", 0); err != nil || len(got) != 0 {
		t.Fatalf("another topic found: %+v, %v", got, err)
	}
	if _, err := s.Forget(ctx, e.Topic, "runner"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read(ctx, e.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("forgotten is there: %v", err)
	}
	prev, err := s.Previous(ctx, 10)
	if err != nil || len(prev) != 2 || prev[0].Content != again.Content || prev[1].Content != e.Content {
		t.Fatalf("history: %+v, %v", prev, err)
	}
	if _, _, err := s.Add(ctx, strings.Repeat("t", MaxTopic+1), "k", "v", "chat", time.Time{}); err == nil {
		t.Fatal("a topic too long")
	}
}

// The memories of core fit MaxCore together, and an expired memory is
// neither loaded nor in the prompt.
func TestCoreAndExpiry(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, _, err := s.Add(ctx, "Core", "name", strings.Repeat("x", MaxCore-1), "chat", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Add(ctx, Core, "more", "yy", "chat", time.Time{}); !errors.Is(err, ErrCoreFull) {
		t.Fatalf("over the core: %v", err)
	}
	if _, _, err := s.Add(ctx, Core, "gone", "z", "chat", time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	core, err := s.CoreMemories(ctx)
	if err != nil || len(core) != 1 || core[0].Key != "name" {
		t.Fatalf("core: %+v, %v", core, err)
	}
	if l, err := s.List(ctx, false, 0); err != nil || l.Total != 2 || l.Hidden != 1 || len(l.Memories) != 1 {
		t.Fatalf("list: %+v, %v", l, err)
	}
}

// What the review kept is taken back: one it created is forgotten, one it
// replaced is as before, and one changed since is left alone.
func TestUndoAndUpdate(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	e, was, _ := s.Add(ctx, General, "style", "short", "c", time.Time{})
	if err := s.Undo(ctx, e, was); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Read(ctx, e.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("created is there: %v", err)
	}
	first, _, _ := s.Add(ctx, General, "style", "short", "c", time.Time{})
	second, was, _ := s.Add(ctx, General, "style", "terse", "c", time.Time{})
	edited, err := s.Update(ctx, second.ID, second.Version, "style", "very terse", "owner")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Undo(ctx, second, was); !errors.Is(err, ErrConflict) {
		t.Fatalf("undo over an edit: %v", err)
	}
	if err := s.Undo(ctx, edited, was); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Read(ctx, first.ID); got.Content != "short" {
		t.Fatalf("undone: %+v", got)
	}
}

// The memories of directories move once to topics: core ones to core,
// global ones to general, those of a directory to its base name; archived
// ones stay behind.
func TestMigration(t *testing.T) {
	dir := t.TempDir()
	db, err := storage.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE memories (id TEXT PRIMARY KEY, project TEXT NOT NULL, key TEXT NOT NULL,
 content TEXT NOT NULL, version INTEGER NOT NULL, created TEXT NOT NULL, updated TEXT NOT NULL, source TEXT NOT NULL,
 core INTEGER NOT NULL DEFAULT 0, expires TEXT NOT NULL DEFAULT '', loaded INTEGER NOT NULL DEFAULT 0,
 used INTEGER NOT NULL DEFAULT 0, archived INTEGER NOT NULL DEFAULT 0, UNIQUE(project,key));
 INSERT INTO memories (id,project,key,content,version,created,updated,source,core,archived) VALUES
 ('a','','name','Lu',1,'t1','t1','s',1,0),
 ('b','','tea','oolong',1,'t1','t1','s',0,0),
 ('c','/Users/lu/Projects/ouro','build','make build',3,'t1','t2','s',0,0),
 ('d','/tmp/wt/ouro','build','make build again',1,'t1','t3','s',0,0),
 ('e','/x','old','gone',1,'t1','t1','s',0,1);`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	topics, _ := s.Topics(ctx)
	if len(topics) != 3 {
		t.Fatalf("topics: %+v", topics)
	}
	if got, _ := s.Load(ctx, "ouro", "", 0); len(got) != 1 || got[0].Content != "make build again" {
		t.Fatalf("ouro: %+v", got)
	}
	if got, _ := s.CoreMemories(ctx); len(got) != 1 || got[0].Content != "Lu" {
		t.Fatalf("core: %+v", got)
	}
	if got, _ := s.Load(ctx, General, "", 0); len(got) != 1 || got[0].Key != "tea" {
		t.Fatalf("general: %+v", got)
	}
	s.Forget(ctx, General, "tea")
	s.Close()
	// Once only: what was forgotten since stays forgotten.
	s, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got, _ := s.Load(ctx, General, "", 0); len(got) != 0 {
		t.Fatalf("migrated twice: %+v", got)
	}
}
