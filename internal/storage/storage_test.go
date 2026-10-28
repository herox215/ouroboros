package storage

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func openTest(t *testing.T, dir string) *sql.DB {
	t.Helper()
	db, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	return db
}

func execTest(t *testing.T, db *sql.DB, query string) {
	t.Helper()
	if _, err := db.Exec(query); err != nil {
		t.Fatal(err)
	}
}

func TestOpenPermissionsAndConnectionSettings(t *testing.T) {
	// URI metacharacters must remain part of the path, not DSN parameters.
	dir := filepath.Join(t.TempDir(), "private ?#% space")
	db := openTest(t, dir)
	for _, item := range []struct {
		path string
		mode os.FileMode
	}{
		{dir, 0o700}, {filepath.Join(dir, FileName), 0o600},
	} {
		info, err := os.Stat(item.path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != item.mode {
			t.Errorf("%s: mode %o, want %o", item.path, info.Mode().Perm(), item.mode)
		}
	}
	// Reopening must tighten existing permissions too.
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, FileName), 0o644); err != nil {
		t.Fatal(err)
	}
	other := openTest(t, dir)
	for _, path := range []string{dir, filepath.Join(dir, FileName)} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			t.Errorf("not private: %s", path)
		}
	}
	for _, pool := range []*sql.DB{db, other} {
		// Force replacement rather than only testing the initial connection.
		pool.SetMaxIdleConns(0)
		for i := 0; i < 3; i++ {
			for pragma, want := range map[string]string{"journal_mode": "wal", "busy_timeout": "5000", "foreign_keys": "1"} {
				var got string
				if err := pool.QueryRow("PRAGMA " + pragma).Scan(&got); err != nil {
					t.Fatal(err)
				}
				if got != want {
					t.Errorf("%s = %s, want %s", pragma, got, want)
				}
			}
		}
	}
}

func TestTransactionsFTS5AndForeignKeys(t *testing.T) {
	dir := t.TempDir()
	db := openTest(t, dir)
	execTest(t, db, "CREATE TABLE parent(id INTEGER PRIMARY KEY)")
	execTest(t, db, "CREATE TABLE child(parent_id INTEGER REFERENCES parent(id))")
	execTest(t, db, "CREATE VIRTUAL TABLE docs USING fts5(body)")
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("INSERT INTO docs(body) VALUES (?)", "private searchable memory"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	tx, err = db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("INSERT INTO docs(body) VALUES ('rolledback')"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	other := openTest(t, dir)
	var body string
	if err := other.QueryRow("SELECT body FROM docs WHERE docs MATCH ?", "searchable").Scan(&body); err != nil {
		t.Fatal(err)
	}
	if body != "private searchable memory" {
		t.Fatalf("unexpected FTS result %q", body)
	}
	var count int
	if err := other.QueryRow("SELECT count(*) FROM docs").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("rollback failed: %d documents", count)
	}
	if _, err := other.Exec("INSERT INTO child VALUES (999)"); err == nil {
		t.Fatal("foreign key violation accepted")
	}
	execTest(t, other, "INSERT INTO parent VALUES (1)")
	execTest(t, other, "INSERT INTO child VALUES (1)")
}

func TestIndependentPoolsWaitForWriterAndReadWAL(t *testing.T) {
	dir := t.TempDir()
	a, b := openTest(t, dir), openTest(t, dir)
	execTest(t, a, "CREATE TABLE counter(n INTEGER NOT NULL)")
	execTest(t, a, "INSERT INTO counter VALUES (0)")
	tx, err := a.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("UPDATE counter SET n=n+1"); err != nil {
		t.Fatal(err)
	}
	var n int
	// A reader on another pool is not blocked by the uncommitted WAL writer.
	if err := b.QueryRow("SELECT n FROM counter").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("uncommitted value visible: %d", n)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := b.ExecContext(ctx, "UPDATE counter SET n=n+1"); done <- err }()
	select {
	case err := <-done:
		t.Fatalf("second writer did not wait: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := b.QueryRow("SELECT n FROM counter").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("lost write: %d", n)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	execTest(t, b, "UPDATE counter SET n=n+1") // closing a does not close b
	if b.Stats().InUse != 0 {
		t.Fatal("connection leaked")
	}
}

func TestConcurrentOpenAndWrites(t *testing.T) {
	dir := t.TempDir()
	const workers, writes = 8, 20
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			db, err := Open(dir)
			if err != nil {
				errs <- err
				return
			}
			defer db.Close()
			if _, err := db.Exec("CREATE TABLE IF NOT EXISTS events(id INTEGER PRIMARY KEY)"); err != nil {
				errs <- err
				return
			}
			for j := 0; j < writes; j++ {
				if _, err := db.Exec("INSERT INTO events DEFAULT VALUES"); err != nil {
					errs <- err
					return
				}
			}
			if db.Stats().InUse != 0 {
				errs <- fmt.Errorf("connection leaked")
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if t.Failed() {
		return
	}
	db := openTest(t, dir)
	var count int
	if err := db.QueryRow("SELECT count(*) FROM events").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != workers*writes {
		t.Fatalf("got %d writes, want %d", count, workers*writes)
	}
}

func TestOpenFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if db, err := Open(path); err == nil || db != nil {
		t.Fatalf("Open(file) = %v, %v", db, err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if db, err := Open(dir); err == nil || db != nil {
		t.Fatalf("Open(corrupt) = %v, %v", db, err)
	}
}

// Each step of Open that fails says which one it was, and nothing is opened.
func TestOpenFailureSteps(t *testing.T) {
	directory, target := lockedPaths(t)
	database := t.TempDir()
	if err := os.Symlink(target, filepath.Join(database, FileName)); err != nil {
		t.Fatal(err)
	}
	// A directory where the file of the database would be.
	occupied := t.TempDir()
	if err := os.Mkdir(filepath.Join(occupied, FileName), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ dir, want string }{
		{directory, "secure storage directory: "},
		{database, "secure storage database: "},
		{occupied, "create storage database: "},
	} {
		if db, err := Open(c.dir); err == nil || db != nil || !strings.HasPrefix(err.Error(), c.want) {
			t.Errorf("Open(%s) = %v, %v; want %q", c.dir, db, err, c.want)
		}
	}
}

// lockedPaths answers a directory that exists but whose mode cannot be
// changed, and a file that opens for writing but whose mode cannot be changed
// either. Neither is changed when the test goes wrong.
func lockedPaths(t *testing.T) (directory, file string) {
	t.Helper()
	switch {
	case runtime.GOOS == "darwin" && os.Geteuid() != 0:
		// System Integrity Protection keeps even root from changing /System;
		// /dev/null opens for anyone but only root may change its mode.
		return "/System/Library", "/dev/null"
	case runtime.GOOS == "linux":
		// Linux lets nobody, not even root, change the mode of what /proc has
		// of a process: its directory and its files.
		if _, err := os.Stat("/proc/self/comm"); err == nil {
			return "/proc/self", "/proc/self/comm"
		}
	}
	t.Skip("no paths whose mode cannot be changed")
	return "", ""
}

// A relative directory is taken from the working directory, and fails once
// that is gone or cannot be read.
func TestOpenWithoutWorkingDirectory(t *testing.T) {
	for name, lose := range map[string]func(string) error{
		"removed":    os.Remove,
		"unreadable": func(dir string) error { return os.Chmod(dir, 0) },
	} {
		t.Run(name, func(t *testing.T) {
			gone := t.TempDir()
			t.Chdir(gone)
			if err := lose(gone); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.Chmod(gone, 0o700) })
			if _, err := os.Getwd(); err == nil {
				t.Skip("the system still reports the working directory")
			}
			if db, err := Open("relative"); err == nil || db != nil || !strings.HasPrefix(err.Error(), "storage directory: ") {
				t.Fatalf("Open(relative) = %v, %v", db, err)
			}
		})
	}
}

// A database that stays in another journal mode is refused: the stores rely
// on WAL to read while another pool writes.
func TestOpenRefusesAnotherJournal(t *testing.T) {
	defer func(pragma string) { walPragma = pragma }(walPragma)
	walPragma = "PRAGMA journal_mode=DELETE"
	if db, err := Open(t.TempDir()); err == nil || db != nil || err.Error() != `enable storage WAL: got journal mode "delete"` {
		t.Fatalf("Open = %v, %v", db, err)
	}
}

// A switch of the journal that worked is not tried again: only a busy
// database is.
func TestEnableWALTriesOnce(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), FileName))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	execTest(t, db, "CREATE TABLE calls(x)")
	defer func(pragma string) { walPragma = pragma }(walPragma)
	walPragma = "INSERT INTO calls VALUES (1) RETURNING 'wal'"
	var journal string
	if err := enableWAL(db, &journal); err != nil || journal != "wal" {
		t.Fatalf("enableWAL = %q, %v", journal, err)
	}
	var calls int
	if err := db.QueryRow("SELECT count(*) FROM calls").Scan(&calls); err != nil || calls != 1 {
		t.Fatalf("switched %d times, %v", calls, err)
	}
}

// The first open of a database waits for a connection that writes in the
// rollback journal, although SQLite itself does not wait when the reader
// that switches the journal turns into a writer.
func TestOpenWaitsToSwitchTheJournal(t *testing.T) {
	dir := t.TempDir()
	other, err := sql.Open("sqlite", "file:"+filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	other.SetMaxOpenConns(1)
	execTest(t, other, "CREATE TABLE t(x)")
	tx, err := other.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("INSERT INTO t VALUES (1)"); err != nil {
		t.Fatal(err)
	}
	releasing := make(chan struct{})
	time.AfterFunc(200*time.Millisecond, func() {
		close(releasing)
		tx.Rollback()
	})
	db := openTest(t, dir)
	select {
	case <-releasing:
	default:
		t.Fatal("the journal was switched while another connection wrote")
	}
	var journal string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&journal); err != nil || journal != "wal" {
		t.Fatalf("journal mode %q, %v", journal, err)
	}
}
