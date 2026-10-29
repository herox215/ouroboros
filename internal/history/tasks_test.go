package history

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"ouroboros/internal/storage"
)

var since = time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)

// The tasks of a conversation run until they are no longer named. A task
// that ran in another process is taken once as restarted.
func TestTasks(t *testing.T) {
	a := at(t)
	if err := a.SetTasks("owner/s1", []Task{{N: 1, Title: "find the callers", Since: since}, {N: 2, Title: "run the tests", Since: since}}, "old"); err != nil {
		t.Fatal(err)
	}
	if err := a.SetTasks("owner/s1", []Task{{N: 2, Title: "run all tests", Since: since}}, "old"); err != nil {
		t.Fatal(err)
	}
	got, err := a.Tasks("owner/s1")
	want := []Task{{N: 1, Title: "find the callers", Since: since, Ended: TaskEnded}, {N: 2, Title: "run all tests", Since: since}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("tasks %+v, %v", got, err)
	}
	if err := a.SetTasks("owner/s2", []Task{{N: 1, Title: "mine", Since: since}}, "new"); err != nil {
		t.Fatal(err)
	}
	lost, err := a.RestartedTasks("new")
	if err != nil || !reflect.DeepEqual(lost, map[string][]Task{"owner/s1": {{N: 2, Title: "run all tests", Since: since}}}) {
		t.Fatalf("restarted %+v, %v", lost, err)
	}
	if got, _ := a.Tasks("owner/s1"); got[1].Ended != TaskRestart {
		t.Fatalf("not ended as restarted: %+v", got)
	}
	if lost, err := a.RestartedTasks("new"); err != nil || len(lost) != 0 {
		t.Fatalf("taken twice: %+v, %v", lost, err)
	}
	if got := TaskLines(want); got != "task 1 · find the callers\ntask 2 · run all tests" {
		t.Fatalf("lines %q", got)
	}
}

// Tasks need a conversation and a process, and an archive that is not there
// has none.
func TestTasksNeedIdentity(t *testing.T) {
	a := Open(filepath.Join(t.TempDir(), "missing"))
	if err := a.SetTasks("nonsense", nil, "p"); err == nil {
		t.Error("tasks of no conversation")
	}
	if err := a.SetTasks("owner/s1", nil, ""); err == nil {
		t.Error("tasks of no process")
	}
	if _, err := a.Tasks("nonsense"); err == nil {
		t.Error("read tasks of no conversation")
	}
	if _, err := a.RestartedTasks(""); err == nil {
		t.Error("restarted tasks of no process")
	}
	if got, err := a.Tasks("owner/s1"); err != nil || got != nil {
		t.Errorf("tasks of no archive: %+v, %v", got, err)
	}
	if got, err := a.RestartedTasks("p"); err != nil || got != nil {
		t.Errorf("restarted tasks of no archive: %+v, %v", got, err)
	}
	if _, err := os.Stat(a.dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("reading created the archive: %v", err)
	}
}

// An archive whose directory is a file fails everything that reaches its
// database.
func TestArchiveInAFile(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	a := Open(file)
	if err := a.SetTasks("owner/s1", nil, "p"); err == nil {
		t.Error("set tasks")
	}
	if _, err := a.Tasks("owner/s1"); err == nil {
		t.Error("tasks")
	}
	if _, err := a.RestartedTasks("p"); err == nil {
		t.Error("restarted tasks")
	}
	if err := a.BeginAnswer("owner/s1"); err == nil {
		t.Error("begin answer")
	}
	if err := a.EndAnswer("owner/s1"); err == nil {
		t.Error("end answer")
	}
	if _, err := a.Interrupted(); err == nil {
		t.Error("interrupted")
	}
}

// cannotBegin returns an archive with a conversation and a task, whose
// database cannot begin a transaction of its own.
func cannotBegin(t *testing.T) *Archive {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "history")
	a := Open(dir)
	if err := a.SetTasks("owner/s1", []Task{{N: 1, Title: "t", Since: since}}, "old"); err != nil {
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
	t.Cleanup(func() { a.Close() })
	db, err := storage.Open(moved)
	if err != nil {
		t.Fatal(err)
	}
	adopt(a, db)
	return a
}

// What needs a transaction fails without one.
func TestTasksCannotBegin(t *testing.T) {
	a := cannotBegin(t)
	if err := a.SetTasks("owner/s1", nil, "p"); err == nil {
		t.Error("set tasks")
	}
	if _, err := a.RestartedTasks("p"); err == nil {
		t.Error("restarted tasks")
	}
	if _, err := a.Interrupted(); err == nil {
		t.Error("interrupted")
	}
}

// A task row the archive did not write fails what reads it.
const (
	// A number that is no number.
	badNumber = "INSERT INTO history_tasks(session_id,n,title,since) VALUES('owner/s1','first','t',0)"
	// A table whose second row fails once it is read, past the first one.
	failingRows = "INSERT INTO history_tasks(session_id,n,title,since) VALUES('owner/s1',2,'t',0);" +
		"CREATE TEMP VIEW history_tasks AS SELECT session_id,n,title,since,CASE WHEN n>1 THEN json(title) ELSE ended END AS ended,process_id FROM main.history_tasks"
)

// Each step of keeping the tasks can fail, and then nothing is kept.
func TestSetTasksFails(t *testing.T) {
	for name, c := range map[string]struct {
		sql     string
		id      string
		running []Task
	}{
		"session": {sql: broken("BEFORE INSERT ON history_sessions"), id: "owner/new"},
		"task":    {sql: broken("BEFORE INSERT ON history_tasks"), running: []Task{{N: 3, Title: "t", Since: since}}},
		"select":  {sql: "ALTER TABLE history_tasks DROP COLUMN ended"},
		"scan":    {sql: badNumber},
		"rows":    {sql: failingRows},
		"end":     {sql: broken("BEFORE UPDATE OF ended ON history_tasks")},
	} {
		t.Run(name, func(t *testing.T) {
			a := at(t)
			if err := a.SetTasks("owner/s1", []Task{{N: 1, Title: "t", Since: since}}, "p"); err != nil {
				t.Fatal(err)
			}
			exec(t, a, c.sql)
			id := c.id
			if id == "" {
				id = "owner/s1"
			}
			if err := a.SetTasks(id, c.running, "p"); err == nil {
				t.Fatal("the tasks were kept")
			}
			if name != "end" {
				return
			}
			if got, _ := a.Tasks("owner/s1"); len(got) != 1 || got[0].Ended != TaskRunning {
				t.Fatalf("tasks %+v", got)
			}
		})
	}
}

// A task list that cannot be read fails instead of being empty.
func TestReadTasksFails(t *testing.T) {
	for name, sql := range map[string]string{
		"select": "ALTER TABLE history_tasks DROP COLUMN ended",
		"scan":   badNumber,
		"rows":   failingRows,
	} {
		t.Run(name, func(t *testing.T) {
			a := at(t)
			if err := a.SetTasks("owner/s1", []Task{{N: 1, Title: "t", Since: since}}, "p"); err != nil {
				t.Fatal(err)
			}
			exec(t, a, sql)
			if got, err := a.Tasks("owner/s1"); err == nil {
				t.Fatalf("tasks %+v", got)
			}
		})
	}
}

// Restarted tasks that cannot be taken stay where they are.
func TestRestartedTasksFails(t *testing.T) {
	for name, sql := range map[string]string{
		"lock":   "ALTER TABLE history_sessions DROP COLUMN goon",
		"select": "ALTER TABLE history_tasks DROP COLUMN process_id",
		"scan":   badNumber,
		"rows":   failingRows,
		"end":    broken("BEFORE UPDATE OF ended ON history_tasks"),
		// A constraint that is checked at the commit, not at the write.
		"commit": `CREATE TABLE pending(id TEXT REFERENCES history_sessions(id) DEFERRABLE INITIALLY DEFERRED);
			CREATE TRIGGER broken AFTER UPDATE OF ended ON history_tasks BEGIN INSERT INTO pending VALUES('nobody'); END`,
	} {
		t.Run(name, func(t *testing.T) {
			a := at(t)
			if err := a.SetTasks("owner/s1", []Task{{N: 1, Title: "t", Since: since}}, "old"); err != nil {
				t.Fatal(err)
			}
			exec(t, a, sql)
			if lost, err := a.RestartedTasks("new"); err == nil {
				t.Fatalf("taken %+v", lost)
			}
			if name != "end" && name != "commit" {
				return
			}
			repair(t, a.dir, "DROP TRIGGER broken")
			if lost, err := a.RestartedTasks("new"); err != nil || len(lost["owner/s1"]) != 1 {
				t.Fatalf("the tasks were taken: %+v, %v", lost, err)
			}
		})
	}
}

// Marks of answers that cannot be consumed stay where they are.
func TestInterruptedFails(t *testing.T) {
	for name, sql := range map[string]string{
		"lock":   "ALTER TABLE history_sessions DROP COLUMN answering",
		"select": "DROP INDEX history_recent; ALTER TABLE history_sessions DROP COLUMN deleted",
		// A constraint that is checked at the commit, not at the write.
		"commit": `CREATE TABLE pending(id TEXT REFERENCES history_sessions(id) DEFERRABLE INITIALLY DEFERRED);
			CREATE TRIGGER broken AFTER UPDATE OF answering ON history_sessions BEGIN INSERT INTO pending VALUES('nobody'); END`,
	} {
		t.Run(name, func(t *testing.T) {
			a := at(t)
			if err := a.BeginAnswer("owner/s1"); err != nil {
				t.Fatal(err)
			}
			exec(t, a, sql)
			if ids, err := a.Interrupted(); err == nil {
				t.Fatalf("interrupted %v", ids)
			}
			if name != "commit" {
				return
			}
			repair(t, a.dir, "DROP TRIGGER broken")
			if ids, err := a.Interrupted(); err != nil || len(ids) != 1 {
				t.Fatalf("the mark was consumed: %v, %v", ids, err)
			}
		})
	}
}

// A deletion that cannot remove the tasks deletes nothing.
func TestDeleteFailsAtTaskRemoval(t *testing.T) {
	a := at(t)
	if err := a.Append(owner, question, nil); err != nil {
		t.Fatal(err)
	}
	if err := a.SetTasks(Owner(owner.Session), []Task{{N: 1, Title: "t", Since: since}}, "p"); err != nil {
		t.Fatal(err)
	}
	exec(t, a, broken("BEFORE DELETE ON history_tasks"))
	if err := a.Delete(Owner(owner.Session)); err == nil || errors.Is(err, ErrNotArchived) {
		t.Fatalf("deleted: %v", err)
	}
	if got, _ := a.Tasks(Owner(owner.Session)); len(got) != 1 {
		t.Fatalf("tasks %+v", got)
	}
}

// A first record the archive did not write fails FirstUser.
func TestFirstUserOfCorruptRecord(t *testing.T) {
	a := at(t)
	if err := a.Append(owner, question, nil); err != nil {
		t.Fatal(err)
	}
	exec(t, a, "UPDATE history_records SET data='not JSON' WHERE ordinal=1")
	if _, err := a.FirstUser(Owner(owner.Session)); err == nil || errors.Is(err, ErrNotArchived) {
		t.Fatalf("first user: %v", err)
	}
}
