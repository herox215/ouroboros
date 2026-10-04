package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"ouroboros/internal/identity"
)

func TestLoadMissingReturnsEmpty(t *testing.T) {
	c, err := Load(t.TempDir())
	if err != nil || !reflect.DeepEqual(c, Config{}) {
		t.Fatalf("got %+v, %v", c, err)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested")
	want := Config{BaseURL: "http://x/v1", APIKey: "sk", Model: "m", Name: "n", PrivateKey: "key"}
	if err := Save(dir, want); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestSaveRestrictsPermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, fileName)
	os.WriteFile(path, []byte("{}"), 0o644)
	if err := Save(dir, Config{}); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("perm = %o, want 600", perm)
	}
}

func TestLoadInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, fileName), []byte("{"), 0o600)
	if _, err := Load(dir); err == nil {
		t.Fatal("want error")
	}
}

func TestComplete(t *testing.T) {
	full := Config{BaseURL: "u", Model: "m", Name: "n"}
	if !full.Complete() {
		t.Fatal("config without API key and node key should be complete")
	}
	partial := full
	partial.Model = ""
	if partial.Complete() {
		t.Fatal("config without model should be incomplete")
	}
}

// The ID is the fingerprint of the node's key and nothing that is stored.
func TestIDFollowsFromKey(t *testing.T) {
	id := identity.Generate()
	if got := (Config{PrivateKey: id.Private()}).ID(); got != id.ID() {
		t.Fatalf("ID = %q, want %q", got, id.ID())
	}
	for _, key := range []string{"", "nonsense"} {
		if got := (Config{PrivateKey: key}).ID(); got != "" {
			t.Fatalf("a node without a valid key has the ID %q", got)
		}
	}
}

// A config written before the ID was derived keeps working: its random ID is
// ignored, its key and everything else stay.
func TestLoadIgnoresStoredID(t *testing.T) {
	dir, id := t.TempDir(), identity.Generate()
	old := `{"base_url": "u", "model": "m", "name": "n", "id": "abc123", "private_key": "` + id.Private() + `"}`
	os.WriteFile(filepath.Join(dir, fileName), []byte(old), 0o600)
	c, err := Load(dir)
	if err != nil || !c.Complete() || c.PrivateKey != id.Private() || c.ID() != id.ID() {
		t.Fatalf("got %+v, %v", c, err)
	}
	if err := Save(dir, c); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, fileName)); strings.Contains(string(b), "abc123") {
		t.Fatalf("the old ID was written again: %s", b)
	}
}

// Without a tools section the owner gets every tool and peers get none.
func TestToolLists(t *testing.T) {
	tests := []struct {
		name      string
		tools     *Tools
		owner     []string
		peerTools []string
	}{
		{"default", nil, []string{AllTools}, nil},
		{"only a2a set", &Tools{A2A: []string{"list_peers"}}, []string{AllTools}, []string{"list_peers"}},
		{"owner without tools", &Tools{H2A: []string{}}, []string{}, nil},
		{"both set", &Tools{H2A: []string{"ask_peer"}, A2A: []string{AllTools}}, []string{"ask_peer"}, []string{AllTools}},
	}
	for _, tt := range tests {
		c := Config{Tools: tt.tools}
		if got := c.OwnerTools(); !reflect.DeepEqual(got, tt.owner) {
			t.Errorf("%s: owner tools %v", tt.name, got)
		}
		if got := c.PeerTools(); !reflect.DeepEqual(got, tt.peerTools) {
			t.Errorf("%s: peer tools %v", tt.name, got)
		}
	}
}

// An owner who took all tools away keeps that choice when the file is rewritten.
func TestToolListsSurviveSaving(t *testing.T) {
	dir := t.TempDir()
	want := Config{Name: "n", Tools: &Tools{H2A: []string{}, A2A: []string{"list_peers"}}}
	if err := Save(dir, want); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir)
	if err != nil || len(got.OwnerTools()) != 0 || !reflect.DeepEqual(got.PeerTools(), []string{"list_peers"}) {
		t.Fatalf("got %+v, %v", got.Tools, err)
	}
}

// What cannot be read or written is reported, not taken for an empty config.
func TestLoadAndSaveReportErrors(t *testing.T) {
	// A directory in the place of the file can neither be read nor written.
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, fileName), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Fatal("Load should report an unreadable file")
	}
	if err := Save(dir, Config{}); err == nil {
		t.Fatal("Save should report an unwritable file")
	}

	// A file in the place of the directory.
	file := filepath.Join(t.TempDir(), "file")
	os.WriteFile(file, nil, 0o600)
	if err := Save(filepath.Join(file, "sub"), Config{}); err == nil {
		t.Fatal("Save should report a directory it cannot create")
	}
}
