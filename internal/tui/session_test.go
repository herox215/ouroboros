package tui

import (
	"context"
	"errors"
	"fmt"
	"slices"
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
		case strings.HasSuffix(l, " sessions") || strings.HasSuffix(l, "1 session") || strings.HasSuffix(l, " conversation") || strings.HasSuffix(l, " conversations"):
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
	if !strings.Contains(view(m), "3 sessions") || strings.TrimSpace(footer(m)) != "↑↓ select · enter show · d delete · tab yours/peers · esc back · ctrl+c quit" {
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

// Watch events and Ask responses are independent streams. Another client
// can start an answer before our rejected Ask response arrives. That error
// must restore our draft without hiding the node's already-observed work.
func TestRejectedAskPreservesObservedAnswer(t *testing.T) {
	for _, replay := range []bool{false, true} {
		for _, background := range []bool{false, true} {
			for _, finished := range []bool{false, true} {
				t.Run(fmt.Sprintf("replay=%v/background=%v/finished=%v", replay, background, finished), func(t *testing.T) {
					m, _ := newModel(t)
					m.input.SetValue("my draft")
					m.send()
					c := m.chat
					if background {
						m.newConversation()
					}
					events := []Event{{Kind: plugin.SessionMessage, Text: "from another client"}}
					if finished {
						events = append(events, Event{Kind: plugin.SessionAnswer, Text: "done"})
					}
					if replay {
						m.replay(c, events)
					} else {
						for _, e := range events {
							m.apply(c, e, true)
						}
					}
					m.Update(askedMsg{c.contextID, "my draft", plugin.ErrBusy})
					draft := m.input.Value()
					if background {
						draft = c.draft
					}
					if c.pending != !finished || draft != "my draft" {
						t.Fatalf("node running=%v, UI pending=%v, draft=%q", !finished, c.pending, draft)
					}
					if c.entries[0].text != "from another client" {
						t.Fatal("observed history changed", c.entries)
					}
					if background {
						m.switchTo(c)
					}
					if !finished && m.cancel() == nil {
						t.Fatal("the observed answer must still be cancellable")
					}
				})
			}
		}
	}
}

// A session the node lists is watched once it is shown, unless its answer is
// on its way: the node keeps many, and loads one when it is watched.
func TestKeptSessionsAreWatchedWhenShown(t *testing.T) {
	m, _ := newModel(t)
	if cmd := m.sessionsLoaded(sessionsMsg{list: []plugin.SessionInfo{{ID: "kept", Title: "from before"}}}); cmd != nil {
		t.Fatal("a session without an answer on its way is only listed")
	}
	c := m.chats["kept"]
	if c == nil || c.title != "from before" || c.watching {
		t.Fatalf("listed %+v", c)
	}
	if cmd := m.switchTo(c); cmd == nil || !c.watching {
		t.Fatal("shown, it is watched")
	}
}

// d pressed twice on the sessions screen deletes the session selected; a key
// in between starts over. One whose answer is on its way is not deleted, nor
// is one without a message. The session shown gives way to a new one, and
// what is typed stays.
func TestDeleteSession(t *testing.T) {
	m, b := newModel(t)
	answered(t, m, say(t, m, "first question"))
	first := m.chat
	release := gated(b)
	drive(t, m, run(m, "/new"), func() bool { return m.chat != first })
	drive(t, m, say(t, m, "wait for beta"), func() bool { return m.chat.doing == "asking beta" })
	second := m.chat
	drive(t, m, run(m, "/new"), func() bool { return m.chat != second })

	m.Update(ctrlS)
	if cmd := press(m, "d"); cmd != nil || m.notice != "nothing to delete here yet" {
		t.Fatalf("a new conversation: %q", m.notice)
	}
	if cmd := press(m, "down", "d", "d"); cmd != nil || m.notice != "an answer is on its way there: /cancel it first" || !m.noticeErr {
		t.Fatalf("on its way: %q", m.notice)
	}
	if cmd := press(m, "down", "d"); cmd != nil || m.deleting != first || m.notice != "press d again to delete “first question” with its history" {
		t.Fatalf("first d: %q", m.notice)
	}
	if cmd := press(m, "up", "down", "d"); cmd != nil || m.deleting != first {
		t.Fatal("a key in between starts over")
	}
	cmd := press(m, "d")
	if cmd == nil {
		t.Fatal("the second d deletes")
	}
	m.Update(cmd())
	if _, ok := m.chats[first.contextID]; ok || m.notice != "deleted “first question”" || m.sessionSel != 1 {
		t.Fatalf("notice %q, sel %d, chats %v", m.notice, m.sessionSel, m.chats)
	}
	if list, _ := b.sessions.List(context.Background()); len(list) != 1 || list[0].ID != second.contextID {
		t.Fatalf("the node has %+v", list)
	}
	// What is not known any more is nothing to forget.
	if cmd := m.deleted(deletedMsg{id: first.contextID}); cmd != nil {
		t.Fatal("deleted twice")
	}
	// Its events, which end, are not watched again.
	if cmd := m.unwatched(first.contextID); cmd != nil {
		t.Fatal("a deleted session is not watched again")
	}

	// The session shown.
	close(release)
	drive(t, m, nil, func() bool { return !second.pending })
	m.switchTo(second)
	m.input.SetValue("typed")
	m.Update(ctrlS)
	press(m, "d")
	_, watch := m.Update(press(m, "d")())
	drive(t, m, watch, func() bool { return true })
	if m.chat == second || m.chat.contextID == second.contextID || m.input.Value() != "typed" || len(m.chat.entries) != 0 {
		t.Fatalf("shown %+v, input %q", m.chat, m.input.Value())
	}

	// What the node says against it is told.
	press(m, "esc")
	drive(t, m, say(t, m, "third"), func() bool { return !m.chat.pending && m.chat.title != "" })
	b.fail = map[string]error{"delete": errors.New("the node is not running")}
	m.Update(ctrlS)
	press(m, "d")
	m.Update(press(m, "d")())
	if m.notice != "not deleted: the node is not running" || !m.noticeErr || m.chats[m.chat.contextID] == nil {
		t.Fatalf("notice %q", m.notice)
	}
}

// tab lists the conversations of peers with the agent. enter shows one, only
// to read: who wrote what, and nothing can be sent there. d pressed twice
// deletes it. What was typed stays with the session of the owner.
func TestPeerConversations(t *testing.T) {
	m, b := newModel(t)
	at := time.Date(2026, 10, 7, 14, 0, 0, 0, time.Local)
	b.peers = []plugin.SessionInfo{
		{ID: "peer-aa/c1", Peer: "beta", Title: "hello alpha", Last: at},
		{ID: "peer-aa/c2", Peer: "beta", Title: "second", Last: at.Add(-time.Hour)},
	}
	b.read = map[string][]Event{"peer-aa/c1": {
		{Kind: plugin.SessionMessage, Text: "hello alpha", At: at},
		{Kind: plugin.SessionAnswer, Text: "hello beta", At: at},
		{Kind: plugin.SessionMessage, Text: "still on its way", At: at},
	}}
	mine := m.chat
	m.input.SetValue("typed for mine")
	m.Update(ctrlS)
	m.Update(m.loadPeers())
	press(m, "tab")
	lines := sessionLines(m)
	if !m.peerTab || len(lines) != 2 || lines[0] != "▸ beta: hello alpha  14:00" || !strings.Contains(view(m), "Sessions · Peers  how peers talked with alpha, only to read") ||
		!strings.Contains(view(m), "2 conversations") {
		t.Fatalf("peers %q\n%s", lines, view(m))
	}

	m.Update(press(m, "enter")())
	c := m.chat
	if m.screen != screenChat || c.peer != "beta" || c.pending || m.input.Value() != "" || mine.draft != "typed for mine" {
		t.Fatalf("shown %+v, input %q", c, m.input.Value())
	}
	if v := view(m); !strings.Contains(v, "beta with alpha  · only to read") || !strings.Contains(v, "beta · 14:00") || !strings.Contains(v, "hello beta") {
		t.Fatalf("view:\n%s", v)
	}
	m.input.SetValue("hi")
	if cmd := press(m, "enter"); cmd != nil {
		t.Fatal("nothing is sent")
	}
	if m.notice != "this is how beta and alpha talked: it is only to read · /new" || !m.noticeErr || len(b.messages()) != 0 {
		t.Fatalf("notice %q, sent %v", m.notice, b.messages())
	}
	if id := m.Current(); id == c.contextID || len(id) != 36 {
		t.Fatalf("current %q", id)
	}
	if cmd := m.watch(c); cmd != nil {
		t.Fatal("a conversation of a peer is not watched")
	}

	// Back on the screen it is selected among the peers; listed anew, it
	// stays the one shown.
	m.Update(ctrlS)
	m.Update(m.loadPeers())
	if !m.peerTab || m.sessionSel != 0 || m.peers[0] != c {
		t.Fatalf("selected %d of %+v", m.sessionSel, m.peers)
	}
	// What the node does not give is said, and the list stays.
	b.fail = map[string]error{"read": errors.New("gone"), "peers": errors.New("not running")}
	m.Update(m.loadPeers())
	press(m, "down")
	m.Update(press(m, "enter")())
	if m.notice != "not read: gone" || m.chat != c || len(m.peers) != 2 {
		t.Fatalf("notice %q", m.notice)
	}
	b.fail = nil

	// Deleted, the one shown gives way to a new session.
	m.Update(ctrlS)
	if cmd := press(m, "d"); cmd != nil || m.notice != "press d again to delete the conversation of beta “hello alpha”" {
		t.Fatalf("first d: %q", m.notice)
	}
	_, watch := m.Update(press(m, "d")())
	drive(t, m, watch, func() bool { return true })
	if len(b.peers) != 1 || len(m.peers) != 1 || m.chat.peer != "" || m.chat == mine || m.notice != "deleted “hello alpha”" {
		t.Fatalf("peers %+v, shown %+v, notice %q", m.peers, m.chat, m.notice)
	}

	// From a conversation of a peer, what is typed goes along to a session.
	m.Update(press(m, "enter")())
	m.input.SetValue("more ")
	m.Update(ctrlS)
	press(m, "tab")
	rows := m.sessionRows()
	m.sessionSel = slices.Index(rows, mine)
	m.Update(press(m, "enter"))
	if m.chat != mine || m.input.Value() != "more typed for mine" {
		t.Fatalf("shown %+v, input %q", m.chat, m.input.Value())
	}

	// Without peers there is nothing to select.
	b.peers = nil
	m.Update(ctrlS)
	m.Update(m.loadPeers())
	press(m, "tab")
	if cmd := press(m, "d", "enter", "up"); cmd != nil || len(sessionLines(m)) != 0 || !strings.Contains(view(m), "0 conversations") {
		t.Fatalf("empty:\n%s", view(m))
	}
	press(m, "esc")
	if m.screen != screenChat {
		t.Fatal("esc goes back")
	}
}
