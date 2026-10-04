package home

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"ouroboros/internal/config"
)

func TestIdentityIsGeneratedOnce(t *testing.T) {
	d := Dir(t.TempDir())
	if err := d.SaveConfig(config.Config{Name: "alpha"}); err != nil {
		t.Fatal(err)
	}
	first, err := d.Identity()
	if err != nil {
		t.Fatal(err)
	}
	second, err := d.Identity()
	if err != nil || second.Public() != first.Public() {
		t.Fatalf("identity changed: %v", err)
	}
	if cfg, _ := d.Config(); cfg.Name != "alpha" || cfg.PrivateKey != first.Private() {
		t.Fatalf("config %+v", cfg)
	}
}

func TestInvalidIdentity(t *testing.T) {
	d := Dir(t.TempDir())
	d.SaveConfig(config.Config{PrivateKey: "nonsense"})
	if _, err := d.Identity(); err == nil {
		t.Fatal("want error")
	}
}

// The node and the UI may both be the first to ask for the identity. They
// get the same one, and it is the one that is persisted.
func TestIdentityIsTheSameForEveryone(t *testing.T) {
	d := Dir(t.TempDir())
	keys := make([]string, 16)
	var wg sync.WaitGroup
	for i := range keys {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := d.Identity()
			if err != nil {
				t.Error(err)
				return
			}
			keys[i] = id.Private()
		}()
	}
	wg.Wait()
	cfg, err := d.Config()
	if err != nil || cfg.PrivateKey == "" {
		t.Fatalf("config %+v, %v", cfg, err)
	}
	for i, key := range keys {
		if key != cfg.PrivateKey {
			t.Fatalf("caller %d got an identity that is not the persisted one", i)
		}
	}
}

// A configuration that was read before the identity existed does not erase
// the identity when it is saved, as the setup does on first run.
func TestSaveConfigKeepsIdentity(t *testing.T) {
	d := Dir(t.TempDir())
	cfg, _ := d.Config()
	id, err := d.Identity()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Name = "alpha"
	if err := d.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if got, _ := d.Config(); got.Name != "alpha" || got.PrivateKey != id.Private() {
		t.Fatalf("config %+v", got)
	}
	if again, err := d.Identity(); err != nil || again.Public() != id.Public() {
		t.Fatalf("identity changed: %v", err)
	}
}

func TestIdentityReportsErrors(t *testing.T) {
	// A home below a file cannot be read.
	file := filepath.Join(t.TempDir(), "file")
	os.WriteFile(file, nil, 0o600)
	if _, err := Dir(filepath.Join(file, "home")).Identity(); err == nil {
		t.Error("an unreadable home should fail")
	}

	// Without the lock no identity is made up: it could differ from the one
	// another process persists.
	d := Dir(t.TempDir())
	os.Mkdir(d.path("config.json.lock"), 0o700)
	if _, err := d.Identity(); err == nil {
		t.Error("a home that cannot be locked should fail")
	}
	if cfg, _ := d.Config(); cfg.PrivateKey != "" {
		t.Error("an identity was persisted without the lock")
	}
}

func TestCreate(t *testing.T) {
	d := Dir(filepath.Join(t.TempDir(), "a", "home"))
	if err := d.Create(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(string(d))
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("home %v, %v", info, err)
	}
	if err := d.Create(); err != nil {
		t.Fatalf("a home that exists: %v", err)
	}
}

// Everything a node keeps is below its home.
func TestFiles(t *testing.T) {
	d := Dir(t.TempDir())
	if got := d.UpdateDir(); got != filepath.Join(string(d), "update") {
		t.Errorf("update dir %q", got)
	}
	s := d.Service()
	if s.PIDFile != filepath.Join(string(d), "ouro.pid") || s.LogFile != filepath.Join(string(d), "ouro.log") {
		t.Errorf("service %+v", s)
	}

	d.Contacts().AddTrusted("key", "beta", "gpt")
	if trusted, _, _ := d.Contacts().State("key"); !trusted {
		t.Error("a contact should be kept")
	}
	if _, err := os.Stat(filepath.Join(string(d), "contacts.json")); err != nil {
		t.Errorf("contacts: %v", err)
	}
}

// Another process than the node adds to the log of the node.
func TestLog(t *testing.T) {
	d := Dir(t.TempDir())
	d.Log("accept %s: %v", "key", "disk full")
	d.Log("second")
	b, _ := os.ReadFile(d.Service().LogFile)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 2 || !strings.HasSuffix(lines[0], " accept key: disk full") || !strings.HasSuffix(lines[1], " second") {
		t.Fatalf("log %q", b)
	}
	if info, _ := os.Stat(d.Service().LogFile); info.Mode().Perm() != 0o600 {
		t.Errorf("perm = %o, want 600", info.Mode().Perm())
	}
	// A log that cannot be written is given up on.
	Dir(filepath.Join(string(d), "missing")).Log("lost")
}
