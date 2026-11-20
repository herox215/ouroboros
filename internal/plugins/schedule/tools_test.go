package schedule

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"ouroboros/internal/agents"
	store "ouroboros/internal/schedule"
	"ouroboros/internal/session"
)

// A job that names tools runs with exactly those, one without with all; an
// unknown tool is refused when the job is saved, and an empty list clears it.
func TestJobTools(t *testing.T) {
	s, f, _ := fixture(t)
	s.known = func() []string { return []string{"memory_read", "bash"} }
	ctx := context.Background()
	far := time.Date(2026, 10, 27, 9, 0, 0, 0, time.UTC).Format(time.RFC3339)
	for _, args := range []string{
		`{"action":"create","name":"n","text":"t","at":"` + far + `","tools":["nope"]}`,
		`{"action":"create","name":"n","text":"t","at":"` + far + `","tools":["bash","bash"]}`,
	} {
		if out := call(t, s, ctx, Update, args); !strings.Contains(out, "tool") || strings.Contains(out, `"id"`) {
			t.Fatalf("accepted %s: %s", args, out)
		}
	}
	limited := saved(t, call(t, s, ctx, Update, `{"action":"create","name":"n","text":"t","at":"`+far+`","tools":["memory_read"]}`)).Job
	open := saved(t, call(t, s, ctx, Update, `{"action":"create","name":"o","text":"t","at":"`+far+`"}`)).Job
	if !slices.Equal(limited.Tools, []string{"memory_read"}) || open.Tools != nil {
		t.Fatalf("saved tools: %v, %v", limited.Tools, open.Tools)
	}
	if out := call(t, s, ctx, Read, `{"id":"`+limited.ID+`"}`); !strings.Contains(out, `"tools":["memory_read"]`) {
		t.Fatal(out)
	}
	for _, job := range []store.Job{limited, open} {
		if _, err := s.runNow(job.ID); err != nil {
			t.Fatal(err)
		}
		c := receive(t, f)
		if !slices.Equal(c.tools, job.Tools) {
			t.Fatalf("%s ran with %v", job.Name, c.tools)
		}
		c.result <- session.ScheduledResult{Text: "done"}
	}
	cleared := saved(t, call(t, s, ctx, Update, `{"action":"update","id":"`+limited.ID+`","tools":[]}`)).Job
	if cleared.Tools != nil {
		t.Fatalf("not cleared: %v", cleared.Tools)
	}
}

// The archivist that was seeded before jobs named their tools gets the memory
// tools once; a list the owner gives it after stays.
func TestArchivistToolsMigrated(t *testing.T) {
	s, _, _ := fixture(t)
	if err := s.open(); err != nil {
		t.Fatal(err)
	}
	job, err := s.store.Create(store.Job{Name: archivistName, Text: archivistText, Cron: archivistCron, Ask: "never", Zone: "UTC"}, s.now())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.path+".defaults", []byte("archivist\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s.tick(context.Background())
	if got, _ := s.store.Get(job.ID); !slices.Equal(got.Tools, archivistTools) {
		t.Fatalf("not migrated: %v", got.Tools)
	}
	if _, err := s.store.Update(job.ID, func(j *store.Job) error { j.Tools = []string{"memory_read"}; return nil }, s.now()); err != nil {
		t.Fatal(err)
	}
	s.tick(context.Background())
	if got, _ := s.store.Get(job.ID); !slices.Equal(got.Tools, []string{"memory_read"}) {
		t.Fatalf("owner's list replaced: %v", got.Tools)
	}
}

// The archivist that got the memory tools from before topics gets the
// current ones once; a list the owner gives it after stays.
func TestArchivistOldToolsMigrated(t *testing.T) {
	s, _, _ := fixture(t)
	if err := s.open(); err != nil {
		t.Fatal(err)
	}
	job, err := s.store.Create(store.Job{Name: archivistName, Text: archivistText, Cron: archivistCron, Ask: "never", Zone: "UTC", Tools: oldArchivistTools}, s.now())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.path+".defaults", []byte("archivist tools\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s.tick(context.Background())
	if got, _ := s.store.Get(job.ID); !slices.Equal(got.Tools, archivistTools) {
		t.Fatalf("not migrated: %v", got.Tools)
	}
	if _, err := s.store.Update(job.ID, func(j *store.Job) error { j.Tools = oldArchivistTools; return nil }, s.now()); err != nil {
		t.Fatal(err)
	}
	s.tick(context.Background())
	if got, _ := s.store.Get(job.ID); !slices.Equal(got.Tools, oldArchivistTools) {
		t.Fatalf("migrated twice: %v", got.Tools)
	}
}

// The archivist that has the memory tools from before it read conversations
// gets the history tools once; a list the owner chose stays.
func TestArchivistHistoryToolsMigrated(t *testing.T) {
	s, _, _ := fixture(t)
	if err := s.open(); err != nil {
		t.Fatal(err)
	}
	theirs := []string{"memory_load", "memory_add"}
	old, err := s.store.Create(store.Job{Name: archivistName, Text: archivistText, Cron: archivistCron, Ask: "never", Zone: "UTC", Tools: topicArchivistTools, Agent: agents.Archivist}, s.now())
	if err != nil {
		t.Fatal(err)
	}
	owned, err := s.store.Create(store.Job{Name: archivistName, Text: archivistText, Cron: archivistCron, Ask: "never", Zone: "UTC", Tools: theirs, Agent: agents.Archivist}, s.now())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.path+".defaults", []byte("archivist topic tools, agent\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s.tick(context.Background())
	if got, _ := s.store.Get(old.ID); !slices.Equal(got.Tools, archivistTools) || len(got.Tools) != 5 {
		t.Fatalf("not migrated: %v", got.Tools)
	}
	if got, _ := s.store.Get(owned.ID); !slices.Equal(got.Tools, theirs) {
		t.Fatalf("owner's list replaced: %v", got.Tools)
	}
	if b, err := os.ReadFile(s.path + ".defaults"); err != nil || string(b) != seededTools {
		t.Fatalf("the marker: %q, %v", b, err)
	}
}

// The archivist job runs as the archivist: the seeded one at once, and the one
// from before jobs ran as an agent once, with its tools as the owner left
// them; an agent the owner gives it after stays. A job that names no agent
// runs as the agent of the thread.
func TestArchivistRunsAsTheArchivist(t *testing.T) {
	s, f, _ := fixture(t)
	s.tick(context.Background())
	seeded := s.store.List()
	if len(seeded) != 1 || seeded[0].Agent != agents.Archivist {
		t.Fatalf("seeded: %+v", seeded)
	}
	if _, err := s.runNow(seeded[0].ID); err != nil {
		t.Fatal(err)
	}
	c := receive(t, f)
	if c.agent != agents.Archivist {
		t.Fatalf("the archivist job ran as %q", c.agent)
	}
	c.result <- session.ScheduledResult{Text: "done"}

	if err := s.store.Remove(seeded[0].ID); err != nil {
		t.Fatal(err)
	}
	owned := []string{"memory_load"}
	job, err := s.store.Create(store.Job{Name: archivistName, Text: archivistText, Cron: archivistCron, Ask: "never", Zone: "UTC", Tools: owned}, s.now())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.path+".defaults", []byte("archivist topic tools\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s.tick(context.Background())
	got, _ := s.store.Get(job.ID)
	if got.Agent != agents.Archivist || !slices.Equal(got.Tools, owned) {
		t.Fatalf("not migrated: %+v", got)
	}
	if b, err := os.ReadFile(s.path + ".defaults"); err != nil || string(b) != seededTools {
		t.Fatalf("the marker: %q, %v", b, err)
	}
	if _, err := s.store.Update(job.ID, func(j *store.Job) error { j.Agent = agents.Thread; return nil }, s.now()); err != nil {
		t.Fatal(err)
	}
	s.tick(context.Background())
	if got, _ := s.store.Get(job.ID); got.Agent != agents.Thread {
		t.Fatalf("the owner's agent replaced: %q", got.Agent)
	}

	plain, err := s.store.Create(store.Job{Name: "Morning", Text: "the calendar", Cron: "30 7 * * *", Zone: "UTC"}, s.now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.runNow(plain.ID); err != nil {
		t.Fatal(err)
	}
	c = receive(t, f)
	if c.agent != "" {
		t.Fatalf("a job of the owner ran as %q", c.agent)
	}
	c.result <- session.ScheduledResult{Text: "done"}
	if _, err := s.store.Create(store.Job{Name: "x", Text: "y", Cron: "30 7 * * *", Zone: "UTC", Agent: "helper"}, s.now()); err == nil {
		t.Fatal("a job for no agent was saved")
	}
}
