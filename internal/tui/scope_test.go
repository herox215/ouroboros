package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"ouroboros/plugin"
)

// enter on the model screen of a scope makes the model chosen that of the
// scope, and on the one chosen already the model of the node again.
func TestPickForScope(t *testing.T) {
	b := &fakeBackend{}
	m := &Model{backend: b}
	acc := plugin.SetupAccess{ID: "acc", Label: "acc"}
	for _, tt := range []struct {
		scope scope
		got   func() (string, string)
	}{
		{scopeSessions, func() (string, string) { return b.status.SessionAccess, b.status.SessionModel }},
		{scopeThreads, func() (string, string) { return b.status.ThreadAccess, b.status.ThreadModel }},
	} {
		s := &modelScreen{scope: tt.scope}
		s.pickForScope(m, acc, "x")()
		if access, model := tt.got(); access != "acc" || model != "x" {
			t.Fatalf("%s answer with %q of %q", tt.scope.name(), model, access)
		}
		m.status = b.status
		s.pickForScope(m, acc, "x")()
		if access, model := tt.got(); access != "" || model != "" {
			t.Fatalf("%s chosen again: %q of %q", tt.scope.name(), model, access)
		}
		m.status = b.status
	}
}

// The list marks the model of each scope, so that the owner sees which one
// is meant when two accesses offer the same name.
func TestListMarksTheScopes(t *testing.T) {
	m := &Model{status: Status{Model: "n", SessionAccess: "a", SessionModel: "same", ThreadAccess: "b", ThreadModel: "other"}}
	s := &modelScreen{lists: []Models{
		{Access: plugin.SetupAccess{ID: "a", Label: "A", Active: true}, Models: []string{"same", "other"}},
		{Access: plugin.SetupAccess{ID: "b", Label: "B"}, Models: []string{"same", "other"}},
	}}
	lines, _ := s.list(m, 100, 40)
	text := ansi.Strip(strings.Join(lines, "\n"))
	if strings.Count(text, "for sessions") != 1 || strings.Count(text, "for the thread") != 1 {
		t.Fatalf("the marks:\n%s", text)
	}
	if !strings.Contains(text, "same  for sessions") || !strings.Contains(text, "other  for the thread") {
		t.Fatalf("the marks are on the wrong models:\n%s", text)
	}
}

// Opening a scope's screen preselects the scope's model, not the node's, so
// that a direct enter repeats the choice instead of setting another scope's
// model.
func TestLoadedPreselectsTheScope(t *testing.T) {
	m := &Model{status: Status{Model: "node", SessionAccess: "a", SessionModel: "s", ThreadAccess: "b", ThreadModel: "t"}}
	models := []Models{
		{Access: plugin.SetupAccess{ID: "a", Label: "A", Active: true}, Models: []string{"node", "s"}},
		{Access: plugin.SetupAccess{ID: "b", Label: "B"}, Models: []string{"t"}},
	}
	for _, tt := range []struct {
		scope scope
		want  int
	}{
		{scopeNode, 0},
		{scopeSessions, 1},
		{scopeThreads, 2},
	} {
		s := &modelScreen{scope: tt.scope}
		s.loaded(m, modelsMsg{models: models})
		if s.sel != tt.want {
			t.Fatalf("%s preselects %d, want %d", tt.scope.name(), s.sel, tt.want)
		}
	}
	// The head of a scope's screen says the scope's model, not the node's.
	got := (&modelScreen{scope: scopeSessions, lists: models}).text(m)
	if !strings.Contains(got, "s · A") || strings.Contains(got, "node") {
		t.Fatalf("the head says %q", got)
	}
	if got := (&modelScreen{scope: scopeThreads, lists: models}).text(m); !strings.Contains(got, "t · B") {
		t.Fatalf("the head says %q", got)
	}
}
