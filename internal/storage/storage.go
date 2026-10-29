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
// Where WAL cannot be enabled for an I/O error, as when the WAL went away
// while another process still has it open, the pool reads without WAL, from
// the file as it is, and refuses every write; the log says so once. Where not
// even that reads, the error names its code.
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
	// per-connection.
	var journal string
	err = enableWAL(db, &journal)
	if err == nil && journal == "wal" {
		err = walIntact(db)
	}
	if err != nil {
		db.Close()
		var sqliteErr *sqlite.Error
		if !errors.As(err, &sqliteErr) || sqliteErr.Code()&0xff != sqlite3.SQLITE_IOERR {
			return nil, fmt.Errorf("enable storage WAL: %w", err)
		}
		// An I/O error, which a pool that only reads the file may get around.
		err = walFailure(err, sqliteErr.Code(), path)
		if ro := readOnly(path); ro != nil {
			if _, told := readingOnly.LoadOrStore(path, true); !told {
				log.Printf("storage %s runs read-only without WAL, from what its file holds, and refuses writes: %v", path, err)
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

// walIntact reads every frame of the WAL of db that is not in its file yet,
// as a passive checkpoint does, which neither waits nor blocks anyone. A WAL
// that is gone at its path while another pool still has it open fails only
// where a frame of it is read, which may otherwise be long after Open.
func walIntact(db *sql.DB) error {
	var busy, frames, copied int
	return db.QueryRow("PRAGMA wal_checkpoint(PASSIVE)").Scan(&busy, &frames, &copied)
}

// readable reads the schema of db, as every store does first.
func readable(db *sql.DB) error {
	var n int
	return db.QueryRow("SELECT count(*) FROM sqlite_master").Scan(&n)
}

// readOnlyMode are the URI parameters of readOnly: a pool that takes the
// file as it is, without the frames of the WAL that are not in it yet. A
// read-only pool that reads the WAL would fail where the pool that writes
// did, as the index of the WAL it reads is the same.
var readOnlyMode = "mode=ro&immutable=1"

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

// walFailure is err of the code with its code named rather than numbered,
// and where it is a short read what that means for a database in WAL: its
// WAL went away while another process still has it open.
func walFailure(err error, code int, path string) error {
	number := fmt.Sprintf("(%d)", code)
	name := cmp.Or(sqlite.ErrorCodeString[code], number)
	name = name[strings.LastIndex(name, "("):] // "(SQLITE_IOERR_SHORT_READ)", without what precedes it
	text := strings.Replace(err.Error(), number, name, 1)
	if code == sqlite3.SQLITE_IOERR_SHORT_READ {
		text += ": the WAL of " + path + " is gone while another process has it open"
	}
	return explained{err, text}
}

// explained is an error that says more than the error it wraps.
type explained struct {
	err  error
	text string
}

func (e explained) Error() string { return e.text }
func (e explained) Unwrap() error { return e.err }

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
