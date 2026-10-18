package tui

import (
	"fmt"

	tea "charm.land/bubbletea/v2"

	"ouroboros/plugin"
)

// The thread is the one conversation in which the owner says what is to be
// done, and the agent hands it to the session it belongs to, or starts one
// for it, and tells there what came of them: /thread shows it. It is a
// session of the node like the others, plugin.ThreadID, which the list of
// sessions leaves out. The first time a UI shows it, the thread takes the
// start directory of the UI as its project: where the sessions it starts
// work, unless the agent names another.

// openThread shows the thread.
func (m *Model) openThread() tea.Cmd {
	if m.chat.contextID == plugin.ThreadID {
		m.screen = screenChat
		m.setNotice("this is the thread: say what is to be done", false)
		return nil
	}
	_, known := m.chats[plugin.ThreadID]
	show := m.switchTo(m.conversation(plugin.ThreadID))
	m.setNotice("the thread: say what is to be done, ouro hands it to its session", false)
	backend, ok := m.backend.(directoryBackend)
	if known || !ok || m.initialDirectory == "" {
		return show
	}
	// Whether the directory comes with what happened in the thread, or as
	// it happens, the thread shows it.
	path := m.initialDirectory
	return tea.Batch(func() tea.Msg {
		if err := backend.SetSessionDirectory(m.ctx, plugin.ThreadID, path); err != nil {
			return actionMsg{err: fmt.Errorf("the thread keeps its project: %w", err)}
		}
		return actionMsg{done: "the thread: what is new starts in " + path}
	}, show)
}

// place is where the session c is, as a notice names it: what it is called,
// and the command that shows it.
func place(c *conversation) string {
	if c.contextID == plugin.ThreadID {
		return "the thread · /thread"
	}
	return "“" + short(c.title, 40) + "” · /session"
}
