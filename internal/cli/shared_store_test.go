package cli_test

import (
	"context"
	"testing"

	"ouroboros/internal/history"
	"ouroboros/internal/home"
	"ouroboros/internal/memory"
	"ouroboros/internal/openai"
)

func TestSharedSessionHistoryMemoryStore(t *testing.T) {
	ctx := context.Background()
	dir := home.Dir(t.TempDir())
	project := t.TempDir()
	// Memory may initialize the shared database before history imports it.
	m, err := memory.Open(dir.History())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	a := history.Open(dir.History())
	defer a.Close()
	who := history.Who{Session: "source"}
	if err := a.Append(who, []openai.Message{{Role: "user", Content: "Remember the project test command"}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := a.SetDirectory(history.Owner(who.Session), project); err != nil {
		t.Fatal(err)
	}
	entry, err := m.Create(ctx, project, "tests", "Run make check", who.Session)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	m, err = memory.Open(dir.History())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	a = history.Open(dir.History())
	defer a.Close()
	if got, set, err := a.Directory(history.Owner(who.Session)); err != nil || !set || got != project {
		t.Fatalf("project: %q, %v, %v", got, set, err)
	}
	hits, err := m.Search(ctx, project, "project", "make check", 5)
	if err != nil || len(hits) != 1 || hits[0].ID != entry.ID || hits[0].Source != who.Session {
		t.Fatalf("memory: %+v, %v", hits, err)
	}
	if _, n, err := a.Search(who, "test command", false, 5); err != nil || n != 1 {
		t.Fatalf("history index: %d, %v", n, err)
	}
	// Session deletion is not memory deletion and never cascades to another area.
	if err := a.Delete(history.Owner(who.Session)); err != nil {
		t.Fatal(err)
	}
	if got, err := m.Read(ctx, project, entry.ID); err != nil || got.Content != entry.Content {
		t.Fatalf("memory after session deletion: %+v, %v", got, err)
	}
	if err := m.Forget(ctx, project, entry.ID, entry.Version); err != nil {
		t.Fatal(err)
	}
	hits, err = m.Search(ctx, project, "project", "make check", 5)
	if err != nil || len(hits) != 0 {
		t.Fatalf("forgotten index: %+v, %v", hits, err)
	}
}
