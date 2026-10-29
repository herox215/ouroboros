package history

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"modernc.org/sqlite"

	"ouroboros/internal/openai"
	"ouroboros/internal/storage"
)

// The databases in directories with these names fail by a hook on every
// connection to them: the one is in a transaction from the start, so that the
// archive cannot begin its own, and the directory of the other cannot be
// listed once its database is open.
const (
	inTransaction = "in-transaction"
	unlisted      = "unlisted"
)

func init() {
	sqlite.RegisterConnectionHook(func(conn sqlite.ExecQuerierContext, dsn string) error {
		u, err := url.Parse(dsn)
		if err != nil {
			return nil // not a database of these tests
		}
		dir := filepath.Dir(u.Path)
		switch filepath.Base(dir) {
		case inTransaction:
			_, err = conn.ExecContext(context.Background(), "BEGIN", nil)
		case unlisted:
			err = os.Chmod(dir, 0o300)
		}
		return err
	})
}

// exec runs sql on the database of a, which it opens.
func exec(t *testing.T, a *Archive, sql string) {
	t.Helper()
	db, err := a.database()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(sql); err != nil {
		t.Fatal(err)
	}
}

// prepared returns the directory of an archive whose database sql changed
// before the legacy archive in it is imported, as on the first start of a
// node that kept its archive in files.
func prepared(t *testing.T, sql string) string {
	t.Helper()
	dir := t.TempDir()
	a := Open(dir)
	exec(t, a, "DELETE FROM history_migrations;"+sql)
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	return dir
}

// repair runs sql on the database in dir, past any archive.
func repair(t *testing.T, dir, sql string) {
	t.Helper()
	db, err := storage.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(sql); err != nil {
		t.Fatal(err)
	}
}

// broken is a trigger that fails a write, named broken.
func broken(when string) string {
	return "CREATE TRIGGER broken " + when + " BEGIN SELECT RAISE(ABORT,'broken'); END"
}

// adopt makes db the open pool of a, as if a had opened it, with the WAL it
// writes to, see Archive.kept.
func adopt(a *Archive, db *sql.DB) {
	a.db, a.wal = db, storage.Watch(db, a.dir)
}

// closeBehind closes the pool of a behind its back. Another connection keeps
// the WAL at its path: SQLite removes the WAL with the last connection, and
// a change would then fail already where kept finds its WAL gone, not where
// the operation uses the closed pool.
func closeBehind(t *testing.T, a *Archive) {
	t.Helper()
	db, err := a.database()
	if err != nil {
		t.Fatal(err)
	}
	other, err := sql.Open("sqlite", "file:"+filepath.Join(a.dir, storage.FileName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { other.Close() })
	if _, err := other.Exec("SELECT 1 FROM history_sessions"); err != nil {
		t.Fatal(err)
	}
	db.Close()
}

// Once the database cannot be reached, every operation fails instead of
// telling of an empty archive.
func TestClosedDatabase(t *testing.T) {
	a := at(t)
	if err := a.Append(owner, question, nil); err != nil {
		t.Fatal(err)
	}
	closeBehind(t, a)
	if err := a.Append(owner, question, nil); err == nil {
		t.Error("append")
	}
	if _, err := a.List(); err == nil {
		t.Error("list")
	}
	if _, err := a.Match("roof"); err == nil {
		t.Error("match")
	}
	if _, _, err := a.Search(owner, "roof", false, 0); err == nil {
		t.Error("search")
	}
	if _, err := a.Page(Owner(owner.Session), 0); err == nil || errors.Is(err, ErrNotArchived) {
		t.Errorf("page: %v", err)
	}
	if err := a.Delete(Owner(owner.Session)); err == nil || errors.Is(err, ErrNotArchived) {
		t.Errorf("delete: %v", err)
	}
	if _, err := a.GoingOn(); err == nil {
		t.Error("going on")
	}
}

// The statistics of an answer are kept with the record that ends it.
func TestAppendStatistics(t *testing.T) {
	a := at(t)
	statistics := Statistics{Context: 1234, Cache: json.RawMessage(`{"read":56}`)}
	if err := a.Append(owner, question, nil, statistics); err != nil {
		t.Fatal(err)
	}
	got := all(t, a, owner)
	if len(got) != 2 || got[0].Statistics != nil || got[1].Statistics == nil || !reflect.DeepEqual(*got[1].Statistics, statistics) {
		t.Fatalf("records %+v", got)
	}
}

// An answer that cannot be kept in full is not kept at all, whichever of its
// writes fails, and the conversation stays as it was.
func TestAppendFailsAtEveryWrite(t *testing.T) {
	for name, c := range map[string]struct {
		sql        string
		statistics []Statistics
	}{
		"session":    {sql: broken("BEFORE INSERT ON history_sessions WHEN new.id='owner/new'")},
		"sequence":   {sql: "UPDATE history_records SET seq='first' WHERE ordinal=1 AND session_id='owner/new'"},
		"message":    {sql: broken("BEFORE INSERT ON history_records WHEN new.session_id='owner/new'")},
		"statistics": {statistics: []Statistics{{Cache: json.RawMessage("{")}}},
		"size":       {sql: broken("BEFORE UPDATE OF archived ON history_sessions WHEN old.id='owner/new'")},
	} {
		t.Run(name, func(t *testing.T) {
			a := at(t)
			who := Who{Session: "new"}
			if name == "sequence" {
				if err := a.Append(who, question, nil); err != nil {
					t.Fatal(err)
				}
			}
			if c.sql != "" {
				exec(t, a, c.sql)
			}
			before, _ := a.Records("owner/new")
			if err := a.Append(who, question, nil, c.statistics...); err == nil {
				t.Fatal("the answer was kept")
			}
			if after, _ := a.Records("owner/new"); len(after) != len(before) {
				t.Fatalf("records %+v, before %+v", after, before)
			}
		})
	}
}

// A legacy archive is imported completely or not at all: whichever write
// fails, nothing of it is kept, and the import is tried again.
func TestSQLImportFails(t *testing.T) {
	for name, sql := range map[string]string{
		"session":   broken("BEFORE INSERT ON history_sessions"),
		"record":    broken("BEFORE INSERT ON history_records WHEN new.seq=2"),
		"size":      broken("BEFORE UPDATE OF archived ON history_sessions"),
		"title":     broken("BEFORE UPDATE OF title ON history_sessions"),
		"migration": broken("BEFORE INSERT ON history_migrations"),
		// A constraint that is checked at the commit, not at the write.
		"commit": `CREATE TABLE pending(id TEXT REFERENCES history_sessions(id) DEFERRABLE INITIALLY DEFERRED);
			CREATE TRIGGER broken AFTER INSERT ON history_migrations BEGIN INSERT INTO pending VALUES('nobody'); END`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := prepared(t, sql)
			legacy(t, dir, "owner/old.jsonl", `{"seq":1,"answer":1,"role":"user","content":"first"}`+"\n"+`{"seq":2,"answer":1,"role":"end","outcome":"answered"}`+"\n")
			legacy(t, dir, "owner/old.title", "Saved title")
			a := Open(dir)
			defer a.Close()
			if list, err := a.List(); err == nil {
				t.Fatalf("imported: %+v", list)
			}
			repair(t, dir, "DROP TRIGGER broken")
			got, err := a.Records("owner/old")
			if err != nil || len(got) != 2 || got[0].Content != "first" || got[1].Outcome != Answered {
				t.Fatalf("retry: %+v, %v", got, err)
			}
			if title, err := a.Title("owner/old"); err != nil || title != "Saved title" {
				t.Fatalf("title %q, %v", title, err)
			}
		})
	}
}

// A database that holds something else under the names of the archive is not
// taken over.
func TestSQLForeignSchema(t *testing.T) {
	for _, sql := range []string{
		"DROP INDEX history_recent; CREATE TABLE history_recent(x)",
		"DROP TABLE history_migrations; CREATE VIEW history_migrations AS SELECT 1 AS x",
	} {
		a := Open(prepared(t, sql))
		if list, err := a.List(); err == nil {
			t.Errorf("%s: listed %+v", sql, list)
		}
		a.Close()
	}
}

// Legacy files the node may not read fail the import instead of being left
// out of it, and the import is tried again once they can be read.
func TestSQLImportUnreadable(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads every file")
	}
	for _, c := range []struct {
		path string
		mode os.FileMode
	}{
		{"owner", 0o400}, // listed, but what it holds cannot be looked at
		{"owner/old.jsonl", 0},
		{"owner/old.title", 0},
	} {
		t.Run(c.path, func(t *testing.T) {
			dir := t.TempDir()
			legacy(t, dir, "owner/old.jsonl", `{"seq":1,"answer":1,"role":"user","content":"first"}`+"\n")
			legacy(t, dir, "owner/old.title", "Saved title")
			path := filepath.Join(dir, c.path)
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, c.mode); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.Chmod(path, info.Mode().Perm()) })
			a := Open(dir)
			defer a.Close()
			if list, err := a.List(); err == nil {
				t.Fatalf("imported: %+v", list)
			}
			if err := os.Chmod(path, info.Mode().Perm()); err != nil {
				t.Fatal(err)
			}
			if got, err := a.Records("owner/old"); err != nil || len(got) != 1 {
				t.Fatalf("retry: %+v, %v", got, err)
			}
			if title, err := a.Title("owner/old"); err != nil || title != "Saved title" {
				t.Fatalf("title %q, %v", title, err)
			}
		})
	}
}

// An archive whose directory cannot be listed once the database is open
// imports nothing, and says so.
func TestSQLImportUnlisted(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root lists every directory")
	}
	dir := filepath.Join(t.TempDir(), unlisted)
	legacy(t, dir, "owner/old.jsonl", `{"seq":1,"answer":1,"role":"user","content":"first"}`+"\n")
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	a := Open(dir)
	defer a.Close()
	if list, err := a.List(); err == nil {
		t.Fatalf("imported: %+v", list)
	}
}

// A database the archive cannot begin a transaction in is not used.
func TestSQLCannotBegin(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "history")
	a := Open(dir)
	if err := a.Append(owner, question, nil); err != nil {
		t.Fatal(err)
	}
	a.Close()
	// The database is in WAL already, which the hook's transaction keeps
	// it from switching to.
	moved := filepath.Join(root, inTransaction)
	if err := os.Rename(dir, moved); err != nil {
		t.Fatal(err)
	}
	a = Open(moved)
	defer a.Close()
	if _, err := a.List(); err == nil {
		t.Fatal("listed")
	}
	if a.db != nil {
		t.Fatal("the database was kept")
	}
}

// Rows the archive did not write itself fail what reads them, instead of
// being taken for something they are not.
func TestSQLCorruptRows(t *testing.T) {
	a := at(t)
	if err := a.Append(owner, question, nil); err != nil {
		t.Fatal(err)
	}
	exec(t, a, "UPDATE history_records SET data='not JSON' WHERE ordinal=2")
	if _, err := a.Match("roof"); err == nil {
		t.Error("match")
	}
	if _, err := a.Records(Owner(owner.Session)); err == nil || errors.Is(err, ErrNotArchived) {
		t.Errorf("records: %v", err)
	}
	// A record without data, which only a view in its place can give.
	b := at(t)
	if err := b.Append(owner, question, nil); err != nil {
		t.Fatal(err)
	}
	exec(t, b, "CREATE TEMP VIEW history_records AS SELECT session_id,ordinal,NULL AS data FROM main.history_records")
	if _, err := b.Records(Owner(owner.Session)); err == nil || errors.Is(err, ErrNotArchived) {
		t.Errorf("records without data: %v", err)
	}
	c := at(t)
	if err := c.Append(owner, question, nil); err != nil {
		t.Fatal(err)
	}
	exec(t, c, "UPDATE history_sessions SET modified='soon'")
	if _, err := c.List(); err == nil {
		t.Error("list of a time that is no number")
	}
	// The index of the search is gone.
	d := at(t)
	if err := d.Append(owner, question, nil); err != nil {
		t.Fatal(err)
	}
	exec(t, d, "DROP TABLE history_fts")
	if _, _, err := d.Search(owner, "roof", false, 0); err == nil {
		t.Error("search without its index")
	}
}

// A deletion that fails on the way deletes nothing.
func TestDeleteFails(t *testing.T) {
	for name, sql := range map[string]string{
		"archived": broken("BEFORE UPDATE OF deleted ON history_sessions"),
		"metadata": broken("BEFORE UPDATE OF deleted ON history_sessions"),
		"records":  broken("BEFORE DELETE ON history_records"),
		// A constraint that is checked at the commit, not at the write.
		"commit": `CREATE TABLE pending(id TEXT REFERENCES history_sessions(id) DEFERRABLE INITIALLY DEFERRED);
			CREATE TRIGGER broken AFTER DELETE ON history_records BEGIN INSERT INTO pending VALUES('nobody'); END`,
	} {
		t.Run(name, func(t *testing.T) {
			a := at(t)
			if name == "metadata" {
				if err := a.SetTitle(Owner(owner.Session), "Draft"); err != nil {
					t.Fatal(err)
				}
			} else if err := a.Append(owner, question, nil); err != nil {
				t.Fatal(err)
			}
			if err := a.SetTitle(Owner(owner.Session), "Kept"); err != nil {
				t.Fatal(err)
			}
			exec(t, a, sql)
			if err := a.Delete(Owner(owner.Session)); err == nil || errors.Is(err, ErrNotArchived) {
				t.Fatalf("deleted: %v", err)
			}
			if title, err := a.Title(Owner(owner.Session)); err != nil || title != "Kept" {
				t.Fatalf("title %q, %v", title, err)
			}
			if records, _ := a.Records(Owner(owner.Session)); name != "metadata" && len(records) != 2 {
				t.Fatalf("records %+v", records)
			}
		})
	}
}

// Marks to go on that cannot be consumed stay where they are.
func TestGoingOnFails(t *testing.T) {
	for name, sql := range map[string]string{
		"lock":   "ALTER TABLE history_sessions DROP COLUMN goon",
		"select": "DROP INDEX history_recent; ALTER TABLE history_sessions DROP COLUMN deleted",
		// A constraint that is checked at the commit, not at the write.
		"commit": `CREATE TABLE pending(id TEXT REFERENCES history_sessions(id) DEFERRABLE INITIALLY DEFERRED);
			CREATE TRIGGER broken AFTER UPDATE OF goon ON history_sessions BEGIN INSERT INTO pending VALUES('nobody'); END`,
	} {
		t.Run(name, func(t *testing.T) {
			a := at(t)
			if err := a.GoOn("owner/s1"); err != nil {
				t.Fatal(err)
			}
			exec(t, a, sql)
			if ids, err := a.GoingOn(); err == nil || len(ids) != 0 {
				t.Fatalf("going on: %v, %v", ids, err)
			}
			if name != "commit" {
				return
			}
			repair(t, a.dir, "DROP TRIGGER broken")
			if ids, err := a.GoingOn(); err != nil || len(ids) != 1 || ids[0] != "owner/s1" {
				t.Fatalf("the mark was consumed: %v, %v", ids, err)
			}
		})
	}
}

// A kind of metadata the archive does not know is a mistake of the code.
func TestUnknownMetadata(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("no panic")
		}
	}()
	metadataColumn(".unknown")
}

// A first page that ends within a character ends before it, and the next one
// goes on with it.
func TestReadCutsBeforeACharacter(t *testing.T) {
	a := at(t)
	keep := MaxReadBytes - readNote
	// The content of the record starts after its head, and so many x put
	// the cut on the second byte of a smiley.
	head := len(show(Record{Seq: 1, Answer: 1, Role: "user", Time: time.Now()}))
	content := strings.Repeat("x", ((keep-head-1)%4+4)%4) + strings.Repeat("🙂", keep/4)
	if err := a.Append(owner, []openai.Message{{Role: "user", Content: content}}, nil); err != nil {
		t.Fatal(err)
	}
	out, err := a.Read(owner, "", 1, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	text, note, _ := strings.Cut(out, "\n[records 1 to 1 of 2: continue with from 1, offset ")
	if len(text) != keep-1 || note != fmt.Sprint(keep-1)+"]" || !strings.HasSuffix(text, "🙂") {
		t.Fatalf("page of %d bytes, note %q", len(text), note)
	}
}

// A deletion that cannot remove the pending restart deletes nothing.
func TestDeleteFailsAtRestartRemoval(t *testing.T) {
	a := at(t)
	if err := a.Append(owner, question, nil); err != nil {
		t.Fatal(err)
	}
	if err := a.PendingRestart(Owner(owner.Session), "old", "presentation", "binary"); err != nil {
		t.Fatal(err)
	}
	exec(t, a, broken("BEFORE DELETE ON history_restarts"))
	if err := a.Delete(Owner(owner.Session)); err == nil || errors.Is(err, ErrNotArchived) {
		t.Fatalf("deleted: %v", err)
	}
	if records, err := a.Records(Owner(owner.Session)); err != nil || len(records) != 2 {
		t.Fatalf("records %+v, %v", records, err)
	}
}

// A note of the model that cannot begin a transaction of its own is not kept.
func TestNoteModelCannotBegin(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "history")
	a := Open(dir)
	if err := a.SetModel("owner/noted", "m"); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(root, inTransaction)
	if err := os.Rename(dir, moved); err != nil {
		t.Fatal(err)
	}
	a = Open(moved)
	defer a.Close()
	// The hook of the directory begins a transaction on every connection, so
	// the model is still kept, but noting it cannot begin one.
	db, err := storage.Open(moved)
	if err != nil {
		t.Fatal(err)
	}
	adopt(a, db)
	if err := a.NoteModel("owner/noted", "m2"); err == nil {
		t.Fatal("the model was noted")
	}
}
