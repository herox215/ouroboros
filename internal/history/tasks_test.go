package history

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

// A report that ended before the agent took it is kept as such, with how long
// the task ran and where it is routed, and another process takes it once.
func TestTaskReportUntilTaken(t *testing.T) {
	a := at(t)
	took := 2*time.Minute + 3*time.Second
	report := Task{N: 1, Title: "find the callers", Since: since, Report: "found them", Took: took, Routed: true}
	if err := a.SetTaskReport("owner/s1", report, "old"); err != nil {
		t.Fatal(err)
	}
	want := report
	want.Ended = TaskReport
	if got, err := a.Tasks("owner/s1"); err != nil || !reflect.DeepEqual(got, []Task{want}) {
		t.Fatalf("tasks %+v, %v", got, err)
	}
	// The process that produced it keeps it as waiting for the agent, not as a
	// report another image must tell.
	if got, err := a.PendingTaskReports("old"); err != nil || len(got) != 0 {
		t.Fatalf("pending for its own process: %+v, %v", got, err)
	}
	pending, err := a.PendingTaskReports("new")
	if err != nil || !reflect.DeepEqual(pending, map[string][]Task{"owner/s1": {want}}) {
		t.Fatalf("pending %+v, %v", pending, err)
	}
	// Another image never takes a report that waits for the agent as lost.
	if lost, err := a.RestartedTasks("new"); err != nil || len(lost) != 0 {
		t.Fatalf("restarted %+v, %v", lost, err)
	}
	if err := a.TaskReportTaken("owner/s1", 1); err != nil {
		t.Fatal(err)
	}
	if err := a.TaskReportTaken("owner/s1", 1); err != nil {
		t.Fatalf("taken twice: %v", err)
	}
	if got, err := a.Tasks("owner/s1"); err != nil || len(got) != 1 || got[0].Ended != TaskEnded || got[0].Report != "" {
		t.Fatalf("tasks %+v, %v", got, err)
	}
	if pending, err := a.PendingTaskReports("new"); err != nil || len(pending) != 0 {
		t.Fatalf("told twice: %+v, %v", pending, err)
	}
	// A report that does not wait is left as it is.
	if err := a.TaskReportTaken("owner/s1", 2); err != nil {
		t.Fatal(err)
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
	if err := a.SetTaskReport("owner/s1", Task{N: 1}, ""); err == nil {
		t.Error("report of no process")
	}
	if _, err := a.PendingTaskReports(""); err == nil {
		t.Error("pending reports of no process")
	}
	if err := a.SetTaskReport("nonsense", Task{N: 1}, "p"); err == nil {
		t.Error("report of no conversation")
	}
	if err := a.TaskReportTaken("nonsense", 1); err == nil {
		t.Error("report taken of no conversation")
	}
	if got, err := a.Tasks("owner/s1"); err != nil || got != nil {
		t.Errorf("tasks of no archive: %+v, %v", got, err)
	}
	if got, err := a.RestartedTasks("p"); err != nil || got != nil {
		t.Errorf("restarted tasks of no archive: %+v, %v", got, err)
	}
	if got, err := a.PendingTaskReports("p"); err != nil || got != nil {
		t.Errorf("pending reports of no archive: %+v, %v", got, err)
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
	if err := a.SetTaskReport("owner/s1", Task{N: 1, Report: "r"}, "p"); err == nil {
		t.Error("set report")
	}
	if _, err := a.PendingTaskReports("p"); err == nil {
		t.Error("pending reports")
	}
	if err := a.TaskReportTaken("owner/s1", 1); err == nil {
		t.Error("report taken")
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
	if err := a.SetTaskReport("owner/s1", Task{N: 1}, "p"); err == nil {
		t.Error("set task report")
	}
}

// Each step of keeping a task report can fail, and then nothing is kept.
func TestSetTaskReportFails(t *testing.T) {
	for name, c := range map[string]struct {
		sql string
		id  string
	}{
		"session": {sql: broken("BEFORE INSERT ON history_sessions"), id: "owner/new"},
		"task":    {sql: broken("BEFORE INSERT ON history_tasks")},
		"commit": {sql: `CREATE TABLE pending(id TEXT REFERENCES history_sessions(id) DEFERRABLE INITIALLY DEFERRED);
			CREATE TRIGGER broken AFTER INSERT ON history_tasks BEGIN INSERT INTO pending VALUES('nobody'); END`},
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
			if err := a.SetTaskReport(id, Task{N: 2, Title: "t", Since: since, Report: "found"}, "p"); err == nil {
				t.Fatal("the report was kept")
			}
			if name != "session" {
				if got, _ := a.Tasks("owner/s1"); len(got) != 1 {
					t.Fatalf("tasks %+v", got)
				}
			}
		})
	}
}

// A report that cannot be marked as taken stays for the next start.
func TestTaskReportTakenFails(t *testing.T) {
	a := at(t)
	if err := a.SetTaskReport("owner/s1", Task{N: 1, Title: "t", Since: since, Report: "found"}, "old"); err != nil {
		t.Fatal(err)
	}
	exec(t, a, broken("BEFORE UPDATE OF ended ON history_tasks"))
	if err := a.TaskReportTaken("owner/s1", 1); err == nil {
		t.Fatal("the report was taken")
	}
	repair(t, a.dir, "DROP TRIGGER broken")
	if err := a.TaskReportTaken("owner/s1", 1); err != nil {
		t.Fatal(err)
	}
	if got, err := a.Tasks("owner/s1"); err != nil || got[0].Ended != TaskEnded {
		t.Fatalf("tasks %+v, %v", got, err)
	}
}

// A report list that cannot be read fails instead of being empty.
func TestPendingTaskReportsFails(t *testing.T) {
	for name, sql := range map[string]string{
		"select": "ALTER TABLE history_tasks DROP COLUMN report",
		"scan":   "UPDATE history_tasks SET took='soon'",
		"rows":   failingRows,
	} {
		t.Run(name, func(t *testing.T) {
			a := at(t)
			if err := a.SetTaskReport("owner/s1", Task{N: 1, Title: "t", Since: since, Report: "found"}, "old"); err != nil {
				t.Fatal(err)
			}
			exec(t, a, sql)
			if got, err := a.PendingTaskReports("new"); err == nil {
				t.Fatalf("reports %+v", got)
			}
		})
	}
}

// A task row the archive did not write fails what reads it.
const (
	// A number that is no number.
	badNumber = "INSERT INTO history_tasks(session_id,n,title,since) VALUES('owner/s1','first','t',0)"
	// A table whose second row fails once it is read, past the first one.
	failingRows = "INSERT INTO history_tasks(session_id,n,title,since) VALUES('owner/s1',2,'t',0);" +
		"CREATE TEMP VIEW history_tasks AS SELECT session_id,n,title,since,CASE WHEN n>1 THEN json(title) ELSE ended END AS ended,process_id,report,took,routed FROM main.history_tasks"
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

// A database from before a task's report was kept gets its columns on the
// next open.
func TestTasksAddReportColumns(t *testing.T) {
	dir := prepared(t, "ALTER TABLE history_tasks DROP COLUMN report;"+
		"ALTER TABLE history_tasks DROP COLUMN took;ALTER TABLE history_tasks DROP COLUMN routed")
	a := Open(dir)
	defer a.Close()
	if err := a.SetTasks("owner/s1", []Task{{N: 1, Title: "t", Since: since}}, "old"); err != nil {
		t.Fatal(err)
	}
	report := Task{N: 1, Title: "t", Since: since, Report: "found it", Took: time.Second, Routed: true}
	if err := a.SetTaskReport("owner/s1", report, "old"); err != nil {
		t.Fatal(err)
	}
	want := report
	want.Ended = TaskReport
	if got, err := a.Tasks("owner/s1"); err != nil || !reflect.DeepEqual(got, []Task{want}) {
		t.Fatalf("tasks %+v, %v", got, err)
	}
}

// A report column that cannot be added fails the open, rather than leaving an
// archive without it.
func TestTasksCannotAddReportColumns(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "history")
	db, err := storage.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	columns := []string{"session_id TEXT NOT NULL", "n INTEGER NOT NULL", "title TEXT NOT NULL", "since INTEGER NOT NULL",
		"ended TEXT NOT NULL DEFAULT ''", "process_id TEXT NOT NULL DEFAULT ''"}
	for i := 0; i < 1994; i++ {
		columns = append(columns, fmt.Sprintf("c%d TEXT", i))
	}
	if _, err := db.Exec("CREATE TABLE history_tasks(" + strings.Join(columns, ",") + ")"); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	a := Open(dir)
	defer a.Close()
	if _, err := a.Tasks("owner/s1"); err == nil || !strings.Contains(err.Error(), "too many columns") {
		t.Fatalf("opened: %v", err)
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
