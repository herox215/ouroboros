package history

import (
	"database/sql"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ouroboros/internal/chat"
	"ouroboros/internal/storage"
)

// unlinkWAL removes the WAL of the database in dir and its shared memory
// behind every pool that has them open, as happened to a running node, and
// keeps a pool open on them, as another process that neither checkpoints nor
// closes: closing the archive is then not the last close, which would
// checkpoint them.
func unlinkWAL(t *testing.T, dir string) {
	t.Helper()
	path := filepath.Join(dir, storage.FileName)
	other, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { other.Close() })
	var n int
	if err := other.QueryRow("SELECT count(*) FROM history_records").Scan(&n); err != nil {
		t.Fatal(err)
	}
	for _, sidecar := range []string{"-wal", "-shm"} {
		if err := os.Remove(path + sidecar); err != nil {
			t.Fatal(err)
		}
	}
}

// An answer appended after the WAL went from under the open archive is kept:
// what the WAL holds is checkpointed and the archive is opened anew, so that
// the answer is still there once the archive is opened again.
func TestAppendAfterTheWALWentAway(t *testing.T) {
	dir := t.TempDir()
	a := Open(dir)
	who := Who{Session: "chat"}
	if err := a.Append(who, []chat.Message{{Role: "user", Content: "first"}}, nil); err != nil {
		t.Fatal(err)
	}
	unlinkWAL(t, dir)
	if err := a.Append(who, []chat.Message{{Role: "user", Content: "second"}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	again := Open(dir)
	defer again.Close()
	records, err := again.Records(Owner(who.Session))
	if err != nil {
		t.Fatal(err)
	}
	var said []string
	for _, r := range records {
		if r.Role == "user" {
			said = append(said, r.Content)
		}
	}
	if strings.Join(said, " ") != "first second" {
		t.Fatalf("archived %q, want first and second", said)
	}
}

// An archive that cannot be opened anew once its WAL went away fails the
// append rather than lose it.
func TestAppendFailsWithoutItsWAL(t *testing.T) {
	dir := t.TempDir()
	a := Open(dir)
	defer a.Close()
	who := Who{Session: "chat"}
	if err := a.Append(who, []chat.Message{{Role: "user", Content: "first"}}, nil); err != nil {
		t.Fatal(err)
	}
	unlinkWAL(t, dir)
	refused := errors.New("refused")
	a.open = func(string) (*sql.DB, error) { return nil, refused }
	if err := a.Append(who, []chat.Message{{Role: "user", Content: "second"}}, nil); !errors.Is(err, refused) {
		t.Fatalf("append = %v, want %v", err, refused)
	}
}

// An archive opened afresh while the WAL is gone from under another pool is
// still searched, from what the database file holds, and refuses to append.
func TestSearchWithoutTheWAL(t *testing.T) {
	dir := t.TempDir()
	a := Open(dir)
	defer a.Close()
	who := Who{Session: "chat"}
	if err := a.Append(who, []chat.Message{{Role: "user", Content: "first"}}, nil); err != nil {
		t.Fatal(err)
	}
	exec(t, a, "PRAGMA wal_checkpoint(TRUNCATE)")
	if err := a.Append(who, []chat.Message{{Role: "user", Content: "second"}}, nil); err != nil {
		t.Fatal(err)
	}
	unlinkWAL(t, dir)
	log.SetOutput(io.Discard) // that the database is read-only
	defer log.SetOutput(os.Stderr)
	b := Open(dir)
	defer b.Close()
	if _, n, err := b.Search(who, "first", true, 10); err != nil || n != 1 {
		t.Fatalf("search found %d, %v", n, err)
	}
	if err := b.Append(who, []chat.Message{{Role: "user", Content: "third"}}, nil); err == nil {
		t.Fatal("appended to an archive that reads without WAL")
	}
}

// An archive whose WAL went away keeps its pool and fails the append while
// a reader keeps the checkpoint from copying all of the WAL.
func TestAppendFailsWhileTheWALCannotBeKept(t *testing.T) {
	dir := t.TempDir()
	a := Open(dir)
	defer a.Close()
	who := Who{Session: "chat"}
	if err := a.Append(who, []chat.Message{{Role: "user", Content: "first"}}, nil); err != nil {
		t.Fatal(err)
	}
	reader, err := sql.Open("sqlite", "file:"+filepath.Join(dir, storage.FileName))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	tx, err := reader.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRow("SELECT count(*) FROM history_records").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if err := a.Append(who, []chat.Message{{Role: "user", Content: "second"}}, nil); err != nil {
		t.Fatal(err)
	}
	exec(t, a, "PRAGMA busy_timeout=0") // not to wait for the reader
	if err := os.Remove(filepath.Join(dir, storage.FileName) + "-wal"); err != nil {
		t.Fatal(err)
	}
	db := a.db
	if err := a.Append(who, []chat.Message{{Role: "user", Content: "third"}}, nil); err == nil || !strings.Contains(err.Error(), "cannot be kept") {
		t.Fatalf("append = %v", err)
	}
	if a.db != db {
		t.Fatal("the pool that holds the WAL was let go")
	}
}
