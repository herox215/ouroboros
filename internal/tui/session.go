package tui

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"ouroboros/plugin"
)

// The conversations of the chat are sessions of the node: it runs their
// answers and tells the UI what happens in them, see Backend.Watch. The UI
// watches every session it knows and shows one of them. The others go on in
// the background, and the command /session opens a screen that lists them,
// as ctrl+s does, which leaves what is typed in the input for the session
// shown: the arrow keys select one, enter shows it, d pressed twice deletes
// it, and esc goes back to the chat.
//
// The node keeps the sessions, also those from before it started. The UI
// watches a session once it shows it, or while an answer is on its way in
// it: the others are only listed, and the node loads them when they are
// watched.
//
// tab switches the screen to the conversations of peers with the agent:
// enter shows one, only to read, and d pressed twice deletes it.

// rewatchDelay is how long the UI waits before it watches a session again
// whose events stopped coming, as when the node stopped or starts anew.
// Tests shorten it.
var rewatchDelay = time.Second

type (
	// watchedMsg is what happened in a session so far, and where what
	// happens from now on comes from: see Backend.Watch.
	watchedMsg struct {
		id   string
		past []Event
		live <-chan Event
		err  error
	}
	// eventMsg is what happened in a session just now.
	eventMsg struct {
		id    string
		event Event
		live  <-chan Event
	}
	// unwatchedMsg says that the events of a session stopped coming.
	unwatchedMsg struct{ id string }
	// rewatchMsg is the time to watch a session again.
	rewatchMsg struct{ id string }
	// sessionsMsg are the sessions the node has.
	sessionsMsg struct {
		list []plugin.SessionInfo
		err  error
	}
	// deletedMsg says whether the session id was deleted.
	deletedMsg struct {
		id  string
		err error
	}
	// peersMsg are the conversations of peers the node has.
	peersMsg struct {
		list []plugin.SessionInfo
		err  error
	}
	// readMsg is what happened in the conversation of a peer c.
	readMsg struct {
		c      *conversation
		events []Event
		err    error
	}
)

// conversation returns the session id, which the UI knows from now on.
func (m *Model) conversation(id string) *conversation {
	c, ok := m.chats[id]
	if !ok {
		c = &conversation{contextID: id}
		m.chats[id] = c
	}
	return c
}

// watch asks for the events of the session c, unless they come already. A
// conversation of a peer is read, not watched.
func (m *Model) watch(c *conversation) tea.Cmd {
	if c == nil || c.watching || c.peer != "" {
		return nil
	}
	c.watching = true
	id := c.contextID
	return func() tea.Msg {
		past, live, err := m.backend.Watch(m.ctx, id)
		return watchedMsg{id, past, live, err}
	}
}

// watched shows what happened in a session so far and follows it. One that
// cannot be watched, as the node does not run, is tried again later.
func (m *Model) watched(msg watchedMsg) tea.Cmd {
	c := m.conversation(msg.id)
	if msg.err != nil {
		c.watching = false
		return m.rewatch(msg.id)
	}
	m.replay(c, msg.past)
	return next(msg.id, msg.live)
}

// next waits for what happens next in the session id.
func next(id string, live <-chan Event) tea.Cmd {
	return func() tea.Msg {
		e, ok := <-live
		if !ok {
			return unwatchedMsg{id}
		}
		return eventMsg{id, e, live}
	}
}

// unwatched watches a session again whose events stopped coming, unless
// it was deleted.
func (m *Model) unwatched(id string) tea.Cmd {
	c, ok := m.chats[id]
	if !ok {
		return nil
	}
	c.watching = false
	return m.rewatch(id)
}

// rewatch watches the session id again in a while, unless the UI quit.
func (m *Model) rewatch(id string) tea.Cmd {
	if m.ctx.Err() != nil {
		return nil
	}
	return tea.Tick(rewatchDelay, func(time.Time) tea.Msg { return rewatchMsg{id} })
}

// replay builds the session c anew from what happened in it.
func (m *Model) replay(c *conversation, past []Event) {
	*c = conversation{contextID: c.contextID, draft: c.draft, art: c.art, watching: true, pending: c.pending && len(past) == 0}
	for _, e := range past {
		m.apply(c, e, false)
	}
	if c == m.chat {
		m.renderHistory(true)
	}
}

// event shows what happened in a session just now and waits for what
// follows.
func (m *Model) event(msg eventMsg) tea.Cmd {
	c := m.conversation(msg.id)
	cmd := m.apply(c, msg.event, true)
	if c == m.chat {
		m.renderHistory(m.history.AtBottom())
	}
	return tea.Batch(cmd, next(msg.id, msg.live))
}

// apply adds what happened, e, to the session c. With live it happened just
// now, and is not replayed: an answer of a session that is not shown is told
// in the footer, and ouro starting anew makes the UI quit for it.
func (m *Model) apply(c *conversation, e Event, live bool) tea.Cmd {
	c.last, c.context, c.cache = e.At, e.Context, e.Cache
	switch e.Kind {
	case plugin.SessionMessage:
		c.add(entry{mine: true, text: e.Text, at: e.At})
		if live && !c.pending { // else the wait was shown, or is shown already
			c.art++
		}
		c.pending, c.running, c.round, c.doing = true, true, 0, ""
		c.title = cmp.Or(c.title, e.Text)
	case plugin.SessionSay:
		c.say(e)
	case plugin.SessionStep:
		c.step(e)
	case plugin.SessionAnswer, plugin.SessionFailed, plugin.SessionCanceled:
		c.end(e)
		if live && c != m.chat {
			verb := "answered"
			if e.Kind != plugin.SessionAnswer {
				verb = "no answer"
			}
			m.setNotice(verb+" in “"+short(c.title, 40)+"” · /session", e.Kind != plugin.SessionAnswer)
		}
	case plugin.SessionTitle:
		c.title = e.Text
	case plugin.SessionRestart:
		if live {
			m.restarts = e.Text
			return m.leave()
		}
	}
	return nil
}

// say begins a round of the loop: what the agent says along with the tools
// it asks for, which is not the answer, becomes part of the conversation, and
// the calls of those tools go below these words. A round the agent said
// nothing of gets its entry with its first call that is shown.
func (c *conversation) say(e Event) {
	c.round, c.doing = 0, ""
	if e.Text != "" {
		c.add(entry{text: e.Text, at: e.At})
		c.round = len(c.entries)
	}
}

// step notes what the agent is waiting for while the answer is pending. A
// call that starts goes into the entry of its round, which a round the agent
// said nothing of gets now; one that ended takes the place of its start,
// with what became of it. A call nothing is shown of is left out.
func (c *conversation) step(e Event) {
	if e.Text == "" {
		return
	}
	if c.round == 0 {
		c.add(entry{at: e.At})
		c.round = len(c.entries)
	}
	r := &c.entries[c.round-1]
	if !e.Done {
		r.calls = append(r.calls, call{text: e.Text})
	} else if i := r.started(e.Text); i >= 0 {
		r.calls[i] = call{text: e.Text, done: true}
	}
	c.doing = r.waitingFor()
}

// end shows how the answer ended: the answer, why it failed, or that it was
// stopped.
func (c *conversation) end(e Event) {
	c.pending, c.running, c.doing, c.round = false, false, "", 0 // the loop of this answer is over
	last := entry{text: e.Text, at: e.At}
	switch e.Kind {
	case plugin.SessionFailed:
		last.failed = true
	case plugin.SessionCanceled:
		last.text, last.failed = "stopped before it answered", true
	}
	c.add(last)
}

// short is text on one line, cut after n characters.
func short(text string, n int) string {
	return ansi.Truncate(strings.Join(strings.Fields(text), " "), n, "…")
}

// loadSessions asks for the sessions of the node.
func (m *Model) loadSessions() tea.Msg {
	list, err := m.backend.Sessions(m.ctx)
	return sessionsMsg{list, err}
}

// sessionsLoaded lists the sessions of the node the UI does not watch, such
// as those of another front end or from before the node started, and
// watches those whose answer is on its way. Without a node to ask, the UI
// goes on with those it knows.
func (m *Model) sessionsLoaded(msg sessionsMsg) tea.Cmd {
	var cmds []tea.Cmd
	for _, info := range msg.list {
		c := m.conversation(info.ID)
		if c.watching {
			continue
		}
		// What the list says, until the events say it all: a title it was
		// given meanwhile too.
		c.title, c.last, c.pending, c.running = cmp.Or(info.Title, c.title), info.Last, info.Running, info.Running
		if info.Running {
			cmds = append(cmds, m.watch(c))
		}
	}
	return tea.Batch(cmds...)
}

// openSessions opens the sessions screen, with the session shown selected:
// among the conversations of peers if it is one of them.
func (m *Model) openSessions() tea.Cmd {
	m.screen = screenSessions
	m.peerTab = m.chat.peer != ""
	m.sessionSel = max(0, slices.Index(m.sessionRows(), m.chat))
	return tea.Batch(m.loadSessions, m.loadPeers)
}

// loadPeers asks for the conversations of peers the node has.
func (m *Model) loadPeers() tea.Msg {
	list, err := m.backend.Peers(m.ctx)
	return peersMsg{list, err}
}

// peersLoaded lists the conversations of peers. The one shown stays as it
// is; without a node to ask, the list stays as it was.
func (m *Model) peersLoaded(msg peersMsg) {
	if msg.err != nil {
		return
	}
	selected := m.sessionRows()
	m.peers = nil
	for _, info := range msg.list {
		c := &conversation{contextID: info.ID, peer: info.Peer, title: info.Title, last: info.Last}
		if m.chat.contextID == info.ID {
			c = m.chat
		}
		m.peers = append(m.peers, c)
	}
	if m.peerTab && m.sessionSel < len(selected) {
		m.sessionSel = max(0, slices.Index(m.peers, selected[m.sessionSel]))
	}
}

// showPeer asks for what happened in the conversation of a peer c, to show
// it.
func (m *Model) showPeer(c *conversation) tea.Cmd {
	m.screen = screenChat
	id := c.contextID
	return func() tea.Msg {
		events, err := m.backend.Read(m.ctx, id)
		return readMsg{c, events, err}
	}
}

// read shows the conversation of a peer that was read. What is typed stays
// with the session that was shown.
func (m *Model) read(msg readMsg) {
	if msg.err != nil {
		m.setNotice("not read: "+msg.err.Error(), true)
		return
	}
	c := msg.c
	c.entries, c.round, c.doing = nil, 0, ""
	for _, e := range msg.events {
		m.apply(c, e, false)
	}
	c.pending = false // what is not over yet is shown as far as it got
	if m.chat.peer == "" {
		m.chat.draft = m.input.Value()
		m.input.Reset()
	}
	m.chat = c
	m.renderHistory(true)
}

// sessionRows are the sessions the sessions screen lists: those with a
// message or with something typed for them, and the one shown, the latest
// first. One without a message yet is the latest of all. Among the
// conversations of peers, those the node listed.
func (m *Model) sessionRows() []*conversation {
	if m.peerTab {
		return m.peers
	}
	var rows []*conversation
	for _, c := range m.chats {
		if c == m.chat || c.title != "" || c.draft != "" {
			rows = append(rows, c)
		}
	}
	slices.SortFunc(rows, func(a, b *conversation) int {
		switch {
		case a.last.IsZero() != b.last.IsZero():
			if a.last.IsZero() {
				return -1
			}
			return 1
		case !a.last.Equal(b.last):
			return b.last.Compare(a.last)
		}
		return strings.Compare(a.contextID, b.contextID)
	})
	return rows
}

// handleSessionsKey handles keys on the sessions screen: up and down select
// a session, enter shows it, d pressed twice deletes it, esc goes back to the
// chat.
func (m *Model) handleSessionsKey(msg tea.KeyPressMsg) tea.Cmd {
	rows := m.sessionRows()
	deleting := m.deleting
	m.deleting = nil
	switch msg.String() {
	case "tab":
		m.peerTab = !m.peerTab
		m.sessionSel = max(0, slices.Index(m.sessionRows(), m.chat))
		return nil
	}
	if len(rows) == 0 {
		if msg.String() == "esc" {
			m.screen = screenChat
		}
		return nil
	}
	switch msg.String() {
	case "d":
		return m.delete(rows[min(m.sessionSel, len(rows)-1)], deleting)
	case "esc":
		m.screen = screenChat
	case "up", "k":
		m.sessionSel = max(0, m.sessionSel-1)
	case "down", "j":
		m.sessionSel = min(len(rows)-1, m.sessionSel+1)
	case "enter":
		if c := rows[min(m.sessionSel, len(rows)-1)]; c.peer != "" {
			return m.showPeer(c)
		}
		return m.switchTo(rows[min(m.sessionSel, len(rows)-1)])
	}
	return nil
}

// switchTo shows the session c in the chat. What was typed for the session
// that was shown stays with it, and what was typed for c comes back; from a
// conversation of a peer, what is typed stays.
func (m *Model) switchTo(c *conversation) tea.Cmd {
	m.screen = screenChat
	if c == m.chat {
		return nil
	}
	if m.chat.peer != "" {
		m.chat = c
		m.input.SetValue(m.input.Value() + c.draft)
		c.draft = ""
		m.renderHistory(true)
		return m.watch(c)
	}
	m.chat.draft = m.input.Value()
	m.chat = c
	m.input.SetValue(c.draft)
	c.draft = ""
	m.renderHistory(true)
	return m.watch(c)
}

// delete deletes the session c once it is selected for it, which it is
// first: a session is deleted with the second d, not by a key pressed by
// mistake. One without a message is not in the node; one whose answer is on
// its way is not deleted.
func (m *Model) delete(c, selected *conversation) tea.Cmd {
	switch {
	case c.title == "":
		m.setNotice("nothing to delete here yet", false)
		return nil
	case c.pending:
		m.setNotice("an answer is on its way there: /cancel it first", true)
		return nil
	case c != selected && c.peer != "":
		m.deleting = c
		m.setNotice("press d again to delete the conversation of "+c.peer+" “"+short(c.title, 40)+"”", false)
		return nil
	case c != selected:
		m.deleting = c
		m.setNotice("press d again to delete “"+short(c.title, 40)+"” with its history", false)
		return nil
	}
	id := c.contextID
	return func() tea.Msg { return deletedMsg{id, m.backend.Delete(m.ctx, id)} }
}

// deleted forgets a session that was deleted. The session shown gives way to
// a new one.
func (m *Model) deleted(msg deletedMsg) tea.Cmd {
	if msg.err != nil {
		m.setNotice("not deleted: "+msg.err.Error(), true)
		return nil
	}
	c, ok := m.chats[msg.id]
	if i := slices.IndexFunc(m.peers, func(p *conversation) bool { return p.contextID == msg.id }); i >= 0 {
		c, ok = m.peers[i], true
		m.peers = slices.Delete(m.peers, i, i+1)
	}
	if !ok {
		return nil
	}
	delete(m.chats, msg.id)
	m.setNotice("deleted “"+short(c.title, 40)+"”", false)
	var cmd tea.Cmd
	if c == m.chat { // what is typed stays
		m.chat = m.conversation(plugin.NewID())
		m.renderHistory(true)
		cmd = m.watch(m.chat)
	}
	m.sessionSel = max(0, min(m.sessionSel, len(m.sessionRows())-1))
	return cmd
}

// cancel stops the answer on its way in the session shown.
func (m *Model) cancel() tea.Cmd {
	if !m.chat.pending {
		m.setNotice("no answer is on its way here", false)
		return nil
	}
	id := m.chat.contextID
	m.setNotice("stopping the answer…", false)
	return func() tea.Msg { return actionMsg{"answer stopped", m.backend.Cancel(m.ctx, id)} }
}

// running are the frames of what runs behind the title of a session whose
// answer is on its way: a dot that crawls along.
var running = []string{".  ", ".. ", "...", " ..", "  .", "   "}

// runningFrame is the frame of running that is shown now. It goes with the
// spinner, a frame every few of its ticks.
func (m *Model) runningFrame() string { return running[m.ticks/3%len(running)] }

// sessionsView lists the sessions: the one selected marked, the one shown
// said, and those whose answer is on its way moving.
func (m *Model) sessionsView() string {
	w, h := m.width, m.bodyHeight()
	rows := m.sessionRows()
	yours, peers, about := boldStyle.Render("Sessions"), mutedStyle.Render("Peers"), "  each goes on in the background while another one is shown"
	if m.peerTab {
		yours, peers, about = mutedStyle.Render("Sessions"), boldStyle.Render("Peers"), "  how peers talked with "+m.agentName()+", only to read"
	}
	lines := []string{yours + mutedStyle.Render(" · ") + peers + mutedStyle.Render(about), ""}
	shown := max(1, h-6) // border, title, blank lines and count
	first := max(0, min(m.sessionSel-shown/2, len(rows)-shown))
	for i := first; i < min(len(rows), first+shown); i++ {
		c := rows[i]
		title := cmp.Or(c.title, "new conversation")
		if c.peer != "" {
			title = c.peer + ": " + title
		}
		var after string
		if c.pending {
			after = " " + lipgloss.NewStyle().Foreground(accent).Render(m.runningFrame())
		}
		if !c.last.IsZero() {
			after += mutedStyle.Render("  " + when(c.last, m.now()))
		}
		if c == m.chat {
			after += mutedStyle.Render("  · shown")
		}
		line := short(title, max(10, w-8-lipgloss.Width(after))) + after
		if i == m.sessionSel {
			lines = append(lines, lipgloss.NewStyle().Foreground(accent).Render("▸ ")+line)
		} else {
			lines = append(lines, "  "+line)
		}
	}
	thing := "session"
	if m.peerTab {
		thing = "conversation"
	}
	count := "1 " + thing
	if len(rows) != 1 {
		count = fmt.Sprintf("%d %ss", len(rows), thing)
	}
	lines = append(lines, "", mutedStyle.Render(count))
	return box(true, m.light).Width(w).Height(h).MaxHeight(h).Render(strings.Join(lines, "\n"))
}

// when is the time t as a list shows it: the time of day for today, with
// the day before it otherwise.
func when(t, now time.Time) string {
	if t.Year() == now.Year() && t.YearDay() == now.YearDay() {
		return t.Format("15:04")
	}
	return t.Format("Jan 2 15:04")
}
