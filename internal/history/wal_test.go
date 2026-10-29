package history

import (
	"database/sql"
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
// returns another pool of the process on the database, as the store of the
// memories is: it still uses the WAL that is gone, and the archive must not
// part from it.
func unlinkWAL(t *testing.T, dir string) *sql.DB {
	t.Helper()
	path := filepath.Join(dir, storage.FileName)
	other, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	other.SetMaxOpenConns(1)
	t.Cleanup(func() { other.Close() })
	if _, err := other.Exec("CREATE TABLE IF NOT EXISTS other(x)"); err != nil {
		t.Fatal(err)
	}
	for _, sidecar := range []string{"-wal", "-shm"} {
		if err := os.Remove(path + sidecar); err != nil {
			t.Fatal(err)
		}
	}
	return other
}

// said is what the owner said in the session of who, as the archive in dir
// has it once it is opened again.
func said(t *testing.T, dir string, who Who) string {
	t.Helper()
	a := Open(dir)
	defer a.Close()
	records, err := a.Records(Owner(who.Session))
	if err != nil {
		t.Fatal(err)
	}
	var said []string
	for _, r := range records {
		if r.Role == "user" {
			said = append(said, r.Content)
		}
	}
	return strings.Join(said, " ")
}

// An archive whose WAL went from under it archives nothing more and says
// why: what it committed would be lost with the process. It still reads, it
// does not open a WAL of its own beside the one the other pools of the
// process use, and the database stays whole.
func TestAppendFailsWhenTheWALIsGone(t *testing.T) {
	dir := t.TempDir()
	a := Open(dir)
	who := Who{Session: "chat"}
	if err := a.Append(who, []chat.Message{{Role: "user", Content: "first"}}, nil); err != nil {
		t.Fatal(err)
	}
	other := unlinkWAL(t, dir)
	const gone = "keeps nothing until ouro starts anew: the WAL "
	if err := a.Append(who, []chat.Message{{Role: "user", Content: "second"}}, nil); err == nil || !strings.Contains(err.Error(), gone) {
		t.Fatalf("append = %v, want %q", err, gone)
	}
	if err := a.EndAnswer(Owner(who.Session)); err == nil || !strings.Contains(err.Error(), gone) {
		t.Fatalf("end answer = %v, want %q", err, gone)
	}
	if records, err := a.Records(Owner(who.Session)); err != nil || len(records) != 2 {
		t.Fatalf("records %+v, %v", records, err)
	}
	if _, err := os.Stat(filepath.Join(dir, storage.FileName) + "-wal"); !os.IsNotExist(err) {
		t.Fatalf("a WAL was opened at the path: %v", err)
	}
	// The other pool goes on with the WAL that is gone, as before.
	if _, err := other.Exec("INSERT INTO other VALUES (1)"); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if err := other.Close(); err != nil {
		t.Fatal(err)
	}
	if got := said(t, dir, who); got != "first" {
		t.Fatalf("archived %q, want first", got)
	}
	db, err := storage.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var check string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&check); err != nil || check != "ok" {
		t.Fatalf("integrity %q, %v", check, err)
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
	const readOnly = "keeps nothing until ouro starts anew: the database is read-only"
	if err := b.Append(who, []chat.Message{{Role: "user", Content: "third"}}, nil); err == nil || !strings.Contains(err.Error(), readOnly) {
		t.Fatalf("append = %v, want %q", err, readOnly)
	}
}
