package memory

import (
	"os"
	"path/filepath"
	"testing"
)

// A lazy store opens nothing until it is used, tries an Open that failed
// again, and fails once it is closed.
func TestLazy(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "history")
	if err := os.WriteFile(dir, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	l := NewLazy(dir)
	if _, err := l.Store(); err == nil {
		t.Fatal("opened in a file")
	}
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	s, err := l.Store()
	if err != nil {
		t.Fatal(err)
	}
	if again, err := l.Store(); err != nil || again != s {
		t.Fatalf("opened anew: %v", err)
	}
	l.Close()
	if _, err := l.Store(); err == nil {
		t.Fatal("used after Close")
	}
	if err := s.db.Ping(); err == nil {
		t.Fatal("Close left the store open")
	}
	unused := NewLazy(filepath.Join(t.TempDir(), "never"))
	unused.Close()
	if _, err := os.Stat(unused.dir); !os.IsNotExist(err) {
		t.Fatalf("an unused store made its directory: %v", err)
	}
}
