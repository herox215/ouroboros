package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"ouroboros/plugin"
)

func TestAcceptAllSuggestionsInOneConversation(t *testing.T) {
	m, b := newModel(t)
	b.trust = Contacts{
		Incoming:    []plugin.Request{{PublicKey: "peer"}},
		Proposals:   []plugin.Proposal{{ID: "p1", Field: "name"}},
		Suggestions: []plugin.Suggestion{{ID: "s1", Text: "first change", Signals: []string{"42 seconds"}}, {ID: "s2", Text: "second change"}},
	}
	m.Update(m.loadContacts())
	first := m.chat
	m.input.SetValue("unfinished draft")
	run(m, "/suggestions")
	if !strings.Contains(ansi.Strip(m.View().Content), "Accept all") {
		t.Fatal("missing bulk action")
	}
	// Select the extra entry after the two suggestions.
	for range 2 {
		press(m, "down")
	}
	if m.suggestionRequests.sel != 2 {
		t.Fatalf("selected %d", m.suggestionRequests.sel)
	}
	cmd := press(m, "enter")
	if cmd == nil || !m.suggestionRequests.acceptingAll {
		t.Fatal("bulk action not started")
	}
	if press(m, "enter") != nil || press(m, "a") != nil {
		t.Fatal("duplicate bulk action")
	}
	// Arrivals after the owner pressed Enter must not be included.
	b.trust.Suggestions = append(b.trust.Suggestions, plugin.Suggestion{ID: "later", Text: "not yet accepted"})
	msg := cmd().(acceptedAllMsg)
	if msg.err != nil || len(msg.suggestions) != 2 {
		t.Fatalf("result %+v", msg)
	}
	if strings.Join(b.accepted, ",") != "suggestion s1,suggestion s2" {
		t.Fatalf("accepted %v", b.accepted)
	}
	_, send := m.Update(msg)
	drive(t, m, send, func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		return len(b.sent) > 0
	})
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.sent) != 1 {
		t.Fatalf("sent %d messages", len(b.sent))
	}
	task := b.sent[0]
	for _, want := range []string{"Make this change of ouro", "\n1. first change\nWhat was measured: 42 seconds\n", "\n2. second change\n", "single conversation"} {
		if !strings.Contains(task.text, want) {
			t.Fatalf("task lacks %q: %s", want, task.text)
		}
	}
	if strings.Count(task.text, "What was measured") != 1 {
		t.Fatalf("a suggestion without measurements says what was measured: %s", task.text)
	}
	if strings.Contains(task.text, "not yet accepted") || task.contextID == first.contextID || m.chat != first || m.screen != screenSuggestions || m.chats[task.contextID] == nil || m.chats[first.contextID] == nil {
		t.Fatalf("incorrect target or contents: %+v", task)
	}
	for _, suggestion := range m.suggestionRequests.trust.Suggestions {
		if suggestion.ID == "s1" || suggestion.ID == "s2" {
			t.Fatalf("taken suggestions still listed: %+v", m.suggestionRequests.trust.Suggestions)
		}
	}
	if m.suggestionRequests.acceptingAll {
		t.Fatal("bulk action still busy")
	}
}

func TestAcceptAllSuggestionsFailuresAndEmpty(t *testing.T) {
	m, b := newModel(t)
	if m.suggestionRequests.acceptAll(m) != nil {
		t.Fatal("empty bulk action")
	}
	b.trust = Contacts{
		Incoming:    []plugin.Request{{PublicKey: "peer"}},
		Proposals:   []plugin.Proposal{{ID: "p1", Field: "name"}},
		Suggestions: []plugin.Suggestion{{ID: "s1"}},
	}
	m.Update(m.loadContacts())
	b.acceptErr = errors.New("disk full")
	first := m.chat
	msg := m.suggestionRequests.acceptAll(m)().(acceptedAllMsg)
	if len(b.accepted) != 1 || len(msg.suggestions) != 0 {
		t.Fatalf("accepted %v, result %+v", b.accepted, msg)
	}
	m.Update(msg)
	if m.chat != first || !m.noticeErr || !strings.Contains(m.notice, "suggestion s1") || m.suggestionRequests.acceptingAll {
		t.Fatalf("notice %q, busy %v", m.notice, m.suggestionRequests.acceptingAll)
	}
	// A missing suggestion must not discard one taken successfully.
	b.acceptErr = nil
	b.trust.Suggestions = nil
	m.suggestionRequests.trust = Contacts{Suggestions: []plugin.Suggestion{{ID: "gone"}, {ID: "ok"}}}
	b.trust.Suggestions = []plugin.Suggestion{{ID: "ok", Text: "make this"}}
	msg = m.suggestionRequests.acceptAll(m)().(acceptedAllMsg)
	if len(msg.suggestions) != 1 || msg.suggestions[0].ID != "ok" || !errors.Is(msg.err, plugin.ErrNoSuggestion) {
		t.Fatalf("partial result %+v", msg)
	}
	m.Update(msg)
	if m.chat != first || len(m.chats) < 2 || !m.noticeErr || !strings.Contains(m.notice, "gone") {
		t.Fatalf("partial work lost, notice %q", m.notice)
	}
}

// Accept all is marked when it is selected, stays selected when more comes to
// decide, and says that it is on its way once it is chosen. An entry selected
// that is no more does not become Accept all.
func TestAcceptAllStaysSelected(t *testing.T) {
	m, b := newModel(t)
	b.trust = Contacts{Incoming: []plugin.Request{{PublicKey: "peer"}}, Suggestions: []plugin.Suggestion{{ID: "s1", Text: "first change"}}}
	m.Update(m.loadContacts())
	if m.suggestionRequests.sel != 0 {
		t.Fatalf("what came first is selected at %d, not the first", m.suggestionRequests.sel)
	}
	run(m, "/suggestions")
	press(m, "down")
	if m.suggestionRequests.sel != 1 || !strings.Contains(view(m), "› Accept all") {
		t.Fatalf("selected %d:\n%s", m.suggestionRequests.sel, view(m))
	}
	b.trust.Suggestions = append(b.trust.Suggestions, plugin.Suggestion{ID: "s2", Text: "second change"})
	m.Update(m.loadContacts())
	if m.suggestionRequests.sel != 2 || !strings.Contains(view(m), "› Accept all") {
		t.Fatalf("with more to decide, selected %d:\n%s", m.suggestionRequests.sel, view(m))
	}
	if cmd := press(m, "enter"); cmd == nil || !strings.Contains(view(m), "› Accepting all…") {
		t.Fatalf("on its way:\n%s", view(m))
	}
	m.suggestionRequests.acceptingAll = false
	press(m, "up")
	b.trust.Suggestions = b.trust.Suggestions[:1]
	m.Update(m.loadContacts())
	if m.suggestionRequests.sel != 0 {
		t.Fatalf("the last suggestion gone, selected %d", m.suggestionRequests.sel)
	}
}
