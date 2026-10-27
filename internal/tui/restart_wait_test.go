package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"ouroboros/plugin"
)

// A message sent while the node starts anew waits in the input and the line
// says when it goes out, instead of going back with a confusing error.
func TestRestartingHoldsTheMessage(t *testing.T) {
	m, _ := newModel(t)
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 30})
	m.input.SetValue("hello")
	if cmd := m.asked(askedMsg{id: m.chat.contextID, text: "hello", steer: true, err: plugin.ErrRestarting}); cmd == nil {
		t.Fatal("a refused message must be offered again")
	}
	if m.chat.awaiting == nil || m.input.Value() != "hello" {
		t.Fatalf("held: %+v, input %q", m.chat.awaiting, m.input.Value())
	}
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "ouro restarts with the new build") || !strings.Contains(view, "your message goes out when it is back") {
		t.Fatalf("the view does not say what happens: %q", view)
	}
	if strings.Contains(view, "send again once it is back") {
		t.Fatal("the node's refusal is shown instead of the wait")
	}
}

// The node takes the waiting message: it shows as one on its way, and the
// notice says the node is back.
func TestRestartingSendsTheMessageWhenTheNodeIsBack(t *testing.T) {
	m, _ := newModel(t)
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 30})
	m.chat.awaiting = &awaitingSend{text: "hello", typed: "hello", steer: true, since: m.now()}
	m.input.SetValue("hello")
	if cmd := m.restartSent(restartSentMsg{id: m.chat.contextID, text: "hello", typed: "hello", err: plugin.ErrRestarting}); cmd != nil {
		t.Fatal("the retry timer is the clock of the wait")
	}
	if m.chat.awaiting == nil {
		t.Fatal("the message was let go while the node was not back")
	}
	if cmd := m.retryRestarting(); cmd == nil {
		t.Fatal("the wait must go on")
	}
	m.restartSent(restartSentMsg{id: m.chat.contextID, text: "hello", typed: "hello"})
	if m.chat.awaiting != nil || m.input.Value() != "" || !m.chat.pending {
		t.Fatalf("sent: awaiting %+v, input %q, pending %v", m.chat.awaiting, m.input.Value(), m.chat.pending)
	}
	if !strings.Contains(m.notice, "ouro is back") {
		t.Fatalf("the notice does not say the node is back: %q", m.notice)
	}
}

// A message the node has not taken when the wait is over stays in the input,
// and the owner is told why.
func TestRestartingGivesTheMessageBack(t *testing.T) {
	m, _ := newModel(t)
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 30})
	m.chat.awaiting = &awaitingSend{text: "hello", typed: "hello", since: m.now().Add(-restartWaitLimit - time.Second)}
	m.input.SetValue("hello")
	m.retryRestarting()
	if m.chat.awaiting != nil || m.input.Value() != "hello" {
		t.Fatalf("given back: awaiting %+v, input %q", m.chat.awaiting, m.input.Value())
	}
	if !m.noticeErr || !strings.Contains(m.notice, "did not come back") {
		t.Fatalf("no reason: %q", m.notice)
	}
}

// What the owner changes meanwhile is not sent behind their back.
func TestRestartingDoesNotSendChangedText(t *testing.T) {
	m, _ := newModel(t)
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 30})
	m.chat.awaiting = &awaitingSend{text: "hello", typed: "hello", since: m.now()}
	m.input.SetValue("hello there")
	if cmd, waits := m.retryAwaiting(m.chat); cmd != nil || waits || m.chat.awaiting != nil {
		t.Fatal("changed text was offered to the node")
	}
	if m.input.Value() != "hello there" {
		t.Fatalf("the input changed: %q", m.input.Value())
	}
}
