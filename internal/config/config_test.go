package config

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func TestLoadMissingReturnsEmpty(t *testing.T) {
	c, err := Load(t.TempDir())
	if err != nil || c != (Config{}) {
		t.Fatalf("got %+v, %v", c, err)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested")
	want := Config{BaseURL: "http://x/v1", APIKey: "sk", Model: "m", Name: "n", ID: "abc123"}
	if err := Save(dir, want); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir)
	if err != nil || got != want {
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
	full := Config{BaseURL: "u", Model: "m", Name: "n", ID: "i"}
	if !full.Complete() {
		t.Fatal("config without API key should be complete")
	}
	partial := full
	partial.Model = ""
	if partial.Complete() {
		t.Fatal("config without model should be incomplete")
	}
}

func TestNewID(t *testing.T) {
	a, b := NewID(), NewID()
	if !regexp.MustCompile(`^[0-9a-f]{6}$`).MatchString(a) || a == b {
		t.Fatalf("bad ids %q %q", a, b)
	}
}
