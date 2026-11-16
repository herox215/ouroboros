package session

import (
	"testing"
	"time"

	"ouroboros/internal/history"
)

// scopeAgent answers with one model in the sessions and another in the thread.
type scopeAgent struct{ fakeAgent }

func (a *scopeAgent) Model(thread bool, _ string) string {
	if thread {
		return "t1"
	}
	return "m1"
}

// The model of a conversation is that of its scope: the thread's own, and the
// sessions' otherwise.
func TestModelPerScope(t *testing.T) {
	s := New(&scopeAgent{})
	t.Cleanup(s.Close)
	if got := s.model("s1"); got != "m1" {
		t.Errorf("a session answers with %q", got)
	}
	if got := s.model(ThreadID); got != "t1" {
		t.Errorf("the thread answers with %q", got)
	}
}

// modelFor notes the model of the scope once, and a change of it only when
// the scope answers with another model: a constant scope model is no change,
// and keeps what the agent remembers.
func TestModelForNotesOnlyChanges(t *testing.T) {
	a := &fakeAgent{model: "m1"}
	s, arch, _ := archived(t, a, t.TempDir(), time.Now())
	t.Cleanup(s.Close)
	for _, text := range []string{"one", "two"} {
		asked(t, s, "s1", text)
	}
	if len(a.forgot) != 0 {
		t.Fatalf("forgot %q", a.forgot)
	}
	if records, _ := arch.Records(history.Owner("s1")); len(records) != 0 {
		t.Fatalf("a constant model was noted: %+v", records)
	}
	if model, _, _ := arch.Model(history.Owner("s1")); model != "m1" {
		t.Fatalf("kept %q", model)
	}
	a.mu.Lock()
	a.model = "m2"
	a.mu.Unlock()
	asked(t, s, "s1", "three")
	if len(a.forgot) != 1 || a.forgot[0] != "s1" {
		t.Fatalf("forgot %q", a.forgot)
	}
	if model, _, _ := arch.Model(history.Owner("s1")); model != "m2" {
		t.Fatalf("kept %q", model)
	}
	if records, _ := arch.Records(history.Owner("s1")); len(records) == 0 {
		t.Fatal("the change was not noted")
	}
}

// The model of a session is that of the agent it runs as, where that agent
// has one of its own: as the session runs as it, or as the archive keeps it
// while the session is not loaded. The thread answers with its own.
func TestModelPerAgent(t *testing.T) {
	a := &fakeAgent{model: "m1", models: map[string]string{"archivist": "small", "agent-0a1b2c3d": "large"}}
	s, arch, _ := archived(t, a, t.TempDir(), time.Now())
	t.Cleanup(s.Close)
	if err := arch.SetAgent(history.Owner("s2"), "archivist"); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions["s3"] = &session{id: "s3", agent: "agent-0a1b2c3d", watchers: map[chan Event]struct{}{}}
	for id, want := range map[string]string{"s2": "small", "s3": "large", "s4": "m1", ThreadID: "m1"} {
		if got := s.model(id); got != want {
			t.Errorf("%s answers with %q, want %q", id, got, want)
		}
	}
}
