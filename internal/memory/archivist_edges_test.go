package memory

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ouroboros/internal/storage"

	"modernc.org/sqlite"
)

var errRows = errors.New("the records end in an error")

// A change whose versions cannot be read after it fails whole: nothing of it
// is written.
func TestArchivistApplyVersionsFail(t *testing.T) {
	s, f := archivistFaults(t)
	ctx := context.Background()
	e := keep(t, s, "", "tea", "oolong", false)
	f.query = failQuery("SELECT version FROM memories WHERE id=?")
	if _, err := s.Apply(ctx, Change{Action: "archive", ID: e.ID, Reason: "old"}); !errors.Is(err, errQuery) {
		t.Fatalf("versions unread: %v", err)
	}
	f.query = nil
	if got, err := s.Get(ctx, e.ID); err != nil || got.Archived {
		t.Fatalf("after a change whose versions could not be read: %+v, %v", got, err)
	}
}

// A merge whose target cannot be read when upsert looks for it fails before
// anything is changed.
func TestArchivistMergeUpsertReadFails(t *testing.T) {
	s, f := archivistFaults(t)
	ctx := context.Background()
	a := keep(t, s, "", "a", "alpha", false)
	b := keep(t, s, "", "b", "beta", false)
	// The first read is the check of merge, the second that of upsert.
	f.query = failQuery("WHERE project=? AND key=?", 2)
	_, err := s.Apply(ctx, Change{Action: "merge", IDs: []string{a.ID, b.ID}, Key: "a", Content: "alpha beta", Reason: "same"})
	if !errors.Is(err, errQuery) {
		t.Fatalf("target unread in upsert: %v", err)
	}
	for _, id := range []string{a.ID, b.ID} {
		if got, err := s.Get(ctx, id); err != nil || got.Archived {
			t.Errorf("after a merge whose target could not be read: %+v, %v", got, err)
		}
	}
}

// versionsOf reads an id once, leaves out a memory that is gone, and fails as
// the database does in the memories before a change and those it made.
func TestArchivistVersionsOfEdges(t *testing.T) {
	s, f := archivistFaults(t)
	ctx := context.Background()
	e := keep(t, s, "", "tea", "oolong", false)
	if err := s.write(ctx, func(tx *sql.Tx) error {
		out, err := versionsOf(ctx, tx, []Entry{e, e}, nil)
		if err != nil {
			return err
		}
		if out[e.ID] != e.Version || len(out) != 1 {
			t.Errorf("versions of a repeated memory: %+v", out)
		}
		out, err = versionsOf(ctx, tx, []Entry{{ID: "missing"}}, nil)
		if err != nil || len(out) != 0 {
			t.Errorf("versions with a memory that is gone: %+v, %v", out, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for name, read := range map[string]struct {
		before  []Entry
		created []string
	}{
		"before":  {before: []Entry{e}},
		"created": {created: []string{e.ID}},
	} {
		f.query = failQuery("SELECT version FROM memories WHERE id=?")
		err := s.write(ctx, func(tx *sql.Tx) error {
			_, err := versionsOf(ctx, tx, read.before, read.created)
			return err
		})
		f.query = nil
		if !errors.Is(err, errQuery) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// undoVersion leaves a record from before versions were kept alone, refuses
// a memory that is gone, and fails as the database does.
func TestArchivistUndoVersionEdges(t *testing.T) {
	s, f := archivistFaults(t)
	ctx := context.Background()
	e := keep(t, s, "", "tea", "oolong", false)
	if err := s.write(ctx, func(tx *sql.Tx) error {
		if err := undoVersion(ctx, tx, e.ID, nil); err != nil {
			return err
		}
		err := undoVersion(ctx, tx, "missing", map[string]int64{"missing": 1})
		if err == nil || !strings.Contains(err.Error(), "no longer there") {
			t.Errorf("undo of a memory that is gone: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	f.query = failQuery("SELECT version FROM memories WHERE id=?")
	err := s.write(ctx, func(tx *sql.Tx) error {
		return undoVersion(ctx, tx, e.ID, map[string]int64{e.ID: e.Version})
	})
	f.query = nil
	if !errors.Is(err, errQuery) {
		t.Fatalf("unread version: %v", err)
	}
}

// carryVersion carries nothing from a record whose undo is no JSON, and fails
// as the database does when it reads the version or the records.
func TestArchivistCarryVersionEdges(t *testing.T) {
	s, f := archivistFaults(t)
	ctx := context.Background()
	e := keep(t, s, "", "tea", "oolong", false)
	if _, err := s.db.Exec("INSERT INTO memory_changes(at,action,reason,undo) VALUES('then','rewrite','old','{')"); err != nil {
		t.Fatal(err)
	}
	if err := s.write(ctx, func(tx *sql.Tx) error {
		return carryVersion(ctx, tx, 2, e)
	}); err != nil {
		t.Fatalf("carry over a record that is no undo: %v", err)
	}
	for name, query := range map[string]string{
		"version": "SELECT version FROM memories WHERE id=?",
		"records": "SELECT id,undo FROM memory_changes",
	} {
		f.query = failQuery(query)
		err := s.write(ctx, func(tx *sql.Tx) error {
			return carryVersion(ctx, tx, 2, e)
		})
		f.query = nil
		if !errors.Is(err, errQuery) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// carryVersion fails on a record it cannot scan and on records that end in an
// error.
func TestArchivistCarryVersionRowsEdges(t *testing.T) {
	ctx := context.Background()
	for name, rows := range map[string]*edgeRows{
		"scan": {cols: []string{"id", "undo"}, vals: []driver.Value{int64(1), nil}},
		"end":  {cols: []string{"id", "undo"}, vals: []driver.Value{int64(1), "{}"}, err: errRows},
	} {
		s := edgeStore(t, func(query string) (driver.Rows, bool) {
			if !strings.Contains(query, "SELECT id,undo FROM memory_changes") {
				return nil, false
			}
			return rows, true
		})
		e := keep(t, s, "", "tea", "oolong", false)
		err := s.write(ctx, func(tx *sql.Tx) error {
			return carryVersion(ctx, tx, 2, e)
		})
		if name == "scan" {
			if err == nil || !strings.Contains(err.Error(), "converting NULL") {
				t.Errorf("scan: %v", err)
			}
		} else if !errors.Is(err, errRows) {
			t.Errorf("end: %v", err)
		}
	}
}

// An undo that cannot carry the version of an earlier change fails whole, and
// goes through once the log can be written.
func TestArchivistUndoCarryFails(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	e := keep(t, s, "", "tea", "oolong tea", false)
	if _, err := s.Apply(ctx, Change{Action: "rewrite", ID: e.ID, Content: "one", Reason: "first"}); err != nil {
		t.Fatal(err)
	}
	rec, err := s.Apply(ctx, Change{Action: "rewrite", ID: e.ID, Content: "two", Reason: "second"})
	if err != nil {
		t.Fatal(err)
	}
	trigger(t, s, "UPDATE ON memory_changes")
	if err := s.UndoChange(ctx, rec.ID); !refused(err) {
		t.Fatalf("carried version not written: %v", err)
	}
	untrigger(t, s)
	if got, err := s.Get(ctx, e.ID); err != nil || got.Content != "two" || got.Version != 3 {
		t.Fatalf("after the failed undo: %+v, %v", got, err)
	}
	if err := s.UndoChange(ctx, rec.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Get(ctx, e.ID); err != nil || got.Content != "one" || got.Version != 4 {
		t.Fatalf("after the undo: %+v, %v", got, err)
	}
}

// An undo skips a memory a change made that was forgotten since, refuses one
// that changed, and fails as the database does when it looks for one.
func TestArchivistUndoCreatedEdges(t *testing.T) {
	ctx := context.Background()
	split := func(t *testing.T, s *Store) (Entry, ChangeRecord) {
		t.Helper()
		e := keep(t, s, "", "drinks", "tea is oolong. coffee is espresso.", false)
		rec, err := s.Apply(ctx, Change{
			Action: "split", ID: e.ID,
			Parts:  []Part{{"tea", "tea is oolong."}, {"coffee", "coffee is espresso."}},
			Reason: "two things",
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(rec.Created) != 2 {
			t.Fatalf("created: %q", rec.Created)
		}
		return e, rec
	}
	t.Run("forgotten", func(t *testing.T) {
		s := testStore(t)
		e, rec := split(t, s)
		if _, err := s.db.Exec("DELETE FROM memories WHERE id=?", rec.Created[0]); err != nil {
			t.Fatal(err)
		}
		if err := s.UndoChange(ctx, rec.ID); err != nil {
			t.Fatalf("undo with a forgotten part: %v", err)
		}
		current, err := s.Current(ctx, 10)
		if err != nil || len(current) != 1 || current[0].ID != e.ID {
			t.Fatalf("after the undo: %+v, %v", current, err)
		}
	})
	t.Run("unread", func(t *testing.T) {
		s, f := archivistFaults(t)
		_, rec := split(t, s)
		f.query = failQuery("SELECT 1 FROM memories WHERE id=?")
		if err := s.UndoChange(ctx, rec.ID); !errors.Is(err, errQuery) {
			t.Fatalf("unread created memory: %v", err)
		}
	})
	t.Run("changed", func(t *testing.T) {
		s := testStore(t)
		_, rec := split(t, s)
		if _, _, err := s.Remember(ctx, "", "tea", "tea is oolong now", "session", false, time.Time{}); err != nil {
			t.Fatal(err)
		}
		if err := s.UndoChange(ctx, rec.ID); err == nil || !strings.Contains(err.Error(), "changed since the change") {
			t.Fatalf("undo over a changed part: %v", err)
		}
	})
}

// edgeConnector opens real SQLite connections whose queries a test may answer
// itself.
type edgeConnector struct {
	dsn    string
	answer func(string) (driver.Rows, bool)
}

func (c edgeConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := (&sqlite.Driver{}).Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &edgeConn{Conn: conn, answer: c.answer}, nil
}

func (c edgeConnector) Driver() driver.Driver { return &sqlite.Driver{} }

type edgeConn struct {
	driver.Conn
	answer func(string) (driver.Rows, bool)
}

func (c *edgeConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
}

func (c *edgeConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
}

func (c *edgeConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if c.answer != nil {
		if rows, ok := c.answer(query); ok {
			return rows, nil
		}
	}
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}

// edgeRows are the rows a test gives instead of those of the database: one
// row, then its end or an error.
type edgeRows struct {
	cols []string
	vals []driver.Value
	err  error
	done bool
}

func (r *edgeRows) Columns() []string { return r.cols }
func (r *edgeRows) Close() error      { return nil }
func (r *edgeRows) Next(dest []driver.Value) error {
	if !r.done {
		r.done = true
		copy(dest, r.vals)
		return nil
	}
	if r.err != nil {
		return r.err
	}
	return io.EOF
}

// edgeStore opens a store whose database answers a query as answer says, and
// runs the ones it does not answer against SQLite.
func edgeStore(t *testing.T, answer func(string) (driver.Rows, bool)) *Store {
	t.Helper()
	u := url.URL{Scheme: "file", Path: filepath.Join(t.TempDir(), storage.FileName), RawQuery: "_pragma=busy_timeout(0)"}
	db := sql.OpenDB(edgeConnector{u.String(), answer})
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	s, err := New(db)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
