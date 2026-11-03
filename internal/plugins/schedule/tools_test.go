package schedule

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

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
