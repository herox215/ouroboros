package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"ouroboros/plugin"
)

// What the review of an answer proposes is a faint line below it, and the
// next answer is summed up apart from it.
func TestSuggestionIsShown(t *testing.T) {
	m, _ := newModel(t)
	c := m.chat
	m.apply(c, Event{Kind: plugin.SessionMessage, Text: "build it"}, true)
	m.apply(c, Event{Kind: plugin.SessionStep, Tool: "bash", Text: "running"}, true)
	m.apply(c, Event{Kind: plugin.SessionAnswer, Text: "built"}, true)
	m.apply(c, Event{Kind: plugin.SessionSuggestion, Text: "bash waited 42s for the build: let it run as a job"}, true)
	m.renderHistory(false)
	view := ansi.Strip(m.history.View())
	at := strings.Index(view, "built")
	if at < 0 || !strings.Contains(view[at:], "✦ bash waited 42s for the build: let it run as a job") {
		t.Fatalf("no suggestion below the answer in:\n%s", view)
	}
	if c.entries[len(c.entries)-1].asks() || len(c.summaries()) != 1 {
		t.Fatalf("entries %+v", c.entries)
	}
}

// What the reviews suggest waits in /pending after the proposals: taking one
// has the agent make it in a new conversation, and the one shown goes on;
// dropping one drops it.
func TestSuggestionsArePending(t *testing.T) {
	m, b := newModel(t)
	at := time.Date(2026, 10, 14, 9, 5, 0, 0, time.Local)
	b.trust = Contacts{
		Proposals: []plugin.Proposal{{ID: "p1", Field: "name", Now: `"a"`, Then: `"b"`, At: at}},
		Suggestions: []plugin.Suggestion{
			{ID: "s1", Text: "let bash run long commands as jobs", Signals: []string{"bash took 42s"}, At: at},
			{ID: "s2", Text: "say which files are near", At: at},
		},
	}
	m.Update(m.loadContacts())
	if line := ansi.Strip(m.requests.pendingView(m)); !strings.Contains(line, "3 pending") || !strings.Contains(line, "reviews suggest 2") {
		t.Fatalf("the line of what is pending: %q", line)
	}
	first := m.chat
	run(m, "/pending")
	view := ansi.Strip(m.View().Content)
	for _, want := range []string{"Changes of ouro the reviews of answers suggest", "  suggestion  · 09:05", "    let bash run long commands as jobs", "measured: bash took 42s", "say which files are near"} {
		if !strings.Contains(view, want) {
			t.Fatalf("the view lacks %q:\n%s", want, view)
		}
	}
	// Down past the proposal to the first suggestion, and take it.
	press(m, "j")
	drive(t, m, press(m, "enter"), func() bool {
		b.mu.Lock()
		defer b.mu.Unlock()
		return len(b.sent) > 0
	})
	if m.screen != screenChat || len(b.accepted) != 1 || b.accepted[0] != "suggestion s1" {
		t.Fatalf("screen %v, accepted %v", m.screen, b.accepted)
	}
	b.mu.Lock()
	last := b.sent[len(b.sent)-1]
	b.mu.Unlock()
	if last.contextID != m.chat.contextID || !strings.HasPrefix(last.text, "Make this change of ouro") ||
		!strings.Contains(last.text, "let bash run long commands as jobs") || !strings.Contains(last.text, "bash took 42s") {
		t.Fatalf("sent %+v", last)
	}
	if m.chats[first.contextID] == nil {
		t.Fatal("the conversation shown before is gone")
	}
	// The other is dropped.
	run(m, "/pending")
	m.requests.selectAt(m, 2)
	m.Update(press(m, "right", "enter")())
	if len(b.rejected) != 1 || b.rejected[0] != "suggestion s2" || m.notice != "suggestion dropped" {
		t.Fatalf("dropped %v, notice %q", b.rejected, m.notice)
	}
	// A drop that fails says why.
	b.trust.Suggestions = []plugin.Suggestion{{ID: "s3", Text: "one more"}}
	m.Update(m.loadContacts())
	if line := ansi.Strip(m.requests.pendingView(m)); !strings.Contains(line, "a review suggests") {
		t.Fatalf("one suggestion: %q", line)
	}
	m.requests.selectAt(m, 1)
	b.rejectErr = errors.New("disk full")
	m.Update(press(m, "right", "enter")())
	if m.notice != "disk full" || !m.noticeErr {
		t.Fatalf("a drop that fails: %q", m.notice)
	}
	b.rejectErr = nil
	// One that is gone meanwhile says so.
	b.trust.Suggestions = []plugin.Suggestion{{ID: "gone"}}
	m.Update(m.loadContacts())
	m.requests.selectAt(m, 1)
	b.trust.Suggestions = nil
	m.Update(press(m, "left", "enter")())
	if m.notice != plugin.ErrNoSuggestion.Error() || !m.noticeErr {
		t.Fatalf("gone: %q", m.notice)
	}
}
