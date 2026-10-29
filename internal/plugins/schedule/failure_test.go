package schedule

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	store "ouroboros/internal/schedule"
	"ouroboros/internal/session"
	"ouroboros/internal/workdir"
)

// logged collects the reports of a scheduler.
type logged struct {
	mu    sync.Mutex
	lines []string
}

func (l *logged) log(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}
func (l *logged) has(part string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, line := range l.lines {
		if strings.Contains(line, part) {
			return true
		}
	}
	return false
}

// A tick reports a store it cannot open or claim from and tries again a
// minute later. A canceled tick does nothing.
func TestTickReportsStoreFailures(t *testing.T) {
	s, _, _ := fixture(t)
	l := &logged{}
	s.log = l.log
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if d := s.tick(canceled); d != time.Minute || s.store != nil {
		t.Fatalf("canceled tick ran: %s", d)
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	path := s.path
	s.path = filepath.Join(file, "schedule.json")
	if d := s.tick(context.Background()); d != time.Minute || !l.has("schedule: open:") {
		t.Fatalf("open failure: %s %q", d, l.lines)
	}
	s.path = path
	create(t, s, "", time.Hour)
	if err := os.WriteFile(path, []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if d := s.tick(context.Background()); d != time.Minute || !l.has("schedule: claim:") {
		t.Fatalf("claim failure: %s %q", d, l.lines)
	}
}

// A failed notification about a missed job is reported, not lost.
func TestMissedNotificationFailureIsReported(t *testing.T) {
	s, f, clock := fixture(t)
	l := &logged{}
	s.log = l.log
	f.err = errors.New("no thread")
	job := create(t, s, "", time.Minute)
	clock.Store(job.Next.Add(25 * time.Hour).UnixNano())
	s.tick(context.Background())
	if !l.has("notify missed " + job.ID + ": no thread") {
		t.Fatalf("missing report: %q", l.lines)
	}
}

// A job due in less than a millisecond still waits one millisecond.
func TestTickWaitsAtLeastOneMillisecond(t *testing.T) {
	s, _, clock := fixture(t)
	job := create(t, s, "", time.Minute)
	clock.Store(job.Next.Add(-time.Microsecond).UnixNano())
	if d := s.tick(context.Background()); d != time.Millisecond {
		t.Fatalf("delay %s", d)
	}
}

// The timer waits for the next job that is not paused, whatever comes
// before it.
func TestTickWaitsForTheNextJobNotPaused(t *testing.T) {
	s, _, _ := fixture(t)
	paused := create(t, s, "", 10*time.Minute)
	if _, err := s.store.Update(paused.ID, func(j *store.Job) error { j.Paused = true; return nil }, s.now()); err != nil {
		t.Fatal(err)
	}
	create(t, s, "", 30*time.Minute)
	if d := s.tick(context.Background()); d != 30*time.Minute {
		t.Fatalf("delay %s", d)
	}
}

// A job removed while it runs cannot take its result. The scheduler reports
// that.
func TestRunOfRemovedJobIsReported(t *testing.T) {
	s, f, clock := fixture(t)
	l := &logged{}
	s.log = l.log
	job := create(t, s, "", time.Minute)
	clock.Store(job.Next.UnixNano())
	s.tick(context.Background())
	c := receive(t, f)
	if err := s.store.Remove(job.ID); err != nil {
		t.Fatal(err)
	}
	c.result <- session.ScheduledResult{Text: "done"}
	s.wg.Wait()
	if !l.has("finish " + job.ID) {
		t.Fatalf("missing report: %q", l.lines)
	}
}

// Several changes before the loop wakes up collapse into one wakeup.
func TestChangesCollapseIntoOneWakeup(t *testing.T) {
	s, _, _ := fixture(t)
	s.wake = make(chan struct{}, 1)
	s.changed()
	s.changed()
	if len(s.wake) != 1 {
		t.Fatalf("%d wakeups", len(s.wake))
	}
}

// The tools refuse to work once the scheduler is closed, and report a store
// they cannot open.
func TestToolsNeedAnOpenStore(t *testing.T) {
	s, _, _ := fixture(t)
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	s.path = filepath.Join(file, "schedule.json")
	for name, raw := range map[string]string{Read: `{}`, Update: `{"action":"remove","id":"x"}`} {
		if out := call(t, s, context.Background(), name, raw); !strings.HasPrefix(out, "error:") {
			t.Fatalf("%s without store: %s", name, out)
		}
	}
	s.close()
	if out := call(t, s, context.Background(), Read, `{}`); !strings.Contains(out, "scheduler is closed") {
		t.Fatal(out)
	}
}

// Create refuses a job when the machine has no zone, and refuses a project
// that is not an absolute path. Removing an unknown job fails.
func TestUpdateRefusals(t *testing.T) {
	s, _, _ := fixture(t)
	const create = `{"action":"create","name":"n","text":"t","cron":"0 9 * * *"}`
	if out := call(t, s, workdir.With(context.Background(), "relative"), Update, create); !strings.Contains(out, "absolute") {
		t.Fatal(out)
	}
	if out := call(t, s, context.Background(), Update, `{"action":"remove","id":"absent"}`); !strings.Contains(out, store.ErrNotFound.Error()) {
		t.Fatal(out)
	}
	old := machineZone
	t.Cleanup(func() { machineZone = old })
	machineZone = func() (string, error) { return "", errors.New("no machine zone") }
	if out := call(t, s, context.Background(), Update, create); !strings.Contains(out, "no machine zone") {
		t.Fatal(out)
	}
}

// Each field alone is a change, and a name may have 128 bytes; an update of
// nothing, and a removal that names a field, are refused.
func TestUpdateFieldsAndTheirBounds(t *testing.T) {
	s, _, _ := fixture(t)
	job := create(t, s, "0 9 * * *", 0)
	ctx := context.Background()
	for _, args := range []string{`"name":"` + strings.Repeat("n", 128) + `"`, `"ask":"thread"`} {
		if out := call(t, s, ctx, Update, `{"action":"update","id":"`+job.ID+`",`+args+`}`); strings.HasPrefix(out, "error:") {
			t.Fatalf("%.40s: %s", args, out)
		}
	}
	if out := call(t, s, ctx, Update, `{"action":"update","id":"`+job.ID+`"}`); !strings.Contains(out, "at least one changed field") {
		t.Fatal(out)
	}
	if out := call(t, s, ctx, Update, `{"action":"remove","id":"`+job.ID+`","name":"n"}`); !strings.Contains(out, "remove requires only action and id") {
		t.Fatal(out)
	}
	if _, err := s.store.Get(job.ID); err != nil {
		t.Fatal(err)
	}
}

// Arguments must be one small, well formed object, and each field keeps its
// limits.
func TestMalformedArguments(t *testing.T) {
	s, _, _ := fixture(t)
	for _, raw := range []string{
		`{"name":"` + strings.Repeat("x", 16*1024) + `"}`,
		`{"action":"update","id":"x","at":"2030-01-01T00:00:00Z","cron":"0 9 * * *"}`,
		`{"action":"create","name":"` + strings.Repeat("n", 129) + `","text":"t","cron":"0 9 * * *"}`,
	} {
		if out := call(t, s, context.Background(), Update, raw); !strings.HasPrefix(out, "error:") {
			t.Fatalf("accepted %.80s: %s", raw, out)
		}
	}
}

// The strict reader refuses broken JSON at every position.
func TestStrictObjectRefusesBrokenJSON(t *testing.T) {
	for _, raw := range []string{`{1:2}`, `{"action":}`, `{"action":"remove"]`} {
		var a updateArgs
		if err := strictObject(json.RawMessage(raw), &a); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	var a updateArgs
	if err := strictObject(json.RawMessage(`[]`), &a); err == nil || err.Error() != "schedule arguments must be an object" {
		t.Errorf("an array: %v", err)
	}
	// An object of 16 KiB is read.
	if err := strictObject(json.RawMessage(`{"name":"`+strings.Repeat("x", 16*1024-11)+`"}`), &a); err != nil {
		t.Errorf("16 KiB: %v", err)
	}
}

// A shortened result never ends inside a character.
func TestFirstLineCutsAtCharacter(t *testing.T) {
	got := firstLine("a" + strings.Repeat("ü", 300))
	if len(got) != 511 || !utf8.ValidString(got) {
		t.Fatalf("%d bytes, valid %v", len(got), utf8.ValidString(got))
	}
	// A line of 512 bytes is kept as it is.
	if line := strings.Repeat("a", 511) + "\xff"; firstLine(line) != line {
		t.Fatal("a line of 512 bytes was cut")
	}
}

// A local time needs a zone that exists.
func TestParseAtNeedsKnownZone(t *testing.T) {
	if _, err := parseAt("2026-10-26T09:00", "Not/A_Zone"); err == nil {
		t.Fatal("accepted unknown zone")
	}
}

// output passes a failure through unchanged.
func TestOutputPassesFailure(t *testing.T) {
	want := errors.New("broken")
	if out, err := output(1, want); out != "" || err != want {
		t.Fatalf("%q %v", out, err)
	}
}

// The preview shows only the stored next run when the schedule cannot be
// followed further, and nothing for an exhausted job.
func TestPreviewOfUnusableSchedules(t *testing.T) {
	next := time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)
	for _, job := range []store.Job{
		{Cron: "x", Zone: "UTC", Next: next},
		{Cron: "0 9 * * *", Zone: "Not/A_Zone", Next: next},
		{Cron: "0 0 31 2 *", Zone: "UTC", Next: next},
	} {
		if v := jobView(job); len(v.NextRuns) != 1 || v.NextRuns[0] != v.Next {
			t.Errorf("%+v: %+v", job, v.NextRuns)
		}
	}
	v := jobView(store.Job{Cron: "0 9 * * *", Zone: "UTC"})
	if v.Next != "" || v.NextRuns != nil {
		t.Fatalf("exhausted job previewed: %+v", v)
	}
	if b, err := json.Marshal(v); err != nil || strings.Contains(string(b), "next_readable") {
		t.Fatalf("%s %v", b, err)
	}
}
