package tui

import (
	tea "charm.land/bubbletea/v2"

	"ouroboros/plugin"
)

// The thread is the one conversation in which the owner says what is to be
// done, and the agent hands it to the session it belongs to, or starts one
// for it, and tells there what came of them: /thread shows it. It is a
// session of the node like the others, plugin.ThreadID, which the list of
// sessions leaves out, and the conversation a UI shows first: the one in
// which the owner talks, there as in the rooms of a gateway. The first time a
// UI shows it, the thread takes the start directory of the UI as its
// project: where the sessions it starts work, unless the agent names
// another. One that answers meanwhile keeps the one it has.

// openThread shows the thread.
func (m *Model) openThread() tea.Cmd {
	if m.chat.contextID == plugin.ThreadID {
		m.screen = screenChat
		m.setNotice("this is the thread: say what is to be done", false)
		return nil
	}
	show := m.switchTo(m.thread())
	m.setNotice("back in the thread", false)
	return show
}

// place is where the session c is, as a notice names it: what it is called,
// and the command that shows it.
func place(c *conversation) string {
	if c.contextID == plugin.ThreadID {
		return "the thread · /thread"
	}
	return "“" + short(c.title, 40) + "” · /session"
}
