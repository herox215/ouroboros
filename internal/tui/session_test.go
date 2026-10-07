package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"ouroboros/plugin"
)

// view is what m shows, without colors.
func view(m *Model) string { return ansi.Strip(m.View().Content) }

// sessionLines are the lines of the sessions screen that list a session,
// without the border and the spaces around them.
func sessionLines(m *Model) []string {
	var lines []string
	listing := false
	for _, l := range strings.Split(view(m), "\n") {
		l = strings.TrimSpace(strings.Trim(strings.TrimSpace(l), "│"))
		switch {
		case strings.HasPrefix(l, "Sessions"):
			listing = true
		case strings.HasSuffix(l, " sessions") || strings.HasSuffix(l, "1 session"):
			listing = false
		case listing && l != "":
			lines = append(lines, l)
		}
	}
	return lines
}

// ctrlS is the press of ctrl+s.
var ctrlS = tea.KeyPressMsg(tea.Key{Code: 's', Mod: tea.ModCtrl})

// gated makes b answer a message that starts with "wait" only once release
// is closed, and every other one at once.
func gated(b *fakeBackend) (release chan struct{}) {
	release = make(chan struct{})
	b.ask = func(ctx context.Context, step func(Step), _ func(string)) (string, error) {
		step(Step{Text: "asking beta"})
		select {
		case <-release:
			return "beta is fine", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return release
}

// /session lists the sessions, the one shown marked and the latest first;
// one whose answer is on its way moves behind its title. enter shows the one
// selected, with its history and the answer on its way, and what was typed
// for each stays with it.
func TestSessionsScreen(t *testing.T) {
	m, b := newModel(t)
	answered(t, m, say(t, m, "first question"))
	first := m.chat
	release := gated(b)
	drive(t, m, run(m, "/new"), func() bool { return m.chat != first })
	drive(t, m, say(t, m, "wait for beta"), func() bool { return m.chat.doing == "asking beta" })
	second := m.chat
	drive(t, m, run(m, "/new"), func() bool { return m.chat != second })
	m.input.SetValue("half a thought")

	_, cmd := m.Update(ctrlS)
	if m.screen != screenSessions || cmd == nil || m.input.Value() != "half a thought" {
		t.Fatalf("screen %v, input %q", m.screen, m.input.Value())
	}
	m.Update(cmd())
	lines := sessionLines(m)
	if len(lines) != 3 || lines[0] != "▸ new conversation  · shown" ||
		!strings.HasPrefix(lines[1], "wait for beta ") || !strings.HasPrefix(lines[2], "first question  ") {
		t.Fatalf("sessions %q\n%s", lines, view(m))
	}
	if !strings.Contains(view(m), "3 sessions") || strings.TrimSpace(footer(m)) != "↑↓ select · enter show · esc back · ctrl+c quit" {
		t.Fatalf("view:\n%s", view(m))
	}
	// Only the session whose answer is on its way moves.
	before := sessionLines(m)
	for range 3 {
		m.Update(m.spinner.Tick())
	}
	if after := sessionLines(m); after[1] == before[1] || after[2] != before[2] ||
		after[2] != "first question  "+m.chats[first.contextID].last.Format("15:04") {
		t.Fatalf("the answer on its way should move: %q, then %q", before, after)
	}

	// Down to the session on its way, and show it.
	press(m, "up", "down")
	if !strings.HasPrefix(sessionLines(m)[1], "▸ wait for beta") {
		t.Fatalf("sessions %q", sessionLines(m))
	}
	m.Update(press(m, "enter"))
	if m.screen != screenChat || m.chat != second || m.input.Value() != "" || !strings.Contains(view(m), "asking beta") {
		t.Fatalf("shown %+v:\n%s", m.chat, view(m))
	}
	// Its answer comes in here.
	close(release)
	drive(t, m, nil, func() bool { return !second.pending })
	if !strings.Contains(view(m), "beta is fine") || m.notice != "" {
		t.Fatalf("view:\n%s", view(m))
	}

	// Back to the new one: what was typed there is back.
	m.Update(ctrlS)
	press(m, "up", "up", "up")
	m.Update(press(m, "enter"))
	if m.input.Value() != "half a thought" || len(m.chat.entries) != 0 {
		t.Fatalf("input %q, entries %+v", m.input.Value(), m.chat.entries)
	}
	// The one shown is shown on.
	m.Update(ctrlS)
	if cmd := press(m, "enter"); cmd != nil || m.screen != screenChat || m.input.Value() != "half a thought" {
		t.Fatalf("screen %v, input %q", m.screen, m.input.Value())
	}
	m.Update(ctrlS)
	press(m, "down", "down", "down", "down", "esc")
	if m.screen != screenChat || m.input.Value() != "half a thought" {
		t.Fatal("esc should go back to the chat")
	}
}

// What happens in a session that is not shown lands there; its answer, or
// why there is none, is told in the footer.
func TestBackgroundSessions(t *testing.T) {
	m, b := newModel(t)
	release := gated(b)
	drive(t, m, say(t, m, "wait for beta"), func() bool { return m.chat.doing == "asking beta" })
	old := m.chat
	drive(t, m, run(m, "/new"), func() bool { return true })
	close(release)
	drive(t, m, nil, func() bool { return !old.pending })
	if m.notice != "answered in “wait for beta” · /session" || m.noticeErr || len(m.chat.entries) != 0 {
		t.Fatalf("notice %q, shown %+v", m.notice, m.chat.entries)
	}

	b.ask, b.err = nil, errors.New("the LLM is down")
	shown := m.chat
	m.switchTo(old)
	drive(t, m, say(t, m, "again"), func() bool { return old.pending })
	m.switchTo(shown)
	drive(t, m, nil, func() bool { return !old.pending })
	if m.notice != "no answer in “wait for beta” · /session" || !m.noticeErr {
		t.Fatalf("notice %q", m.notice)
	}
}

// The sessions another front end started are listed and followed too.
func TestSessionsOfOtherFrontEnds(t *testing.T) {
	m, b := newModel(t)
	gate := gated(b)
	ctx := context.Background()
	if err := b.sessions.Ask(ctx, "elsewhere", "wait, asked in ouro chat"); err != nil {
		t.Fatal(err)
	}
	drive(t, m, m.loadSessions, func() bool { c := m.chats["elsewhere"]; return c != nil && c.doing == "asking beta" })
	c := m.chats["elsewhere"]
	if c.title != "wait, asked in ouro chat" || !c.pending {
		t.Fatalf("the session of another front end: %+v", c)
	}
	// Known already, it is not watched twice.
	if cmd := m.sessionsLoaded(sessionsMsg{list: []plugin.SessionInfo{{ID: "elsewhere"}}}); cmd != nil {
		if msg := cmd(); msg != nil {
			t.Fatalf("watched again: %T", msg)
		}
	}
	close(gate)
	drive(t, m, nil, func() bool { return !c.pending })

	// Without a node the list stays as it is.
	b.fail = map[string]error{"sessions": errors.New("the node is not running")}
	m.Update(m.loadSessions())
	if len(m.chats) != 2 {
		t.Fatalf("sessions %v", m.chats)
	}
}

// A session whose events stop coming, as when the node stops, is watched
// again in a while; one that cannot be watched is tried again. A UI that
// quit tries nothing more.
func TestSessionsAreWatchedAgain(t *testing.T) {
	m, b := newModel(t)
	rewatchDelay = time.Millisecond
	t.Cleanup(func() { rewatchDelay = time.Second })
	answered(t, m, say(t, m, "ping"))
	c := m.chat

	// The node stops: the events end.
	_, cmd := m.Update(unwatchedMsg{c.contextID})
	if c.watching || cmd == nil {
		t.Fatal("the session should be watched again")
	}
	msg := cmd()
	if msg != (rewatchMsg{c.contextID}) {
		t.Fatalf("got %#v", msg)
	}
	b.fail = map[string]error{"watch": errors.New("the node is not running")}
	_, cmd = m.Update(msg)
	_, cmd = m.Update(cmd())
	if c.watching || cmd == nil || cmd() != (rewatchMsg{c.contextID}) {
		t.Fatal("a session that cannot be watched should be tried again")
	}
	b.fail = nil
	_, cmd = m.Update(rewatchMsg{c.contextID})
	drive(t, m, cmd, func() bool { return c.watching && len(c.entries) == 2 })
	if !strings.Contains(view(m), "pong") {
		t.Fatalf("the history after watching again:\n%s", view(m))
	}

	// A session the UI no longer knows is not watched.
	if _, cmd := m.Update(rewatchMsg{"gone"}); cmd != nil {
		t.Fatal("nothing to watch")
	}
	m.quit()
	if _, cmd := m.Update(unwatchedMsg{c.contextID}); cmd != nil {
		t.Fatal("a UI that quit watches nothing")
	}
}

// A message the node does not take for a session that is not shown any more
// waits there to be sent again.
func TestRefusedMessageOfAnotherSession(t *testing.T) {
	m, b := newModel(t)
	b.fail = map[string]error{"ask": plugin.ErrBusy}
	cmd := say(t, m, "hello")
	old := m.chat
	m.newConversation()
	drive(t, m, cmd, func() bool { return m.noticeErr })
	if old.pending || old.draft != "hello" || m.input.Value() != "" || m.notice != plugin.ErrBusy.Error() {
		t.Fatalf("old %+v, input %q, notice %q", old, m.input.Value(), m.notice)
	}
}

// A session whose last event was on another day says which.
func TestWhen(t *testing.T) {
	now := time.Date(2026, 10, 7, 9, 0, 0, 0, time.Local)
	if got := when(now.Add(-time.Hour), now); got != "08:00" {
		t.Errorf("today: %q", got)
	}
	if got := when(now.Add(-24*time.Hour), now); got != "Oct 6 09:00" {
		t.Errorf("yesterday: %q", got)
	}
	if got := when(now.AddDate(-1, 0, 0), now); got != "Oct 7 09:00" {
		t.Errorf("a year ago: %q", got)
	}
}

// The sessions screen lists those without a message first, then the latest;
// sessions of the same time in the order of their IDs. A call of a tool
// nothing is shown of is left out of the history.
func TestSessionRows(t *testing.T) {
	m, _ := newModel(t)
	at := time.Date(2026, 10, 7, 9, 0, 0, 0, time.Local)
	m.chats = map[string]*conversation{}
	shown := m.conversation("z")
	m.chat = shown
	m.conversation("y").draft = "half a thought"
	m.conversation("b").title, m.conversation("b").last = "b", at
	m.conversation("a").title, m.conversation("a").last = "a", at
	m.conversation("c").title, m.conversation("c").last = "c", at.Add(time.Minute)
	m.conversation("x") // nothing in it, and not shown
	var ids []string
	for _, c := range m.sessionRows() {
		ids = append(ids, c.contextID)
	}
	if strings.Join(ids, " ") != "y z c a b" {
		t.Fatalf("rows %v", ids)
	}

	m.Update(eventMsg{id: "z", event: Event{Kind: plugin.SessionMessage, Text: "hi"}})
	m.Update(eventMsg{id: "z", event: Event{Kind: plugin.SessionStep}})
	if len(shown.entries) != 1 || shown.round != 0 {
		t.Fatalf("entries %+v", shown.entries)
	}
}
