package session

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"ouroboros/internal/assistant"
	"ouroboros/internal/history"
	"ouroboros/internal/openai"
	"ouroboros/internal/workdir"
)

// unavailableArchive fails through the public API without depending on the
// archive's database filename or its former per-session sidecars.
func unavailableArchive(t *testing.T) *history.Archive {
	t.Helper()
	path := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	return history.Open(path)
}

func TestPersistenceEmptySessions(t *testing.T) {
	ctx := context.Background()
	root, project := t.TempDir(), t.TempDir()
	s, _, _ := archived(t, &fakeAgent{}, root, time.Now())
	fresh, err := s.New(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetDirectory(ctx, "project", project); err != nil {
		t.Fatal(err)
	}
	if err := s.SetEffort(ctx, "effort", ""); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{fresh, "project", "effort"} {
		list, err := s.List(ctx)
		if err != nil || !slices.ContainsFunc(list, func(i Info) bool { return i.ID == id }) {
			t.Fatalf("missing %s: %+v, %v", id, list, err)
		}
	}
	s.Close()
	a := &fakeAgent{}
	s, _, _ = archived(t, a, root, time.Now())
	if err := s.SetInitialDirectory(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	list, err := s.List(ctx)
	if err != nil || len(list) != 3 {
		t.Fatalf("after restart: %+v, %v", list, err)
	}
	for id, want := range map[string]string{fresh: "", "project": project} {
		if got := directoryAt(t, s, id); got != want {
			t.Fatalf("%s directory %q, want %q", id, got, want)
		}
	}
	past, _, err := s.Watch(ctx, "effort")
	if err != nil || kinds(past) != "effort" || past[0].Text != "" {
		t.Fatalf("explicit auto effort: %+v, %v", past, err)
	}
	if len(a.resumed) != 0 {
		t.Fatalf("empty sessions must not resume agent memory: %v", a.resumed)
	}
	for _, id := range []string{fresh, "project", "effort"} {
		_, live, err := s.Watch(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.Delete(ctx, id); err != nil {
			t.Fatal(err)
		}
		if _, open := <-live; open {
			t.Fatal("deleted session still watched")
		}
	}
	s.Close()
	s, _, _ = archived(t, &fakeAgent{}, root, time.Now())
	if list, err := s.List(ctx); err != nil || len(list) != 0 {
		t.Fatalf("deleted metadata returned: %+v, %v", list, err)
	}
}

func TestPersistenceProjectConversationAndPeerIsolation(t *testing.T) {
	ctx := context.Background()
	root, project := t.TempDir(), t.TempDir()
	s, arch, _ := archived(t, &fakeAgent{}, root, time.Now())
	if err := s.SetDirectory(ctx, "same", project); err != nil {
		t.Fatal(err)
	}
	if err := s.SetEffort(ctx, "same", "high"); err != nil {
		t.Fatal(err)
	}
	if err := arch.SetTitle(history.Owner("same"), "Saved project"); err != nil {
		t.Fatal(err)
	}
	for _, who := range []history.Who{{Session: "same"}, {Session: "same", Peer: "key"}} {
		if err := arch.Append(who, []openai.Message{{Role: "user", Content: "unique persistence needle"}, {Role: "assistant", Content: "saved answer"}}, nil); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()
	got := make(chan string, 1)
	a := &fakeAgent{run: func(ctx context.Context, text string, _ func(assistant.Step), _ func(string)) (string, error) {
		got <- workdir.Of(ctx) + ":" + openai.EffortOf(ctx)
		return "continued", nil
	}}
	s, arch, _ = archived(t, a, root, time.Now())
	list, err := s.List(ctx)
	if err != nil || len(list) != 1 || list[0].Title != "Saved project" {
		t.Fatalf("list %+v, %v", list, err)
	}
	past, _, err := s.Watch(ctx, "same")
	if err != nil || kinds(past) != "message answer title directory effort" {
		t.Fatalf("restored %+v, %v", past, err)
	}
	asked(t, s, "same", "continue")
	if result := <-got; result != project+":high" {
		t.Fatalf("restored runtime settings %q", result)
	}
	peer := history.Party("key") + "/same"
	hits, err := s.Find(ctx, "unique persistence needle")
	if err != nil || !slices.Contains(hits, "same") || !slices.Contains(hits, peer) {
		t.Fatalf("find %v, %v", hits, err)
	}
	if err := s.Ask(ctx, peer, "not an owner session"); !errors.Is(err, ErrNoSession) {
		t.Fatalf("peer ask %v", err)
	}
	if err := s.Delete(ctx, "same"); err != nil {
		t.Fatal(err)
	}
	if events, err := s.Read(ctx, peer); err != nil || kinds(events) != "message answer" {
		t.Fatalf("owner deletion affected peer: %+v, %v", events, err)
	}
	if _, ok, err := arch.Directory(history.Owner("same")); err != nil || ok {
		t.Fatalf("deleted directory survived: %v, %v", ok, err)
	}
	hits, err = s.Find(ctx, "unique persistence needle")
	if err != nil || !slices.Equal(hits, []string{peer}) {
		t.Fatalf("find after owner deletion %v, %v", hits, err)
	}
	if err := s.Delete(ctx, peer); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, _, _ = archived(t, &fakeAgent{}, root, time.Now())
	if list, _ := s.List(ctx); len(list) != 0 {
		t.Fatalf("owner returned: %+v", list)
	}
	if peers, _ := s.Peers(ctx); len(peers) != 0 {
		t.Fatalf("peer returned: %+v", peers)
	}
	if hits, err := s.Find(ctx, "unique persistence needle"); err != nil || len(hits) != 0 {
		t.Fatalf("deleted history searchable: %v, %v", hits, err)
	}
}

func TestPersistenceMetadataTitleWithoutMessages(t *testing.T) {
	root := t.TempDir()
	arch := history.Open(root)
	if err := arch.SetTitle(history.Owner("title-only"), "A saved title"); err != nil {
		t.Fatal(err)
	}
	s, _, _ := archived(t, &fakeAgent{}, root, time.Now())
	ctx := context.Background()
	past, _, err := s.Watch(ctx, "title-only")
	if err != nil || kinds(past) != "title" || past[0].Text != "A saved title" {
		t.Fatalf("metadata title: %+v, %v", past, err)
	}
	if list, _ := s.List(ctx); len(list) != 1 || list[0].Title != "A saved title" {
		t.Fatalf("list %+v", list)
	}
	if err := s.Delete(ctx, "title-only"); err != nil {
		t.Fatal(err)
	}
}

func TestPersistenceNewFailure(t *testing.T) {
	s := service(t, &fakeAgent{})
	s.Archive(unavailableArchive(t), func(string, ...any) {}, func(string) string { return "" })
	if id, err := s.New(context.Background()); err == nil || id != "" {
		t.Fatalf("new %q, %v", id, err)
	}
	if len(s.sessions) != 0 {
		t.Fatalf("failed creation left sessions: %v", s.sessions)
	}
}
