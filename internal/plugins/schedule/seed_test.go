package schedule

import (
	"context"
	"slices"
	"testing"
	"time"

	store "ouroboros/internal/schedule"
)

// The scheduler creates the archivist job once, and a job the owner removed
// stays removed.
func TestSeedArchivistOnce(t *testing.T) {
	s, _, _ := fixture(t)
	s.tick(context.Background())
	jobs := s.store.List()
	if len(jobs) != 1 || jobs[0].Name != archivistName || jobs[0].Cron != archivistCron || jobs[0].Ask != "never" || !slices.Equal(jobs[0].Tools, archivistTools) {
		t.Fatalf("seeded jobs: %+v", jobs)
	}
	if err := s.store.Remove(jobs[0].ID); err != nil {
		t.Fatal(err)
	}
	fresh := &scheduler{path: s.path, sessions: s.sessions, now: s.now, limit: time.Hour, active: map[string]bool{}}
	fresh.tick(context.Background())
	if jobs := fresh.store.List(); len(jobs) != 0 {
		t.Fatalf("a removed archivist job came back: %+v", jobs)
	}
}

// An archivist job with the default text from before memories had topics
// gets the current one; one whose text the owner changed keeps it.
func TestUpgradeArchivistText(t *testing.T) {
	s, _, _ := fixture(t)
	s.tick(context.Background())
	jobs := s.store.List()
	if len(jobs) != 1 || jobs[0].Text != archivistText {
		t.Fatalf("seeded jobs: %+v", jobs)
	}
	setText := func(id, text string) {
		t.Helper()
		if _, err := s.store.Update(id, func(j *store.Job) error { j.Text = text; return nil }, s.now()); err != nil {
			t.Fatal(err)
		}
	}
	setText(jobs[0].ID, oldArchivistText)
	owned, err := s.store.Create(store.Job{Name: archivistName, Text: "Tidy only the topic work.", Cron: archivistCron, Ask: "never"}, s.now())
	if err != nil {
		t.Fatal(err)
	}
	s.tick(context.Background())
	for _, j := range s.store.List() {
		want := archivistText
		if j.ID == owned.ID {
			want = owned.Text
		}
		if j.Text != want || j.Cron != archivistCron {
			t.Fatalf("job %s after upgrade: %+v", j.ID, j)
		}
	}
}
