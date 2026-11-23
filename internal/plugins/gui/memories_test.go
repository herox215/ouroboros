package gui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ouroboros/internal/config"
	"ouroboros/internal/home"
	"ouroboros/internal/memory"
	"ouroboros/internal/web"
	"ouroboros/plugin"
)

// storeOf is a memory store in a temporary profile, with the memories that
// add puts into it, and the listing of it as the owner makes it.
func storeOf(t *testing.T, add func(s *memory.Store)) (home.Dir, func(ctx context.Context) (plugin.MemoryListing, error)) {
	t.Helper()
	dir := home.Dir(t.TempDir())
	store, err := memory.Open(dir.History())
	if err != nil {
		t.Fatal(err)
	}
	add(store)
	store.Close()
	return dir, func(ctx context.Context) (plugin.MemoryListing, error) {
		s, err := memory.Open(dir.History())
		if err != nil {
			return plugin.MemoryListing{}, err
		}
		defer s.Close()
		return s.List(ctx, false, 0)
	}
}

// The window lists the memories of the profile that have not expired, each
// with its topic, and says how many it left out.
func TestWindowListsTheMemories(t *testing.T) {
	ctx := context.Background()
	_, list := storeOf(t, func(s *memory.Store) {
		for _, m := range []struct{ topic, key, content string }{
			{plugin.MemoryCore, "language", "German"},
			{"matrix", "room", "!abc:server"},
			{plugin.MemoryGeneral, "style", "short\nanswers"},
		} {
			if _, _, err := s.Add(ctx, m.topic, m.key, m.content, "c", time.Time{}); err != nil {
				t.Fatal(err)
			}
		}
		if _, _, err := s.Add(ctx, "matrix", "gone", "old", "c", time.Now().Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
	})
	p := fakeProfile("pythia")
	p.memories = list
	w := newWindow(p, "pythia")
	t.Cleanup(w.Close)

	got, err := w.Memories()
	if err != nil || len(got.Memories) != 3 || got.Total != 4 || got.Hidden != 1 {
		t.Fatalf("the memories: %+v, %v", got, err)
	}
	byKey := map[string]Memory{}
	for _, m := range got.Memories {
		byKey[m.Key] = m
	}
	if m := byKey["language"]; m.Topic != plugin.MemoryCore || m.Content != "German" || m.ID == "" || m.Updated == "" {
		t.Fatalf("the memory of the core: %+v", m)
	}
	if m := byKey["style"]; m.Topic != plugin.MemoryGeneral || m.Content != "short\nanswers" {
		t.Fatalf("a memory with lines: %+v", m)
	}
	if _, gone := byKey["gone"]; gone {
		t.Fatal("an expired memory is listed")
	}
	// The window shows what is there to read, and nothing of how it is kept.
	out, err := json.Marshal(got.Memories[0])
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(out, &fields); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"version", "source_session", "used", "created"} {
		if _, ok := fields[field]; ok {
			t.Fatalf("a memory says %q: %s", field, out)
		}
	}

	// Listing is no use of a memory: nothing in the store changed.
	before, _ := list(ctx)
	if _, err := w.Memories(); err != nil {
		t.Fatal(err)
	}
	after, _ := list(ctx)
	for i := range before.Memories {
		if before.Memories[i] != after.Memories[i] {
			t.Fatalf("listing changed a memory: %+v, %+v", before.Memories[i], after.Memories[i])
		}
	}
}

// A profile without memories lists none, as a list and not as null, and a
// window that cannot ask, or a store that cannot be read, says why.
func TestWindowMemoriesOfAnEmptyOrBrokenProfile(t *testing.T) {
	_, empty := storeOf(t, func(*memory.Store) {})
	p := fakeProfile("pythia")
	p.memories = empty
	w := newWindow(p, "pythia")
	t.Cleanup(w.Close)
	got, err := w.Memories()
	if err != nil || got.Memories == nil || len(got.Memories) != 0 || got.Total != 0 {
		t.Fatalf("the memories of an empty profile: %+v, %v", got, err)
	}
	if out, err := json.Marshal(got); err != nil || string(out) != `{"memories":[],"total":0,"hidden":0}` {
		t.Fatalf("the empty list as the page reads it: %s, %v", out, err)
	}

	bare := newWindow(fakeProfile("pythia"), "pythia")
	t.Cleanup(bare.Close)
	if _, err := bare.Memories(); !errors.Is(err, errNoMemories) {
		t.Fatalf("the memories without an owner: %v", err)
	}

	broken := fakeProfile("pythia")
	broken.memories = func(context.Context) (plugin.MemoryListing, error) {
		return plugin.MemoryListing{}, errors.New("no store")
	}
	w2 := newWindow(broken, "pythia")
	t.Cleanup(w2.Close)
	if _, err := w2.Memories(); err == nil || err.Error() != "no store" {
		t.Fatalf("a store that cannot be read: %v", err)
	}
}

// The page lists the memories behind the login.
func TestPageListsTheMemories(t *testing.T) {
	dir, list := storeOf(t, func(s *memory.Store) {
		if _, _, err := s.Add(context.Background(), "matrix", "room", "!abc:server", "c", time.Time{}); err != nil {
			t.Fatal(err)
		}
	})
	hash, err := web.Hash("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Save(string(dir), config.Config{Model: "m", Web: config.Web{Enabled: true, User: "owner", Password: hash}}); err != nil {
		t.Fatal(err)
	}
	p := fakeProfile("pythia")
	p.dir = dir
	p.memories = list
	front := newWebFront(dir, p)
	server := httptest.NewServer(front.handler)
	t.Cleanup(server.Close)
	client := stillClient()

	res := do(t, client, http.MethodGet, server.URL+"/api/memories", nil, nil)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("the memories without a login: %d", res.StatusCode)
	}
	res.Body.Close()
	login := signIn(t, client, server.URL, "owner", "correct horse battery")
	cookie := sessionOf(t, login)
	login.Body.Close()
	res = do(t, client, http.MethodGet, server.URL+"/api/memories", cookie, nil)
	var got MemoryList
	if err := json.Unmarshal([]byte(body(t, res)), &got); err != nil || res.StatusCode != http.StatusOK || len(got.Memories) != 1 || got.Memories[0].Key != "room" || got.Total != 1 {
		t.Fatalf("the memories of the page: %d, %+v, %v", res.StatusCode, got, err)
	}

	p.memories = nil
	res = do(t, client, http.MethodGet, server.URL+"/api/memories", cookie, nil)
	if got := body(t, res); res.StatusCode != http.StatusConflict {
		t.Fatalf("a page without memories: %d, %q", res.StatusCode, got)
	}
}
