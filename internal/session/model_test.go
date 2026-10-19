package session

import (
	"context"
	"strings"
	"testing"
	"time"

	"ouroboros/internal/history"
	"ouroboros/internal/openai"
)

// A session remembers the model that answered in it. Another model does not
// continue it: the agent forgets what was said, the archive notes where the
// new context starts, and what is loaded again holds only what came after.
func TestModelChangeStartsAnew(t *testing.T) {
	dir := t.TempDir()
	a := &fakeAgent{model: "m1"}
	s, arch, l := archived(t, a, dir, time.Now())
	ctx := context.Background()
	asked(t, s, "s1", "one")
	arch.Append(history.Who{Session: "s1"}, []openai.Message{{Role: "user", Content: "one"}, {Role: "assistant", Content: "pong"}}, nil)
	if model, _, _ := arch.Model(history.Owner("s1")); model != "m1" || len(l.lines) != 0 {
		t.Fatalf("kept %q, log %q", model, l.lines)
	}

	a.mu.Lock()
	a.model = "m2"
	a.mu.Unlock()
	asked(t, s, "s1", "two")
	arch.Append(history.Who{Session: "s1"}, []openai.Message{{Role: "user", Content: "two"}, {Role: "assistant", Content: "pong"}}, nil)
	if len(a.forgot) != 1 || a.forgot[0] != "s1" {
		t.Fatalf("forgot %q", a.forgot)
	}
	if model, _, _ := arch.Model(history.Owner("s1")); model != "m2" {
		t.Fatalf("kept %q", model)
	}
	s.Close()

	b := &fakeAgent{model: "m2"}
	s, _, _ = archived(t, b, dir, time.Now())
	defer s.Close()
	if _, _, err := s.Watch(ctx, "s1"); err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	memory := string(b.resumed["s1"])
	b.mu.Unlock()
	if strings.Contains(memory, "one") || !strings.Contains(memory, "two") {
		records, _ := arch.Records(history.Owner("s1"))
		var roles []string
		for _, r := range records {
			roles = append(roles, r.Role)
		}
		t.Fatalf("resumed %s, records %v, log %q", memory, roles, l.lines)
	}
}

// notAModeler is an agent that cannot say which model it answers with, as
// only a modeler can: its sessions are continued as they are.
type notAModeler struct{ Assistant }

// An agent that cannot tell its model answers under none, and a session of a
// service without an archive is kept with none either.
func TestModelWithoutAModelerOrAnArchive(t *testing.T) {
	an := notAModeler{&fakeAgent{model: "m1"}}
	if _, ok := any(an).(modeler); ok {
		t.Fatal("the agent tells its model")
	}
	s := New(an)
	t.Cleanup(s.Close)
	if got := s.model(); got != "" {
		t.Fatalf("model %q", got)
	}
	se := &session{id: "s1"}
	s.modelOf(se) // without an archive there is nothing to read it from
	if se.model != "" {
		t.Fatalf("kept %q", se.model)
	}
}
