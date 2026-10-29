package history

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"ouroboros/internal/openai"
)

func TestDurableRestartCompletion(t *testing.T) {
	dir := t.TempDir()
	a := Open(dir)
	t.Cleanup(func() { a.Close() })
	if err := a.Append(Who{Session: "trigger"}, []openai.Message{{Role: "user", Content: "build"}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := a.PendingRestart("owner/trigger", "old-image", "restart-id", "/new/ouro"); err != nil {
		t.Fatal(err)
	}
	if got, err := a.CompleteRestarts("old-image"); err != nil || len(got) != 0 {
		t.Fatalf("same process completed: %v, %v", got, err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	a = Open(dir)
	if got, err := a.CompleteRestarts("old-image"); err != nil || len(got) != 0 {
		t.Fatalf("reopen completed: %v, %v", got, err)
	}
	completed, err := a.CompleteRestarts("new-image")
	if err != nil {
		t.Fatal(err)
	}
	r := completed["owner/trigger"]
	if len(completed) != 1 || r.Role != Restarted || r.PresentationID != "restart-id" || r.Content != "/new/ouro" || r.Seq != 3 || r.Answer != 1 {
		t.Fatalf("completion: %+v", completed)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	a = Open(dir)
	for _, process := range []string{"new-image", "third-image"} {
		if got, err := a.CompleteRestarts(process); err != nil || len(got) != 0 {
			t.Fatalf("duplicate: %v, %v", got, err)
		}
	}
	records, err := a.Records("owner/trigger")
	if err != nil || len(records) != 3 || !reflect.DeepEqual(records[2], r) {
		t.Fatalf("records: %+v, %v", records, err)
	}
	if err := a.Append(Who{Session: "trigger"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	records, err = a.Records("owner/trigger")
	if err != nil || records[3].Seq != 4 || records[3].Answer != 2 {
		t.Fatalf("append after completion: %+v, %v", records, err)
	}
}

func TestRestartCompletionRollbackKeepsIntent(t *testing.T) {
	a := Open(t.TempDir())
	defer a.Close()
	if err := a.PendingRestart("owner/trigger", "old", "stable-id", "binary"); err != nil {
		t.Fatal(err)
	}
	// Fail after inserting the completion, at the final consumption step.
	exec(t, a, "CREATE TRIGGER reject_restart_consumption BEFORE DELETE ON history_restarts BEGIN SELECT RAISE(ABORT,'test failure'); END")
	if _, err := a.CompleteRestarts("new"); err == nil {
		t.Fatal("completion ignored failure")
	}
	exec(t, a, "DROP TRIGGER reject_restart_consumption")
	got, err := a.CompleteRestarts("new")
	if err != nil || len(got) != 1 {
		t.Fatalf("intent lost: %v, %v", got, err)
	}
	records, err := a.Records("owner/trigger")
	if err != nil || len(records) != 1 || records[0].PresentationID != "stable-id" {
		t.Fatalf("duplicate or missing completion: %+v, %v", records, err)
	}
}

func TestDeletedRestartDoesNotComplete(t *testing.T) {
	a := Open(t.TempDir())
	defer a.Close()
	if err := a.PendingRestart("owner/deleted", "old", "id", "binary"); err != nil {
		t.Fatal(err)
	}
	if err := a.Delete("owner/deleted"); err != nil && !errors.Is(err, ErrNotArchived) {
		t.Fatal(err)
	}
	if got, err := a.CompleteRestarts("new"); err != nil || len(got) != 0 {
		t.Fatalf("deleted completed: %v, %v", got, err)
	}
	if err := a.PendingRestart("peer-key/session", "old", "id", "binary"); err == nil {
		t.Fatal("peer restart accepted")
	}
}

// A pending restart needs the identities of its owner session, its process
// and its presentation: without them no completion could be published.
func TestPendingRestartRequiresIdentities(t *testing.T) {
	a := at(t)
	for _, c := range []struct{ id, process, presentation string }{
		{Owner("trigger"), "", "presentation"},
		{Owner("trigger"), "process", ""},
		{"peer-0123456789abcdef0123456789abcdef/trigger", "process", "presentation"},
	} {
		if err := a.PendingRestart(c.id, c.process, c.presentation, "/new/ouro"); err == nil {
			t.Errorf("%+v accepted", c)
		}
	}
}

// A restart mark that cannot be kept completely is not kept at all.
func TestPendingRestartFailsAtEveryWrite(t *testing.T) {
	for name, c := range map[string]struct {
		closed bool
		sql    string
	}{
		"transaction": {closed: true},
		"session":     {sql: broken("BEFORE INSERT ON history_sessions")},
		"restart":     {sql: broken("BEFORE INSERT ON history_restarts")},
	} {
		t.Run(name, func(t *testing.T) {
			a := at(t)
			if c.closed {
				closeBehind(t, a)
			} else {
				exec(t, a, c.sql)
			}
			if err := a.PendingRestart("owner/trigger", "process", "presentation", "/new/ouro"); err == nil {
				t.Fatal("the mark was kept")
			}
			if c.closed {
				return
			}
			if list, err := a.List(); err != nil || len(list) != 0 {
				t.Fatalf("a failed mark left %+v, %v", list, err)
			}
		})
	}
}

// A completion needs the identity of the process that consumes the marks.
func TestCompleteRestartsRequiresProcess(t *testing.T) {
	a := at(t)
	if got, err := a.CompleteRestarts(""); err == nil || got != nil {
		t.Fatalf("completion without a process: %v, %v", got, err)
	}
}

// A completion that cannot be recorded completely is not recorded at all,
// whichever of its steps fails.
func TestCompleteRestartsFailsAtEveryStep(t *testing.T) {
	for name, prepare := range map[string]func(t *testing.T, a *Archive){
		"transaction": closeBehind,
		"schema": func(t *testing.T, a *Archive) {
			exec(t, a, "DROP TABLE history_restarts")
		},
		"select": func(t *testing.T, a *Archive) {
			exec(t, a, "DROP INDEX history_recent; ALTER TABLE history_sessions DROP COLUMN deleted")
		},
		"scan": func(t *testing.T, a *Archive) {
			// The presentation of a pending mark is missing.
			exec(t, a, "CREATE TEMP TABLE history_restarts(session_id,process_id,presentation_id,binary); INSERT INTO temp.history_restarts VALUES('owner/trigger','old',NULL,'binary')")
		},
		"rows": func(t *testing.T, a *Archive) {
			// The mark cannot be read to its end. The update has to be
			// accepted before the query is stepped.
			exec(t, a, `CREATE TEMP VIEW history_restarts AS SELECT session_id,presentation_id,CASE WHEN session_id LIKE 'owner/%' THEN abs(-9223372036854775808) ELSE binary END AS binary,process_id FROM main.history_restarts;
				CREATE TEMP TRIGGER no_update INSTEAD OF UPDATE ON history_restarts BEGIN SELECT 1; END`)
		},
		"ordinal": func(t *testing.T, a *Archive) {
			// The ordinal of the completion is no whole number.
			exec(t, a, "CREATE TEMP TABLE history_records(session_id,seq,answer,ordinal); INSERT INTO temp.history_records VALUES('owner/trigger',0.5,0.5,0.5)")
		},
		"time": func(t *testing.T, a *Archive) {
			a.now = func() time.Time { return time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC) }
		},
		"record": func(t *testing.T, a *Archive) {
			exec(t, a, broken("BEFORE INSERT ON history_records"))
		},
		"size": func(t *testing.T, a *Archive) {
			exec(t, a, broken("BEFORE UPDATE OF archived ON history_sessions"))
		},
		"commit": func(t *testing.T, a *Archive) {
			// A constraint that is checked at the commit, not at the write.
			exec(t, a, `CREATE TABLE pending(id TEXT REFERENCES history_sessions(id) DEFERRABLE INITIALLY DEFERRED);
				CREATE TRIGGER broken AFTER DELETE ON history_restarts BEGIN INSERT INTO pending VALUES('nobody'); END`)
		},
	} {
		t.Run(name, func(t *testing.T) {
			a := at(t)
			if err := a.PendingRestart("owner/trigger", "old", "presentation", "/new/ouro"); err != nil {
				t.Fatal(err)
			}
			prepare(t, a)
			if completed, err := a.CompleteRestarts("new"); err == nil || completed != nil {
				t.Fatalf("completion: %+v, %v", completed, err)
			}
			if name != "commit" {
				return
			}
			exec(t, a, "DROP TRIGGER broken")
			got, err := a.CompleteRestarts("new")
			if err != nil || len(got) != 1 {
				t.Fatalf("intent lost after a failed commit: %v, %v", got, err)
			}
		})
	}
}

// A mark that cannot reach the archive database at all is reported, not kept.
func TestPendingRestartReportsAnUnopenableArchive(t *testing.T) {
	base := t.TempDir()
	if err := os.Chmod(base, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(base, 0o755) })
	a := Open(filepath.Join(base, "history"))
	defer a.Close()
	if err := a.PendingRestart("owner/trigger", "process", "presentation", "/new/ouro"); err == nil {
		t.Fatal("the mark was kept")
	}
}

// A completion reports an archive directory it cannot look at.
func TestCompleteRestartsReportsAnUnreadableDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "history")
	if err := os.Symlink(filepath.Join(dir, "gone"), dir); err != nil {
		t.Fatal(err)
	}
	a := Open(dir)
	defer a.Close()
	if completed, err := a.CompleteRestarts("new"); err == nil || completed != nil {
		t.Fatalf("completion: %+v, %v", completed, err)
	}
}

// A completion that cannot read the pending marks to their end records
// nothing.
func TestCompleteRestartsReportsAFailingRead(t *testing.T) {
	a := at(t)
	if err := a.PendingRestart("owner/trigger", "old", "presentation", "/new/ouro"); err != nil {
		t.Fatal(err)
	}
	// The select of the marks is stepped and fails after the update.
	exec(t, a, `CREATE TEMP VIEW history_restarts AS SELECT session_id, presentation_id, (SELECT value FROM json_each('{broken')) AS binary, process_id FROM main.history_restarts;
		CREATE TEMP TRIGGER no_update INSTEAD OF UPDATE ON history_restarts BEGIN SELECT 1; END`)
	if completed, err := a.CompleteRestarts("new"); err == nil || completed != nil {
		t.Fatalf("completion: %+v, %v", completed, err)
	}
}

// A completion whose read fails after some marks records none of them.
func TestCompleteRestartsReportsAReadThatFailsPartway(t *testing.T) {
	a := at(t)
	if err := a.PendingRestart("owner/trigger", "old", "presentation", "/new/ouro"); err != nil {
		t.Fatal(err)
	}
	// The first mark reads, the second raises an error while stepping.
	exec(t, a, `CREATE TEMP VIEW history_restarts AS
		SELECT session_id, presentation_id, binary, process_id FROM main.history_restarts
		UNION ALL SELECT 'owner/zzz', 'presentation', abs(-9223372036854775808), 'old' FROM (SELECT 1);
		CREATE TEMP TRIGGER no_update INSTEAD OF UPDATE ON history_restarts BEGIN SELECT 1; END`)
	if completed, err := a.CompleteRestarts("new"); err == nil || completed != nil {
		t.Fatalf("completion: %+v, %v", completed, err)
	}
}

// A completed restart is counted in the size of the conversation it closes.
func TestCompletedRestartGrowsTheSize(t *testing.T) {
	a := at(t)
	if err := a.PendingRestart("owner/trigger", "old", "presentation", "/new/ouro"); err != nil {
		t.Fatal(err)
	}
	got, err := a.CompleteRestarts("new")
	if err != nil || len(got) != 1 {
		t.Fatalf("completion: %v, %v", got, err)
	}
	b, err := json.Marshal(got["owner/trigger"])
	if err != nil {
		t.Fatal(err)
	}
	if entries, err := a.List(); err != nil || len(entries) != 1 || entries[0].Size != int64(len(b)+1) {
		t.Fatalf("entries %+v, %v; want a size of %d", entries, err, len(b)+1)
	}
}

// Nothing is pending in an archive that does not exist, which needs no
// database to say so.
func TestCompleteRestartsWithoutAnArchive(t *testing.T) {
	a := Open(filepath.Join(t.TempDir(), "missing"))
	defer a.Close()
	if got, err := a.CompleteRestarts("new"); err != nil || got != nil {
		t.Fatalf("completion: %v, %v", got, err)
	}
}
