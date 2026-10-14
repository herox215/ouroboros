package tui

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"ouroboros/plugin"
)

// The conversations of the chat are sessions of the node: it runs their
// answers and tells the UI what happens in them, see Backend.Watch. The UI
// watches every session it knows and shows one of them. The others go on in
// the background, and the command /session lists them in a dialog over the
// chat, as ctrl+s does, which leaves what is typed in the input for the
// session shown: the arrow keys select one, enter shows it, d pressed twice
// deletes it, and esc goes back to the chat.
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
	// findMsg are the sessions whose content holds a filter, as the node
	// found them.
	findMsg struct {
		query string
		ids   []string
		err   error
	}
	// readMsg is what happened in the conversation of a peer c.
	readMsg struct {
		c      *conversation
		events []Event
		err    error
	}
)

// sessionsScreen is the state of the sessions screen.
type sessionsScreen struct {
	sel      int             // the session selected
	filter   string          // what is typed to filter the list, as it is typed
	hits     map[string]bool // the sessions whose content holds the filter, as the node found them
	hitsFor  string          // the filter hits was looked for: what is typed may have gone on
	deleting *conversation   // the session that ctrl+d deletes when it is pressed again
	peerTab  bool            // the screen lists the conversations of peers
	peers    []*conversation // the conversations of peers, as far as they were listed
}

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
	if m.resuming == c {
		m.resuming = nil
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
// it was deleted. An answer that was on its way is not any more as far as
// the UI knows, as when the node stopped: what became of it is in what the
// session shows once it is watched again.
func (m *Model) unwatched(id string) tea.Cmd {
	c, ok := m.chats[id]
	if !ok {
		return nil
	}
	c.watching = false
	c.pending, c.running, c.doing, c.round, c.asking, c.jobs = false, false, "", 0, nil, nil
	c.partial = ""
	if c == m.chat {
		m.renderHistory(m.followsEnd())
	}
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
	*c = conversation{contextID: c.contextID, draft: c.draft, images: c.images, art: c.art, watching: true, pending: c.pending && len(past) == 0}
	for _, e := range past {
		m.apply(c, e, false)
	}
	if c == m.chat {
		m.renderHistory(true)
	}
}

// event shows what happened in a session just now and waits for what
// follows.
//
// A history that follows its end, as it does at the bottom, glides to what
// came instead of jumping there.
func (m *Model) event(msg eventMsg) tea.Cmd {
	c := m.conversation(msg.id)
	following := c == m.chat && m.followsEnd()
	cmd := m.apply(c, msg.event, true)
	var glide tea.Cmd
	// The pieces of the answer on its way are not drawn per piece but on the
	// clock of the chat, see answerMsg.
	if c == m.chat && msg.event.Kind != plugin.SessionAnswering {
		m.renderHistory(false)
		if following {
			glide = m.glideTo(&m.history, topLine(&m.history))
		}
	}
	return tea.Batch(cmd, glide, next(msg.id, msg.live))
}

// apply adds what happened, e, to the session c. With live it happened just
// now, and is not replayed: an answer of a session that is not shown is told
// in the footer, and ouro starting anew makes the UI quit for it.
func (m *Model) apply(c *conversation, e Event, live bool) tea.Cmd {
	c.last, c.context, c.cache = e.At, e.Context, e.Cache
	switch e.Kind {
	case plugin.SessionMessage:
		c.add(entry{mine: true, text: e.Text, at: e.At})
		c.queued = c.queued[min(1, len(c.queued)):] // one that was queued is the first of them
		if live && !c.pending {                     // else the wait was shown, or is shown already
			c.art++
		}
		c.pending, c.running, c.round, c.doing = true, true, 0, ""
		c.title = cmp.Or(c.title, e.Text)
	case plugin.SessionTask:
		// The report of a task starts an answer, or goes into the one on its
		// way, as a message of the owner does.
		c.add(entry{task: true, text: e.Text, failed: e.Failed, at: e.At})
		if live && !c.pending {
			c.art++
		}
		c.pending, c.running, c.round, c.doing = true, true, 0, ""
	case plugin.SessionTasks:
		c.tasks = nil
		if e.Text != "" {
			c.tasks = strings.Split(e.Text, "\n")
		}
	case plugin.SessionQueued:
		c.queued = append(c.queued, e.Text)
	case plugin.SessionUnqueued:
		c.queued = c.queued[:max(0, len(c.queued)-1)]
	case plugin.SessionSay:
		c.say(e)
	case plugin.SessionThought:
		c.thought(e)
	case plugin.SessionStep:
		c.step(e)
	case plugin.SessionAsked:
		c.ask(e)
		if live && !e.Done && c != m.chat {
			m.setNotice(m.agentName()+" asks you in “"+short(c.title, 40)+"” · /session", false)
		}
	case plugin.SessionCalling:
		c.calling = e.Text
	case plugin.SessionAnswering:
		c.partial += e.Text
		if live && c == m.chat && !m.streaming { // from here the chat draws it
			m.streaming = true
			return answerTick()
		}
	case plugin.SessionAnswer, plugin.SessionFailed, plugin.SessionCanceled:
		c.end(e)
		if live && c != m.chat {
			verb := "answered"
			if e.Kind != plugin.SessionAnswer {
				verb = "no answer"
			}
			m.setNotice(verb+" in “"+short(c.title, 40)+"” · /session", e.Kind != plugin.SessionAnswer)
		}
	case plugin.SessionJobs:
		c.jobs = nil
		if e.Text != "" {
			c.jobs = strings.Split(e.Text, "\n")
		}
	case plugin.SessionSuggestion:
		c.add(entry{suggestion: true, text: e.Text, at: e.At})
	case plugin.SessionTitle:
		c.title = e.Text
	case plugin.SessionEffort:
		c.effort, c.efforted = e.Text, true
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
// nothing of gets its entry with its first call that is shown. Words that are
// only blank, as some models say them along with their tools, are nothing:
// they would leave an empty block in the chat.
func (c *conversation) say(e Event) {
	c.round, c.doing, c.partial, c.calling = 0, "", "", ""
	if strings.TrimSpace(e.Text) != "" {
		c.add(entry{text: e.Text, at: e.At})
		c.round = len(c.entries)
	}
}

// thought adds what the model thought to the entry of the round of the loop
// that is on, which a round that said nothing of gets now: the thinking grows
// there while the answer is on its way, and stays with its round.
func (c *conversation) thought(e Event) {
	if c.round == 0 {
		c.add(entry{at: e.At})
		c.round = len(c.entries)
	}
	c.entries[c.round-1].thought += e.Text
}

// step notes what the agent is waiting for while the answer is pending. A
// call that starts goes into the entry of its round, which a round the agent
// said nothing of gets now; one that ended takes the place of its start,
// with what became of it. A call nothing is shown of counts all the same;
// the script that calls the tools is no tool of its own.
func (c *conversation) step(e Event) {
	if e.Tool == toolRun {
		return
	}
	c.partial, c.calling = "", "" // what a round wrote before its tools is not the answer
	if c.round == 0 {
		c.add(entry{at: e.At})
		c.round = len(c.entries)
	}
	r := &c.entries[c.round-1]
	if !e.Done {
		r.calls = append(r.calls, call{text: e.Text})
	} else if i := r.started(e.Text); i >= 0 {
		r.calls[i] = call{text: e.Text, done: true, failed: e.Failed}
	}
	if doing := r.waitingFor(); doing != c.doing {
		c.doing, c.since = doing, e.At
	}
}

// end shows how the answer ended: the answer, why it failed, or that it was
// stopped.
func (c *conversation) end(e Event) {
	c.pending, c.running, c.doing, c.round = false, false, "", 0                  // the loop of this answer is over
	c.asking, c.jobs, c.partial, c.thread, c.calling = nil, nil, "", thread{}, "" // its jobs end with it, and with
	// them the clock the answer was drawn on: its next frame sees pending gone
	last := entry{text: e.Text, at: e.At}
	switch e.Kind {
	case plugin.SessionFailed:
		last.failed = true
	case plugin.SessionCanceled:
		last.text, last.failed = "stopped before it answered", true
	}
	// An answer without words is nothing to show, as words that are only
	// blank are nothing in a round of the loop.
	if strings.TrimSpace(last.text) != "" {
		c.add(last)
	}
}

// answerFrame is how often the chat is drawn while the answer is being
// written: the stream brings its pieces per token, and to draw every one of
// them would cost far more than the eye can follow.
const answerFrame = time.Second / 20

// answerMsg is a frame of the answer that is being written.
type answerMsg struct{}

func answerTick() tea.Cmd {
	return tea.Tick(answerFrame, func(time.Time) tea.Msg { return answerMsg{} })
}

// thread is how far the newest word of the answer is drawn in.
type thread struct {
	words int // how many words the stream had when the chat last drew them
	frame int // how many frames the newest of them has flickered for
}

// settleFrames is how many frames of the chat a word flickers before it
// stands: a blink at 20 frames a second, and no wait for the reading.
const settleFrames = 3

// advance says which frame of its animation the newest word of the answer is
// at: a word the stream brings starts at the first, and the frames the chat
// draws without a new one let the last of them come to rest.
func (c *conversation) advance() int {
	if w := len(strings.Fields(c.partial)); w != c.thread.words {
		c.thread = thread{words: w}
	} else if c.thread.frame < settleFrames {
		c.thread.frame++
	}
	return c.thread.frame
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
	m.sessions.peerTab, m.sessions.filter = m.chat.peer != "", ""
	m.sessions.hits, m.sessions.hitsFor = nil, ""
	m.sessions.sel = m.sessions.pick(m)
	return tea.Batch(m.loadSessions, func() tea.Msg { return m.sessions.loadPeers(m) })
}

// loadPeers asks for the conversations of peers the node has.
func (l *sessionsScreen) loadPeers(m *Model) tea.Msg {
	list, err := m.backend.Peers(m.ctx)
	return peersMsg{list, err}
}

// peersLoaded lists the conversations of peers. The one shown stays as it
// is; without a node to ask, the list stays as it was.
func (l *sessionsScreen) peersLoaded(m *Model, msg peersMsg) {
	if msg.err != nil {
		return
	}
	selected := l.rows(m)
	l.peers = nil
	for _, info := range msg.list {
		c := &conversation{contextID: info.ID, peer: info.Peer, title: info.Title, last: info.Last}
		if m.chat.contextID == info.ID {
			c = m.chat
		}
		l.peers = append(l.peers, c)
	}
	if l.peerTab && l.sel < len(selected) {
		l.sel = max(0, slices.Index(l.peers, selected[l.sel]))
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

// pick is where the session shown stands in the list, or the first of what
// is left of it once what is typed filters the list.
func (l *sessionsScreen) pick(m *Model) int {
	return max(0, slices.Index(l.rows(m), m.chat))
}

// rows are the sessions the screen lists, of which what is typed may leave
// only some: see all.
func (l *sessionsScreen) rows(m *Model) []*conversation {
	all := l.all(m)
	if l.filter == "" {
		return all
	}
	want := strings.ToLower(l.filter)
	hits := l.hits
	if l.hitsFor != l.filter {
		hits = nil // what the node found belongs to another filter: a newer search is on its way
	}
	var rows []*conversation
	for _, c := range all {
		if strings.Contains(strings.ToLower(c.peer+" "+cmp.Or(c.title, "new conversation")), want) || hits[c.contextID] {
			rows = append(rows, c)
		}
	}
	return rows
}

// find asks the node for the sessions whose content holds what is typed, so
// that rows keeps them besides those whose title holds it. Nothing is asked
// for a filter that was looked for already, and nothing for an empty one.
func (l *sessionsScreen) find(m *Model) tea.Cmd {
	q := strings.TrimSpace(l.filter)
	if q == "" {
		l.hits, l.hitsFor = nil, ""
		return nil
	}
	if l.hitsFor == q {
		return nil
	}
	l.hitsFor = q
	return func() tea.Msg {
		ids, err := m.backend.Find(m.ctx, q)
		return findMsg{query: q, ids: ids, err: err}
	}
}

// found takes what the node found: the list shows those sessions too, as
// what was said in them holds what is typed. What was found for another
// filter is dropped, as a newer search is on its way.
func (l *sessionsScreen) found(m *Model, msg findMsg) {
	if msg.err != nil || msg.query != l.filter {
		return
	}
	l.hits = make(map[string]bool, len(msg.ids))
	for _, id := range msg.ids {
		l.hits[id] = true
	}
	l.sel = l.pick(m)
}

// all are the sessions the sessions screen lists: those with a
// message or with something typed for them, and the one shown, the latest
// first. One without a message yet is the latest of all. Among the
// conversations of peers, those the node listed.
func (l *sessionsScreen) all(m *Model) []*conversation {
	if l.peerTab {
		return l.peers
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

// handleKey handles keys on the sessions screen: what is typed filters the
// list, up and down select a session, enter shows it, ctrl+d pressed twice
// deletes it, and esc takes back what is typed and then goes back to the chat.
func (l *sessionsScreen) handleKey(m *Model, msg tea.KeyPressMsg) tea.Cmd {
	rows, deleting := l.rows(m), l.deleting
	l.deleting = nil
	one := func() *conversation { return rows[min(l.sel, len(rows)-1)] }
	switch msg.String() {
	case "tab":
		l.peerTab = !l.peerTab
		l.sel = l.pick(m)
		return nil
	case "esc":
		// What is typed only filters the list: esc takes it back first, and
		// only from an empty filter goes back to the chat.
		if l.filter == "" {
			m.screen = screenChat
		} else {
			l.filter = ""
			l.sel = l.pick(m)
		}
		return l.find(m)
	case "backspace":
		if l.filter != "" {
			r := []rune(l.filter)
			l.filter, l.sel = string(r[:len(r)-1]), l.pick(m)
		}
		return l.find(m)
	case "up":
		if len(rows) > 0 {
			l.sel = cycle(l.sel, -1, len(rows))
		}
		return nil
	case "down":
		if len(rows) > 0 {
			l.sel = cycle(l.sel, 1, len(rows))
		}
		return nil
	case "enter":
		if len(rows) == 0 {
			return nil
		}
		if c := one(); c.peer != "" {
			return m.showPeer(c)
		}
		return m.switchTo(one())
	case "ctrl+d":
		if len(rows) == 0 {
			return nil
		}
		return l.delete(m, one(), deleting)
	}
	if r := []rune(msg.String()); len(r) == 1 && unicode.IsPrint(r[0]) {
		l.filter, l.sel = l.filter+string(r), l.pick(m) // what is typed filters, as it is typed
		return l.find(m)                                // and what was said in a session is looked for with it
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
// first: a session is deleted with the second ctrl+d, not by a key pressed by
// mistake. One without a message is not in the node; one whose answer is on
// its way is not deleted.
func (l *sessionsScreen) delete(m *Model, c, selected *conversation) tea.Cmd {
	switch {
	case c.title == "":
		m.setNotice("nothing to delete here yet", false)
		return nil
	case c.pending:
		m.setNotice("an answer is on its way there: /cancel it first", true)
		return nil
	case c != selected && c.peer != "":
		l.deleting = c
		m.setNotice("press ctrl+d again to delete the conversation of "+c.peer+" “"+short(c.title, 40)+"”", false)
		return nil
	case c != selected:
		l.deleting = c
		m.setNotice("press ctrl+d again to delete “"+short(c.title, 40)+"” with its history", false)
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
	if i := slices.IndexFunc(m.sessions.peers, func(p *conversation) bool { return p.contextID == msg.id }); i >= 0 {
		c, ok = m.sessions.peers[i], true
		m.sessions.peers = slices.Delete(m.sessions.peers, i, i+1)
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
	m.sessions.sel = max(0, min(m.sessions.sel, len(m.sessions.rows(m))-1))
	return cmd
}

// cancel stops the answer on its way in the session shown, and the tasks
// the agent delegated in it.
func (m *Model) cancel() tea.Cmd {
	if !m.chat.pending && len(m.chat.tasks) == 0 {
		m.setNotice("no answer is on its way here", false)
		return nil
	}
	id := m.chat.contextID
	what := "answer"
	switch {
	case !m.chat.pending:
		what = "tasks"
	case len(m.chat.tasks) > 0:
		what = "answer and the tasks"
	}
	m.setNotice("stopping the "+what+"…", false)
	return func() tea.Msg { return actionMsg{what + " stopped", m.backend.Cancel(m.ctx, id)} }
}

// running are the frames of what runs behind the title of a session whose
// answer is on its way: a dot that crawls along.
var running = []string{".  ", ".. ", "...", " ..", "  .", "   "}

// runningFrame is the frame of running that is shown now. It goes with the
// spinner, a frame every few of its ticks.
func (m *Model) runningFrame() string { return running[m.ticks/3%len(running)] }

// sessionsView lists the sessions in a dialog: the one selected marked, the
// one shown said, those whose answer is on its way moving, and below the
// tabs what is typed to filter the list.
func (l *sessionsScreen) view(m *Model) string {
	w, h := m.dialogSize()
	rows, all := l.rows(m), l.all(m)
	yours, peers, about := boldStyle.Render("Sessions"), mutedStyle.Render("Peers"), "  each goes on in the background"
	if l.peerTab {
		yours, peers, about = mutedStyle.Render("Sessions"), boldStyle.Render("Peers"), "  how peers talked with "+m.agentName()+", only to read"
	}
	lines := []string{yours + mutedStyle.Render(" · ") + peers + mutedStyle.Render(about), ""}
	shown := max(1, h-6) // border, tabs, blank lines and count
	if l.filter != "" {
		lines = append(lines, mutedStyle.Render("find ")+ansi.Truncate(l.filter, max(1, w-12), "…"), "")
		shown = max(1, shown-2) // the line it is typed on takes two of them
	}
	first := max(0, min(l.sel-shown/2, len(rows)-shown))
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
		if i == l.sel {
			lines = append(lines, lipgloss.NewStyle().Foreground(accent).Render("▸ ")+line)
		} else {
			lines = append(lines, "  "+line)
		}
	}
	if len(rows) == 0 && l.filter != "" {
		lines = append(lines, mutedStyle.Render("nothing here has “"+short(l.filter, 20)+"” in it"))
	}
	thing := "session"
	if l.peerTab {
		thing = "conversation"
	}
	noun := thing + "s"
	if len(all) == 1 {
		noun = thing
	}
	count := fmt.Sprintf("%d %s", len(rows), noun)
	if l.filter != "" {
		count = fmt.Sprintf("%d of %d %s", len(rows), len(all), noun)
	}
	lines = append(lines, "", mutedStyle.Render(count))
	return m.dialog(lines)
}

// when is the time t as a list shows it: the time of day for today, with
// the day before it otherwise.
func when(t, now time.Time) string {
	if t.Year() == now.Year() && t.YearDay() == now.YearDay() {
		return t.Format("15:04")
	}
	return t.Format("Jan 2 15:04")
}
