package session

import (
	"slices"
	"testing"
	"time"

	"ouroboros/internal/config"
	"ouroboros/internal/history"
	"ouroboros/internal/openai"
)

// The thread hands messages to the sessions of the configured window, not of
// a fixed one: a session older than a window of an hour is no longer one of
// them, and one inside it is.
func TestThreadWindowIsConfigured(t *testing.T) {
	dir := t.TempDir()
	arch := history.Open(dir)
	arch.Append(history.Who{Session: "old"}, []openai.Message{{Role: "user", Content: "from before"}}, nil)
	arch.SetTitle(history.Owner("old"), "Old")
	arch.Close()
	at := &clock{at: time.Now()}
	s, _, _ := archived(t, &fakeAgent{}, dir, at.now())
	s.now = at.now
	s.ThreadWindow = func() time.Duration { return time.Hour }
	if got := routableOf(t, s); !slices.Contains(got, "old") {
		t.Fatalf("inside the window: %q", got)
	}
	at.move(time.Hour + time.Minute)
	if got := routableOf(t, s); slices.Contains(got, "old") {
		t.Fatalf("outside the window of an hour: %q", got)
	}
	// The texts name the window as it is configured.
	for d, want := range map[time.Duration]string{
		time.Hour: "1 hour", 3 * time.Hour: "3 hours", 90 * time.Minute: "90 minutes", 90 * time.Second: "1m30s",
	} {
		if got := config.Span(d); got != want {
			t.Fatalf("%v as %q", d, got)
		}
	}
}

// routableOf are the IDs of the sessions the thread may hand messages to.
func routableOf(t *testing.T, s *Service) []string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []string
	for _, r := range s.routable() {
		ids = append(ids, r.id)
	}
	return ids
}
