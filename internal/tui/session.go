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
// shown: the arrow keys select one, enter shows it, and esc goes back to the
// chat.

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

// watch asks for the events of the session c, unless they come already.
func (m *Model) watch(c *conversation) tea.Cmd {
	if c == nil || c.watching {
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

// unwatched watches a session again whose events stopped coming.
func (m *Model) unwatched(id string) tea.Cmd {
	m.conversation(id).watching = false
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
	*c = conversation{contextID: c.contextID, draft: c.draft, watching: true, pending: c.pending && len(past) == 0}
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
	c.last, c.context = e.At, e.Context
	switch e.Kind {
	case plugin.SessionMessage:
		c.add(entry{mine: true, text: e.Text, at: e.At})
		c.pending, c.round, c.doing = true, 0, ""
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
	c.pending, c.doing, c.round = false, "", 0 // the loop of this answer is over
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

// sessionsLoaded watches the sessions of the node the UI does not know yet,
// such as those of another front end. Without a node to ask, the UI goes on
// with those it knows.
func (m *Model) sessionsLoaded(msg sessionsMsg) tea.Cmd {
	var cmds []tea.Cmd
	for _, info := range msg.list {
		c := m.conversation(info.ID)
		if c.watching {
			continue
		}
		// What the list says, until the events say it all.
		c.title, c.last, c.pending = cmp.Or(c.title, info.Title), info.Last, info.Running
		cmds = append(cmds, m.watch(c))
	}
	return tea.Batch(cmds...)
}

// openSessions opens the sessions screen, with the session shown selected.
func (m *Model) openSessions() tea.Cmd {
	m.screen = screenSessions
	m.sessionSel = max(0, slices.Index(m.sessionRows(), m.chat))
	return m.loadSessions
}

// sessionRows are the sessions the sessions screen lists: those with a
// message or with something typed for them, and the one shown, the latest
// first. One without a message yet is the latest of all.
func (m *Model) sessionRows() []*conversation {
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
// a session, enter shows it, esc goes back to the chat.
func (m *Model) handleSessionsKey(msg tea.KeyPressMsg) tea.Cmd {
	rows := m.sessionRows()
	switch msg.String() {
	case "esc":
		m.screen = screenChat
	case "up", "k":
		m.sessionSel = max(0, m.sessionSel-1)
	case "down", "j":
		m.sessionSel = min(len(rows)-1, m.sessionSel+1)
	case "enter":
		return m.switchTo(rows[min(m.sessionSel, len(rows)-1)])
	}
	return nil
}

// switchTo shows the session c in the chat. What was typed for the session
// that was shown stays with it, and what was typed for c comes back.
func (m *Model) switchTo(c *conversation) tea.Cmd {
	m.screen = screenChat
	if c == m.chat {
		return nil
	}
	m.chat.draft = m.input.Value()
	m.chat = c
	m.input.SetValue(c.draft)
	c.draft = ""
	m.renderHistory(true)
	return m.watch(c)
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
	lines := []string{boldStyle.Render("Sessions") + mutedStyle.Render("  each goes on in the background while another one is shown"), ""}
	shown := max(1, h-6) // border, title, blank lines and count
	first := max(0, min(m.sessionSel-shown/2, len(rows)-shown))
	for i := first; i < min(len(rows), first+shown); i++ {
		c := rows[i]
		title := cmp.Or(c.title, "new conversation")
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
	count := "1 session"
	if len(rows) != 1 {
		count = fmt.Sprintf("%d sessions", len(rows))
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
