package history

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"ouroboros/internal/openai"
)

func legacy(t *testing.T, dir, relative, text string) string {
	t.Helper()
	path := filepath.Join(dir, relative)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSQLMigrationRestartDeletion(t *testing.T) {
	dir := t.TempDir()
	r := Record{Seq: 7, Answer: 3, Time: time.Now().UTC(), Role: "user", Content: "legacy Roof", Statistics: &Statistics{Context: 123, Cache: json.RawMessage(`{"tokens":9}`)}}
	b, _ := json.Marshal(r)
	original := "not json\n{\"seq\":0}\n" + string(b) + "\n{\"seq\":8,\"answ"
	path := legacy(t, dir, "owner/old.jsonl", original)
	legacy(t, dir, "owner/old.title", "Saved title")
	legacy(t, dir, "owner/old.effort", "")
	legacy(t, dir, "owner/old.directory", "/project")
	legacy(t, dir, "owner/old.goon", "")
	legacy(t, dir, "owner/unanswered.directory", "")
	legacy(t, dir, "owner/unanswered.goon", "")
	legacy(t, dir, "owner/empty.jsonl", "")
	// What the archive did not write itself is left alone.
	legacy(t, dir, "owner/notes.txt", "")
	legacy(t, dir, "owner/not named.jsonl", string(b)+"\n")
	peer := Party("peer")
	legacy(t, dir, peer+"/old.jsonl", string(b)+"\n")
	legacy(t, dir, peer+"/old.goon", "")
	a := Open(dir)
	// Open itself is lazy: nothing has been migrated yet.
	if _, err := os.Stat(filepath.Join(dir, "ouroboros.sqlite")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("eager open: %v", err)
	}
	got, err := a.Records("owner/old")
	if err != nil || !reflect.DeepEqual(got, []Record{r}) {
		t.Fatalf("import: %+v %v", got, err)
	}
	if v, err := a.Title("owner/old"); err != nil || v != "Saved title" {
		t.Fatalf("title: %q %v", v, err)
	}
	if v, ok, err := a.Effort("owner/old"); err != nil || !ok || v != "" {
		t.Fatalf("effort: %q %v %v", v, ok, err)
	}
	if v, ok, err := a.Directory("owner/old"); err != nil || !ok || v != "/project" {
		t.Fatalf("directory: %q %v %v", v, ok, err)
	}
	if v, ok, err := a.Directory("owner/unanswered"); err != nil || !ok || v != "" {
		t.Fatalf("empty reset: %q %v %v", v, ok, err)
	}
	entries, err := a.List()
	if err != nil || len(entries) != 4 {
		t.Fatalf("list: %+v %v", entries, err)
	}
	if err := a.Append(Who{Session: "old"}, question, nil); err != nil {
		t.Fatal(err)
	}
	got, err = a.Records("owner/old")
	if err != nil || len(got) != 3 || got[1].Seq != 8 || got[1].Answer != 4 {
		t.Fatalf("continue: %+v %v", got, err)
	}
	if err := a.SetTitle("owner/old", "SQL title"); err != nil {
		t.Fatal(err)
	}
	if err := a.SetDirectory("owner/old", ""); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	a = Open(dir)
	defer a.Close()
	if v, _ := a.Title("owner/old"); v != "SQL title" {
		t.Fatalf("replayed title: %q", v)
	}
	if v, ok, err := a.Directory("owner/old"); v != "" || !ok || err != nil {
		t.Fatalf("reset lost: %q %v %v", v, ok, err)
	}
	if ids, err := a.GoingOn(); err != nil || strings.Join(ids, ",") != "owner/old,owner/unanswered" {
		t.Fatalf("goon: %v %v", ids, err)
	}
	a.Close()
	a = Open(dir)
	defer a.Close()
	if ids, err := a.GoingOn(); err != nil || len(ids) != 0 {
		t.Fatalf("replayed goon: %v %v", ids, err)
	}
	if err := a.Delete("owner/old"); err != nil {
		t.Fatal(err)
	}
	a.Close()
	a = Open(dir)
	defer a.Close()
	if _, err := a.Records("owner/old"); !errors.Is(err, ErrNotArchived) {
		t.Fatalf("resurrected: %v", err)
	}
	if v, ok, err := a.Directory("owner/old"); v != "" || ok || err != nil {
		t.Fatalf("deleted metadata: %q %v %v", v, ok, err)
	}
	if _, err := a.Records(peer + "/old"); err != nil {
		t.Fatalf("peer deleted: %v", err)
	}
	if _, hits, err := a.Search(Who{Session: "old"}, "Roof", true, 0); err != nil || hits != 0 {
		t.Fatalf("deleted FTS: %d %v", hits, err)
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != original {
		t.Fatalf("legacy changed: %q %v", b, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "owner/old.title")); string(b) != "Saved title" {
		t.Fatal("original metadata changed")
	}
	if err := a.Append(Who{Session: "old"}, question, nil); err != nil {
		t.Fatal(err)
	}
	got, err = a.Records("owner/old")
	if err != nil || len(got) != 2 || got[0].Seq != 1 || got[0].Answer != 1 {
		t.Fatalf("rearchive: %+v %v", got, err)
	}
	if v, _ := a.Title("owner/old"); v != "" {
		t.Fatalf("resurrected metadata %q", v)
	}
}

func TestSQLMigrationRollback(t *testing.T) {
	dir := t.TempDir()
	legacy(t, dir, "owner/a.jsonl", "{\"seq\":1,\"role\":\"user\",\"content\":\"first\"}\n")
	bad := filepath.Join(dir, "owner/z.title")
	if err := os.Mkdir(bad, 0700); err != nil {
		t.Fatal(err)
	}
	a := Open(dir)
	defer a.Close()
	if _, err := a.List(); err == nil {
		t.Fatal("incomplete migration succeeded")
	}
	// Fix the invalid fixture and retry the same handle. The earlier session
	// must not be duplicated and the completed migration must survive reopening.
	if err := os.Remove(bad); err != nil {
		t.Fatal(err)
	}
	legacy(t, dir, "owner/z.title", "before first answer")
	got, err := a.Records("owner/a")
	if err != nil || len(got) != 1 {
		t.Fatalf("retry: %+v %v", got, err)
	}
	a.Close()
	b := Open(dir)
	defer b.Close()
	got, err = b.Records("owner/a")
	if err != nil || len(got) != 1 {
		t.Fatalf("restart: %+v %v", got, err)
	}
	if v, err := b.Title("owner/z"); err != nil || v != "before first answer" {
		t.Fatalf("metadata only: %q %v", v, err)
	}
}

func TestSQLConcurrentHandles(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a := Open(dir)
			defer a.Close()
			for j := 0; j < 8; j++ {
				if err := a.Append(Who{Session: "shared"}, question, nil); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	a := Open(dir)
	defer a.Close()
	got, err := a.Records("owner/shared")
	if err != nil || len(got) != 64 {
		t.Fatalf("concurrent records: %d %v", len(got), err)
	}
	for i, r := range got {
		if r.Seq != i+1 || r.Answer != i/2+1 {
			t.Fatalf("sequence: %+v", r)
		}
	}
}

func TestSQLSubstringCandidates(t *testing.T) {
	a := Open(t.TempDir())
	defer a.Close()
	msgs := []openai.Message{{Role: "user", Content: "İSTANBUL Needle substring a_b% c\"d x.y café Σ KELVIN K 你好\x00after-null"},
		{Role: "assistant", ToolCalls: []openai.ToolCall{{ID: "c", Function: openai.FunctionCall{Name: "run", Arguments: "prefix-quoted argument"}}}}}
	if err := a.Append(owner, msgs, nil); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"need", "STR", "a_b%", "c\"d", "x.y", "fé", "Σ", "kelvin", "你好", "prefix-quoted", "quoted argument", "after-null", "e", "%", "not found"} {
		want := 0
		for _, r := range all(t, a, owner) {
			if _, ok := match(r, strings.ToLower(q)); ok {
				want++
			}
		}
		_, got, err := a.Search(owner, q, false, 50)
		if err != nil || got != want {
			t.Errorf("%s: %d != %d (%v)", fmt.Sprintf("%q", q), got, want, err)
		}
	}
}
