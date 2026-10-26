package memory

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ouroboros/internal/storage"

	"modernc.org/sqlite"
)

// faults are what the connections of a faultConnector do besides SQLite:
// errors a healthy database never gives, and a look at what a store does
// between the statements of a transaction.
type faults struct {
	exec     func(query string, err error) error // told of each statement and its error, returns the error to give
	query    func(query string) error            // an error for a query instead of running it, or nil
	affected error                               // of every RowsAffected
	commit   error                               // of every commit, which rolls back instead
}

// faultConnector opens real SQLite connections to dsn that fail as its
// faults say.
type faultConnector struct {
	dsn string
	*faults
}

func (c faultConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := (&sqlite.Driver{}).Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &faultConn{conn, c.faults}, nil
}

func (c faultConnector) Driver() driver.Driver { return &sqlite.Driver{} }

type faultConn struct {
	driver.Conn
	*faults
}

func (c *faultConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	tx, err := c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return faultTx{tx, c.faults}, nil
}

func (c *faultConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	r, err := c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
	if c.exec != nil {
		err = c.exec(query, err)
	}
	if err != nil {
		return nil, err
	}
	return faultResult{r, c.affected}, nil
}

func (c *faultConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if c.query != nil {
		if err := c.query(query); err != nil {
			return nil, err
		}
	}
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}

type faultTx struct {
	driver.Tx
	*faults
}

func (tx faultTx) Commit() error {
	if tx.commit != nil {
		tx.Tx.Rollback()
		return tx.commit
	}
	return tx.Tx.Commit()
}

type faultResult struct {
	driver.Result
	affected error
}

func (r faultResult) RowsAffected() (int64, error) {
	if r.affected != nil {
		return 0, r.affected
	}
	return r.Result.RowsAffected()
}

// faultDB opens the database of the stores in dir through f, with one
// connection like storage.Open, but without waiting for the lock of a
// writer: a statement that would wait fails at once with SQLITE_BUSY.
func faultDB(t *testing.T, dir string, f *faults) *sql.DB {
	t.Helper()
	u := url.URL{Scheme: "file", Path: filepath.Join(dir, storage.FileName), RawQuery: "_pragma=busy_timeout(0)"}
	db := sql.OpenDB(faultConnector{u.String(), f})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

// busy tells whether err is SQLite's for a database another connection
// writes to.
func busy(err error) bool {
	var e *sqlite.Error
	return errors.As(err, &e) && e.Code()&0xff == 5
}

// A store cannot be opened where no database can be, nor over a database
// whose tables cannot be those of memories.
func TestOpenFails(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if s, err := Open(file); err == nil {
		s.Close()
		t.Fatal("opened a store in a file")
	}
	dir := t.TempDir()
	db, err := storage.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec("CREATE VIEW memories AS SELECT 1 AS id")
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	if s, err := Open(dir); err == nil || !strings.Contains(err.Error(), "memory schema") {
		if s != nil {
			s.Close()
		}
		t.Fatalf("opened over a view: %v", err)
	}
}

// New installs its tables in one transaction or not at all, and a store of
// a pool the caller owns leaves the pool open when it closes.
func TestNewOnTheCallersPool(t *testing.T) {
	closed, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	closed.Close()
	if _, err := New(closed); err == nil {
		t.Fatal("installed the tables over a closed pool")
	}

	broken := errors.New("disk gone")
	f := &faults{commit: broken}
	db := faultDB(t, t.TempDir(), f)
	if _, err := New(db); !errors.Is(err, broken) {
		t.Fatalf("a commit that failed: %v", err)
	}
	var tables int
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name LIKE 'memor%'").Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("%d tables left of a commit that failed, %v", tables, err)
	}
	f.commit = nil
	s, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.Ping(); err != nil {
		t.Fatalf("closing the store closed the caller's pool: %v", err)
	}
}

// An update or a deletion needs a version a memory can have and valid text:
// a version below 1 conflicts before the memory is even looked for.
func TestUpdateAndForgetRefuseWhatCannotBe(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	e, err := s.Create(ctx, "", "drink", "oolong", "owner")
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []struct{ key, content, source string }{{"", "tea", "owner"}, {"drink", "", "owner"}, {"drink", "tea", ""}} {
		if _, err := s.Update(ctx, "", e.ID, 1, a.key, a.content, a.source); err == nil {
			t.Errorf("updated with %+v", a)
		}
	}
	if _, err := s.Update(ctx, "", "missing", 0, "drink", "tea", "owner"); !errors.Is(err, ErrConflict) {
		t.Errorf("update of version 0: %v", err)
	}
	if err := s.Forget(ctx, "", "missing", 0); !errors.Is(err, ErrConflict) {
		t.Errorf("forget of version 0: %v", err)
	}
	if got, err := s.Read(ctx, "", e.ID); err != nil || got != e {
		t.Fatalf("refused changes changed the memory: %+v, %v", got, err)
	}
}

// A deletion the database refuses leaves the memory and its index as they
// were.
func TestForgetThatFailsKeepsTheMemory(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	e, err := s.Create(ctx, "", "drink", "oolong", "owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER keep BEFORE DELETE ON memories BEGIN SELECT RAISE(ABORT,'kept'); END;`); err != nil {
		t.Fatal(err)
	}
	if err := s.Forget(ctx, "", e.ID, 1); err == nil || !strings.Contains(err.Error(), "kept") {
		t.Fatalf("forget: %v", err)
	}
	if hits, err := s.Search(ctx, "", "global", "oolong", 10); err != nil || len(hits) != 1 || hits[0].ID != e.ID {
		t.Fatalf("after a failed forget: %+v, %v", hits, err)
	}
}

// A query is at most 1024 bytes. Without a limit, a search finds 10
// memories, and never more than MaxHits.
func TestSearchLimits(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	for i := range MaxHits + 1 {
		if _, err := s.Create(ctx, "", strings.Repeat("k", i+1), "token", "owner"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Search(ctx, "", "global", strings.Repeat("a", 1025), 10); err == nil {
		t.Error("searched for a query of 1025 bytes")
	}
	if hits, err := s.Search(ctx, "", "global", strings.Repeat("a", 1024), 10); err != nil || len(hits) != 0 {
		t.Errorf("a query of 1024 bytes: %+v, %v", hits, err)
	}
	for limit, want := range map[int]int{-1: 10, 0: 10, 1: 1, MaxHits: MaxHits, MaxHits + 1: MaxHits} {
		if hits, err := s.Search(ctx, "", "global", "token", limit); err != nil || len(hits) != want {
			t.Errorf("limit %d: %d hits, %v; want %d", limit, len(hits), err, want)
		}
	}
}

// A search that is stopped, or that comes upon a row that is no memory,
// fails instead of giving part of an answer.
func TestSearchFails(t *testing.T) {
	s := testStore(t)
	stopped, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Search(stopped, "", "global", "", 10); !errors.Is(err, context.Canceled) {
		t.Fatalf("stopped: %v", err)
	}
	// SQLite keeps what it is given, also a version that is no number.
	if _, err := s.db.Exec("INSERT INTO memories (" + columns + ") VALUES('bad','key','content','','x','t','t','owner',0,'',0,0,0)"); err != nil {
		t.Fatal(err)
	}
	if hits, err := s.Search(context.Background(), "", "global", "", 10); err == nil {
		t.Fatalf("searched over a row that is no memory: %+v", hits)
	}
	if _, err := s.Read(context.Background(), "", "bad"); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("read a row that is no memory: %v", err)
	}
}

// An update whose outcome cannot be told fails with why, and changes
// nothing.
func TestUpdateWhoseOutcomeIsUnknown(t *testing.T) {
	dir := t.TempDir()
	f := &faults{}
	s, err := New(faultDB(t, dir, f))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	e, err := s.Create(ctx, "", "drink", "oolong", "owner")
	if err != nil {
		t.Fatal(err)
	}
	uncounted := errors.New("rows not counted")
	f.affected = uncounted
	if _, err := s.Update(ctx, "", e.ID, 1, "drink", "espresso", "owner"); !errors.Is(err, uncounted) {
		t.Fatalf("uncounted update: %v", err)
	}
	f.affected = nil
	unread := errors.New("not read")
	f.query = func(query string) error {
		if strings.HasPrefix(query, "SELECT 1 ") {
			return unread
		}
		return nil
	}
	// A stale version: whether the memory is still there is asked for.
	if _, err := s.Update(ctx, "", e.ID, 2, "drink", "espresso", "owner"); !errors.Is(err, unread) {
		t.Fatalf("unread conflict: %v", err)
	}
	f.query = nil
	if got, err := s.Read(ctx, "", e.ID); err != nil || got != e {
		t.Fatalf("failed updates changed the memory: %+v, %v", got, err)
	}
}

// heldStores returns a store in a directory of its own, and one over the
// same database that calls busySeen each time a writer kept it from writing.
// The first has begun to write in the transaction it returns, which the
// caller ends.
func heldStores(t *testing.T, busySeen func()) (*Store, *Store, *sql.Tx) {
	t.Helper()
	dir := t.TempDir()
	holder, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { holder.Close() })
	waiter, err := New(faultDB(t, dir, &faults{exec: func(_ string, err error) error {
		if busy(err) {
			busySeen()
		}
		return err
	}}))
	if err != nil {
		t.Fatal(err)
	}
	held, err := holder.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { held.Rollback() })
	if _, err := held.Exec("INSERT INTO memories (" + columns + ") VALUES('held','held','held','',1,'t','t','owner',0,'',0,0,0)"); err != nil {
		t.Fatal(err)
	}
	return holder, waiter, held
}

// A writer that finds the database busy tries the whole transaction again,
// every 10 ms, until the other writer is done.
func TestWriteWaitsForAnotherWriter(t *testing.T) {
	const tries = 11
	var seen []time.Time // when the writer found the database busy
	blocked := make(chan struct{})
	holder, waiter, held := heldStores(t, func() {
		if seen = append(seen, time.Now()); len(seen) == tries {
			close(blocked)
		}
	})
	done := make(chan error, 1)
	go func() {
		_, err := waiter.Create(context.Background(), "", "drink", "oolong", "owner")
		done <- err
	}()
	select {
	case <-blocked:
	case err := <-done:
		t.Fatalf("written past another writer: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("the writer was never kept waiting")
	}
	if err := held.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("the waiting writer gave up: %v", err)
	}
	if took := seen[tries-1].Sub(seen[0]); took < (tries-1)*10*time.Millisecond {
		t.Errorf("tried %d times in %s: it does not wait between the tries", tries, took)
	}
	hits, err := holder.Search(context.Background(), "", "global", "", 10)
	if err != nil || len(hits) != 2 {
		t.Fatalf("both writes: %+v, %v", hits, err)
	}
}

// A writer that is stopped while it waits for another stops waiting.
func TestWriteStopsWaitingWhenStopped(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	holder, waiter, held := heldStores(t, cancel)
	if _, err := waiter.Create(ctx, "", "drink", "oolong", "owner"); !errors.Is(err, context.Canceled) {
		t.Fatalf("stopped while waiting: %v", err)
	}
	if err := held.Rollback(); err != nil {
		t.Fatal(err)
	}
	if hits, err := holder.Search(context.Background(), "", "global", "", 10); err != nil || len(hits) != 0 {
		t.Fatalf("a stopped write was written: %+v, %v", hits, err)
	}
}
