package schedule

import (
	"context"
	"slices"
	"testing"
	"time"
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
