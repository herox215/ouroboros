package memory

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// archivistFaults opens a store whose queries fail as f says, see faults.
func archivistFaults(t *testing.T) (*Store, *faults) {
	t.Helper()
	f := &faults{}
	s, err := New(faultDB(t, t.TempDir(), f))
	if err != nil {
		t.Fatal(err)
	}
	return s, f
}

var errQuery = errors.New("query refused")

// failQuery fails the queries that contain sub: the given occurrences of
// them, counted from 1, or every one.
func failQuery(sub string, which ...int) func(string) error {
	n := 0
	return func(query string) error {
		if !strings.Contains(query, sub) {
			return nil
		}
		n++
		if len(which) == 0 || slices.Contains(which, n) {
			return errQuery
		}
		return nil
	}
}

// trigger installs a trigger that refuses what it is put before.
func trigger(t *testing.T, s *Store, before string) {
	t.Helper()
	if _, err := s.db.Exec("CREATE TRIGGER refuse BEFORE " + before + " BEGIN SELECT RAISE(ABORT,'refused'); END;"); err != nil {
		t.Fatal(err)
	}
}

func untrigger(t *testing.T, s *Store) {
	t.Helper()
	if _, err := s.db.Exec("DROP TRIGGER refuse"); err != nil {
		t.Fatal(err)
	}
}

func refused(err error) bool { return err != nil && strings.Contains(err.Error(), "refused") }

func keep(t *testing.T, s *Store, project, key, content string, core bool) Entry {
	t.Helper()
	e, _, err := s.Remember(context.Background(), project, key, content, "session", core, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// Find finds current memories of every scope by any of the words of a
// query, and nothing for a query without words; Get and Version give one
// memory of any scope.
func TestArchivistFindGetVersion(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	a := keep(t, s, "", "tea", "oolong", false)
	b := keep(t, s, t.TempDir(), "coffee", "espresso", false)
	old := keep(t, s, "", "juice", "oolong juice", false)
	if _, err := s.Apply(ctx, Change{Action: "archive", ID: old.ID, Reason: "old"}); err != nil {
		t.Fatal(err)
	}
	hits, err := s.Find(ctx, "oolong espresso", 10)
	if err != nil || len(hits) != 2 {
		t.Fatalf("find: %+v, %v", hits, err)
	}
	if hits, err := s.Find(ctx, "!!", 10); err != nil || hits == nil || len(hits) != 0 {
		t.Fatalf("find without words: %+v, %v", hits, err)
	}
	if e, err := s.Get(ctx, b.ID); err != nil || e != b {
		t.Fatalf("get: %+v, %v", e, err)
	}
	if v, err := s.Version(ctx, a.ID); err != nil || v != 1 {
		t.Fatalf("version: %d, %v", v, err)
	}
	if _, err := s.Version(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("version of a missing memory: %v", err)
	}
}

// A change needs its reason and an action the archivist knows.
func TestArchivistApplyRefuses(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	e := keep(t, s, "", "tea", "oolong", false)
	if _, err := s.Apply(ctx, Change{Action: "archive", ID: e.ID, Reason: " "}); err == nil {
		t.Fatal("applied a change without its reason")
	}
	if _, err := s.Apply(ctx, Change{Action: "delete", ID: e.ID, Reason: "gone"}); !errors.Is(err, ErrNoChange) {
		t.Fatalf("unknown action: %v", err)
	}
}

// A change whose memories cannot be read or whose log cannot be written
// leaves nothing behind.
func TestArchivistApplyFails(t *testing.T) {
	s, f := archivistFaults(t)
	ctx := context.Background()
	e := keep(t, s, "", "tea", "oolong", false)
	f.query = failQuery("WHERE id IN (")
	if _, err := s.Apply(ctx, Change{Action: "archive", ID: e.ID, Reason: "old"}); !errors.Is(err, errQuery) {
		t.Fatalf("unread memories: %v", err)
	}
	f.query = nil
	trigger(t, s, "INSERT ON memory_changes")
	if _, err := s.Apply(ctx, Change{Action: "archive", ID: e.ID, Reason: "old"}); !refused(err) {
		t.Fatalf("unwritten log: %v", err)
	}
	if got, err := s.Get(ctx, e.ID); err != nil || got.Archived {
		t.Fatalf("after a failed change: %+v, %v", got, err)
	}
}

// A merge needs two memories that are there, of one scope, and a text that
// does not say more than theirs.
func TestArchivistMergeRefuses(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	a := keep(t, s, "", "a", "alpha", false)
	b := keep(t, s, "", "b", "beta", false)
	p := keep(t, s, t.TempDir(), "p", "pi", false)
	for name, c := range map[string]Change{
		"one":       {IDs: []string{a.ID}, Content: "alpha"},
		"missing":   {IDs: []string{a.ID, "missing"}, Content: "alpha"},
		"scopes":    {IDs: []string{a.ID, p.ID}, Content: "alpha"},
		"says more": {IDs: []string{a.ID, b.ID}, Content: strings.Repeat("x", 100)},
	} {
		c.Action, c.Reason = "merge", name
		if _, err := s.Apply(ctx, c); err == nil {
			t.Errorf("%s: merged", name)
		}
	}
}

// A merge into the key of a third memory is refused: the archivist never saw
// it. One into a new key or without a key, which keeps the first key, goes
// through.
func TestArchivistMergeIntoAThird(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	a := keep(t, s, "", "a", "alpha", false)
	b := keep(t, s, "", "b", "beta", false)
	c := keep(t, s, "", "c", "gamma", false)
	if _, err := s.Apply(ctx, Change{Action: "merge", IDs: []string{a.ID, b.ID}, Key: "c", Content: "alpha beta", Reason: "same"}); err == nil || !strings.Contains(err.Error(), "already there") {
		t.Fatalf("merge into a third: %v", err)
	}
	if got, err := s.Get(ctx, c.ID); err != nil || got.Content != "gamma" || got.Archived {
		t.Fatalf("third after the refused merge: %+v, %v", got, err)
	}
	if _, err := s.Apply(ctx, Change{Action: "merge", IDs: []string{a.ID, b.ID}, Key: "ab", Content: "alpha beta", Reason: "same"}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Get(ctx, c.ID); err != nil || got.Content != "gamma" {
		t.Fatalf("third after a merge into a new key: %+v, %v", got, err)
	}
	if _, err := s.Apply(ctx, Change{Action: "merge", IDs: []string{a.ID, b.ID}, Content: "alpha beta", Reason: "same"}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Get(ctx, a.ID); err != nil || got.Content != "alpha beta" || got.Archived {
		t.Fatalf("first after a merge without a key: %+v, %v", got, err)
	}
	if got, err := s.Get(ctx, b.ID); err != nil || !got.Archived {
		t.Fatalf("second after a merge without a key: %+v, %v", got, err)
	}
}

// A merge whose target cannot be read, or whose sources cannot be archived,
// fails.
func TestArchivistMergeFails(t *testing.T) {
	s, f := archivistFaults(t)
	ctx := context.Background()
	a := keep(t, s, "", "a", "alpha", false)
	b := keep(t, s, "", "b", "beta", false)
	keep(t, s, "", "c", "gamma", false)
	merge := Change{Action: "merge", IDs: []string{a.ID, b.ID}, Key: "c", Content: "alpha beta", Reason: "same"}
	f.query = failQuery("WHERE project=? AND key=?")
	if _, err := s.Apply(ctx, merge); !errors.Is(err, errQuery) {
		t.Fatalf("unread target: %v", err)
	}
	f.query = nil
	trigger(t, s, "UPDATE OF archived ON memories")
	fresh := merge
	fresh.Key = "ab"
	if _, err := s.Apply(ctx, fresh); !refused(err) {
		t.Fatalf("unarchived sources: %v", err)
	}
	untrigger(t, s)
}

// A change does not land on a text the archivist did not see: a memory that
// changed since is refused, and keeps the text it has.
func TestArchivistChangeRefusesNewerText(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	a := keep(t, s, "", "a", "first", false)
	seen, err := s.Get(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Remember(ctx, "", "a", "second", "session", false, time.Time{}); err != nil {
		t.Fatal(err)
	}
	_, err = s.Apply(ctx, Change{Action: "rewrite", ID: a.ID, Content: "third", Reason: "shorter", Versions: map[string]int64{a.ID: seen.Version}})
	if err == nil || !strings.Contains(err.Error(), "changed since you read it") {
		t.Fatalf("rewrite over a newer text: %v", err)
	}
	if got, err := s.Get(ctx, a.ID); err != nil || got.Content != "second" {
		t.Fatalf("memory after the refused rewrite: %+v, %v", got, err)
	}
}

// A rewrite may not push the core memories over MaxCore, as no other write
// may.
func TestArchivistRewriteKeepsCoreBudget(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	a := keep(t, s, "", "a", strings.Repeat("a", MaxCore), true)
	_, err := s.Apply(ctx, Change{Action: "rewrite", ID: a.ID, Content: strings.Repeat("x", MaxCore+60), Reason: "shorter"})
	if !errors.Is(err, ErrCoreFull) {
		t.Fatalf("rewrite past MaxCore: %v", err)
	}
	if got, err := s.Get(ctx, a.ID); err != nil || got.Content != strings.Repeat("a", MaxCore) {
		t.Fatalf("memory after the refused rewrite: %+v, %v", got, err)
	}
}

// A merge of core memories past MaxCore fails through the write that keeps
// them, though merge itself does not check the budget.
func TestArchivistMergeKeepsCoreBudget(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	a := keep(t, s, "", "a", strings.Repeat("a", 900), true)
	b := keep(t, s, "", "b", strings.Repeat("b", 900), true)
	_, err := s.Apply(ctx, Change{Action: "merge", IDs: []string{a.ID, b.ID}, Key: "ab", Content: strings.Repeat("x", 1800), Reason: "same"})
	if !errors.Is(err, ErrCoreFull) {
		t.Fatalf("merge past MaxCore: %v", err)
	}
}

// A rewrite needs a memory that is there and valid text; it keeps the key
// without one, and fails as the database does.
func TestArchivistRewrite(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	e := keep(t, s, "", "tea", "oolong tea", false)
	for name, c := range map[string]Change{
		"missing": {ID: "missing", Content: "oolong"},
		"invalid": {ID: e.ID, Key: strings.Repeat("k", MaxKey+1), Content: "oolong"},
	} {
		c.Action, c.Reason = "rewrite", name
		if _, err := s.Apply(ctx, c); err == nil {
			t.Errorf("%s: rewritten", name)
		}
	}
	trigger(t, s, "UPDATE OF content ON memories")
	if _, err := s.Apply(ctx, Change{Action: "rewrite", ID: e.ID, Content: "oolong", Reason: "shorter"}); !refused(err) {
		t.Fatalf("refused rewrite: %v", err)
	}
	untrigger(t, s)
	if _, err := s.Apply(ctx, Change{Action: "rewrite", ID: e.ID, Content: "oolong", Reason: "shorter"}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Get(ctx, e.ID); err != nil || got.Key != "tea" || got.Content != "oolong" || got.Version != 2 {
		t.Fatalf("after the rewrite: %+v, %v", got, err)
	}
}

// A split needs a memory that is there, two valid parts of new keys, and
// may not say more than its memory; it fails as the database does.
func TestArchivistSplitRefuses(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	e := keep(t, s, "", "drinks", "tea is oolong. coffee is espresso.", false)
	for name, c := range map[string]Change{
		"missing":   {ID: "missing", Parts: []Part{{"a", "a"}, {"b", "b"}}},
		"one part":  {ID: e.ID, Parts: []Part{{"a", "a"}}},
		"says more": {ID: e.ID, Parts: []Part{{"a", strings.Repeat("a", 100)}, {"b", "b"}}},
		"invalid":   {ID: e.ID, Parts: []Part{{"a", "a"}, {"", "b"}}},
		"taken key": {ID: e.ID, Parts: []Part{{"a", "a"}, {"drinks", "b"}}},
	} {
		c.Action, c.Reason = "split", name
		if _, err := s.Apply(ctx, c); err == nil {
			t.Errorf("%s: split", name)
		}
	}
	trigger(t, s, "UPDATE OF archived ON memories")
	if _, err := s.Apply(ctx, Change{Action: "split", ID: e.ID, Parts: []Part{{"a", "a"}, {"b", "b"}}, Reason: "two"}); !refused(err) {
		t.Fatalf("refused split: %v", err)
	}
}

// A move keeps a core memory core when it stays global, and makes it no
// core one when it goes to a project; a change of the core flag makes a
// memory global when it becomes core.
func TestArchivistScopeAndCore(t *testing.T) {
	yes, no := true, false
	project := t.TempDir()
	for _, c := range []struct {
		name        string
		core        bool
		project     string
		change      Change
		wantProject string
		wantCore    bool
	}{
		{name: "core stays global", core: true, change: Change{Action: "scope"}, wantCore: true},
		{name: "core to a project", core: true, change: Change{Action: "scope", Project: project}, wantProject: project},
		{name: "made core by a move", project: project, change: Change{Action: "scope", Core: &yes}, wantCore: true},
		{name: "core off by a move", core: true, change: Change{Action: "scope", Core: &no}},
		{name: "made core", project: project, change: Change{Action: "core", Core: &yes}, wantCore: true},
		{name: "no core", core: true, change: Change{Action: "core", Core: &no}},
		{name: "no core without a flag", core: true, change: Change{Action: "core"}},
	} {
		s := testStore(t)
		ctx := context.Background()
		e := keep(t, s, c.project, "tea", "oolong", c.core)
		c.change.ID, c.change.Reason = e.ID, c.name
		if _, err := s.Apply(ctx, c.change); err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if got, err := s.Get(ctx, e.ID); err != nil || got.Project != c.wantProject || got.Core != c.wantCore || got.Version != 2 {
			t.Errorf("%s: %+v, %v", c.name, got, err)
		}
	}
}

// A move or a change of the core flag needs a memory that is there, and
// fails as the database does.
func TestArchivistScopeAndCoreFail(t *testing.T) {
	for _, action := range []string{"scope", "core"} {
		s := testStore(t)
		ctx := context.Background()
		e := keep(t, s, "", "tea", "oolong", false)
		if _, err := s.Apply(ctx, Change{Action: action, ID: "missing", Reason: "missing"}); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s of a missing memory: %v", action, err)
		}
		trigger(t, s, "UPDATE OF project ON memories")
		if _, err := s.Apply(ctx, Change{Action: action, ID: e.ID, Reason: "refused"}); !refused(err) {
			t.Errorf("refused %s: %v", action, err)
		}
	}
}

// The log gives 20 changes for a limit it does not take, and fails on a row
// that is no change.
func TestArchivistChanges(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	e := keep(t, s, "", "tea", "oolong", false)
	for i := range 21 {
		if _, err := s.Apply(ctx, Change{Action: "core", ID: e.ID, Reason: fmt.Sprint(i)}); err != nil {
			t.Fatal(err)
		}
	}
	for _, limit := range []int{0, 101} {
		if changes, err := s.Changes(ctx, limit); err != nil || len(changes) != 20 || changes[0].Reason != "20" {
			t.Errorf("limit %d: %d changes, %v", limit, len(changes), err)
		}
	}
	if _, err := s.db.Exec("DROP TABLE memory_changes; CREATE TABLE memory_changes (id INTEGER PRIMARY KEY, at TEXT, action TEXT, reason TEXT, undone INTEGER); INSERT INTO memory_changes(id) VALUES(1)"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Changes(ctx, 10); err == nil {
		t.Fatal("read a change without its time")
	}
}
