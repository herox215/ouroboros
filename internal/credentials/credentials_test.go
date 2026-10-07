package credentials

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func secret(key string) json.RawMessage {
	b, _ := json.Marshal(map[string]string{"api_key": key})
	return b
}

// An access is saved with an ID that stays, and found by it again.
func TestPutGetList(t *testing.T) {
	dir := t.TempDir()
	s := Open(dir)
	if list, err := s.List(); err != nil || list != nil {
		t.Fatalf("a node without accesses lists %v, %v", list, err)
	}
	if _, err := s.Get("none"); !errors.Is(err, ErrGone) {
		t.Fatalf("Get of no access: %v", err)
	}
	first, err := s.Put(Access{Provider: "openai", Credentials: Credentials{Title: "OpenAI", About: "https://api", Secret: secret("sk-1")}})
	if err != nil || first.ID == "" || first.Rev == "" {
		t.Fatalf("Put = %+v, %v", first, err)
	}
	second, err := s.Put(Access{Provider: "chatgpt", Name: "Work"})
	if err != nil || second.ID == first.ID {
		t.Fatalf("a second access got the ID of the first: %+v, %v", second, err)
	}
	got, err := s.Get(first.ID)
	if err != nil || got.ID != first.ID || got.Rev != first.Rev || got.About != "https://api" || !reflect.DeepEqual(got.Secrets(), []string{"sk-1"}) {
		t.Fatalf("Get = %+v, %v, want %+v", got, err, first)
	}
	list, err := s.List()
	if err != nil || len(list) != 2 || list[0].ID != first.ID || list[1].ID != second.ID {
		t.Fatalf("List = %+v, %v", list, err)
	}

	// The file is the owner's alone, and so is its directory's lock.
	for _, path := range Files(dir) {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s: %v, %v", path, info, err)
		}
	}
	if Files(dir)[0] != filepath.Join(dir, FileName) || Files(dir)[1] != filepath.Join(dir, LockName) {
		t.Fatalf("Files = %v", Files(dir))
	}
}

// Signing in again replaces what the access holds: its ID and its name stay,
// its Rev changes.
func TestPutReplaces(t *testing.T) {
	s := Open(t.TempDir())
	a, _ := s.Put(Access{Provider: "openai", Name: "Private", Credentials: Credentials{Secret: secret("old")}})
	again, err := s.Put(Access{ID: a.ID, Provider: "openai", Credentials: Credentials{Secret: secret("new")}})
	if err != nil || again.ID != a.ID || again.Name != "Private" || again.Rev == a.Rev {
		t.Fatalf("signed in again: %+v, %v (was %+v)", again, err, a)
	}
	renamed, _ := s.Put(Access{ID: a.ID, Provider: "openai", Name: "Work"})
	if renamed.Name != "Work" {
		t.Fatalf("a name that is given is kept: %+v", renamed)
	}
	if list, _ := s.List(); len(list) != 1 || string(list[0].Secret) != "" {
		t.Fatalf("List = %+v", list)
	}
	// An ID that is given and not there yet is taken as it is.
	if fixed, _ := s.Put(Access{ID: "default", Provider: "openai"}); fixed.ID != "default" {
		t.Fatalf("Put with an ID = %+v", fixed)
	}
}

func TestRenameRemove(t *testing.T) {
	s := Open(t.TempDir())
	a, _ := s.Put(Access{Provider: "openai"})
	b, _ := s.Put(Access{Provider: "openai"})
	if err := s.Rename(a.ID, "  Private "); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(a.ID); got.Name != "Private" || got.Rev != a.Rev || got.Label() != "Private" {
		t.Fatalf("renamed: %+v", got)
	}
	if err := s.Remove(a.ID); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.List(); len(list) != 1 || list[0].ID != b.ID {
		t.Fatalf("after removing: %+v", list)
	}
	for name, err := range map[string]error{"Rename": s.Rename(a.ID, "x"), "Remove": s.Remove(a.ID)} {
		if !errors.Is(err, ErrGone) {
			t.Errorf("%s of an access that is gone: %v", name, err)
		}
	}
}

func TestLabel(t *testing.T) {
	a := Access{Provider: "openai"}
	if a.Label() != "openai" {
		t.Fatalf("Label = %q", a.Label())
	}
	a.Title = "localhost"
	if a.Label() != "localhost" {
		t.Fatalf("Label = %q", a.Label())
	}
}

// Every text under Secret is a secret, however deep; nothing else is.
func TestSecrets(t *testing.T) {
	c := Credentials{Title: "ChatGPT", About: "me@example.com", Data: json.RawMessage(`{"client_id":"oaiapp_1"}`),
		Secret: json.RawMessage(`{"access_token":"at","nested":{"list":["rt","at",""],"n":3},"id_token":"it"}`)}
	if got := c.Secrets(); !reflect.DeepEqual(got, []string{"at", "it", "rt"}) {
		t.Fatalf("Secrets = %q", got)
	}
	if got := (Credentials{}).Secrets(); got != nil {
		t.Fatalf("Secrets of nothing = %q", got)
	}
	if got := (Credentials{Secret: json.RawMessage(`not json`)}).Secrets(); !reflect.DeepEqual(got, []string{"not json"}) {
		t.Fatalf("Secrets of what cannot be read = %q", got)
	}
}

// Changes made at the same time do not overwrite each other.
func TestChangeIsExclusive(t *testing.T) {
	s := Open(t.TempDir())
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if _, err := s.Put(Access{Provider: "openai"}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if list, _ := s.List(); len(list) != 20 {
		t.Fatalf("%d of 20 accesses survived", len(list))
	}
}

func TestErrors(t *testing.T) {
	// A file that is no JSON is reported, and left alone.
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, FileName), []byte("{"), 0o600)
	s := Open(dir)
	if _, err := s.List(); err == nil {
		t.Error("List should report a file it cannot read")
	}
	if _, err := s.Get("x"); err == nil || errors.Is(err, ErrGone) {
		t.Errorf("Get should report a file it cannot read, got %v", err)
	}
	if _, err := s.Put(Access{}); err == nil {
		t.Error("Put should report a file it cannot read")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, FileName)); string(b) != "{" {
		t.Errorf("the file was written: %q", b)
	}

	// A directory in the place of the file.
	dir = t.TempDir()
	os.Mkdir(filepath.Join(dir, FileName), 0o700)
	if _, err := Open(dir).List(); err == nil {
		t.Error("List should report a file it cannot open")
	}

	// A directory in the place of the lock.
	dir = t.TempDir()
	os.Mkdir(filepath.Join(dir, LockName), 0o700)
	if _, err := Open(dir).Put(Access{}); err == nil {
		t.Error("Put should report a lock it cannot take")
	}

	// A change that fails writes nothing.
	dir = t.TempDir()
	boom := errors.New("boom")
	if err := Open(dir).Change(func([]Access) ([]Access, error) { return nil, boom }); !errors.Is(err, boom) {
		t.Errorf("Change = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, FileName)); err == nil {
		t.Error("a change that failed wrote the file")
	}
}
