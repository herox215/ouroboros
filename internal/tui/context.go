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

// openContext opens the context screen and loads the context of the
// conversation.
func (m *Model) openContext() tea.Cmd {
	m.screen = screenContext
	m.contents = viewport.New()
	m.contents.KeyMap = viewport.KeyMap{} // the keys are handled explicitly
	m.loaded = nil
	m.sizeContents()
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
func (m *Model) sizeContents() {
	m.contents.SetWidth(max(1, m.width-4))
	m.contents.SetHeight(max(1, m.bodyHeight()-4))
	m.renderContents()
}

// contentsLoaded shows the context that came in, unless the conversation
// changed meanwhile.
func (m *Model) contentsLoaded(msg contentsMsg) {
	if msg.contextID != m.chat.contextID {
		return
	}
	m.loaded = &msg
	m.renderContents()
}

// renderContents fills the viewport of the context screen with the context
// that came in, wrapped to its width.
func (m *Model) renderContents() {
	msg := m.loaded
	if msg == nil {
		m.contents.SetContent(mutedStyle.Render("…"))
		return
	}
	body := lipgloss.NewStyle().Width(m.contents.Width()).PaddingLeft(2)
	var blocks []string
	if msg.err != nil {
		blocks = append(blocks, lipgloss.Wrap(errStyle.Render("✗ "+msg.err.Error()), m.contents.Width(), ""))
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
	m.contents.SetContent(strings.Join(blocks, "\n\n"))
}

// handleContextKey handles keys on the context screen.
func (m *Model) handleContextKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		m.screen = screenChat
	case "up", "k":
		m.contents.ScrollUp(1)
	case "down", "j":
		m.contents.ScrollDown(1)
	case "pgup":
		m.contents.PageUp()
	case "pgdown":
		m.contents.PageDown()
	case "home":
		m.contents.GotoTop()
	case "end":
		m.contents.GotoBottom()
	}
	return nil
}

// contextView shows the context of the conversation, with how long it was
// when the LLM was last called.
func (m *Model) contextView() string {
	w, h := m.width, m.bodyHeight()
	title := boldStyle.Render("Context")
	if m.chat.context > 0 {
		title += mutedStyle.Render("  " + contextSize(m.chat.context) + " at the last call")
	}
	if m.chat.cache.Tokens > 0 {
		title += mutedStyle.Render(", cached " + cacheRate(m.chat.cache))
	}
	title += mutedStyle.Render(fmt.Sprintf("  · %.0f%%  · ↑↓ pgup pgdn scroll · esc back", m.contents.ScrollPercent()*100))
	return box(true, m.light).Width(w).Height(h).MaxHeight(h).Render(title + "\n\n" + m.contents.View())
}
