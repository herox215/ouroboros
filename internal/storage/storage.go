// Package storage opens the node's shared SQLite database. Domain packages own
// their schemas and migrations; this package only owns connection setup.
package storage

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"modernc.org/sqlite"
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
	err = f.Chmod(0o600)
	closeErr := f.Close()
	if err != nil {
		return nil, fmt.Errorf("secure storage database: %w", err)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close storage database file: %w", closeErr)
	}
	u := url.URL{Scheme: "file", Path: path}
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, fmt.Errorf("open storage database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	// WAL is persistent database state; the other pragmas above are per-connection.
	var journal string
	if err := enableWAL(db, &journal); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable storage WAL: %w", err)
	}
	if journal != "wal" {
		db.Close()
		return nil, fmt.Errorf("enable storage WAL: got journal mode %q", journal)
	}
	return db, nil
}

// Concurrent first opens can receive SQLITE_BUSY during the journal-mode
// transition without invoking SQLite's busy handler. Retry that transition too;
// the DSN timeout still handles ordinary write contention.
func enableWAL(db *sql.DB, journal *string) error {
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := db.QueryRow("PRAGMA journal_mode=WAL").Scan(journal)
		var sqliteErr *sqlite.Error
		if !errors.As(err, &sqliteErr) || sqliteErr.Code()&0xff != 5 || !time.Now().Before(deadline) {
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
}
