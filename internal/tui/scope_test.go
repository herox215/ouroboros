package tui

import (
	"testing"

	"ouroboros/plugin"
)

// enter on the model screen of a scope makes the model chosen that of the
// scope, and on the one chosen already the model of the node again.
func TestPickForScope(t *testing.T) {
	b := &fakeBackend{}
	m := &Model{backend: b}
	s := &modelScreen{scope: scopeSessions}
	access := plugin.SetupAccess{ID: "acc", Label: "acc"}
	s.pickForScope(m, access, "x")()
	if b.status.SessionAccess != "acc" || b.status.SessionModel != "x" {
		t.Fatalf("sessions answer with %q of %q", b.status.SessionModel, b.status.SessionAccess)
	}
	m.status = b.status
	s.pickForScope(m, access, "x")()
	if b.status.SessionAccess != "" || b.status.SessionModel != "" {
		t.Fatalf("chosen again: %q of %q", b.status.SessionModel, b.status.SessionAccess)
	}
}
