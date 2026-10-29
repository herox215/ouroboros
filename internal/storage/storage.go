// Package storage opens the node's shared SQLite database. Domain packages own
// their schemas and migrations; this package only owns connection setup.
package storage

import (
	"cmp"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// FileName is the database shared by the node's storage packages.
const FileName = "ouroboros.sqlite"

// Open opens the database in dir, creating a private directory and file as
// needed. Existing directory and database permissions are tightened as well.
// Each call returns an independent pool which the caller must Close (usually
// once when its owning store closes, not once per operation). No handles are
// cached globally. Independently opened stores and processes share WAL safely.
//
// Each pool uses one connection to serialize its writes. Busy timeout (5s) and
// foreign key enforcement are configured on every connection, including any
// replacement opened by database/sql. Transactions must use their Tx, not the
// pool, until committed or rolled back. Close query rows before another query.
// Write transactions should be short; a deferred read transaction upgraded to
// a writer may still fail with SQLITE_BUSY and need retrying by its owner.
//
// Where SQLite reads less than it expects, as from a WAL that was removed
// while connections of this process still use the one they opened, the pool
// reads the file as it is, without the WAL, and refuses writes, and the log
// says so once. Where not even that reads, the error says why. A WAL that is
// gone is found so only where a read needs one of its frames; see Watch for
// the pools that keep writing.
func Open(dir string) (*sql.DB, error) {
	path, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("storage directory: %w", err)
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return nil, fmt.Errorf("create storage directory: %w", err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return nil, fmt.Errorf("secure storage directory: %w", err)
	}
	path = filepath.Join(path, FileName)
	// Create before SQLite opens it so it and its WAL sidecars are private even
	// with a permissive umask. SQLite derives sidecar permissions from the DB.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create storage database: %w", err)
	}
	// Nothing was written, so a failed close loses nothing either: it fails
	// the open like a failed chmod.
	if err := errors.Join(f.Chmod(0o600), f.Close()); err != nil {
		return nil, fmt.Errorf("secure storage database: %w", err)
	}
	db := pool(path, "")
	// WAL is persistent database state; the other pragmas of pool are
	// per-connection. The schema is read once, as every store does first: it
	// may need a frame of the WAL.
	var journal string
	err = enableWAL(db, &journal)
	if err == nil && journal == "wal" {
		err = readable(db)
	}
	if err != nil {
		db.Close()
		err = shortRead(err, path)
		if !errors.As(err, new(walGone)) {
			return nil, fmt.Errorf("enable storage WAL: %w", err)
		}
		if ro := readOnly(path); ro != nil {
			if _, told := readingOnly.LoadOrStore(path, true); !told {
				log.Printf("storage %s is opened read-only, without its WAL, and refuses writes: %v", path, err)
			}
			return ro, nil
		}
		return nil, fmt.Errorf("enable storage WAL: %w", err)
	}
	if journal != "wal" {
		db.Close()
		return nil, fmt.Errorf("enable storage WAL: got journal mode %q", journal)
	}
	readingOnly.Delete(path)
	return db, nil
}

// readingOnly holds the paths of the databases Open last opened read-only,
// so that the log says it once and not at every open until WAL works again.
var readingOnly sync.Map

// resolved is path with its symbolic links resolved, as SQLite does to find
// the WAL beside the file, or path where that fails.
func resolved(path string) string {
	real, _ := filepath.EvalSymlinks(path) // "" where it fails
	return cmp.Or(real, path)
}

// pool is the pool of the database at path, with the URI parameters params
// besides the pragmas every connection gets.
func pool(path, params string) *sql.DB {
	u := url.URL{Scheme: "file", Path: path}
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	u.RawQuery = q.Encode()
	if params != "" {
		u.RawQuery += "&" + params
	}
	// sql.Open only looks up the driver, which the import of modernc.org/sqlite
	// registers; it connects at the first query.
	db, _ := sql.Open("sqlite", u.String())
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	return db
}

// shortRead is err, and where it is a short read of SQLite one that says so
// by name and what it means: a file of the database at path is shorter than
// SQLite expects, as the WAL is when it was removed or replaced while
// connections of this process still use the one they opened.
func shortRead(err error, path string) error {
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) || sqliteErr.Code() != sqlite3.SQLITE_IOERR_SHORT_READ {
		return err
	}
	text := strings.Replace(err.Error(), fmt.Sprintf("(%d)", sqlite3.SQLITE_IOERR_SHORT_READ), "(SQLITE_IOERR_SHORT_READ)", 1) +
		": a file of " + path + " is shorter than SQLite expects, as its WAL is when that was removed or replaced while the database was open"
	return walGone{err, text}
}

// readOnlyMode are the URI parameters of readOnly: a pool that takes the
// file as it is, without the frames of the WAL that are not in it yet. A
// read-only pool that reads the WAL would read the WAL at the path through
// the same index, as the pool that failed did. query_only marks the pool as
// read-only for Watch.
var readOnlyMode = "mode=ro&immutable=1&_pragma=query_only%281%29"

// readOnly is a pool that reads the database at path without WAL, nil if it
// does not.
func readOnly(path string) *sql.DB {
	db := pool(path, readOnlyMode)
	if readable(db) != nil {
		db.Close()
		return nil
	}
	return db
}

// readable reads the schema of db, as every store does first.
func readable(db *sql.DB) error {
	var n int
	return db.QueryRow("SELECT count(*) FROM sqlite_master").Scan(&n)
}

// WAL is the WAL that a pool of Open writes to, as Watch found it.
type WAL struct {
	path string      // where the WAL is
	info os.FileInfo // of the WAL at path then, nil if the pool reads without one
}

// Watch is the WAL that db, a pool Open opened on the database in dir, writes
// to: the one at its path now. A pool that Open opened read-only writes to
// none.
func Watch(db *sql.DB, dir string) WAL {
	w := WAL{path: resolved(filepath.Join(dir, FileName)) + "-wal"}
	var readOnly bool
	if db.QueryRow("PRAGMA query_only").Scan(&readOnly) == nil && !readOnly {
		w.info, _ = os.Stat(w.path) // nil without a WAL
	}
	return w
}

// Kept fails where what the pool of w writes would not be kept: where it
// reads without WAL, and where the WAL it writes to is not the one at its
// path any more. A WAL that was removed while a pool had it open takes every
// commit, but the next process to open the database finds no WAL at the path
// and never reads them. While the pool is open, SQLite does not remove its
// WAL itself. Nothing mends it but closing every pool of the process on the
// database: a pool opened anew would write to a WAL of its own at the path,
// apart from the pools that use the WAL that is gone. Kept copies nothing
// into the database file either: a WAL that another process began at the
// path starts from the file as it was.
func (w WAL) Kept() error {
	if w.info == nil {
		return errors.New("the database is read-only, as it could not be opened with its WAL (see the log)")
	}
	info, err := os.Stat(w.path)
	if err == nil && os.SameFile(info, w.info) {
		return nil
	}
	return fmt.Errorf("the WAL %s was removed or replaced while the database was open: what is written now would be lost when the process ends", w.path)
}

// walGone is the error of a WAL that is not the one the connections of the
// process use any more, see shortRead: err is how it showed, text what it
// means.
type walGone struct {
	err  error
	text string
}

func (e walGone) Error() string { return e.text }
func (e walGone) Unwrap() error { return e.err }

// walPragma turns WAL on and answers with the journal mode the database is in
// then. SQLite keeps the old mode where its VFS cannot share memory between
// processes, as with the locking styles of some network file systems; the
// tests ask for another mode instead.
var walPragma = "PRAGMA journal_mode=WAL"

// Concurrent first opens can receive SQLITE_BUSY during the journal-mode
// transition without invoking SQLite's busy handler. Retry that transition too;
// the DSN timeout still handles ordinary write contention.
func enableWAL(db *sql.DB, journal *string) error {
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := db.QueryRow(walPragma).Scan(journal)
		var sqliteErr *sqlite.Error
		if !errors.As(err, &sqliteErr) || sqliteErr.Code()&0xff != 5 || !time.Now().Before(deadline) {
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
}
