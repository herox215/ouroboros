package setup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ouroboros/internal/config"
	"ouroboros/internal/credentials"
	"ouroboros/internal/home"
	"ouroboros/internal/identity"
	"ouroboros/internal/llm"
	"ouroboros/internal/llm/llmtest"
)

// two are the providers of the tests: neither is an OpenAI-compatible API.
func two() []llm.Provider { return []llm.Provider{llmtest.Provider("one"), llmtest.Provider("two")} }

// session is the setup of a new node with the providers of the tests.
func session(t *testing.T) *Session {
	t.Helper()
	return New(home.Dir(t.TempDir()), two, config.Config{})
}

// token answers the question of a test provider with tokens, one after the
// other.
func token(tokens ...string) *llmtest.Dialog {
	return &llmtest.Dialog{Answers: map[string][]string{"Token": tokens}}
}

// signedIn is the setup of a node that signed in to provider with tok and
// saved model: it returns the setup the node has from then on, and the access.
func signedIn(t *testing.T, s *Session, provider, tok, model string) Access {
	t.Helper()
	if _, err := s.Login(context.Background(), provider, token(tok)); err != nil {
		t.Fatal(err)
	}
	if err := s.Save(model, "alpha"); err != nil {
		t.Fatal(err)
	}
	return active(t, s)
}

// active is the access the node answers through.
func active(t *testing.T, s *Session) Access {
	t.Helper()
	list, err := s.Accesses()
	if err != nil {
		t.Fatal(err)
	}
	at := slices.IndexFunc(list, func(a Access) bool { return a.Active })
	if at < 0 {
		t.Fatalf("no access is in use: %+v", list)
	}
	return list[at]
}

// A second provider signs in with its own questions, is saved as an access of
// its own, and the configuration names it without holding what it signed in
// with.
func TestLoginAndSave(t *testing.T) {
	s := session(t)
	ctx := context.Background()
	if got := s.Providers(); !reflect.DeepEqual(got, []Provider{{"one", "Test one"}, {"two", "Test two"}}) {
		t.Fatalf("providers %+v", got)
	}
	if list, err := s.Accesses(); err != nil || len(list) != 0 {
		t.Fatalf("a new node has the accesses %+v, %v", list, err)
	}
	if err := s.Save("two-large", "alpha"); err == nil {
		t.Fatal("saved without signing in")
	}

	d := token("bad", llmtest.Token)
	models, err := s.Login(ctx, "two", d)
	want := Models{Chat: []string{"two-small", "two-large"}, All: []string{"two-small", "two-large"}, Default: "two-small"}
	if err != nil || !reflect.DeepEqual(models, want) {
		t.Fatalf("got %+v, %v", models, err)
	}
	if !reflect.DeepEqual(d.Rejected(), []string{"wrong token"}) || len(d.Asked()) != 2 || d.Asked()[0].Title != "Token" || !d.Asked()[0].Secret {
		t.Fatalf("the provider asked %+v and rejected %v", d.Asked(), d.Rejected())
	}
	// Until it is saved, nothing is kept.
	if list, _ := s.book.Store.List(); len(list) != 0 {
		t.Fatalf("kept before saving: %+v", list)
	}

	if err := s.Save("two-large", " alpha "); err != nil {
		t.Fatal(err)
	}
	cfg, _ := s.home.Config()
	if cfg.Model != "two-large" || cfg.Name != "alpha" || cfg.ID() == "" || cfg.Access == "" || cfg.BaseURL != "" || cfg.APIKey != "" {
		t.Fatalf("saved %+v", cfg)
	}
	if !reflect.DeepEqual(cfg.Tools, config.DefaultTools()) {
		t.Fatalf("saved %+v", cfg.Tools)
	}
	saved, err := s.book.Store.Get(cfg.Access)
	if err != nil || saved.Provider != "two" || saved.About != "account good" || !reflect.DeepEqual(saved.Secrets(), []string{"good"}) {
		t.Fatalf("the access: %+v, %v", saved, err)
	}
	// The token is in the credentials, which are the owner's alone, and
	// nowhere in the configuration.
	written, _ := os.ReadFile(filepath.Join(string(s.home), "config.json"))
	if strings.Contains(string(written), "good") {
		t.Fatalf("the configuration holds the token: %s", written)
	}
	if info, _ := os.Stat(filepath.Join(string(s.home), credentials.FileName)); info.Mode().Perm() != 0o600 {
		t.Fatalf("the credentials are readable by others: %v", info.Mode())
	}
	if got := active(t, s); got.ID != cfg.Access || got.Provider != "two" || got.Label != "Test two" || got.About != "account good" || got.Missing || got.Name != "" {
		t.Fatalf("as the owner sees it: %+v", got)
	}
	// What was saved is what the setup starts from next time.
	if s.Name() != "alpha" {
		t.Fatalf("after saving: %q", s.Name())
	}
	// The model the node answers with is offered again for that access.
	if again, err := s.Use(ctx, cfg.Access); err != nil || again.Default != "two-large" {
		t.Fatalf("Use offers %+v, %v", again, err)
	}
}

// Signing in to an access again replaces what it holds once it is saved: its
// ID stays, and what refers to it.
func TestAgain(t *testing.T) {
	s := session(t)
	ctx := context.Background()
	first := signedIn(t, s, "one", "good-1", "one-large")
	if err := s.Rename(first.ID, "Private"); err != nil {
		t.Fatal(err)
	}
	before, _ := s.book.Store.Get(first.ID)

	d := token("good-2")
	if _, err := s.Again(ctx, first.ID, d); err != nil {
		t.Fatal(err)
	}
	if d.Asked()[0].Hint != "signing in again" {
		t.Fatalf("the provider was not told what the access held: %+v", d.Asked())
	}
	if kept, _ := s.book.Store.Get(first.ID); kept.Rev != before.Rev || kept.Secrets()[0] != "good-1" {
		t.Fatalf("replaced before it was saved: %+v", kept)
	}
	if err := s.Save("one-small", "alpha"); err != nil {
		t.Fatal(err)
	}
	after, _ := s.book.Store.Get(first.ID)
	if after.Rev == before.Rev || after.Secrets()[0] != "good-2" || after.Name != "Private" {
		t.Fatalf("signed in again: %+v", after)
	}
	if list, _ := s.Accesses(); len(list) != 1 || list[0].ID != first.ID || list[0].Label != "Private" || !list[0].Active {
		t.Fatalf("accesses %+v", list)
	}
	// Saved once more, it is not written again.
	if err := s.Save("one-small", "alpha"); err != nil {
		t.Fatal(err)
	}
	if again, _ := s.book.Store.Get(first.ID); again.Rev != after.Rev {
		t.Fatal("an access that was saved is written again")
	}

	if _, err := s.Again(ctx, "none", token()); !errors.Is(err, credentials.ErrGone) {
		t.Fatalf("Again of no access: %v", err)
	}
}

// One provider may have several accesses, as two accounts: each has an ID of
// its own, which renaming does not change, and the same names of models do
// not get mixed up.
func TestSeveralAccessesOfOneProvider(t *testing.T) {
	s := session(t)
	ctx := context.Background()
	private := signedIn(t, s, "one", "good-private", "one-small")
	work := signedIn(t, s, "one", "good-work", "one-small")
	if private.ID == work.ID {
		t.Fatal("two accesses share an ID")
	}
	if err := s.Rename(private.ID, "Private"); err != nil {
		t.Fatal(err)
	}
	if err := s.Rename(work.ID, "Work"); err != nil {
		t.Fatal(err)
	}
	list, err := s.Accesses()
	want := []Access{
		{ID: private.ID, Provider: "one", Name: "Private", Label: "Private", About: "account good-private"},
		{ID: work.ID, Provider: "one", Name: "Work", Label: "Work", About: "account good-work", Active: true},
	}
	if err != nil || !reflect.DeepEqual(list, want) {
		t.Fatalf("accesses %+v, %v", list, err)
	}
	// The model of each is answered with its own account.
	for id, account := range map[string]string{private.ID: "good-private", work.ID: "good-work"} {
		model, err := s.book.Model(id)
		if err != nil {
			t.Fatal(err)
		}
		if msg, _ := model.Complete(ctx, "one-small", nil, nil); msg.Content != "[one one-small as "+account+"] " {
			t.Errorf("%s answers %q", id, msg.Content)
		}
	}
	// The model of the node is offered for the access it answers through,
	// not for another one that has a model of the same name, and not when
	// the access has no such model any more.
	if _, err := s.Pick(work.ID, "one-large"); err != nil {
		t.Fatal(err)
	}
	s.cfg, _ = s.home.Config()
	if models, err := s.Use(ctx, work.ID); err != nil || models.Default != "one-large" {
		t.Fatalf("the access in use offers %+v, %v", models, err)
	}
	if models, err := s.Use(ctx, private.ID); err != nil || models.Default != "one-small" {
		t.Fatalf("another access offers %+v, %v", models, err)
	}
	s.cfg.Model = "gone"
	if models, err := s.Use(ctx, work.ID); err != nil || models.Default != "one-small" {
		t.Fatalf("an access that lost the model offers %+v, %v", models, err)
	}
	// Picking the same model of the other access switches the access.
	if switched, err := s.Pick(private.ID, "one-small"); err != nil || !switched {
		t.Fatalf("Pick = %v, %v", switched, err)
	}
	if got := active(t, s); got.ID != private.ID {
		t.Fatalf("in use: %+v", got)
	}
	if err := s.Rename("none", "x"); !errors.Is(err, credentials.ErrGone) {
		t.Fatalf("Rename of no access: %v", err)
	}
}

// revoking is a provider that says what it signed out, and may fail to.
func revoking(name string, out *[]string, err error) llm.Provider {
	p := llmtest.Provider(name)
	p.Logout = func(_ context.Context, a llm.Access) error {
		c, _ := a.Credentials()
		*out = append(*out, c.About)
		return err
	}
	return p
}

func TestRemove(t *testing.T) {
	ctx := context.Background()
	var out []string
	var failing error
	s := New(home.Dir(t.TempDir()), func() []llm.Provider {
		return []llm.Provider{revoking("one", &out, failing), llmtest.Provider("two")}
	}, config.Config{})

	// The only access stays.
	only := signedIn(t, s, "one", "good-1", "one-large")
	if _, err := s.Remove(ctx, only.ID); !errors.Is(err, ErrLastAccess) {
		t.Fatalf("removing the last access: %v", err)
	}
	if _, err := s.Remove(ctx, "none"); !errors.Is(err, credentials.ErrGone) {
		t.Fatalf("removing no access: %v", err)
	}

	// One that is not in use is signed out and removed; the node answers as before.
	second := signedIn(t, s, "two", "good-2", "two-large")
	third := signedIn(t, s, "one", "good-3", "one-small")
	s.Models(ctx) // what is kept of their models
	note, err := s.Remove(ctx, only.ID)
	if err != nil || note != "" || !reflect.DeepEqual(out, []string{"account good-1"}) {
		t.Fatalf("removed with %q, %v, signed out %v", note, err, out)
	}
	if got := active(t, s); got.ID != third.ID || s.cfg.Access != third.ID || s.cfg.Model != "one-small" {
		t.Fatalf("in use: %+v, the setup goes on from %+v", got, s.cfg)
	}
	if kept, _ := os.ReadFile(s.home.Models()); strings.Contains(string(kept), only.ID) || !strings.Contains(string(kept), second.ID) {
		t.Fatalf("the models kept: %s", kept)
	}

	// The access in use hands over to the first of the others, with the
	// model that one offers: exactly one access is in use afterwards. That
	// it could not be signed out at the provider is told, and it is removed.
	failing = errors.New("no network")
	note, err = s.Remove(ctx, third.ID)
	if err != nil || !strings.Contains(note, "the agents answer with two-small of Test two now") || !strings.Contains(note, "not signed out at the provider: no network") {
		t.Fatalf("removed the access in use with %q, %v", note, err)
	}
	cfg, _ := s.home.Config()
	list, _ := s.Accesses()
	if cfg.Access != second.ID || cfg.Model != "two-small" || len(list) != 1 || !list[0].Active || !cfg.Complete() {
		t.Fatalf("after removing the access in use: %+v, %+v", cfg, list)
	}
	if s.cfg.Access != second.ID || s.cfg.Model != "two-small" {
		t.Fatalf("the setup goes on from %+v", s.cfg)
	}
}

// An access another profile of the node answers through stays.
func TestRemoveKeepsTheAccessOfAnotherProfile(t *testing.T) {
	ctx := context.Background()
	root := home.Dir(filepath.Join(t.TempDir(), "node"))
	t.Cleanup(func() { os.RemoveAll(string(root) + "-workspaces") })
	root.Create()
	s := New(root, func() []llm.Provider { return []llm.Provider{llmtest.Provider("one"), llmtest.Provider("two")} }, config.Config{})
	first := signedIn(t, s, "one", "good-1", "one-large")
	luna, err := root.AddProfile("Luna")
	if err != nil {
		t.Fatal(err)
	}
	signedIn(t, s, "two", "good-2", "two-large")
	if _, err := s.Remove(ctx, first.ID); err == nil || err.Error() != "Test one is in use by the profile Luna: give it another access first" {
		t.Fatalf("Remove = %v", err)
	}
	// So does one it answers peers through.
	other := signedIn(t, s, "one", "good-1", "one-large")
	theirs, _ := luna.Config()
	theirs.PeerAccess, theirs.PeerModel = other.ID, "one-small"
	if err := luna.SaveConfig(theirs); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Remove(ctx, other.ID); err == nil || err.Error() != "Test one is in use by the profile Luna: give it another access first" {
		t.Fatalf("Remove = %v", err)
	}
	os.WriteFile(filepath.Join(string(luna), "config.json"), []byte("{"), 0o600)
	if _, err := s.Remove(ctx, first.ID); err == nil || strings.Contains(err.Error(), "in use") {
		t.Fatalf("a profile that cannot be read: %v", err)
	}
	os.RemoveAll(filepath.Join(string(root), "profiles"))
	os.WriteFile(filepath.Join(string(root), "profiles"), nil, 0o600)
	if _, err := s.Remove(ctx, first.ID); err == nil || strings.Contains(err.Error(), "in use") {
		t.Fatalf("profiles that cannot be listed: %v", err)
	}
	os.Remove(filepath.Join(string(root), "profiles"))
	if note, err := s.Remove(ctx, first.ID); err != nil || note != "" {
		t.Fatalf("Remove = %q, %v", note, err)
	}
}

// An access that cannot take over does not: the one in use stays, and
// nothing is signed out.
func TestRemoveKeepsTheAccessInUseWithoutAnotherToUse(t *testing.T) {
	ctx := context.Background()
	var out []string
	empty := llmtest.Provider("empty")
	empty.Models = func(context.Context, llm.Access) (llm.Models, error) { return llm.Models{}, nil }
	s := New(home.Dir(t.TempDir()), func() []llm.Provider {
		return []llm.Provider{revoking("one", &out, nil), llmtest.Provider("two"), empty}
	}, config.Config{})
	down, err := s.book.Store.Put(credentials.Access{Provider: "two", Credentials: llm.Credentials{Title: "Down", Secret: []byte(`{"token":"good-down"}`)}})
	if err != nil {
		t.Fatal(err)
	}
	used := signedIn(t, s, "one", "good-1", "one-large")
	if _, err := s.Remove(ctx, used.ID); err == nil || err.Error() != "Test one is in use, and Down cannot take over: two is down" {
		t.Fatalf("Remove = %v", err)
	}
	// One that lists no model cannot take over either.
	s.book.Store.Remove(down.ID)
	s.book.Store.Change(func(list []credentials.Access) ([]credentials.Access, error) {
		return append([]credentials.Access{{ID: "e", Provider: "empty", Credentials: llm.Credentials{Title: "Empty", Secret: []byte(`{"token":"good"}`)}}}, list...), nil
	})
	if _, err := s.Remove(ctx, used.ID); err == nil || err.Error() != "Test one is in use, and Empty cannot take over: it offers no model" {
		t.Fatalf("Remove = %v", err)
	}
	if got := active(t, s); got.ID != used.ID || len(out) != 0 {
		t.Fatalf("in use: %+v, signed out %v", got, out)
	}

	// An access that is put in use while it is removed, as by another front
	// end: what was decided for it does not hold any more, and it stays.
	s.book.Store.Remove("e")
	other := signedIn(t, s, "two", "good-2", "two-small")
	if _, err := s.Pick(used.ID, "one-small"); err != nil {
		t.Fatal(err)
	}
	two := llmtest.Provider("two")
	two.Logout = func(context.Context, llm.Access) error {
		// Between the look at the configuration and the change of it.
		_, err := s.Pick(other.ID, "two-small")
		return err
	}
	racing := New(s.home, func() []llm.Provider { return []llm.Provider{revoking("one", &out, nil), two} }, config.Config{})
	if _, err := racing.Remove(ctx, other.ID); err == nil || !strings.Contains(err.Error(), "put in use just now") {
		t.Fatalf("removing an access that was put in use meanwhile: %v", err)
	}
	if got := active(t, s); got.ID != other.ID {
		t.Fatalf("in use: %+v", got)
	}
}

// The models of all accesses are listed together. One that does not answer
// does not keep the others back: it shows with why, and with the models it
// listed the last time.
func TestModels(t *testing.T) {
	ctx := context.Background()
	var down atomic.Bool
	hang := make(chan struct{})
	t.Cleanup(func() { close(hang) })
	flaky := llmtest.Provider("flaky")
	listed := flaky.Models
	flaky.Models = func(ctx context.Context, a llm.Access) (llm.Models, error) {
		if down.Load() {
			return llm.Models{}, errors.New("flaky is unreachable")
		}
		return listed(ctx, a)
	}
	slow := llmtest.Provider("slow")
	slow.Models = func(context.Context, llm.Access) (llm.Models, error) {
		<-hang // it does not even watch its context
		return llm.Models{}, nil
	}
	providers := []llm.Provider{llmtest.Provider("one"), flaky, slow}
	s := New(home.Dir(t.TempDir()), func() []llm.Provider { return providers }, config.Config{})
	if _, err := s.Models(ctx); !errors.Is(err, config.ErrIncomplete) {
		t.Fatalf("a node that is not set up: %v", err)
	}
	one := signedIn(t, s, "one", "good-1", "one-large")
	two := signedIn(t, s, "flaky", "good-2", "flaky-small")
	never, _ := s.book.Store.Put(credentials.Access{Provider: "one", Name: "Never", Credentials: llm.Credentials{Secret: []byte(`{"token":"good-down"}`)}})
	gone, _ := s.book.Store.Put(credentials.Access{Provider: "gone", Credentials: llm.Credentials{Secret: []byte(`{"token":"good"}`)}})

	got, err := s.Models(ctx)
	if err != nil || len(got) != 4 {
		t.Fatalf("got %+v, %v", got, err)
	}
	want := []Listed{
		{Access: Access{ID: one.ID, Provider: "one", Label: "Test one", About: "account good-1"}, Models: []string{"one-small", "one-large"}},
		{Access: Access{ID: two.ID, Provider: "flaky", Label: "Test flaky", About: "account good-2", Active: true}, Models: []string{"flaky-small", "flaky-large"}},
		{Access: Access{ID: never.ID, Provider: "one", Name: "Never", Label: "Never"}, Err: "one is down"},
		{Access: Access{ID: gone.ID, Provider: "gone", Label: "gone", Missing: true}, Err: `provider "gone": its provider is not part of this ouro`},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}

	// Only lists that came are kept: an access that listed nothing is not in the file.
	if kept, _ := os.ReadFile(s.home.Models()); strings.Contains(string(kept), never.ID) || strings.Contains(string(kept), gone.ID) || !strings.Contains(string(kept), one.ID) {
		t.Fatalf("kept %s", kept)
	}
	// The access that listed its models before shows them when it is down;
	// the one that never did shows none. Another setup knows them too: they
	// are kept with the node.
	down.Store(true)
	s = New(s.home, s.book.Providers, s.cfg)
	got, err = s.Models(ctx)
	want[1].Err = "flaky is unreachable"
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("with an access down:\ngot  %+v\nwant %+v", got, want)
	}
	// And again: what is kept stays while the access is down.
	if got, _ = s.Models(ctx); !reflect.DeepEqual(got, want) {
		t.Fatalf("down for longer:\ngot  %+v\nwant %+v", got, want)
	}

	// One that does not answer at all is not waited for.
	old := listTime
	t.Cleanup(func() { listTime = old })
	listTime = 50 * time.Millisecond
	hanging, _ := s.book.Store.Put(credentials.Access{Provider: "slow", Credentials: llm.Credentials{Title: "Slow"}})
	start := time.Now()
	got, err = s.Models(ctx)
	if err != nil || len(got) != 5 || got[4].Access.ID != hanging.ID || got[4].Err != "it did not answer in time" || got[0].Err != "" || len(got[0].Models) != 2 {
		t.Fatalf("with an access that hangs: %+v, %v", got, err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("the list waited %v for an access that does not answer", took)
	}

	// What is kept of the models is not needed: without it, or with one
	// that cannot be read, the list comes all the same.
	os.WriteFile(s.home.Models(), []byte("{"), 0o600)
	s.forget(one.ID)
	if b, _ := os.ReadFile(s.home.Models()); string(b) != "{" {
		t.Fatalf("a file that cannot be read was written: %s", b)
	}
	if got, err = s.Models(ctx); err != nil || len(got[0].Models) != 2 || len(got[1].Models) != 0 {
		t.Fatalf("without what was kept: %+v, %v", got, err)
	}
	os.Remove(s.home.Models())
	s.forget(one.ID)
	if _, err := os.Stat(s.home.Models()); err == nil {
		t.Fatal("forgetting made a file")
	}
}

func TestPick(t *testing.T) {
	s := session(t)
	one := signedIn(t, s, "one", "good-1", "one-large")
	two := signedIn(t, s, "two", "good-2", "two-large")

	// Another model of the same access is no other access.
	if switched, err := s.Pick(two.ID, " two-small "); err != nil || switched {
		t.Fatalf("Pick = %v, %v", switched, err)
	}
	if cfg, _ := s.home.Config(); cfg.Access != two.ID || cfg.Model != "two-small" {
		t.Fatalf("picked %+v", cfg)
	}
	if switched, err := s.Pick(one.ID, "one-small"); err != nil || !switched {
		t.Fatalf("Pick of another access = %v, %v", switched, err)
	}
	if cfg, _ := s.home.Config(); cfg.Access != one.ID || cfg.Model != "one-small" || cfg.Name != "alpha" {
		t.Fatalf("picked %+v", cfg)
	}

	if _, err := s.Pick(one.ID, " "); !errors.Is(err, ErrNoModel) {
		t.Fatalf("no model: %v", err)
	}
	if switched, err := s.Pick("none", "m"); !errors.Is(err, credentials.ErrGone) || switched {
		t.Fatalf("no access: %v, %v", switched, err)
	}
	// A node that is not set up has nothing to pick for.
	fresh := session(t)
	a, _ := fresh.book.Store.Put(credentials.Access{Provider: "one"})
	if switched, err := fresh.Pick(a.ID, "one-small"); !errors.Is(err, config.ErrIncomplete) || switched {
		t.Fatalf("not set up: %v, %v", switched, err)
	}
}

func TestLoginFails(t *testing.T) {
	s := session(t)
	if _, err := s.Login(context.Background(), "three", token()); !errors.Is(err, llm.ErrNoProvider) {
		t.Fatalf("no such provider: %v", err)
	}
	// The owner gave up.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Login(ctx, "one", token()); !errors.Is(err, context.Canceled) {
		t.Fatalf("given up: %v", err)
	}
	// Signed in, but the models do not come: nothing to save.
	if _, err := s.Login(context.Background(), "one", token(llmtest.Down)); err == nil || err.Error() != "one is down" {
		t.Fatalf("no models: %v", err)
	}
	if err := s.Save("one-small", "alpha"); err == nil {
		t.Fatal("a failed login is nothing to save")
	}
	if _, err := s.Use(context.Background(), "none"); !errors.Is(err, credentials.ErrGone) {
		t.Fatalf("Use of no access: %v", err)
	}
	down, _ := s.book.Store.Put(credentials.Access{Provider: "one", Credentials: llm.Credentials{Secret: []byte(`{"token":"good-down"}`)}})
	if _, err := s.Use(context.Background(), down.ID); err == nil || err.Error() != "one is down" {
		t.Fatalf("Use of an access that is down: %v", err)
	}
	// A provider that lists nothing to choose offers nothing.
	if got := s.offer(Models{}, ""); got.Default != "" {
		t.Fatalf("offer of nothing: %+v", got)
	}
	// One that names no default offers its first model.
	if got := s.offer(Models{Chat: []string{"a", "b"}}, ""); got.Default != "a" {
		t.Fatalf("offer without a default: %+v", got)
	}
}

func TestSaveKeepsWhatTheNodeHas(t *testing.T) {
	key := identity.Generate().Private()
	tools := &config.Tools{H2A: config.Named(), Protected: &[]string{}} // the owner's decision to protect nothing
	// A configuration from before there were accesses: what is left of it goes.
	s := New(home.Dir(t.TempDir()), two, config.Config{BaseURL: "http://old", APIKey: "sk-old", PrivateKey: key, Tools: tools})
	if _, err := s.Login(context.Background(), "one", token(llmtest.Token)); err != nil {
		t.Fatal(err)
	}
	if err := s.Save("one-small", " "); err == nil {
		t.Fatal("saved without a name")
	}
	if err := s.Save("", "alpha"); err == nil {
		t.Fatal("saved without a model")
	}
	err := s.Save("one-small", "alpha")
	cfg, _ := s.home.Config()
	if err != nil || cfg.PrivateKey != key || !reflect.DeepEqual(cfg.Tools, tools) || cfg.BaseURL != "" || cfg.APIKey != "" {
		t.Fatalf("got %+v, %v", cfg, err)
	}
}

func TestSaveFails(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	// A home that cannot be written: the access is not kept.
	s := New(home.Dir(filepath.Join(file, "home")), two, config.Config{})
	if _, err := s.Login(context.Background(), "one", token(llmtest.Token)); err != nil {
		t.Fatal(err)
	}
	if err := s.Save("one-small", "alpha"); err == nil {
		t.Fatal("want error")
	}
	if s.Name() == "alpha" {
		t.Fatal("what was not saved is not what the node has")
	}
	if _, err := s.Accesses(); err == nil {
		t.Fatal("Accesses should report credentials it cannot read")
	}
	if _, err := s.Remove(context.Background(), "x"); err == nil {
		t.Fatal("Remove should report credentials it cannot read")
	}

	// The access is kept, the configuration cannot be written.
	dir := home.Dir(t.TempDir())
	s = New(dir, two, config.Config{})
	if _, err := s.Login(context.Background(), "one", token(llmtest.Token)); err != nil {
		t.Fatal(err)
	}
	os.Mkdir(filepath.Join(string(dir), "config.json.lock"), 0o700)
	if err := s.Save("one-small", "alpha"); err == nil {
		t.Fatal("want error")
	}
	// The credentials are there, the configuration is not to be read.
	os.Remove(filepath.Join(string(dir), "config.json.lock"))
	os.WriteFile(filepath.Join(string(dir), "config.json"), []byte("{"), 0o600)
	if _, err := s.Accesses(); err == nil {
		t.Fatal("Accesses should report a configuration it cannot read")
	}
	second, _ := s.book.Store.Put(credentials.Access{Provider: "one"})
	if _, err := s.Remove(context.Background(), second.ID); err == nil {
		t.Fatal("Remove should report a configuration it cannot read")
	}
	s.cfg = config.Config{Access: "a", Model: "m", Name: "n"}
	if _, err := s.Models(context.Background()); err == nil {
		t.Fatal("Models should report a configuration it cannot read")
	}
}

func TestOpen(t *testing.T) {
	dir := home.Dir(t.TempDir())
	if err := dir.SaveConfig(config.Config{Name: "alpha"}); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir, two)
	if err != nil || s.Name() != "alpha" || len(s.Providers()) != 2 {
		t.Fatalf("got %+v, %v", s, err)
	}
	broken := t.TempDir()
	if err := os.WriteFile(filepath.Join(broken, "config.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(home.Dir(broken), two); err == nil {
		t.Fatal("want error")
	}
}

func TestRights(t *testing.T) {
	owner, peers, protected := New("", two, config.Config{}).Rights()
	if !slices.Equal(owner, []string{config.AllTools}) || len(peers) != 0 || !slices.Equal(protected, config.DefaultProtected()) {
		t.Fatalf("a new node: %v %v %v", owner, peers, protected)
	}
	// A node from before there were tools for files keeps its lists and
	// gets what is protected; the configuration it has is left as it is.
	old := &config.Tools{H2A: config.Named("read"), A2A: config.Named("list_peers")}
	owner, peers, protected = New("", two, config.Config{Tools: old}).Rights()
	if !slices.Equal(owner, []string{"read"}) || !slices.Equal(peers, []string{"list_peers"}) || !slices.Equal(protected, config.DefaultProtected()) {
		t.Fatalf("a node from before: %v %v %v", owner, peers, protected)
	}
	if old.Protected != nil {
		t.Fatal("the tools of the node were changed")
	}
}

// A host without a name still gives the node one.
func TestHostname(t *testing.T) {
	old := osHostname
	t.Cleanup(func() { osHostname = old })
	osHostname = func() (string, error) { return "", errors.New("no name") }
	if got := hostname(); got != "ouroboros" {
		t.Fatalf("got %q", got)
	}
	// A name without an error is the name; either alone is none.
	osHostname = func() (string, error) { return "", nil }
	if got := hostname(); got != "ouroboros" {
		t.Fatalf("a host with an empty name: %q", got)
	}
	osHostname = func() (string, error) { return "half", errors.New("no name") }
	if got := hostname(); got != "ouroboros" {
		t.Fatalf("a name that came with an error: %q", got)
	}
	osHostname = func() (string, error) { return "box", nil }
	if got := New("", two, config.Config{}).Name(); got != "box" {
		t.Fatalf("got %q", got)
	}
}

// The owner picks an access and a model for peers, beside what the node
// answers the owner with, and takes the choice back.
func TestPickForPeers(t *testing.T) {
	s := session(t)
	one := signedIn(t, s, "one", "good-1", "one-large")
	two := signedIn(t, s, "two", "good-2", "two-large")
	if err := s.PickForPeers(one.ID, " one-small "); err != nil {
		t.Fatal(err)
	}
	cfg, _ := s.home.Config()
	if cfg.PeerAccess != one.ID || cfg.PeerModel != "one-small" || cfg.Access != two.ID || cfg.Model != "two-large" {
		t.Fatalf("picked %+v", cfg)
	}
	if err := s.PickForPeers(one.ID, " "); !errors.Is(err, ErrNoModel) {
		t.Fatalf("no model: %v", err)
	}
	if err := s.PickForPeers("none", "m"); !errors.Is(err, credentials.ErrGone) {
		t.Fatalf("no access: %v", err)
	}
	if cfg, _ := s.home.Config(); cfg.PeerAccess != one.ID || cfg.PeerModel != "one-small" {
		t.Fatalf("a choice that failed changed %+v", cfg)
	}
	// Taken back, whatever model is named along.
	if err := s.PickForPeers("", "one-small"); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := s.home.Config(); cfg.PeerAccess != "" || cfg.PeerModel != "" || cfg.Access != two.ID || cfg.Model != "two-large" {
		t.Fatalf("taken back: %+v", cfg)
	}
	fresh := session(t)
	a, _ := fresh.book.Store.Put(credentials.Access{Provider: "one"})
	if err := fresh.PickForPeers(a.ID, "one-small"); !errors.Is(err, config.ErrIncomplete) {
		t.Fatalf("not set up: %v", err)
	}
}

// Peers that were answered through an access that is removed are answered
// like the owner again; those of another access stay as they are.
func TestRemoveTakesBackTheChoiceForPeers(t *testing.T) {
	ctx := context.Background()
	s := session(t)
	one := signedIn(t, s, "one", "good-1", "one-large")
	two := signedIn(t, s, "two", "good-2", "two-large")
	three := signedIn(t, s, "one", "good-1", "one-large")
	if err := s.PickForPeers(two.ID, "two-small"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Remove(ctx, one.ID); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := s.home.Config(); cfg.PeerAccess != two.ID || cfg.PeerModel != "two-small" || cfg.Access != three.ID {
		t.Fatalf("another access was removed: %+v", cfg)
	}
	if note, err := s.Remove(ctx, two.ID); err != nil || note != "" {
		t.Fatalf("Remove = %q, %v", note, err)
	}
	if cfg, _ := s.home.Config(); cfg.PeerAccess != "" || cfg.PeerModel != "" || cfg.Access != three.ID || cfg.Model != "one-large" {
		t.Fatalf("the access of the peers was removed: %+v", cfg)
	}
}
