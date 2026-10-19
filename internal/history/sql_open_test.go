package history

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// scriptedDriver is a database that fails where a test needs it to while the
// schema is inspected: at the PRAGMA itself, at one of its columns, or at the
// end of its columns.
type scriptedDriver struct{}

func (scriptedDriver) Open(fail string) (driver.Conn, error) { return &scriptedConn{fail: fail}, nil }

type scriptedConn struct{ fail string }

func (c *scriptedConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("not scripted")
}
func (c *scriptedConn) Close() error              { return nil }
func (c *scriptedConn) Begin() (driver.Tx, error) { return scriptedTx{}, nil }
func (c *scriptedConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return driver.RowsAffected(0), nil
}
func (c *scriptedConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	if !strings.HasPrefix(query, "PRAGMA table_info") {
		return nil, errors.New("unexpected query: " + query)
	}
	if c.fail == "query" {
		return nil, errors.New("the schema cannot be read")
	}
	return &scriptedRows{fail: c.fail}, nil
}

type scriptedTx struct{}

func (scriptedTx) Commit() error   { return nil }
func (scriptedTx) Rollback() error { return nil }

type scriptedRows struct {
	fail string
	read bool
}

func (r *scriptedRows) Columns() []string {
	return []string{"cid", "name", "type", "notnull", "dflt_value", "pk"}
}
func (r *scriptedRows) Close() error { return nil }
func (r *scriptedRows) Next(dest []driver.Value) error {
	if r.fail == "scan" && !r.read {
		r.read = true
		dest[0] = "not a number"
		dest[1], dest[2], dest[3], dest[4], dest[5] = "id", "TEXT", 0, nil, 0
		return nil
	}
	if r.fail == "rows" {
		return errors.New("the columns cannot be read to their end")
	}
	return io.EOF
}

func init() { sql.Register("history-scripted", scriptedDriver{}) }

// TestSQLSchemaInspectionFails opens the archive over a database whose schema
// inspection fails, so that each failure is reported rather than ignored.
func TestSQLSchemaInspectionFails(t *testing.T) {
	for _, tc := range []struct{ name, fail, want string }{
		{"the schema cannot be read", "query", "the schema cannot be read"},
		{"a column cannot be read", "scan", "converting driver.Value"},
		{"the columns end in an error", "rows", "the columns cannot be read to their end"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := &Archive{dir: t.TempDir(), now: time.Now, open: func(string) (*sql.DB, error) {
				return sql.Open("history-scripted", tc.fail)
			}}
			defer a.Close()
			if _, err := a.readDatabase(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

// TestNoteModelCannotOpenTheArchive opens the database before it keeps the
// model, so a failure leaves neither the model nor the record that says where
// the conversation goes on.
func TestNoteModelCannotOpenTheArchive(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "archive")
	if err := os.WriteFile(dir, nil, 0600); err != nil {
		t.Fatal(err)
	}
	a := Open(dir)
	defer a.Close()
	if err := a.NoteModel("owner/s1", "gpt"); err == nil || !strings.Contains(err.Error(), "create storage directory") {
		t.Fatalf("got %v", err)
	}
}
