package tui

import (
	"encoding/json"

	tea "charm.land/bubbletea/v2"
)

// undo takes back the last memory the review of an answer in the
// conversation shown kept that is not taken back yet, see /undo.
func (m *Model) undo() tea.Cmd {
	c := m.chat
	for i := len(c.entries) - 1; i >= 0; i-- {
		e := c.entries[i]
		if e.remembered == nil || c.undone[string(e.remembered)] {
			continue
		}
		return func() tea.Msg {
			return undoneMsg{c, e, m.backend.UndoRemembered(e.remembered)}
		}
	}
	m.setNotice("nothing remembered here to take back", false)
	return nil
}

// undoneMsg tells that the memory of the entry remembered of the
// conversation c is taken back, or why not.
type undoneMsg struct {
	c          *conversation
	remembered entry
	err        error
}

// undone shows that a memory is taken back: a faint line below, as the one
// that told it was kept.
func (m *Model) undone(msg undoneMsg) {
	if msg.err != nil {
		m.setNotice(msg.err.Error(), true)
		return
	}
	c := msg.c
	if c.undone == nil {
		c.undone = map[string]bool{}
	}
	c.undone[string(msg.remembered.remembered)] = true
	c.add(entry{text: msg.remembered.text, at: m.now(), undoes: true})
	m.setNotice("memory taken back", false)
}

// rememberedArgs are what a SessionRemembered brings to take it back: never
// nil, so that an entry tells it is one.
func rememberedArgs(args json.RawMessage) json.RawMessage {
	if args == nil {
		return json.RawMessage{}
	}
	return args
}
