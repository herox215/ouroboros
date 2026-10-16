package memory

import (
	"context"
	"errors"
	"strings"
	"testing"
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
func TestCRUDVersionAndIndex(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	e, err := s.Create(ctx, "", "drink", "oolong tea", "owner-session")
	if err != nil {
		t.Fatal(err)
	}
	if e.Version != 1 || e.Source != "owner-session" || e.Created == "" || e.Updated == "" {
		t.Fatalf("bad metadata: %+v", e)
	}
	if _, err = s.Create(ctx, "", "drink", "duplicate", "s"); err == nil {
		t.Fatal("duplicate key accepted")
	}
	got, err := s.Read(ctx, "", e.ID)
	if err != nil || got != e {
		t.Fatalf("read: %+v %v", got, err)
	}
	hits, err := s.Search(ctx, "", "all", "oolong", 10)
	if err != nil || len(hits) != 1 {
		t.Fatalf("search: %+v %v", hits, err)
	}
	updated, err := s.Update(ctx, "", e.ID, 1, "drink", "espresso coffee", "second-session")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version != 2 || updated.Source != "second-session" || updated.Created != e.Created {
		t.Fatalf("update: %+v", updated)
	}
	if _, err = s.Update(ctx, "", e.ID, 1, "drink", "stale", "s"); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale: %v", err)
	}
	hits, err = s.Search(ctx, "", "all", "oolong", 10)
	if err != nil || len(hits) != 0 {
		t.Fatalf("old index content: %+v %v", hits, err)
	}
	hits, err = s.Search(ctx, "", "all", "espresso", 10)
	if err != nil || len(hits) != 1 {
		t.Fatalf("new index: %+v %v", hits, err)
	}
	if err = s.Forget(ctx, "", e.ID, 1); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale forget: %v", err)
	}
	if err = s.Forget(ctx, "", e.ID, 2); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Read(ctx, "", e.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("forgotten read: %v", err)
	}
	var n int
	if err = s.db.QueryRow("SELECT count(*) FROM memory_fts").Scan(&n); err != nil || n != 0 {
		t.Fatalf("retained FTS row: %d %v", n, err)
	}
	if err = s.db.QueryRow("SELECT count(*) FROM memories").Scan(&n); err != nil || n != 0 {
		t.Fatalf("retained row: %d %v", n, err)
	}
}
func TestScopePrivacyAndTransactions(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	var foreign Entry
	for _, p := range []string{"", "/a", "/b"} {
		e, err := s.Create(ctx, p, "same", "shared token", "owner")
		if err != nil {
			t.Fatal(err)
		}
		if p == "/b" {
			foreign = e
		}
	}
	for _, test := range []struct {
		project, scope string
		want           int
	}{{"/a", "all", 2}, {"/a", "project", 1}, {"/a", "global", 1}, {"", "all", 1}} {
		hits, err := s.Search(ctx, test.project, test.scope, "token", 20)
		if err != nil || len(hits) != test.want {
			t.Fatalf("%+v: %+v %v", test, hits, err)
		}
	}
	if _, err := s.Read(ctx, "/a", foreign.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := s.Update(ctx, "/a", foreign.ID, 1, "same", "leak", "owner"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if err := s.Forget(ctx, "/a", foreign.ID, 1); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	other, err := s.Create(ctx, "/b", "other", "unchanged", "owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Update(ctx, "/b", other.ID, 1, "same", "changed", "owner"); err == nil {
		t.Fatal("unique update succeeded")
	}
	got, err := s.Read(ctx, "/b", other.ID)
	if err != nil || got != other {
		t.Fatalf("failed update changed row: %+v %v", got, err)
	}
	hits, err := s.Search(ctx, "/b", "project", "unchanged", 10)
	if err != nil || len(hits) != 1 {
		t.Fatalf("rollback changed index: %+v %v", hits, err)
	}
}
func TestSafeSearchAndBounds(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	for i := 0; i < 25; i++ {
		if _, err := s.Create(ctx, "", strings.Repeat("k", i+1), strings.Repeat("é", 2000)+" token", "s"); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{`" OR * NEAR( [ :`, "token*", "token OR nope", "--", "", "é"} {
		hits, err := s.Search(ctx, "", "global", q, 1000)
		if err != nil {
			t.Fatalf("query %q: %v", q, err)
		}
		if len(hits) > MaxHits {
			t.Fatal("unbounded hits")
		}
		for _, h := range hits {
			if len([]rune(h.Content)) > 256 {
				t.Fatal("unbounded excerpt")
			}
		}
	}
	for _, a := range []struct{ k, c, source string }{{"", "text", "s"}, {"key", "", "s"}, {"key", "text", ""}, {"key", strings.Repeat("x", MaxContent+1), "s"}} {
		if _, err := s.Create(ctx, "", a.k, a.c, a.source); err == nil {
			t.Fatal("invalid memory accepted")
		}
	}
	if _, err := s.Search(ctx, "", "project", "", 1); err == nil {
		t.Fatal("project scope without project")
	}
	if _, err := s.Search(ctx, "", "bogus", "", 1); err == nil {
		t.Fatal("invalid scope")
	}
}
