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

// sessionLines are the lines of the sessions dialog that list a session,
// without the border and the spaces around them.
func sessionLines(m *Model) []string {
	var lines []string
	listing := false
	for _, l := range strings.Split(ansi.Strip(drawnOf(m.sessions.view(m))), "\n") {
		l = strings.Trim(l, " │")
		switch {
		case strings.HasPrefix(l, "Sessions"):
			listing = true
		case strings.HasSuffix(l, " sessions") || strings.HasSuffix(l, "1 session") || strings.HasSuffix(l, " conversation") || strings.HasSuffix(l, " conversations"):
			listing = false
		case listing && strings.HasPrefix(l, "find "): // what is typed filters the list
		case listing && strings.HasPrefix(l, "nothing here"):
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
	if !strings.Contains(view(m), "3 sessions") || strings.TrimSpace(footer(m)) != "type to filter · ↑↓ select · enter show · ctrl+d delete · tab yours/peers · esc back" {
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

	// Up goes around to the last, and down from there to the session on
	// its way, which is then shown.
	press(m, "up", "down", "down")
	if !strings.HasPrefix(sessionLines(m)[1], "▸ wait for beta") {
		t.Fatalf("sessions %q", sessionLines(m))
	}
	m.Update(press(m, "enter"))
	m.now = func() time.Time { return time.Now().Add(toolShown) } // the tool has run long enough to be named
	if m.screen != screenChat || m.chat != second || m.input.Value() != "" || !strings.Contains(view(m), "ASKING BETA") {
		t.Fatalf("shown %+v:\n%s", m.chat, view(m))
	}
	// Its answer comes in here.
	close(release)
	drive(t, m, nil, func() bool { return !second.pending })
	if !strings.Contains(view(m), "beta is fine") || m.notice != "" {
		t.Fatalf("view:\n%s", view(m))
	}

	// Up goes around to the one shown, where what was typed is back.
	m.Update(ctrlS)
	press(m, "up")
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

	// The node stops: the events end, and with them the answer on its way.
	c.pending, c.running, c.doing = true, true, "asking beta"
	_, cmd := m.Update(unwatchedMsg{c.contextID})
	if c.watching || cmd == nil {
		t.Fatal("the session should be watched again")
	}
	if c.pending || c.running || c.doing != "" || strings.Contains(view(m), "thinking") {
		t.Fatalf("the answer is still on its way:\n%s", view(m))
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
// nothing is shown of counts, regardless of its name.
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
	for _, c := range m.sessions.rows(m) {
		ids = append(ids, c.contextID)
	}
	if strings.Join(ids, " ") != "y z c a b" {
		t.Fatalf("rows %v", ids)
	}

	m.Update(eventMsg{id: "z", event: Event{Kind: plugin.SessionMessage, Text: "hi"}})
	// Archived run events count just like unknown or direct tools.
	for _, tool := range []string{"run", "unknown", "bash"} {
		m.Update(eventMsg{id: "z", event: Event{Kind: plugin.SessionStep, Tool: tool}})
		m.Update(eventMsg{id: "z", event: Event{Kind: plugin.SessionStep, Tool: tool, Done: true, Failed: true}})
	}
	if len(shown.entries) != 2 || shown.summaries()[1] != "Used 3 tools, 3 failed" {
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

// A session is named for its topic once the node names it: as it happens,
// as it happened before, and in the list of a session that is not watched.
func TestSessionTitle(t *testing.T) {
	m, _ := newModel(t)
	c := m.conversation("named")
	m.apply(c, Event{Kind: plugin.SessionMessage, Text: "hi, plan my garden"}, true)
	m.apply(c, Event{Kind: plugin.SessionTitle, Text: "Garden plans"}, true)
	if c.title != "Garden plans" {
		t.Fatalf("title %q", c.title)
	}
	m.replay(c, []Event{{Kind: plugin.SessionMessage, Text: "hi"}, {Kind: plugin.SessionAnswer, Text: "ho"}, {Kind: plugin.SessionTitle, Text: "Greeting"}})
	if c.title != "Greeting" {
		t.Fatalf("replayed title %q", c.title)
	}
	kept := m.conversation("kept")
	m.sessionsLoaded(sessionsMsg{list: []plugin.SessionInfo{{ID: "kept", Title: "hi"}}})
	m.sessionsLoaded(sessionsMsg{list: []plugin.SessionInfo{{ID: "kept", Title: "Greeting"}}})
	if kept.title != "Greeting" {
		t.Fatalf("listed title %q", kept.title)
	}
	m.sessionsLoaded(sessionsMsg{list: []plugin.SessionInfo{{ID: "kept"}}})
	if kept.title != "Greeting" {
		t.Fatalf("a list without a title took it: %q", kept.title)
	}
}

// d pressed twice on the sessions screen deletes the session selected; a key
// in between starts over. One whose answer is on its way is not deleted, nor
// is one without a message. The session shown gives way to the thread, and
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
	if cmd := press(m, "ctrl+d"); cmd != nil || m.notice != "nothing to delete here yet" {
		t.Fatalf("a new conversation: %q", m.notice)
	}
	if cmd := press(m, "down", "ctrl+d", "ctrl+d"); cmd != nil || m.notice != "an answer is on its way there: /cancel it first" || !m.noticeErr {
		t.Fatalf("on its way: %q", m.notice)
	}
	if cmd := press(m, "down", "ctrl+d"); cmd != nil || m.sessions.deleting != first || m.notice != "press ctrl+d again to delete “first question” with its history" {
		t.Fatalf("first ctrl+d: %q", m.notice)
	}
	if cmd := press(m, "up", "down", "ctrl+d"); cmd != nil || m.sessions.deleting != first {
		t.Fatal("a key in between starts over")
	}
	cmd := press(m, "ctrl+d")
	if cmd == nil {
		t.Fatal("the second ctrl+d deletes")
	}
	m.Update(cmd())
	if _, ok := m.chats[first.contextID]; ok || m.notice != "deleted “first question”" || m.sessions.sel != 1 {
		t.Fatalf("notice %q, sel %d, chats %v", m.notice, m.sessions.sel, m.chats)
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
	press(m, "ctrl+d")
	_, watch := m.Update(press(m, "ctrl+d")())
	drive(t, m, watch, func() bool { return true })
	if m.chat.contextID != plugin.ThreadID || m.input.Value() != "typed" || len(m.chat.entries) != 0 {
		t.Fatalf("shown %+v, input %q", m.chat, m.input.Value())
	}

	// What the node says against it is told.
	press(m, "esc")
	m.input.Reset()
	drive(t, m, run(m, "/new"), func() bool { return m.chat.contextID != plugin.ThreadID })
	drive(t, m, say(t, m, "third"), func() bool { return !m.chat.pending && m.chat.title != "" })
	b.fail = map[string]error{"delete": errors.New("the node is not running")}
	m.Update(ctrlS)
	press(m, "ctrl+d")
	m.Update(press(m, "ctrl+d")())
	if m.notice != "not deleted: the node is not running" || !m.noticeErr || m.chats[m.chat.contextID] == nil {
		t.Fatalf("notice %q", m.notice)
	}
}

// tab lists the conversations of peers with the agent. enter shows one, only
// to read: who wrote what, and nothing can be sent there. d pressed twice
// deletes it. What was typed stays with the session of the owner.
func TestPeerConversations(t *testing.T) {
	m, b := newModel(t)
	now := time.Now() // a time of today is shown without its date
	at := time.Date(now.Year(), now.Month(), now.Day(), 14, 0, 0, 0, time.Local)
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
	m.Update(m.sessions.loadPeers(m))
	press(m, "tab")
	lines := sessionLines(m)
	if !m.sessions.peerTab || len(lines) != 2 || lines[0] != "▸ beta: hello alpha  14:00" || !strings.Contains(view(m), "Sessions · Peers  how peers talked with alpha, only to read") ||
		!strings.Contains(view(m), "2 conversations") {
		t.Fatalf("peers %q\n%s", lines, view(m))
	}

	m.Update(press(m, "enter")())
	c := m.chat
	if m.screen != screenChat || c.peer != "beta" || c.pending || m.input.Value() != "" || mine.draft != "typed for mine" {
		t.Fatalf("shown %+v, input %q", c, m.input.Value())
	}
	if v := view(m); !strings.Contains(v, "beta · only to read") || !strings.Contains(v, "beta · 14:00") || !strings.Contains(v, "hello beta") {
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
	m.Update(m.sessions.loadPeers(m))
	if !m.sessions.peerTab || m.sessions.sel != 0 || m.sessions.peers[0] != c {
		t.Fatalf("selected %d of %+v", m.sessions.sel, m.sessions.peers)
	}
	// What the node does not give is said, and the list stays.
	b.fail = map[string]error{"read": errors.New("gone"), "peers": errors.New("not running")}
	m.Update(m.sessions.loadPeers(m))
	press(m, "down")
	m.Update(press(m, "enter")())
	if m.notice != "not read: gone" || m.chat != c || len(m.sessions.peers) != 2 {
		t.Fatalf("notice %q", m.notice)
	}
	b.fail = nil

	// Deleted, the one shown gives way to a new session.
	m.Update(ctrlS)
	if cmd := press(m, "ctrl+d"); cmd != nil || m.notice != "press ctrl+d again to delete the conversation of beta “hello alpha”" {
		t.Fatalf("first ctrl+d: %q", m.notice)
	}
	_, watch := m.Update(press(m, "ctrl+d")())
	drive(t, m, watch, func() bool { return true })
	if len(b.peers) != 1 || len(m.sessions.peers) != 1 || m.chat.peer != "" || m.chat == mine || m.notice != "deleted “hello alpha”" {
		t.Fatalf("peers %+v, shown %+v, notice %q", m.sessions.peers, m.chat, m.notice)
	}

	// From a conversation of a peer, what is typed goes along to a session.
	m.Update(press(m, "enter")())
	m.input.SetValue("more ")
	m.Update(ctrlS)
	press(m, "tab")
	rows := m.sessions.rows(m)
	m.sessions.sel = slices.Index(rows, mine)
	m.Update(press(m, "enter"))
	if m.chat != mine || m.input.Value() != "more typed for mine" {
		t.Fatalf("shown %+v, input %q", m.chat, m.input.Value())
	}

	// Without peers there is nothing to select.
	b.peers = nil
	m.Update(ctrlS)
	m.Update(m.sessions.loadPeers(m))
	press(m, "tab")
	if cmd := press(m, "ctrl+d", "enter", "up"); cmd != nil || len(sessionLines(m)) != 0 || !strings.Contains(view(m), "0 conversations") {
		t.Fatalf("empty:\n%s", view(m))
	}
	press(m, "esc")
	if m.screen != screenChat {
		t.Fatal("esc goes back")
	}
}

// A session that is built anew still waits for its answer only if it did
// before and nothing of it is known yet: one that did not wait does not
// start to, and what is known says itself whether an answer is on its way.
func TestReplayKeepsWaitingOnlyForWhatWaited(t *testing.T) {
	m, _ := newModel(t)
	idle := &conversation{contextID: "idle"}
	if m.replay(idle, nil); idle.pending {
		t.Fatal("a session that did not wait is waiting")
	}
	asked := &conversation{contextID: "asked", pending: true, art: 3}
	if m.replay(asked, nil); !asked.pending || asked.art != 3 {
		t.Fatalf("a session that waited: pending %v, art %d", asked.pending, asked.art)
	}
	answered := &conversation{contextID: "answered", pending: true}
	m.replay(answered, []Event{{Kind: plugin.SessionMessage, Text: "hi"}, {Kind: plugin.SessionAnswer, Text: "ho"}})
	if answered.pending {
		t.Fatal("a session whose answer is known is waiting")
	}
}

// A session built anew without events is pending if it was; with events,
// they tell if it is.
func TestReplayIsPendingOnlyWithoutEvents(t *testing.T) {
	m, _ := newModel(t)
	for _, tc := range []struct {
		pending bool
		past    []Event
		want    bool
	}{
		{true, nil, true},
		{false, nil, false},
		{true, []Event{{Kind: plugin.SessionSay}}, false},
	} {
		c := &conversation{contextID: "c", pending: tc.pending}
		if m.replay(c, tc.past); c.pending != tc.want {
			t.Errorf("pending %v with %d events: %v", tc.pending, len(tc.past), c.pending)
		}
	}
}

// The sessions after one that is watched are listed too.
func TestSessionsAfterAWatchedOne(t *testing.T) {
	m, _ := newModel(t)
	m.chat.watching = true
	m.sessionsLoaded(sessionsMsg{list: []plugin.SessionInfo{{ID: m.chat.contextID, Title: "shown"}, {ID: "other", Title: "of another front end"}}})
	if c := m.chats["other"]; c == nil || c.title != "of another front end" || m.chat.title != "" {
		t.Fatalf("listed %+v", m.chats)
	}
}

// The conversations of peers listed anew leave the selection among the
// sessions as it is; among no peers there is none to follow.
func TestPeersListedAnewKeepTheSelection(t *testing.T) {
	m, _ := newModel(t)
	m.conversation("a").title, m.conversation("b").title = "a", "b"
	m.Update(ctrlS)
	m.sessions.sel = 1
	peers := peersMsg{list: []plugin.SessionInfo{{ID: "peer-aa/c1", Peer: "beta", Title: "hello"}}}
	if m.sessions.peersLoaded(m, peers); m.sessions.sel != 1 {
		t.Fatalf("selected %d", m.sessions.sel)
	}
	m.sessions.peers = nil
	press(m, "tab")
	if m.sessions.peersLoaded(m, peers); !m.sessions.peerTab || m.sessions.sel != 0 || len(m.sessions.peers) != 1 {
		t.Fatalf("selected %d of %v", m.sessions.sel, m.sessions.peers)
	}
}

// down stops at the last session, and a selection past the end stands for
// the last one.
func TestSessionSelectionStopsAtTheLast(t *testing.T) {
	m, _ := newModel(t)
	m.conversation("a").title, m.conversation("a").last = "a", time.Now()
	m.Update(ctrlS)
	press(m, "down", "down", "down")
	if m.sessions.sel != 1 {
		t.Fatalf("selected %d", m.sessions.sel)
	}
	for _, key := range []string{"ctrl+d", "enter"} {
		m.screen, m.sessions.sel = screenSessions, 5
		press(m, key)
	}
	if m.chat != m.chats["a"] || m.notice != "" {
		t.Fatalf("shown %+v, notice %q", m.chat, m.notice)
	}
	m.sessions.peers = []*conversation{{contextID: "peer-aa/c1", peer: "beta", title: "hello"}}
	m.screen, m.sessions.peerTab, m.sessions.sel = screenSessions, true, 5
	if cmd := press(m, "enter"); cmd == nil {
		t.Fatal("the last conversation of a peer should be read")
	}
}

// An answer without words leaves nothing in the chat: not even its head, which
// would read as a message that got lost.
func TestEmptyAnswerLeavesNothing(t *testing.T) {
	m, _ := newModel(t)
	m.chat.add(entry{text: "hi", at: m.now()})
	m.apply(m.chat, Event{Kind: plugin.SessionAnswer, Text: " "}, true)
	if len(m.chat.entries) != 1 {
		t.Fatalf("an answer without words left an entry: %+v", m.chat.entries)
	}
	m.chat.add(entry{at: m.now()}) // of a history that holds no words either
	m.renderHistory(true)
	empty := m.history.GetContent()
	m.chat.entries = m.chat.entries[:1]
	m.renderHistory(true)
	if empty != m.history.GetContent() {
		t.Fatalf("an entry without words is shown:\n%s", ansi.Strip(empty))
	}
}

// The dot behind a session whose answer is on its way moves on every third
// tick of the spinner, and starts over.
func TestRunningFrame(t *testing.T) {
	m, _ := newModel(t)
	for ticks, want := range map[int]string{0: ".  ", 1: ".  ", 2: ".  ", 3: ".. ", 12: "  .", 14: "  .", 15: "   ", 18: ".  "} {
		if m.ticks = ticks; m.runningFrame() != want {
			t.Errorf("tick %d: %q", ticks, m.runningFrame())
		}
	}
}

// Of many sessions, the dialog lists those around the one selected, as many
// as fit.
func TestSessionsListFollowsTheSelection(t *testing.T) {
	m, _ := newModel(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 16})
	at := time.Now()
	for i := range 20 {
		c := m.conversation(fmt.Sprintf("s%02d", i))
		c.title, c.last = c.contextID, at.Add(-time.Duration(i)*time.Minute)
	}
	m.Update(ctrlS)
	titles := func() []string {
		var titles []string
		for _, l := range sessionLines(m) {
			title, selected := strings.CutPrefix(l, "▸ ")
			if title = strings.Fields(title)[0]; selected {
				title = "▸" + title
			}
			titles = append(titles, title)
		}
		return titles
	}
	// The session shown, new, is the first row; s00 the second.
	m.sessions.sel = 10
	if got := strings.Join(titles(), " "); got != "s06 s07 s08 ▸s09 s10 s11" {
		t.Fatalf("around the middle: %s", got)
	}
	m.sessions.sel = 20
	if got := strings.Join(titles(), " "); got != "s14 s15 s16 s17 s18 ▸s19" {
		t.Fatalf("at the last: %s", got)
	}
}

// What is typed in the dialog filters the list as it is typed: enter shows
// what is left of it, backspace takes a letter back, esc the whole filter
// and only then goes back to the chat.
func TestSessionsFilter(t *testing.T) {
	m, _ := newModel(t)
	at := time.Now()
	for _, title := range []string{"the cache of the prompt", "beta and the mesh"} {
		c := m.conversation("id-" + title)
		c.title, c.last = title, at
	}
	m.Update(ctrlS)
	if lines := sessionLines(m); len(lines) != 3 {
		t.Fatalf("listed %q", lines)
	}
	press(m, "m", "e", "s", "h")
	if lines := sessionLines(m); len(lines) != 1 || !strings.HasPrefix(lines[0], "▸ beta and the mesh") { // what is left is selected
		t.Fatalf("filtered %q", lines)
	}
	if v := view(m); !strings.Contains(v, "find mesh") || !strings.Contains(v, "1 of 3 sessions") {
		t.Fatalf("view:\n%s", v)
	}
	press(m, "backspace", "backspace", "backspace", "backspace")
	if lines := sessionLines(m); len(lines) != 3 || !strings.Contains(view(m), "3 sessions") {
		t.Fatalf("emptied %q", lines)
	}
	// A key that is no letter changes nothing.
	press(m, "c", "a")
	if cmd := press(m, "left"); cmd != nil || m.sessions.filter != "ca" {
		t.Fatalf("left: %q", m.sessions.filter)
	}
	press(m, "c", "h", "enter")
	if m.screen != screenChat || m.chat.title != "the cache of the prompt" {
		t.Fatalf("screen %v, shown %q", m.screen, m.chat.title)
	}
	// What is pasted filters the list by its first line.
	m.Update(ctrlS)
	if _, cmd := m.Update(tea.PasteMsg{Content: "mesh\nand more"}); cmd != nil || m.sessions.filter != "mesh" {
		t.Fatalf("pasted: %q", m.sessions.filter)
	}
	if lines := sessionLines(m); len(lines) != 1 || !strings.HasPrefix(lines[0], "▸ beta and the mesh") {
		t.Fatalf("filtered by what was pasted %q", lines)
	}
	press(m, "esc", "esc")
	// Nothing matches: the dialog says so and stays open.
	m.Update(ctrlS)
	press(m, "z", "z", "z")
	if len(sessionLines(m)) != 0 || !strings.Contains(view(m), "nothing here has “zzz” in it") {
		t.Fatalf("nothing:\n%s", view(m))
	}
	if press(m, "esc"); m.sessions.filter != "" || m.screen != screenSessions {
		t.Fatal("esc takes the filter back first")
	}
	if press(m, "esc"); m.screen != screenChat {
		t.Fatal("and then goes back to the chat")
	}
}

// A space is typed into the filter like a letter: the words of a title are
// found as they stand there.
func TestSessionsFilterTakesASpace(t *testing.T) {
	m, _ := newModel(t)
	for _, title := range []string{"the cache of the prompt", "beta and the mesh"} {
		c := m.conversation("id-" + title)
		c.title, c.last = title, time.Now()
	}
	m.Update(ctrlS)
	press(m, "t", "h", "e", " ", "m")
	if lines := sessionLines(m); m.sessions.filter != "the m" || len(lines) != 1 || !strings.HasPrefix(lines[0], "▸ beta and the mesh") {
		t.Fatalf("filter %q: %q", m.sessions.filter, lines)
	}
}

// What is typed filters the list by what was said in a session too: the node
// gives the sessions whose content holds it, and the list keeps them besides
// those whose title does.
func TestSessionsFilterSearchesTheContent(t *testing.T) {
	m, f := newModel(t)
	f.hits = []string{"said"}
	c := m.conversation("said")
	c.title, c.last = "the mesh of the peers", time.Now()
	m.Update(ctrlS)
	cmd := press(m, "q", "q", "q")
	if lines := sessionLines(m); len(lines) != 0 { // no title holds it, not even that of the chat
		t.Fatalf("before the node answers: %q", lines)
	}
	// What was found for another filter, and a search that failed, are
	// dropped.
	m.Update(findMsg{query: "qq", ids: []string{"said"}})
	m.Update(findMsg{query: "qqq", err: errors.New("the archive cannot be read")})
	if lines := sessionLines(m); len(lines) != 0 || m.sessions.hits != nil {
		t.Fatalf("with what was found for something else: %q", lines)
	}
	m.Update(cmd()) // what the node found for what is typed
	if lines := sessionLines(m); len(lines) != 1 || !strings.Contains(lines[0], "the mesh of the peers") {
		t.Fatalf("with the hits of the node: %q", lines)
	}
	// What was looked for already is not asked for again: a space after it
	// looks for the same.
	if cmd := press(m, " "); cmd != nil || m.sessions.filter != "qqq " || m.sessions.hitsFor != "qqq" {
		t.Fatalf("a space after it: %q", m.sessions.filter)
	}
	press(m, "backspace")
	press(m, "esc") // the filter is taken back, and with it what was found for it
	if m.sessions.hits != nil || m.sessions.hitsFor != "" || len(sessionLines(m)) != 2 {
		t.Fatalf("emptied: %v, %q", m.sessions.hits, sessionLines(m))
	}
}

// A title too long for the dialog is cut to what is left of the line.
func TestSessionTitleIsCut(t *testing.T) {
	m, _ := newModel(t)
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 30})
	m.chat.title = "a title much too long for the narrow screen"
	m.Update(ctrlS)
	if lines := sessionLines(m); !slices.Contains(lines, "▸ a title much too l…  · shown") {
		t.Fatalf("listed %q", lines)
	}
}

// Opening the sessions screen asks the node for its sessions and for the
// conversations of its peers.
func TestOpeningTheSessionsAsksForThePeers(t *testing.T) {
	m, b := newModel(t)
	b.peers = []plugin.SessionInfo{{ID: "peer/1", Peer: "luna", Title: "hello"}}
	batch, ok := m.openSessions()().(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatalf("opening asks for %d things", len(batch))
	}
	for _, cmd := range batch {
		m.Update(cmd())
	}
	if len(m.sessions.peers) != 1 || m.sessions.peers[0].peer != "luna" || m.screen != screenSessions {
		t.Fatalf("peers %+v", m.sessions.peers)
	}
}

// A round the agent said only blank lines in leaves no empty block in the
// chat: its calls go into the line below the message.
func TestBlankRoundsLeaveNoSpace(t *testing.T) {
	m, _ := newModel(t)
	c := m.chat
	m.apply(c, Event{Kind: plugin.SessionMessage, Text: "pack it"}, true)
	for range 3 {
		m.apply(c, Event{Kind: plugin.SessionSay, Text: "\n\n"}, true)
		m.apply(c, Event{Kind: plugin.SessionStep, Text: "reading a file"}, true)
		m.apply(c, Event{Kind: plugin.SessionStep, Text: "reading a file", Done: true}, true)
	}
	m.renderHistory(true)
	lines := strings.Split(ansi.Strip(m.history.GetContent()), "\n")
	if len(lines) != 4 || strings.TrimSpace(lines[2]) != "" || strings.TrimSpace(lines[3]) != "│ ↳ Used 3 tools" {
		t.Fatalf("the history:\n%s", strings.Join(lines, "\n"))
	}
}

// What an answer has written is drawn once and kept: only what is on its way
// is drawn again, and everything at another width.
func TestRenderedEntriesAreKept(t *testing.T) {
	m, _ := newModel(t)
	c := m.chat
	c.entries = []entry{{mine: true, text: "one"}, {text: "two"}}
	m.renderHistory(true)
	if c.shown.n != 2 || len(c.shown.blocks) == 0 {
		t.Fatalf("an answer that is over is kept whole: %+v", c.shown)
	}
	c.shown.blocks[0] = "kept"
	c.entries = append(c.entries, entry{mine: true, text: "three"})
	c.pending = true
	m.renderHistory(true)
	if !strings.Contains(ansi.Strip(m.history.GetContent()), "kept") {
		t.Fatal("what an answer wrote is drawn again")
	}
	if c.shown.n != 2 {
		t.Fatalf("the answer on its way is not kept: %d", c.shown.n)
	}
	m.history.SetWidth(m.history.Width() - 10)
	m.renderHistory(true)
	if strings.Contains(ansi.Strip(m.history.GetContent()), "kept") {
		t.Fatal("at another width everything is drawn anew")
	}
}

// A word of the answer is threaded in: it shows as glyphs of its own width
// and settles into its letters over the frames that follow.
func TestNewWordIsThreadedIn(t *testing.T) {
	m, _ := newModel(t)
	c := m.chat
	m.apply(c, Event{Kind: plugin.SessionMessage, Text: "hi"}, true)
	m.apply(c, Event{Kind: plugin.SessionAnswering, Text: "one two"}, true)
	m.renderHistory(true)
	first := ansi.Strip(m.history.GetContent())
	if strings.Contains(first, "two") || !strings.Contains(first, "one") {
		t.Fatalf("the newest word comes in as noise:\n%s", first)
	}
	for i := 0; i < settleFrames; i++ {
		m.renderHistory(true)
	}
	if shown := ansi.Strip(m.history.GetContent()); !strings.Contains(shown, "two") {
		t.Fatalf("the word does not come to rest:\n%s", shown)
	}
	// A word of Chinese is noise of the same width, or the line would jump.
	if got := threadIn("两个", 0); strings.Contains(got, "两") {
		t.Fatalf("the word shows as itself: %q", got)
	}
	if w := ansi.StringWidth(threadIn("两个", 0)); w != 4 {
		t.Fatalf("noise of a wide word takes %d cells, not 4", w)
	}
	// After a space no word is on its way: all of it stands.
	if got := threadIn("one two ", 0); got != "one two " {
		t.Fatalf("after a space: %q", got)
	}
}

// The answer grows in the chat as its pieces come, and a round that ends
// with tools takes back what it wrote meanwhile; the Answer of the end
// replaces the whole of it.
func TestAnswerGrowsAsItIsWritten(t *testing.T) {
	m, _ := newModel(t)
	c := m.chat
	m.apply(c, Event{Kind: plugin.SessionMessage, Text: "tell me"}, true)
	m.apply(c, Event{Kind: plugin.SessionAnswering, Text: "it was "}, true)
	m.apply(c, Event{Kind: plugin.SessionAnswering, Text: "a dark"}, true)
	if c.partial != "it was a dark" || !m.streaming {
		t.Fatalf("partial %q, streaming %v", c.partial, m.streaming)
	}
	for i := 0; i <= settleFrames; i++ { // the newest word first comes to rest
		m.renderHistory(true)
	}
	if shown := ansi.Strip(m.history.GetContent()); !strings.Contains(shown, "it was a dark") {
		t.Fatalf("the chat while the answer grows:\n%s", shown)
	}
	m.apply(c, Event{Kind: plugin.SessionStep, Text: "reading a file"}, true)
	if c.partial != "" {
		t.Fatalf("a round with tools leaves nothing of its words: %q", c.partial)
	}
	m.apply(c, Event{Kind: plugin.SessionAnswer, Text: "the end"}, true)
	m.renderHistory(true)
	if shown := ansi.Strip(m.history.GetContent()); !strings.Contains(shown, "the end") || strings.Contains(shown, "it was a dark") {
		t.Fatalf("the chat after the answer:\n%s", shown)
	}
}

// What the model thought goes with the round of the loop it thought in, which
// gets an entry for it if it said nothing yet. The chat shows the last lines
// of it, faint, below a head that says what they are; ctrl+r hides them, and
// shows them again.
func TestThoughtsGoWithTheirRound(t *testing.T) {
	m, _ := newModel(t)
	c := m.chat
	m.apply(c, Event{Kind: plugin.SessionMessage, Text: "think"}, true)
	m.apply(c, Event{Kind: plugin.SessionThought, Text: "first "}, true)
	m.apply(c, Event{Kind: plugin.SessionThought, Text: "idea"}, true)
	if len(c.entries) != 2 || c.entries[1].thought != "first idea" || c.entries[1].text != "" {
		t.Fatalf("the first round: %+v", c.entries)
	}
	m.apply(c, Event{Kind: plugin.SessionSay, Text: "let me look"}, true)
	m.apply(c, Event{Kind: plugin.SessionThought, Text: "second thought"}, true)
	if len(c.entries) != 3 || c.entries[2].thought != "second thought" || c.entries[2].text != "let me look" || c.entries[1].thought != "first idea" {
		t.Fatalf("the second round: %+v", c.entries)
	}
	m.apply(c, Event{Kind: plugin.SessionAnswer, Text: "done"}, true)
	m.renderHistory(true)
	shown := ansi.Strip(m.history.GetContent())
	if strings.Count(shown, "· thought") != 2 || !strings.Contains(shown, "first idea") || !strings.Contains(shown, "second thought") ||
		strings.Index(shown, "second thought") > strings.Index(shown, "let me look") {
		t.Fatalf("the thoughts:\n%s", shown)
	}
	ctrlR := tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl}
	m.Update(ctrlR)
	if shown := ansi.Strip(m.history.GetContent()); !m.thoughtsHidden || strings.Contains(shown, "thought") || !strings.Contains(shown, "let me look") || !strings.Contains(shown, "done") {
		t.Fatalf("hidden:\n%s", shown)
	}
	m.Update(ctrlR)
	if shown := ansi.Strip(m.history.GetContent()); m.thoughtsHidden || strings.Count(shown, "· thought") != 2 {
		t.Fatalf("shown again:\n%s", shown)
	}
}

// Of a long thought the chat shows its last lines, as one paragraph.
func TestThoughtBlockShowsTheLastLines(t *testing.T) {
	block := strings.Split(ansi.Strip(thoughtBlock(strings.Repeat("early words ", 40)+"\n\n  the  late   end", 40)), "\n")
	if len(block) != maxThought+1 || block[0] != "· thought" || !strings.HasSuffix(block[len(block)-1], "the late end") {
		t.Fatalf("the block: %q", block)
	}
	for _, l := range block[1:] {
		if !strings.HasPrefix(l, "  ") || ansi.StringWidth(l) > 40-4+2 {
			t.Errorf("a line of it: %q", l)
		}
	}
	if block := strings.Split(ansi.Strip(thoughtBlock("short", 10)), "\n"); len(block) != 2 || block[1] != "  short" {
		t.Errorf("a short one in a narrow chat: %q", block)
	}
}

// While the answer is written, every frame draws the chat anew and follows
// its end, unless the owner scrolled away from it; once the answer is over,
// the frames stop.
func TestAnswerFramesFollowTheEnd(t *testing.T) {
	m, _ := newModel(t)
	fill(m, 40)
	c := m.chat
	m.apply(c, Event{Kind: plugin.SessionMessage, Text: "tell me"}, true)
	tick := m.apply(c, Event{Kind: plugin.SessionAnswering, Text: "it was"}, true)
	if tick == nil || !m.streaming {
		t.Fatal("the first piece of the answer should start the frames")
	}
	start := time.Now()
	if _, ok := tick().(answerMsg); !ok || time.Since(start) < 40*time.Millisecond || time.Since(start) > 5*time.Second {
		t.Fatalf("a frame of the answer, after %v: it comes 20 times a second", time.Since(start))
	}
	if m.apply(c, Event{Kind: plugin.SessionAnswering, Text: " a dark"}, true) != nil {
		t.Fatal("the frames are started once")
	}
	_, next := m.Update(answerMsg{})
	if batch, ok := next().(tea.BatchMsg); !ok || len(batch) != 2 || m.glide == nil || m.glide.target != topLine(&m.history) {
		t.Fatalf("following the end: %T, %+v", next(), m.glide)
	}
	if !strings.Contains(ansi.Strip(m.history.GetContent()), "it was") {
		t.Fatalf("the chat with the answer:\n%s", ansi.Strip(m.history.GetContent()))
	}
	// Scrolled away from the end, it stays where it is.
	m.stopGlide(&m.history)
	m.history.SetYOffset(0)
	if _, next := m.Update(answerMsg{}); next == nil || m.glide != nil || m.history.YOffset() != 0 || !m.streaming {
		t.Fatalf("scrolled away: %+v at %d", m.glide, m.history.YOffset())
	}
	// A frame stops the clock when the chat is not on it, when the answer
	// wrote nothing since its last round, and when it is over.
	for _, tt := range []struct {
		streaming, pending bool
		partial            string
	}{
		{false, true, "it was"},
		{true, true, ""},
		{true, false, "it was"},
	} {
		m.streaming, c.pending, c.partial = tt.streaming, tt.pending, tt.partial
		if _, next := m.Update(answerMsg{}); next != nil || m.streaming {
			t.Errorf("%+v: the frames go on", tt)
		}
	}
	m.streaming, c.pending, c.partial = true, true, "it was"
	m.apply(c, Event{Kind: plugin.SessionAnswer, Text: "it was a dark night"}, true)
	if _, next := m.Update(answerMsg{}); next != nil || m.streaming {
		t.Fatal("the frames go on after the answer")
	}
}

// The report of a task that starts an answer, live, starts a new picture of
// the wait; one in an answer on its way, or replayed, does not.
func TestTaskReportStartsTheWait(t *testing.T) {
	m, _ := newModel(t)
	c := m.chat
	m.apply(c, Event{Kind: plugin.SessionTask, Text: "task 1 · read ✓ 1s"}, false)
	if c.art != 0 {
		t.Fatalf("a replayed report: picture %d", c.art)
	}
	c.pending = false
	m.apply(c, Event{Kind: plugin.SessionTask, Text: "task 2 · read ✓ 1s"}, true)
	if c.art != 1 || !c.pending {
		t.Fatalf("a live report: picture %d, pending %v", c.art, c.pending)
	}
	m.apply(c, Event{Kind: plugin.SessionTask, Text: "task 3 · read ✓ 1s"}, true)
	if c.art != 1 {
		t.Fatalf("a report in an answer on its way: picture %d", c.art)
	}
	if m.renderHistory(true); !strings.Contains(ansi.Strip(m.history.GetContent()), "TASK 3") {
		t.Fatalf("the reports:\n%s", ansi.Strip(m.history.GetContent()))
	}
}

// The answer of another session, or one replayed, puts the chat on no
// clock; a piece of the answer of the chat is drawn on its clock alone, and
// the chat no longer shows it once the session is not watched any more.
func TestAnswerPiecesAreDrawnOnTheClock(t *testing.T) {
	m, _ := newModel(t)
	other := m.conversation("other")
	m.apply(other, Event{Kind: plugin.SessionMessage, Text: "hi"}, true)
	if m.apply(other, Event{Kind: plugin.SessionAnswering, Text: "elsewhere"}, true) != nil || m.streaming {
		t.Fatal("the answer of another session put the chat on its clock")
	}
	c := m.chat
	m.apply(c, Event{Kind: plugin.SessionMessage, Text: "tell me"}, false)
	if m.apply(c, Event{Kind: plugin.SessionAnswering, Text: "replayed"}, false) != nil || m.streaming {
		t.Fatal("a replayed piece put the chat on its clock")
	}
	m.renderHistory(true)
	before := m.history.GetContent()
	m.event(eventMsg{id: c.contextID, event: Event{Kind: plugin.SessionAnswering, Text: " and more"}})
	if m.history.GetContent() != before || !m.streaming {
		t.Fatal("a piece of the answer was drawn at once, not on the clock")
	}
	for i := 0; i <= settleFrames; i++ {
		m.Update(answerMsg{})
	}
	if !strings.Contains(ansi.Strip(m.history.GetContent()), "replayed and more") {
		t.Fatalf("on the clock:\n%s", ansi.Strip(m.history.GetContent()))
	}
	m.unwatched(c.contextID)
	if strings.Contains(ansi.Strip(m.history.GetContent()), "replayed") {
		t.Fatalf("the watch ended, and the chat still shows the answer on its way:\n%s", ansi.Strip(m.history.GetContent()))
	}
}

// The newest word comes in over the frames and then stands; a new word
// starts the frames anew.
func TestAdvanceCountsTheFramesOfTheNewestWord(t *testing.T) {
	c := &conversation{partial: "one"}
	var frames []int
	for range settleFrames + 2 {
		frames = append(frames, c.advance())
	}
	c.partial = "one two"
	frames = append(frames, c.advance())
	if want := []int{0, 1, 2, 3, 3, 0}; !slices.Equal(frames, want) {
		t.Fatalf("frames %v, want %v", frames, want)
	}
}

// With a filter the list makes room for the line it is typed on: it shows
// two sessions fewer, cuts a long filter to the dialog, and says that nothing
// has the filter only where nothing has it.
func TestSessionsFilterTakesRoom(t *testing.T) {
	m, _ := newModel(t)
	at := time.Now()
	for i := range 12 {
		c := m.conversation(fmt.Sprintf("id-%d", i))
		c.title, c.last = fmt.Sprintf("session %d", i), at.Add(-time.Duration(i)*time.Minute)
	}
	m.Update(ctrlS)
	all := len(sessionLines(m))
	press(m, "s")
	if got := len(sessionLines(m)); got != all-2 || strings.Contains(view(m), "nothing here") {
		t.Fatalf("with a filter %d of %d lines:\n%s", got, all, view(m))
	}
	m.Update(tea.PasteMsg{Content: strings.Repeat("x", 60) + " the end"})
	if v := view(m); !strings.Contains(v, "find s"+strings.Repeat("x", 50)+"… ") || strings.Contains(v, "the end") {
		t.Fatalf("a long filter:\n%s", v)
	}
}
