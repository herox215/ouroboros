package tui

import (
	"strings"
	"testing"

	"ouroboros/plugin"
)

// A relayed approval remains actionable in the main chat after the router's
// answer ends, and is removed only when its matching completion arrives.
func TestRelayedApprovalIsVisibleInThread(t *testing.T) {
	m, _ := newModel(t)
	thread := m.conversation(plugin.ThreadID)
	m.chat = thread
	m.apply(thread, Event{Kind: plugin.SessionMessage, Text: "Rebuild Ouro"}, true)
	m.apply(thread, Event{Kind: plugin.SessionAnswer, Text: "Handed to the session"}, true)
	m.event(eventMsg{id: plugin.ThreadID, event: Event{
		Kind: plugin.SessionAsked, Text: "Allow rebuild?",
		Options: []string{"Allow once", "No"}, Recommended: "Allow once",
	}})
	m.apply(thread, Event{Kind: plugin.SessionWorking, N: 1}, true)
	view := strip(m.View().Content)
	for _, want := range []string{"YOUR DECISION", "Allow rebuild?", "Allow once", "No", "NEEDS YOUR ANSWER"} {
		if !strings.Contains(view, want) {
			t.Fatalf("thread does not show %q:\n%s", want, view)
		}
	}
	// Enter still targets the visible approval rather than sending a new
	// router message. Delivery to the session is covered by the service test.
	if cmd := press(m, "enter"); cmd == nil || !thread.asking.sent {
		t.Fatal("the approval cannot be answered from the main chat")
	}
	m.event(eventMsg{id: plugin.ThreadID, event: Event{
		Kind: plugin.SessionAsked, Done: true, Text: "Allow once",
	}})
	if thread.asking != nil || strings.Contains(strip(m.View().Content), "YOUR DECISION") {
		t.Fatal("completed approval is still offered in the main chat")
	}
}
