package llm

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"

	"ouroboros/internal/agent"
	"ouroboros/internal/credentials"
	"ouroboros/internal/openai"
)

// said is a model that answers with what it is.
type said string

func (s said) Complete(context.Context, string, []openai.Message, []openai.Tool) (openai.Message, error) {
	return openai.Message{Content: string(s)}, nil
}

// fake is a provider that answers with the key of the access, lists that key
// as its only model and records what it signed out.
func fake(name string, out *[]string) Provider {
	key := func(a Access) string {
		c, err := a.Credentials()
		if err != nil {
			return "error: " + err.Error()
		}
		var s struct{ Key string }
		json.Unmarshal(c.Secret, &s)
		return s.Key
	}
	p := Provider{
		Name:   name,
		Title:  "Fake " + name,
		Models: func(_ context.Context, a Access) (Models, error) { return Models{Chat: []string{key(a)}}, nil },
		Model:  func(a Access) agent.LLM { return said(key(a)) },
	}
	if out != nil {
		p.Catalog = func(a Access) string { return "catalog of " + key(a) }
		p.Logout = func(_ context.Context, a Access) error {
			*out = append(*out, key(a))
			return errors.New("not revoked")
		}
	}
	return p
}

func secret(key string) json.RawMessage {
	b, _ := json.Marshal(map[string]string{"Key": key})
	return b
}

func TestBook(t *testing.T) {
	store := credentials.Open(t.TempDir())
	var out []string
	b := Book{Store: store, Providers: func() []Provider { return []Provider{fake("one", nil), fake("two", &out)} }}
	one, _ := store.Put(credentials.Access{Provider: "one", Name: "First", Credentials: credentials.Credentials{Secret: secret("k1")}})
	two, _ := store.Put(credentials.Access{Provider: "two", Credentials: credentials.Credentials{Title: "Two", Secret: secret("k2")}})
	gone, _ := store.Put(credentials.Access{Provider: "three", Credentials: credentials.Credentials{Secret: secret("k3")}})

	// The model of an access is the one of its provider, with its own key.
	for id, want := range map[string]string{one.ID: "k1", two.ID: "k2"} {
		model, err := b.Model(id)
		if err != nil {
			t.Fatal(err)
		}
		if msg, _ := model.Complete(t.Context(), "", nil, nil); msg.Content != want {
			t.Errorf("the model of %s answers %q, want %q", id, msg.Content, want)
		}
		models, err := b.Models(t.Context(), id)
		if err != nil || !reflect.DeepEqual(models.Chat, []string{want}) {
			t.Errorf("Models(%s) = %v, %v", id, models, err)
		}
	}
	if b.Label(one.ID) != "First" || b.Label(two.ID) != "Two" || b.Label("none") != "" {
		t.Errorf("labels %q, %q, %q", b.Label(one.ID), b.Label(two.ID), b.Label("none"))
	}
	if b.Revision(one.ID) != one.Rev || b.Revision("none") != "" {
		t.Errorf("revisions %q, %q", b.Revision(one.ID), b.Revision("none"))
	}
	if got := b.Secrets(); !reflect.DeepEqual(got, []string{"k1", "k2", "k3"}) {
		t.Errorf("Secrets = %q", got)
	}

	// An access that is no more, and one whose provider is not there.
	if _, err := b.Model("none"); !errors.Is(err, credentials.ErrGone) {
		t.Errorf("Model of no access: %v", err)
	}
	if _, err := b.Models(t.Context(), "none"); !errors.Is(err, credentials.ErrGone) {
		t.Errorf("Models of no access: %v", err)
	}
	if _, err := b.Model(gone.ID); !errors.Is(err, ErrNoProvider) {
		t.Errorf("Model without its provider: %v", err)
	}
	if _, err := b.Provider("three"); !errors.Is(err, ErrNoProvider) {
		t.Errorf("Provider(three): %v", err)
	}

	// The provider names an access to models.dev, if it can.
	if b.Catalog(two.ID) != "catalog of k2" || b.Catalog(one.ID) != "" || b.Catalog(gone.ID) != "" || b.Catalog("none") != "" {
		t.Errorf("catalog %q, %q", b.Catalog(two.ID), b.Catalog(one.ID))
	}

	// Signing out is for the provider that can; its error is handed on.
	if err := b.Logout(t.Context(), one.ID); err != nil || out != nil {
		t.Errorf("Logout without a way to: %v, %v", err, out)
	}
	if err := b.Logout(t.Context(), two.ID); err == nil || !reflect.DeepEqual(out, []string{"k2"}) {
		t.Errorf("Logout = %v, signed out %v", err, out)
	}
	if err := b.Logout(t.Context(), gone.ID); !errors.Is(err, ErrNoProvider) {
		t.Errorf("Logout without its provider: %v", err)
	}
}

// A provider reaches its own access and no other: the credentials of an
// access that became one of another provider stay closed, and so does the
// list of all of them.
func TestAccessIsOfOneProvider(t *testing.T) {
	store := credentials.Open(t.TempDir())
	own, _ := store.Put(credentials.Access{Provider: "one", Credentials: credentials.Credentials{Secret: secret("mine")}})
	other, _ := store.Put(credentials.Access{Provider: "two", Credentials: credentials.Credentials{Secret: secret("theirs")}})
	a := stored{store, own.ID, "one"}

	c, err := a.Credentials()
	if err != nil || string(c.Secret) == "" {
		t.Fatalf("its own credentials: %+v, %v", c, err)
	}
	// It renews what it keeps: the Rev stays, so no agent is built anew.
	if err := a.Renew(func(c *Credentials) error {
		c.Secret = secret("renewed")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.Get(own.ID); got.Rev != own.Rev || !reflect.DeepEqual(got.Secrets(), []string{"renewed"}) {
		t.Fatalf("renewed: %+v", got)
	}
	if got, _ := store.Get(other.ID); !reflect.DeepEqual(got.Secrets(), []string{"theirs"}) {
		t.Fatalf("the other access changed: %+v", got)
	}
	// A renewal that fails changes nothing.
	boom := errors.New("boom")
	if err := a.Renew(func(c *Credentials) error {
		c.Secret = secret("lost")
		return boom
	}); !errors.Is(err, boom) {
		t.Fatalf("Renew = %v", err)
	}
	if got, _ := store.Get(own.ID); !reflect.DeepEqual(got.Secrets(), []string{"renewed"}) {
		t.Fatalf("a failed renewal was saved: %+v", got)
	}

	// The access of another provider, reached by its ID.
	foreign := stored{store, other.ID, "one"}
	if _, err := foreign.Credentials(); !errors.Is(err, errForeign) {
		t.Fatalf("credentials of another provider: %v", err)
	}
	if err := foreign.Renew(func(*Credentials) error { t.Error("renewed"); return nil }); !errors.Is(err, errForeign) {
		t.Fatalf("renewing those of another provider: %v", err)
	}
	// One that is gone.
	missing := stored{store, "none", "one"}
	if _, err := missing.Credentials(); !errors.Is(err, credentials.ErrGone) {
		t.Fatalf("credentials of no access: %v", err)
	}
	if err := missing.Renew(func(*Credentials) error { return nil }); !errors.Is(err, credentials.ErrGone) {
		t.Fatalf("renewing no access: %v", err)
	}
	// Access is all a provider is handed: the interface has no way to the
	// store or to another access.
	var _ Access = a
	if n := reflect.TypeFor[Access]().NumMethod(); n != 2 {
		t.Fatalf("Access has %d methods: what does a provider reach with them?", n)
	}
}

func TestUnsaved(t *testing.T) {
	c := Credentials{About: "a"}
	a := Unsaved(&c)
	if got, err := a.Credentials(); err != nil || got.About != "a" {
		t.Fatalf("%+v, %v", got, err)
	}
	if err := a.Renew(func(c *Credentials) error { c.About = "b"; return nil }); err != nil || c.About != "b" {
		t.Fatalf("renewed to %+v, %v", c, err)
	}
	boom := errors.New("boom")
	if err := a.Renew(func(c *Credentials) error { c.About = "lost"; return boom }); !errors.Is(err, boom) || c.About != "b" {
		t.Fatalf("a failed renewal: %+v, %v", c, err)
	}
}

func TestResolve(t *testing.T) {
	m := Models{Chat: []string{"a", "b"}, All: []string{"a", "b", "tts"}}
	for pick, want := range map[string]string{"1": "a", "2": "b", "b": "b", "tts": "tts"} {
		if got, ok := m.Resolve(pick); !ok || got != want {
			t.Errorf("Resolve(%q) = %q, %v", pick, got, ok)
		}
	}
	for _, pick := range []string{"0", "3", "none", ""} {
		if got, ok := m.Resolve(pick); ok {
			t.Errorf("Resolve(%q) = %q", pick, got)
		}
	}
}

// Browse starts the program of the system that opens a URL, with the URL.
func TestBrowse(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "url")
	script := filepath.Join(dir, "open.sh")
	os.WriteFile(script, []byte("#!/bin/sh\nprintf %s \"$2\" > \"$1\"\n"), 0o700)
	old := opener
	t.Cleanup(func() { opener = old })
	opener = map[string][]string{runtime.GOOS: {script, out}}
	if err := Browse("https://example.com/?a=1&b=2"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if b, _ := os.ReadFile(out); string(b) == "https://example.com/?a=1&b=2" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the URL was not opened")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// A system without an entry uses xdg-open; one without that program fails.
	opener = map[string][]string{}
	t.Setenv("PATH", dir)
	if err := Browse("https://example.com"); err == nil {
		t.Fatal("Browse without a program to open with should fail")
	}
	os.WriteFile(filepath.Join(dir, "xdg-open"), []byte("#!/bin/sh\n"), 0o700)
	if err := Browse("https://example.com"); err != nil {
		t.Fatalf("Browse with xdg-open: %v", err)
	}
}
