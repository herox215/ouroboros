package memory

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// ids are the IDs of entries, in their order.
func ids(entries []Entry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.ID
	}
	return out
}

// remember keeps a memory for a test.
func remember(t *testing.T, s *Store, project, key, content string, expires time.Time) Entry {
	t.Helper()
	e, _, err := s.Remember(context.Background(), project, key, content, "chat", false, expires)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// A topic is given the memories of its project, the most used for how often
// they were loaded first, and those of any scope that have any of its
// words; never core ones, archived, expired or stale ones, nor those of
// another project.
func TestTopicMemories(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	build := remember(t, s, "/p", "build", "make build compiles ouro", time.Time{})
	tests := remember(t, s, "/p", "tests", "make test runs the tests", time.Time{})
	stale := remember(t, s, "/p", "stale", "never used for build", time.Time{})
	remember(t, s, "/p", "gone", "an issue of the build", time.Now().Add(-time.Hour))
	other := remember(t, s, "/q", "deploy", "build the deploy", time.Time{})
	global := remember(t, s, "", "editor", "vim for build files", time.Time{})
	if _, err := s.CreateCore(ctx, "name", "build Lu", "owner"); err != nil {
		t.Fatal(err)
	}
	for range StaleLoads {
		if err := s.Loaded(ctx, []string{stale.ID, build.ID}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Used(ctx, []string{build.ID}); err != nil {
		t.Fatal(err)
	}
	got, err := s.OfProject(ctx, "/p", 10)
	if err != nil || !slices.Equal(ids(got), []string{tests.ID, build.ID}) {
		t.Fatalf("of the project: %+v, %v", got, err)
	}
	if got[1].Loaded != StaleLoads || got[1].Used != 1 || got[1].Version != 1 {
		t.Errorf("counted: %+v", got[1])
	}
	if got, err := s.OfProject(ctx, "", 10); err != nil || len(got) != 0 {
		t.Fatalf("of no project: %+v, %v", got, err)
	}
	related, err := s.Related(ctx, "/p", []string{"deploy, build?", "--"}, 10)
	if err != nil || !slices.Contains(ids(related), build.ID) || !slices.Contains(ids(related), global.ID) || len(related) != 2 {
		t.Fatalf("related: %+v, %v", related, err)
	}
	if related, err := s.Related(ctx, "/q", []string{"deploy"}, 10); err != nil || !slices.Equal(ids(related), []string{other.ID}) {
		t.Fatalf("related of another project: %+v, %v", related, err)
	}
	if related, err := s.Related(ctx, "/p", []string{"…"}, 10); err != nil || len(related) != 0 {
		t.Fatalf("related without words: %+v, %v", related, err)
	}
	keys, err := s.Keys(ctx, "/p", 10)
	if err != nil || keys[0] != "build" || slices.Contains(keys, "gone") || slices.Contains(keys, "name") || slices.Contains(keys, "deploy") || len(keys) != 4 {
		t.Fatalf("keys: %q, %v", keys, err)
	}
}

// Tidy archives what expired and what was loaded ArchiveLoads times and
// never used; an archived memory is found by a search alone, and one that is
// remembered again is no longer archived.
func TestTidy(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	expired := remember(t, s, "", "issue", "working on issue 77", time.Now().Add(-time.Minute))
	later := remember(t, s, "", "release", "release on friday", time.Now().Add(time.Hour))
	unused := remember(t, s, "", "unused", "a fact", time.Time{})
	for range ArchiveLoads {
		if err := s.Loaded(ctx, []string{unused.ID}); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := s.Tidy(ctx); err != nil || n != 2 {
		t.Fatalf("tidied %d, %v", n, err)
	}
	if n, err := s.Tidy(ctx); err != nil || n != 0 {
		t.Fatalf("tidied again %d, %v", n, err)
	}
	active, err := s.Active(ctx, 10)
	if err != nil || !slices.Equal(ids(active), []string{later.ID}) {
		t.Fatalf("active: %+v, %v", active, err)
	}
	if hits, err := s.Search(ctx, "", "global", "issue", 10); err != nil || len(hits) != 1 || !hits[0].Archived {
		t.Fatalf("an archived memory is not found: %+v, %v", hits, err)
	}
	again, was, err := s.Remember(ctx, "", "issue", "working on issue 80", "chat", false, time.Time{})
	if err != nil || again.Archived || again.Expires != "" || !was.Archived || was.ID != expired.ID {
		t.Fatalf("remembered again: %+v, %+v, %v", again, was, err)
	}
	if err := s.Undo(ctx, again, was); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Read(ctx, "", expired.ID); !got.Archived || got.Expires == "" {
		t.Fatalf("undone, it is archived again: %+v", got)
	}
	if err := s.Archive(ctx, "/other", []string{later.ID}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Read(ctx, "", later.ID); got.Archived {
		t.Fatal("archived in another scope")
	}
	if err := s.Archive(ctx, "", []string{later.ID}); err != nil {
		t.Fatal(err)
	}
	if active, err := s.Active(ctx, 10); err != nil || len(active) != 0 {
		t.Fatalf("active after Archive: %+v, %v", active, err)
	}
}

// When something last happened is kept, the zero time before it ever did.
func TestMark(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if at, err := s.Marked(ctx, "consolidated"); err != nil || !at.IsZero() {
		t.Fatalf("never: %v, %v", at, err)
	}
	at := time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)
	for _, when := range []time.Time{at.Add(-time.Hour), at} {
		if err := s.Mark(ctx, "consolidated", when); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := s.Marked(ctx, "consolidated"); err != nil || !got.Equal(at) {
		t.Fatalf("marked %v, %v", got, err)
	}
	if _, err := s.db.Exec("UPDATE memory_meta SET value='never'"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Marked(ctx, "consolidated"); err == nil {
		t.Fatal("read a time that is none")
	}
}

// What the database fails at fails the reads and writes of topics, usage and
// tidying.
func TestLearnedFailures(t *testing.T) {
	s := testStore(t)
	stopped, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.OfProject(stopped, "/p", 1); !errors.Is(err, context.Canceled) {
		t.Errorf("of the project: %v", err)
	}
	if _, err := s.Related(stopped, "/p", []string{"x"}, 1); !errors.Is(err, context.Canceled) {
		t.Errorf("related: %v", err)
	}
	if _, err := s.Keys(stopped, "/p", 1); !errors.Is(err, context.Canceled) {
		t.Errorf("keys: %v", err)
	}
	if _, err := s.Active(stopped, 1); !errors.Is(err, context.Canceled) {
		t.Errorf("active: %v", err)
	}
	if _, err := s.Marked(stopped, "x"); !errors.Is(err, context.Canceled) {
		t.Errorf("marked: %v", err)
	}
	if _, err := s.db.Exec("CREATE TRIGGER keep BEFORE UPDATE ON memories BEGIN SELECT RAISE(ABORT,'kept'); END;"); err != nil {
		t.Fatal(err)
	}
	e := remember(t, s, "", "k", "v", time.Now().Add(-time.Minute))
	for name, err := range map[string]error{
		"loaded":  s.Loaded(context.Background(), []string{e.ID}),
		"archive": s.Archive(context.Background(), "", []string{e.ID}),
	} {
		if err == nil || !strings.Contains(err.Error(), "kept") {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := s.Tidy(context.Background()); err == nil {
		t.Error("tidied")
	}
	if _, err := s.db.Exec("DROP TABLE memory_meta"); err != nil {
		t.Fatal(err)
	}
	if err := s.Mark(context.Background(), "x", time.Now()); err == nil {
		t.Error("marked without a table")
	}
}
