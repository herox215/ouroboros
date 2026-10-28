package tui

import (
	"errors"
	"strings"
	"testing"

	"ouroboros/plugin"
)

// The wait asks again on its clock: the message goes to the node as it was
// sent, steered or asked, and is not offered a second time while the node
// has not answered.
func TestRestartingOffersTheMessageAgainOnItsClock(t *testing.T) {
	was := restartRetryDelay
	restartRetryDelay = 0
	t.Cleanup(func() { restartRetryDelay = was })
	m, b := newModel(t)
	if _, ok := retryRestart()().(restartRetryMsg); !ok {
		t.Fatal("the clock of the wait brings no retry")
	}
	// Nothing waits: no clock is kept.
	if cmd := m.retryRestarting(); cmd != nil {
		t.Fatal("a clock is kept for nothing")
	}

	b.fail = map[string]error{"steer": plugin.ErrRestarting, "ask": errors.New("refused")}
	m.chat.awaiting = &awaitingSend{text: "hello", typed: "hello", steer: true, since: m.now()}
	m.input.SetValue("hello")
	cmd, waits := m.retryAwaiting(m.chat)
	if cmd == nil || !waits || !m.chat.awaiting.inflight {
		t.Fatal("the message is not offered again")
	}
	if again, waits := m.retryAwaiting(m.chat); again != nil || !waits {
		t.Fatal("the message is offered again before the node answered")
	}
	sent, ok := cmd().(restartSentMsg)
	if !ok || !errors.Is(sent.err, plugin.ErrRestarting) {
		t.Fatalf("the steer brought %+v", sent)
	}
	m.Update(sent)
	if m.chat.awaiting == nil || m.chat.awaiting.inflight {
		t.Fatal("the message should wait for the next turn of the clock")
	}

	// A message that was asked, not steered, is asked again; a refusal of
	// another kind gives it back for the owner to send.
	m.chat.awaiting.steer = false
	if m.Update(restartRetryMsg{}); !m.chat.awaiting.inflight {
		t.Fatal("the turn of the clock offers no message")
	}
	m.chat.awaiting.inflight = false
	cmd, _ = m.retryAwaiting(m.chat)
	m.input.SetValue("")
	m.Update(cmd())
	if m.chat.awaiting != nil || m.input.Value() != "hello" || !m.noticeErr || m.notice != "refused" {
		t.Fatalf("given back: awaiting %+v, input %q, notice %q", m.chat.awaiting, m.input.Value(), m.notice)
	}
}

// An answer about a session that is gone, or about a message that does not
// wait any more, changes nothing.
func TestRestartingIgnoresStaleAnswers(t *testing.T) {
	m, _ := newModel(t)
	if cmd := m.restartSent(restartSentMsg{id: "gone", typed: "hello"}); cmd != nil {
		t.Fatal("an answer about a session that is gone did something")
	}
	if cmd := m.restartSent(restartSentMsg{id: m.chat.contextID, typed: "hello"}); cmd != nil {
		t.Fatal("an answer about nothing that waits did something")
	}
}

// A message held in a session the owner does not look at waits in its
// draft, and the line says where it goes out; once the node takes it, the
// draft is empty again, and one the node refuses goes back into it.
func TestRestartingHoldsTheMessageOfAnotherSession(t *testing.T) {
	m, _ := newModel(t)
	held := m.chat
	held.title = "notes"
	m.switchTo(m.newChat())
	m.asked(askedMsg{id: held.contextID, text: "hello", err: plugin.ErrRestarting})
	if held.awaiting == nil || held.draft != "hello" || !strings.Contains(m.notice, "the message in “notes” · /session goes out") {
		t.Fatalf("held: %+v, draft %q, notice %q", held.awaiting, held.draft, m.notice)
	}
	m.restartSent(restartSentMsg{id: held.contextID, text: "hello", typed: "hello"})
	if held.awaiting != nil || held.draft != "" || !held.pending {
		t.Fatalf("sent: awaiting %+v, draft %q", held.awaiting, held.draft)
	}

	held.pending = false
	held.awaiting = &awaitingSend{text: "hello", typed: "hello", since: m.now()}
	m.restartSent(restartSentMsg{id: held.contextID, text: "hello", typed: "hello", err: errors.New("refused")})
	if held.awaiting != nil || held.draft != "hello" {
		t.Fatalf("given back: awaiting %+v, draft %q", held.awaiting, held.draft)
	}
}
