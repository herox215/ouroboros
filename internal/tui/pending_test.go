package tui

import (
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"ouroboros/plugin"
)

func TestAcceptAllPendingInOneConversation(t *testing.T) {
	m, b := newModel(t)
	b.trust = Contacts{
		Incoming:    []plugin.Request{{PublicKey: "peer"}},
		Proposals:   []plugin.Proposal{{ID: "p1", Field: "name"}},
		Suggestions: []plugin.Suggestion{{ID: "s1", Text: "first change", Signals: []string{"42 seconds"}}, {ID: "s2", Text: "second change"}},
	}
	m.Update(m.loadContacts())
	first := m.chat
	m.input.SetValue("unfinished draft")
	run(m, "/pending")
	if !strings.Contains(ansi.Strip(m.View().Content), "Accept all") {
		t.Fatal("missing bulk action")
	}
	// Select the extra entry after the four pending items.
	for range 4 {
		press(m, "down")
	}
	if m.requests.sel != 4 {
		t.Fatalf("selected %d", m.requests.sel)
	}
	cmd := press(m, "enter")
	if cmd == nil || !m.requests.acceptingAll {
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
	if strings.Join(b.accepted, ",") != "peer,proposal p1,suggestion s1,suggestion s2" {
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
	for _, want := range []string{"Make this change of ouro", "first change", "second change", "42 seconds", "single conversation"} {
		if !strings.Contains(task.text, want) {
			t.Fatalf("task lacks %q: %s", want, task.text)
		}
	}
	if strings.Contains(task.text, "not yet accepted") || task.contextID == first.contextID || task.contextID != m.chat.contextID || m.chats[first.contextID] == nil {
		t.Fatalf("incorrect target or contents: %+v", task)
	}
	if m.requests.acceptingAll {
		t.Fatal("bulk action still busy")
	}
}

func TestAcceptAllPendingFailuresAndEmpty(t *testing.T) {
	m, b := newModel(t)
	if m.requests.acceptAll(m) != nil {
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
	msg := m.requests.acceptAll(m)().(acceptedAllMsg)
	if len(b.accepted) != 3 || len(msg.suggestions) != 0 {
		t.Fatalf("accepted %v, result %+v", b.accepted, msg)
	}
	m.Update(msg)
	if m.chat != first || !m.noticeErr || !strings.Contains(m.notice, "connection peer") || !strings.Contains(m.notice, "proposal name") || !strings.Contains(m.notice, "suggestion s1") || m.requests.acceptingAll {
		t.Fatalf("notice %q, busy %v", m.notice, m.requests.acceptingAll)
	}
	// A missing suggestion must not discard one taken successfully.
	b.acceptErr = nil
	b.trust.Suggestions = nil
	m.requests.trust = Contacts{Suggestions: []plugin.Suggestion{{ID: "gone"}, {ID: "ok"}}}
	b.trust.Suggestions = []plugin.Suggestion{{ID: "ok", Text: "make this"}}
	msg = m.requests.acceptAll(m)().(acceptedAllMsg)
	if len(msg.suggestions) != 1 || msg.suggestions[0].ID != "ok" || !errors.Is(msg.err, plugin.ErrNoSuggestion) {
		t.Fatalf("partial result %+v", msg)
	}
	m.Update(msg)
	if m.chat == first || !m.noticeErr || !strings.Contains(m.notice, "gone") {
		t.Fatalf("partial work lost, notice %q", m.notice)
	}
}
