package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
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

// The settings show the model of the sessions and of the thread, and open the
// model screen of each: its title says which, and only enter sets a model
// there, since peers and fallbacks are those of the node.
func TestSettingsOpenTheModelsOfAScope(t *testing.T) {
	m, b := newModel(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	b.settings = someSettings()
	b.offers = []Models{local("gpt-4o", "gpt-4o-mini")}
	m.status.SessionAccess, m.status.SessionModel = "a1", "gpt-4o-mini"
	m.status.ThreadAccess, m.status.ThreadModel = "gone", "old"
	deliver(m, run(m, "/settings"))
	for label, want := range map[string]string{"Model for sessions": "gpt-4o-mini · a1", "Model for the thread": "old · gone"} {
		pick(t, m, nodeSection, label, "")
		if r := m.config.selectedRow(m); r.value != want {
			t.Errorf("%s: %q", label, r.value)
		}
	}

	for _, c := range []struct{ label, title, head string }{
		{"Model for sessions", "Model for sessions", "gpt-4o-mini · Ollama local"},
		// An access that is gone has no label: the head names the model.
		{"Model for the thread", "Model for the thread", "old"},
	} {
		m.screen = screenSettings
		pick(t, m, nodeSection, c.label, "open the models")
		do(m)
		if m.screen != screenModel {
			t.Fatalf("%s opened %v", c.label, m.screen)
		}
		view := ansi.Strip(m.View().Content)
		if !strings.Contains(view, c.title+"  "+c.head) {
			t.Fatalf("the screen of %s:\n%s", c.label, view)
		}
		if press(m, "p") != nil || press(m, "f") != nil {
			t.Fatalf("p or f did something on the screen of %s", c.label)
		}
	}
	// Enter there sets the model of the thread; a thread without a model of
	// its own is answered like the owner, as its head says.
	deliver(m, press(m, "enter"))
	if b.status.ThreadAccess != "a1" || b.status.ThreadModel != "gpt-4o" {
		t.Fatalf("enter set %q of %q", b.status.ThreadModel, b.status.ThreadAccess)
	}
	m.status.ThreadModel = ""
	if got := (&modelScreen{scope: scopeThreads}).text(m); !strings.HasPrefix(got, "the same as yours: ") {
		t.Fatalf("the head says %q", got)
	}
	if got := scopeNode.name(); got != "the node" {
		t.Fatalf("the node is called %q", got)
	}
}
