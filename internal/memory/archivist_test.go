package memory

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// Two memories that say the same are candidates, are merged in one change,
// and the change can be undone.
func TestArchivistCandidatesMergeUndo(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	a, _, err := s.Remember(ctx, "", "minerva-zugang", "Zugang zu minerva über SSH als root.", "session", false, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := s.Remember(ctx, "", "minerva-ssh-access", "Zugang zu minerva per SSH als root.", "session", false, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	pairs, err := s.Pairs(ctx, 10)
	if err != nil || len(pairs) != 1 {
		t.Fatalf("pairs: %+v, %v", pairs, err)
	}
	candidates, err := s.Candidates(ctx, time.Time{}, 0, 10)
	if err != nil || len(candidates) != 2 {
		t.Fatalf("candidates: %+v, %v", candidates, err)
	}
	rec, err := s.Apply(ctx, Change{
		Action: "merge", IDs: []string{a.ID, b.ID}, Key: "minerva-ssh-access",
		Content: "Zugang zu minerva per SSH als root.", Reason: "both say the same about the access",
	})
	if err != nil {
		t.Fatal(err)
	}
	current, err := s.Current(ctx, 10)
	if err != nil || len(current) != 1 || current[0].Key != "minerva-ssh-access" {
		t.Fatalf("after merge: %+v, %v", current, err)
	}
	changes, err := s.Changes(ctx, 10)
	if err != nil || len(changes) != 1 || changes[0].Action != "merge" || changes[0].Reason == "" {
		t.Fatalf("log: %+v, %v", changes, err)
	}
	if err := s.UndoChange(ctx, rec.ID); err != nil {
		t.Fatal(err)
	}
	current, err = s.Current(ctx, 10)
	if err != nil || len(current) != 2 {
		t.Fatalf("after undo: %+v, %v", current, err)
	}
}

// A rewrite may not say more than its source: the change fails whole, and
// nothing of it is logged.
func TestArchivistRefusesToSayMore(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	e, _, err := s.Remember(ctx, "", "k", "short", "session", false, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Apply(ctx, Change{Action: "rewrite", ID: e.ID, Content: string(make([]byte, 4096)), Reason: "longer"}); err == nil {
		t.Fatal("a rewrite that says more than its source was applied")
	}
	changes, err := s.Changes(ctx, 10)
	if err != nil || len(changes) != 0 {
		t.Fatalf("log after a refused change: %+v, %v", changes, err)
	}
}

// What was examined in its version is no candidate again; a changed one is.
func TestArchivistExaminesOncePerVersion(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	e, _, err := s.Remember(ctx, "", "k", "a memory of a state", "session", false, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetMeta(ctx, "archivist/seen/"+e.ID, "1"); err != nil {
		t.Fatal(err)
	}
	candidates, err := s.Candidates(ctx, time.Now(), 0, 10)
	if err != nil || len(candidates) != 0 {
		t.Fatalf("examined memory is a candidate again: %+v, %v", candidates, err)
	}
	marker := time.Now().Add(-time.Hour)
	if _, _, err := s.Remember(ctx, "", "k", "a memory of another state", "session", false, time.Time{}); err != nil {
		t.Fatal(err)
	}
	candidates, err = s.Candidates(ctx, marker, 0, 10)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("changed memory is no candidate: %+v, %v", candidates, err)
	}
}

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

func reasons(candidates []Candidate, id string) []string {
	for _, c := range candidates {
		if c.ID == id {
			return c.Reasons
		}
	}
	return nil
}

// A memory is a candidate for each thing that is wrong with it: it was
// written long ago and never used, its project directory is gone, or
// it is core while the core memories are nearly full.
func TestArchivistCandidateReasons(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	gone := keep(t, s, filepath.Join(t.TempDir(), "gone"), "gone", "of a project that is gone", false)
	core := keep(t, s, "", "core", strings.Repeat("c", MaxCore*9/10), true)
	candidates, err := s.Candidates(ctx, time.Now().Add(time.Hour), time.Nanosecond, 10)
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string][]string{
		gone.ID: {"written long ago and never used", "its project directory is gone"},
		core.ID: {"written long ago and never used", "the core memories are nearly full"},
	} {
		if got := reasons(candidates, id); !slices.Equal(got, want) {
			t.Errorf("reasons of %s: %q, want %q", id, got, want)
		}
	}
}

// A pair whose note says it was judged in the versions it has now is no
// candidate, and neither is a pair of two scopes.
func TestArchivistPairNote(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	keep(t, s, "", "minerva-zugang", "Zugang zu minerva über SSH als root.", false)
	keep(t, s, "", "minerva-ssh-access", "Zugang zu minerva per SSH als root.", false)
	keep(t, s, t.TempDir(), "minerva", "Zugang zu minerva per SSH als root.", false)
	pairs, err := s.Pairs(ctx, 10)
	if err != nil || len(pairs) != 1 {
		t.Fatalf("pairs: %+v, %v", pairs, err)
	}
	if err := s.SetMeta(ctx, PairName(pairs[0].B, pairs[0].A), PairVersions(pairs[0])); err != nil {
		t.Fatal(err)
	}
	if candidates, err := s.Candidates(ctx, time.Now().Add(time.Hour), 0, 10); err != nil || candidates != nil {
		t.Fatalf("candidates of a judged pair: %+v, %v", candidates, err)
	}
}

// The overlap of the words of two memories is the share of the shorter one,
// and nothing for fewer than three words in common.
func TestArchivistOverlap(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want float64
	}{
		{"", "alpha beta gamma", 0},
		{"alpha beta gamma", "", 0},
		{"alpha beta gamma delta", "alpha beta gamma", 1},
		{"alpha beta gamma", "alpha beta gamma delta", 1},
		{"alpha beta gamma", "alpha beta delta", 0},
	} {
		if got := overlap(wordsOf(c.a), wordsOf(c.b)); got != c.want {
			t.Errorf("overlap(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
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

// Reads that are stopped fail instead of giving part of an answer.
func TestArchivistReadsStopped(t *testing.T) {
	s := testStore(t)
	keep(t, s, "", "tea", "oolong", false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for name, read := range map[string]func() error{
		"current":    func() error { _, err := s.Current(ctx, 10); return err },
		"candidates": func() error { _, err := s.Candidates(ctx, time.Time{}, 0, 10); return err },
		"pairs":      func() error { _, err := s.Pairs(ctx, 10); return err },
		"find":       func() error { _, err := s.Find(ctx, "oolong", 10); return err },
		"version":    func() error { _, err := s.Version(ctx, "x"); return err },
		"changes":    func() error { _, err := s.Changes(ctx, 10); return err },
	} {
		if err := read(); !errors.Is(err, context.Canceled) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// Candidates fails when any of its queries fails.
func TestArchivistCandidatesFail(t *testing.T) {
	for name, query := range map[string]func(string) error{
		"seen":  failQuery("memory_meta", 1),
		"notes": failQuery("memory_meta", 2),
		"core":  failQuery("core=1 AND archived=0"),
		"pairs": failQuery("ORDER BY project,key", 2),
	} {
		s, f := archivistFaults(t)
		keep(t, s, "", "tea", "oolong", false)
		f.query = query
		if _, err := s.Candidates(context.Background(), time.Time{}, 0, 10); !errors.Is(err, errQuery) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// Notes are kept in one write, nothing for none, and are read back by
// prefix; a write or read the database refuses fails.
func TestArchivistMetas(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if err := s.SetMetas(ctx, nil); err != nil {
		t.Fatal(err)
	}
	trigger(t, s, "INSERT ON memory_meta")
	if err := s.SetMetas(ctx, map[string]string{"a": "1"}); !refused(err) {
		t.Fatalf("refused write: %v", err)
	}
	if _, err := s.db.Exec("DROP TABLE memory_meta"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Metas(ctx, "a"); err == nil {
		t.Fatal("read notes without their table")
	}
	if _, err := s.Candidates(ctx, time.Time{}, 0, 10); err == nil {
		t.Fatal("candidates without the table of notes")
	}
	if _, err := s.db.Exec("CREATE TABLE memory_meta (name TEXT, value TEXT); INSERT INTO memory_meta VALUES('a',NULL)"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Metas(ctx, "a"); err == nil {
		t.Fatal("read a note without a value")
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

// A merge into the key of a third memory replaces it, and the undo gives it
// back; one without a key keeps the key of the first memory.
func TestArchivistMergeIntoAThird(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	a := keep(t, s, "", "a", "alpha", false)
	b := keep(t, s, "", "b", "beta", false)
	c := keep(t, s, "", "c", "gamma", false)
	rec, err := s.Apply(ctx, Change{Action: "merge", IDs: []string{a.ID, b.ID}, Key: "c", Content: "alpha beta", Reason: "same"})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.Get(ctx, c.ID); err != nil || got.Content != "alpha beta" {
		t.Fatalf("third after the merge: %+v, %v", got, err)
	}
	if err := s.UndoChange(ctx, rec.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Get(ctx, c.ID); err != nil || got.Content != "gamma" || got.Archived {
		t.Fatalf("third after the undo: %+v, %v", got, err)
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

// A merge of core memories is core unless the change says otherwise.
func TestArchivistMergeCore(t *testing.T) {
	no := false
	for _, c := range []struct {
		core *bool
		want bool
	}{{nil, true}, {&no, false}} {
		s := testStore(t)
		ctx := context.Background()
		a := keep(t, s, "", "a", "alpha", true)
		b := keep(t, s, "", "b", "beta", true)
		if _, err := s.Apply(ctx, Change{Action: "merge", IDs: []string{a.ID, b.ID}, Key: "ab", Core: c.core, Content: "alpha beta", Reason: "same"}); err != nil {
			t.Fatal(err)
		}
		current, err := s.Current(ctx, 10)
		if err != nil || len(current) != 1 || current[0].Core != c.want {
			t.Errorf("core %v: %+v, %v", c.core, current, err)
		}
	}
}

// A merge whose target cannot be read, or whose sources cannot be archived,
// fails; one whose target is found only when it is written still keeps that
// target in its undo.
func TestArchivistMergeFails(t *testing.T) {
	s, f := archivistFaults(t)
	ctx := context.Background()
	a := keep(t, s, "", "a", "alpha", false)
	b := keep(t, s, "", "b", "beta", false)
	c := keep(t, s, "", "c", "gamma", false)
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
	f.query = failQuery("WHERE project=? AND key=?", 1)
	rec, err := s.Apply(ctx, merge)
	f.query = nil
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UndoChange(ctx, rec.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Get(ctx, c.ID); err != nil || got.Content != "gamma" {
		t.Fatalf("target after the undo: %+v, %v", got, err)
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

// A split archives its memory and makes one of each part, in its scope and
// core if it was; the undo archives the parts and gives the memory back.
func TestArchivistSplit(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	e := keep(t, s, "", "drinks", "tea is oolong. coffee is espresso.", true)
	rec, err := s.Apply(ctx, Change{Action: "split", ID: e.ID, Parts: []Part{{"tea", "tea is oolong."}, {"coffee", "coffee is espresso."}}, Reason: "two things"})
	if err != nil {
		t.Fatal(err)
	}
	current, err := s.Current(ctx, 10)
	if err != nil || len(current) != 2 || !current[0].Core || !current[1].Core {
		t.Fatalf("after the split: %+v, %v", current, err)
	}
	if err := s.UndoChange(ctx, rec.ID); err != nil {
		t.Fatal(err)
	}
	current, err = s.Current(ctx, 10)
	if err != nil || len(current) != 1 || current[0].ID != e.ID {
		t.Fatalf("after the undo: %+v, %v", current, err)
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

// An archive needs memories that are there, archives them all, and fails as
// the database does.
func TestArchivistArchive(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	a := keep(t, s, "", "a", "alpha", false)
	b := keep(t, s, "", "b", "beta", false)
	for name, c := range map[string]Change{
		"none":    {},
		"missing": {IDs: []string{a.ID, "missing"}},
	} {
		c.Action, c.Reason = "archive", name
		if _, err := s.Apply(ctx, c); err == nil {
			t.Errorf("%s: archived", name)
		}
	}
	trigger(t, s, "UPDATE OF archived ON memories")
	if _, err := s.Apply(ctx, Change{Action: "archive", ID: a.ID, Reason: "refused"}); !refused(err) {
		t.Fatalf("refused archive: %v", err)
	}
	untrigger(t, s)
	if _, err := s.Apply(ctx, Change{Action: "ARCHIVE ", ID: a.ID, IDs: []string{a.ID, b.ID}, Reason: "old"}); err != nil {
		t.Fatal(err)
	}
	if current, err := s.Current(ctx, 10); err != nil || len(current) != 0 {
		t.Fatalf("after the archive: %+v, %v", current, err)
	}
}

// A change is undone once, an unknown one not at all, and an undo the
// database refuses in any of its steps changes nothing.
func TestArchivistUndoFails(t *testing.T) {
	s, f := archivistFaults(t)
	ctx := context.Background()
	a := keep(t, s, "", "a", "alpha", false)
	b := keep(t, s, "", "b", "beta", false)
	rec, err := s.Apply(ctx, Change{Action: "merge", IDs: []string{a.ID, b.ID}, Key: "ab", Content: "alpha beta", Reason: "same"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UndoChange(ctx, rec.ID+1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("undo of an unknown change: %v", err)
	}
	f.query = failQuery("FROM memory_changes WHERE id=?")
	if err := s.UndoChange(ctx, rec.ID); !errors.Is(err, errQuery) {
		t.Fatalf("unread change: %v", err)
	}
	f.query = nil
	for name, before := range map[string]string{
		"restore": "UPDATE OF key ON memories",
		"created": "UPDATE OF archived ON memories WHEN new.archived=1",
		"log":     "UPDATE ON memory_changes",
	} {
		trigger(t, s, before)
		if err := s.UndoChange(ctx, rec.ID); !refused(err) {
			t.Errorf("%s: %v", name, err)
		}
		untrigger(t, s)
	}
	if current, err := s.Current(ctx, 10); err != nil || len(current) != 1 || current[0].Key != "ab" {
		t.Fatalf("after failed undos: %+v, %v", current, err)
	}
	if err := s.UndoChange(ctx, rec.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.UndoChange(ctx, rec.ID); err == nil {
		t.Fatal("undid a change twice")
	}
	if _, err := s.db.Exec("UPDATE memory_changes SET undone=0,undo='{' WHERE id=?", rec.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.UndoChange(ctx, rec.ID); err == nil {
		t.Fatal("undid a change whose undo is no JSON")
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
