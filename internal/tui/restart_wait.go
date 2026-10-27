package tui

import (
	"errors"
	"time"

	tea "charm.land/bubbletea/v2"

	"ouroboros/plugin"
)

// awaitingSend is a message the node did not take because it starts anew with
// a new build. The node says so before it takes anything, so the UI keeps the
// message and offers it again once the node is back: the owner need not watch
// for the moment it takes messages again.
type awaitingSend struct {
	text     string    // the message as it goes to the node
	typed    string    // what the input holds for it
	steer    bool      // whether it was to steer the answer on its way
	since    time.Time // when the node refused it
	inflight bool      // the node was offered it, and has not answered yet
}

// restartWaitLimit is how long a message waits for the node to come back
// before it stays in the input for the owner to send.
const restartWaitLimit = 2 * time.Minute

// restartRetryDelay is how long the UI waits before it offers the refused
// message to the node again. Tests shorten it.
var restartRetryDelay = time.Second

// restartRetryMsg is the time to offer the messages the node refused again.
type restartRetryMsg struct{}

func retryRestart() tea.Cmd {
	return tea.Tick(restartRetryDelay, func(time.Time) tea.Msg { return restartRetryMsg{} })
}

// retryRestarting offers every message the node refused while it starts anew
// again, and keeps the clock for those that wait still.
func (m *Model) retryRestarting() tea.Cmd {
	var cmds []tea.Cmd
	waiting := false
	for _, c := range m.chats {
		cmd, waits := m.retryAwaiting(c)
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
		waiting = waiting || waits
	}
	if waiting {
		cmds = append(cmds, retryRestart())
	}
	return tea.Batch(cmds...)
}

// retryAwaiting offers the message the node refused again, unless the owner
// changed it meanwhile or it waited too long. It reports whether the message
// waits still.
func (m *Model) retryAwaiting(c *conversation) (tea.Cmd, bool) {
	a := c.awaiting
	if a == nil {
		return nil, false
	}
	if a.inflight { // the node has not said yet
		return nil, true
	}
	typed := c.draft
	if c == m.chat {
		typed = m.input.Value()
	}
	if typed != a.typed {
		c.awaiting = nil // the owner writes something else: the message is theirs
		return nil, false
	}
	if m.now().Sub(a.since) > restartWaitLimit {
		c.awaiting = nil
		m.giveBack(c, a.typed, "ouro did not come back in time")
		return nil, false
	}
	id, text, steer := c.contextID, a.text, a.steer
	ask := func() tea.Msg {
		var err error
		if steer {
			err = m.backend.Steer(plugin.WithMessageSource(m.ctx, plugin.MessageSource{Interface: "tui"}), id, text)
		} else {
			err = m.backend.Ask(plugin.WithMessageSource(m.ctx, plugin.MessageSource{Interface: "tui"}), id, text)
		}
		return restartSentMsg{id: id, text: text, typed: a.typed, err: err}
	}
	a.inflight = true
	return ask, true
}

// restartSentMsg is what the node said to a message that waited for it to
// come back.
type restartSentMsg struct {
	id, text, typed string
	err             error
}

// restartSent takes the node's answer to a message that waited for its
// restart. One the node takes shows in the chat as a message of the owner
// that is on its way; one it refuses still is offered again.
func (m *Model) restartSent(msg restartSentMsg) tea.Cmd {
	c, ok := m.chats[msg.id]
	if !ok || c.awaiting == nil || c.awaiting.typed != msg.typed {
		return nil
	}
	c.awaiting.inflight = false
	if errors.Is(msg.err, plugin.ErrRestarting) {
		return nil // retryRestarting keeps the clock for this message
	}
	c.awaiting = nil
	if msg.err != nil {
		m.giveBack(c, msg.typed, msg.err.Error())
		return nil
	}
	c.pending, c.round, c.art = true, 0, c.art+1
	if c == m.chat {
		if m.input.Value() == msg.typed {
			m.input.Reset()
		}
		m.setNotice("ouro is back · your message is on its way", false)
		m.renderHistory(true)
	} else if c.draft == msg.typed {
		c.draft = ""
	}
	return m.watch(c)
}

// giveBack puts a message the node did not take back into the input of its
// session, unless the owner writes something there meanwhile.
func (m *Model) giveBack(c *conversation, typed, why string) {
	if c == m.chat {
		if m.input.Value() == "" {
			m.input.SetValue(typed)
		}
		m.renderHistory(m.history.AtBottom())
	} else if c.draft == "" {
		c.draft = typed
	}
	m.setNotice(why, true)
}
