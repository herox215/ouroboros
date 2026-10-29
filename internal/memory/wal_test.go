package memory

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ouroboros/internal/storage"
)

// A store whose WAL went from under it writes nothing more and says why:
// what it committed would be lost with the process. It still reads. A store
// opened meanwhile reads without the WAL, and writes nothing either.
func TestWriteFailsWhenTheWALIsGone(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	// The tables are in the file, as in a node that ran before.
	if _, err := s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	e, err := s.Create(ctx, "", "first", "a fact", "test")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, storage.FileName)
	for _, sidecar := range []string{"-wal", "-shm"} {
		if err := os.Remove(path + sidecar); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Create(ctx, "", "second", "another fact", "test"); err == nil || !strings.Contains(err.Error(), "the memories keep nothing until ouro starts anew: the WAL ") {
		t.Fatalf("create = %v", err)
	}
	if got, err := s.Read(ctx, "", e.ID); err != nil || got.Key != "first" {
		t.Fatalf("read %+v, %v", got, err)
	}
	log.SetOutput(io.Discard) // that the database is read-only
	defer log.SetOutput(os.Stderr)
	ro, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if _, err := ro.Create(ctx, "", "third", "a third fact", "test"); err == nil || !strings.Contains(err.Error(), "the memories keep nothing until ouro starts anew: the database is read-only") {
		t.Fatalf("create = %v", err)
	}
}
