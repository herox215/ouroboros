// Package storage opens the node's shared SQLite database. Domain packages own
// their schemas and migrations; this package only owns connection setup.
package storage

import (
	"cmp"
	"context"
	"database/sql"
	"database/sql/driver"
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
// cached globally; the process only counts its connections to each database,
// see files. Independently opened stores and processes share WAL safely.
//
// Each pool uses one connection to serialize its writes. Busy timeout (5s) and
// foreign key enforcement are configured on every connection, including any
// replacement opened by database/sql. Transactions must use their Tx, not the
// pool, until committed or rolled back. Close query rows before another query.
// Write transactions should be short; a deferred read transaction upgraded to
// a writer may still fail with SQLITE_BUSY and need retrying by its owner.
//
// A WAL that was removed or replaced while connections of this process still
// use the one they opened is no longer the WAL at the path: a pool on it would
// read frames that are not there, or write where the others do not read, see
// walIntact. The pool then reads the file as it is, without the WAL, and
// refuses writes, and the log says so once. Where not even that reads, the
// error says why.
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
	path = resolved(path)
	db := pool(path, "", true)
	var journal string
	err = walIntact(path)
	if err == nil {
		// WAL is persistent database state; the other pragmas of pool are
		// per-connection.
		err = enableWAL(db, &journal)
	}
	if err == nil && journal == "wal" {
		err = walFound(db, path)
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
// besides the pragmas every connection gets. The connections of a counted
// pool are counted in files while they are open.
func pool(path, params string, counted bool) *sql.DB {
	u := url.URL{Scheme: "file", Path: path}
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	u.RawQuery = q.Encode()
	if params != "" {
		u.RawQuery += "&" + params
	}
	// It connects at the first query.
	db := sql.OpenDB(connector{u.String(), path, counted})
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	return db
}

// sqliteDriver is the driver that the import of modernc.org/sqlite registers,
// with the hooks registered on it.
var sqliteDriver = func() driver.Driver {
	db, _ := sql.Open("sqlite", "")
	defer db.Close()
	return db.Driver()
}()

// connector connects to the database at path by dsn, see pool.
type connector struct {
	dsn, path string
	counted   bool
}

func (c connector) Connect(context.Context) (driver.Conn, error) {
	conn, err := sqliteDriver.Open(c.dsn)
	if err != nil || !c.counted {
		return conn, err
	}
	files.opened(c.path)
	return &countedConn{conn.(sqliteConn), c.path}, nil
}

func (c connector) Driver() driver.Driver { return sqliteDriver }

// sqliteConn is what a connection of modernc.org/sqlite does that
// database/sql asks of a connection.
type sqliteConn interface {
	driver.Conn
	driver.ConnBeginTx
	driver.ConnPrepareContext
	driver.ExecerContext
	driver.QueryerContext
	driver.SessionResetter
	driver.Validator
	driver.Pinger
}

// countedConn is a connection to the database at path that files counts
// while it is open.
type countedConn struct {
	sqliteConn
	path string
}

func (c *countedConn) Close() error {
	err := c.sqliteConn.Close()
	files.closed(c.path)
	return err
}

// files are the databases the process has connections to that may use the
// WAL, by the path of the database: how many, and the WAL at the path when
// the first of them used it, see walIntact. SQLite shares the index of the
// WAL between all connections of a process to a file, but each connection
// keeps the WAL it opened: one that is removed lives on in them. Only once
// they are all closed may the WAL at the path be another one.
var files = fileCount{open: map[string]*file{}}

type fileCount struct {
	sync.Mutex
	open map[string]*file
}

type file struct {
	conns int
	wal   os.FileInfo // nil until found
}

// opened counts a connection to the database at path.
func (fc *fileCount) opened(path string) {
	fc.Lock()
	defer fc.Unlock()
	f := fc.open[path]
	if f == nil {
		f = &file{}
		fc.open[path] = f
	}
	f.conns++
}

// closed counts a connection to the database at path less, and forgets its
// WAL with the last one.
func (fc *fileCount) closed(path string) {
	fc.Lock()
	defer fc.Unlock()
	f := fc.open[path]
	if f.conns--; f.conns == 0 {
		delete(fc.open, path)
	}
}

// walIntact checks that the WAL at the path of the database at path is the
// one the connections of the process to it use, see files, where the process
// knows that one: before a new connection reads anything through the index
// of the WAL. A WAL at the path that is not that one may hold the frames of
// another process, which the index of this process does not describe.
func walIntact(path string) error {
	if !files.gone(path) {
		return nil
	}
	text := "the WAL of " + path + " was removed or replaced while this process had the database open"
	return walGone{errors.New(text), text}
}

// walFound reads db once, which opens the WAL at the path of the database at
// path, also for a database that holds nothing yet, and takes it for the one
// the connections of the process use where it knows none yet, see files.
func walFound(db *sql.DB, path string) error {
	err := readable(db)
	if err == nil {
		files.found(path)
	}
	return err
}

// gone reports whether the WAL at the path of the database at path is not
// the one its connections use, as far as that is known.
func (fc *fileCount) gone(path string) bool {
	info, err := os.Stat(path + "-wal")
	fc.Lock()
	defer fc.Unlock()
	f := fc.open[path]
	return f != nil && f.wal != nil && (err != nil || !os.SameFile(info, f.wal))
}

// found takes the WAL at the path of the database at path for the one its
// connections use, unless one is known already.
func (fc *fileCount) found(path string) {
	info, err := os.Stat(path + "-wal")
	fc.Lock()
	defer fc.Unlock()
	if f := fc.open[path]; f != nil && f.wal == nil && err == nil {
		f.wal = info
	}
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
	db := pool(path, readOnlyMode, false)
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
	db   *sql.DB
	path string      // where the WAL is
	info os.FileInfo // of the WAL at path then, nil if the pool reads without one
}

// Watch is the WAL that db, a pool Open opened on the database in dir, writes
// to: the one at its path now. A pool that Open opened read-only writes to
// none.
func Watch(db *sql.DB, dir string) WAL {
	w := WAL{db: db, path: resolved(filepath.Join(dir, FileName)) + "-wal"}
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
// and never reads them. Nothing mends that but closing every pool of the
// process on the database: a pool opened anew would write to a WAL of its own
// at the path, apart from the pools that use the WAL that is gone.
func (w WAL) Kept() error {
	if w.info == nil {
		return errors.New("the database is read-only, as it could not be opened with its WAL (see the log)")
	}
	info, err := os.Stat(w.path)
	if err == nil && os.SameFile(info, w.info) {
		return nil
	}
	// What the WAL that is gone holds is copied into the database file, as
	// far as that goes without waiting: no process that opens the database
	// anew reads it, and nothing writes to it now that would copy it later.
	// Not where another process has begun a WAL of its own at the path: that
	// one starts from the file as it was.
	if err != nil || info.Size() <= walHeader {
		w.db.Exec("PRAGMA wal_checkpoint(PASSIVE)") // what it could not copy, it could not
	}
	return fmt.Errorf("the WAL %s was removed or replaced while the database was open: what is written now would be lost when the process ends", w.path)
}

// walHeader is how long the header of a WAL is: a WAL no longer than that
// holds no frame.
const walHeader = 32

// walGone is the error of a WAL that is not the one the connections of the
// process use any more, see walIntact: err is how it showed, text what it
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
