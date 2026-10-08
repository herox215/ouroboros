package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// The context screen is what the command /context opens: what the next call
// of the LLM in the conversation is sent, message by message – the system
// prompt, the tools and the conversation so far. The arrow keys and pgup and
// pgdn scroll, esc goes back to the chat.

// Message is a message of the context of a conversation: who it is of, like
// "system", "user" or "tool run", and its text.
type Message struct {
	Role string
	Text string
}

// contentsMsg is the context of a conversation, on the way to the context
// screen.
type contentsMsg struct {
	contextID string
	msgs      []Message
	err       error
}

// contextScreen is the state of the context screen.
type contextScreen struct {
	port viewport.Model // where the context is shown
	got  *contentsMsg   // the context that came in, to wrap again when the window changes
}

// openContext opens the context screen and loads the context of the
// conversation.
func (m *Model) openContext() tea.Cmd {
	m.screen = screenContext
	m.context.port = viewport.New()
	m.context.port.KeyMap = viewport.KeyMap{} // the keys are handled explicitly
	m.context.got = nil
	m.context.size(m)
	contextID := m.chat.contextID
	return func() tea.Msg {
		msgs, err := m.backend.Contents(m.ctx, contextID)
		return contentsMsg{contextID, msgs, err}
	}
}

// sizeContents fits the viewport of the context screen into its box: the
// border and padding, and the title with a blank line below it. The context
// is wrapped to the width here and not by the viewport, which would measure
// all of it again on every step of scrolling.
func (x *contextScreen) size(m *Model) {
	x.port.SetWidth(max(1, m.width-4))
	x.port.SetHeight(max(1, m.bodyHeight()-4))
	x.render(m)
}

// contentsLoaded shows the context that came in, unless the conversation
// changed meanwhile.
func (x *contextScreen) loaded(m *Model, msg contentsMsg) {
	if msg.contextID != m.chat.contextID {
		return
	}
	x.got = &msg
	x.render(m)
}

// renderContents fills the viewport of the context screen with the context
// that came in, wrapped to its width.
func (x *contextScreen) render(m *Model) {
	msg := x.got
	if msg == nil {
		x.port.SetContent(mutedStyle.Render("…"))
		return
	}
	body := lipgloss.NewStyle().Width(x.port.Width()).PaddingLeft(2)
	var blocks []string
	if msg.err != nil {
		blocks = append(blocks, lipgloss.Wrap(errStyle.Render("✗ "+msg.err.Error()), x.port.Width(), ""))
	}
	if len(msg.msgs) == 0 {
		blocks = append(blocks, mutedStyle.Render("Nothing yet."))
	}
	for _, c := range msg.msgs {
		color := muted
		switch c.Role {
		case "user":
			color = accent
		case "assistant":
			color = peer
		}
		size := mutedStyle.Render(fmt.Sprintf("  %d bytes", len(c.Text)))
		blocks = append(blocks, boldStyle.Foreground(color).Render(c.Role)+size+"\n"+body.Render(c.Text))
	}
	x.port.SetContent(strings.Join(blocks, "\n\n"))
}

// handleContextKey handles keys on the context screen.
func (x *contextScreen) handleKey(m *Model, msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		m.stopGlide(&x.port) // the screen it was for is left
		m.screen = screenChat
	case "up", "k":
		return m.scrollBy(&x.port, -1)
	case "down", "j":
		return m.scrollBy(&x.port, 1)
	case "pgup":
		return m.scrollBy(&x.port, -x.port.Height())
	case "pgdown":
		return m.scrollBy(&x.port, x.port.Height())
	case "home":
		return m.scrollBy(&x.port, -topLine(&x.port))
	case "end":
		return m.scrollBy(&x.port, topLine(&x.port))
	}
	return nil
}

// contextView shows the context of the conversation, with how long it was
// when the LLM was last called.
func (x *contextScreen) view(m *Model) string {
	w, h := m.width, m.bodyHeight()
	title := boldStyle.Render("Context")
	if m.chat.context > 0 {
		title += mutedStyle.Render("  " + contextSize(m.chat.context) + " at the last call")
	}
	if m.chat.cache.Tokens > 0 {
		title += mutedStyle.Render(", cached " + cacheRate(m.chat.cache))
	}
	if metrics := callMetrics(m.chat.cache); metrics != "" {
		title += mutedStyle.Render("  · " + metrics)
	}
	title += mutedStyle.Render(fmt.Sprintf("  · %.0f%%  · ↑↓ pgup pgdn scroll · esc back", x.port.ScrollPercent()*100))
	return box(true, m.light).Width(w).Height(h).MaxHeight(h).Render(title + "\n\n" + x.port.View())
}
