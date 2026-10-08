package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"ouroboros/internal/agent"
	"ouroboros/plugin"
)

// steerable makes b answer as an agent that waits for a tool until tools is
// closed, takes what was said meanwhile, and answers with it.
func steerable(b *fakeBackend) (tools chan struct{}) {
	tools = make(chan struct{})
	b.ask = func(ctx context.Context, step func(Step), _ func(string)) (string, error) {
		step(Step{Text: "asking beta"})
		<-tools
		step(Step{Text: "asking beta", Done: true})
		return "took " + strings.Join(agent.Steered(ctx), ","), nil
	}
	return tools
}

// What is sent while an answer is on its way is queued, and shown as that
// below the conversation, until the answer takes it. Up takes the last that
// waits back into the input, where it is the owner's again.
func TestSteering(t *testing.T) {
	m, b := newModel(t)
	tools := steerable(b)
	c := m.chat
	drive(t, m, say(t, m, "ping"), func() bool { return c.doing == "asking beta" })

	// Up is the input's while nothing waits.
	press(m, "up")
	drive(t, m, say(t, m, "also this"), func() bool { return len(c.queued) == 1 })
	if m.input.Value() != "" || m.notice != "" || !c.pending {
		t.Fatalf("input %q, notice %q", m.input.Value(), m.notice)
	}
	drive(t, m, say(t, m, "and that"), func() bool { return len(c.queued) == 2 })
	view := m.View().Content
	if strings.Count(view, " · queued") != 2 || strings.Count(view, "queued, ↑ takes it back") != 1 ||
		strings.Index(view, "also this") > strings.Index(view, "and that") || strings.Index(view, "also this") < strings.Index(view, "ping") {
		t.Fatalf("the queue is not shown in its order:\n%s", view)
	}
	if len(c.entries) != 2 { // the message and the round of the tool
		t.Fatalf("what waits is no part of the conversation yet: %+v", c.entries)
	}

	// What is typed keeps up for itself.
	m.input.SetValue("typing")
	press(m, "up")
	if b.unsteers != 0 {
		t.Fatal("up is the input's while something is typed, or nothing waits")
	}
	m.input.Reset()
	drive(t, m, press(m, "up"), func() bool { return m.input.Value() == "and that" && len(c.queued) == 1 })
	if view := m.View().Content; strings.Count(view, " · queued") != 1 || !strings.Contains(view, "queued, ↑ takes it back") {
		t.Fatalf("one still waits:\n%s", view)
	}

	// The answer takes what waits: it is a message of the conversation now.
	close(tools)
	drive(t, m, nil, func() bool { return !c.pending })
	if len(c.queued) != 0 || len(c.entries) != 4 || !c.entries[2].mine || c.entries[2].text != "also this" || c.entries[3].text != "took also this" {
		t.Fatalf("entries %+v, queued %q", c.entries, c.queued)
	}
	if strings.Contains(m.View().Content, "queued") {
		t.Fatalf("nothing waits any more:\n%s", m.View().Content)
	}
	if got := b.messages(); len(got) != 1 {
		t.Fatalf("only the first message is asked: %+v", got)
	}
}

// A message that is taken back goes before what was typed meanwhile, also
// for a session that is not shown any more; one the answer took meanwhile
// is not there to take back.
func TestTakenBack(t *testing.T) {
	m, b := newModel(t)
	defer close(steerable(b))
	c := m.chat
	drive(t, m, say(t, m, "ping"), func() bool { return c.doing == "asking beta" })
	for i, text := range []string{"one", "two", "three"} {
		drive(t, m, say(t, m, text), func() bool { return len(c.queued) == i+1 })
	}

	back := press(m, "up")()
	m.input.SetValue("typed")
	m.Update(back)
	if m.input.Value() != "three\ntyped" {
		t.Fatalf("input %q", m.input.Value())
	}

	// The session is not shown any more: it is typed for it.
	m.input.Reset()
	back = press(m, "up")()
	run(m, "/new")
	m.Update(back)
	if c.draft != "two" || m.input.Value() != "" {
		t.Fatalf("draft %q, input %q", c.draft, m.input.Value())
	}
	m.Update(unsteeredMsg{id: c.contextID, text: "one"})
	if c.draft != "one\ntwo" {
		t.Fatalf("draft %q", c.draft)
	}

	m.Update(unsteeredMsg{id: m.chat.contextID})
	if m.input.Value() != "" || m.notice != "new conversation" {
		t.Fatalf("nothing was taken back: input %q, notice %q", m.input.Value(), m.notice)
	}
	m.Update(unsteeredMsg{id: m.chat.contextID, text: "lost", err: errors.New("the node is gone")})
	if m.input.Value() != "" || m.notice != "the node is gone" || !m.noticeErr {
		t.Fatalf("input %q, notice %q", m.input.Value(), m.notice)
	}
}

// A message the node does not take is back in the input, with why.
func TestSteeringFails(t *testing.T) {
	m, b := newModel(t)
	defer close(steerable(b))
	c := m.chat
	drive(t, m, say(t, m, "ping"), func() bool { return c.doing == "asking beta" })
	b.fail = map[string]error{"steer": errors.New("the node is gone")}
	drive(t, m, say(t, m, "also this"), func() bool { return m.notice != "" })
	if m.input.Value() != "also this" || m.notice != "the node is gone" || !c.pending || len(c.queued) != 0 {
		t.Fatalf("input %q, notice %q, pending %v", m.input.Value(), m.notice, c.pending)
	}
}

// The queue follows what the node says of it, also when it says more than
// there is.
func TestQueueEvents(t *testing.T) {
	m, _ := newModel(t)
	c := m.chat
	m.apply(c, Event{Kind: plugin.SessionUnqueued}, true)
	m.apply(c, Event{Kind: plugin.SessionQueued, Text: "a"}, true)
	m.apply(c, Event{Kind: plugin.SessionQueued, Text: "b"}, true)
	m.apply(c, Event{Kind: plugin.SessionQueued, Text: "c"}, true)
	m.apply(c, Event{Kind: plugin.SessionUnqueued, Text: "c"}, true)
	if strings.Join(c.queued, ",") != "a,b" {
		t.Fatalf("queued %q", c.queued)
	}
	m.apply(c, Event{Kind: plugin.SessionMessage, Text: "a"}, true)
	if strings.Join(c.queued, ",") != "b" || len(c.entries) != 1 {
		t.Fatalf("queued %q, entries %+v", c.queued, c.entries)
	}
}
