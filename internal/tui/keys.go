package tui

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// A keybinding is a key of the chat and what it does there, as the dialog
// of the keys lists it: ctrl+k and /keys open it.
type keybinding struct {
	keys string
	help string
}

// keybindings are the keys of the chat. shift+tab is one of them only in a
// UI of several profiles.
func (m *Model) keybindings() []keybinding {
	keys := []keybinding{
		{"enter", "send"},
		{"shift+enter / ctrl+j", "a new line"},
		{"↑ ↓", "bring back what you entered, the latest first"},
		{"↑ in an empty input", "take back the last message that is queued"},
		{"tab", "complete the command that is typed"},
		{"ctrl+v", "paste the clipboard, a picture too"},
		{"ctrl+s", "the conversations, like /session; what is typed stays"},
		{"ctrl+k", "these keys, like /keys"},
		{"pgup pgdn", "scroll the chat"},
	}
	if m.profileOf > 1 {
		keys = append(keys, keybinding{"shift+tab", "show the next profile"})
	}
	return append(keys,
		keybinding{"esc", "stop the answer on its way; on a screen: back"},
		keybinding{"ctrl+c", "quit, everywhere"},
	)
}

// openKeys opens the dialog of the keys. What is typed stays in the input.
func (m *Model) openKeys() tea.Cmd {
	m.screen = screenKeys
	return nil
}

// keysView lists the keys of the chat.
func (m *Model) keysView() string {
	keys := m.keybindings()
	width := 0
	for _, k := range keys {
		width = max(width, lipgloss.Width(k.keys))
	}
	lines := []string{boldStyle.Render("Keys"), ""}
	for _, k := range keys {
		lines = append(lines, lipgloss.NewStyle().Foreground(accent).Width(width+3).Render(k.keys)+mutedStyle.Render(k.help))
	}
	lines = append(lines, "", mutedStyle.Render("A screen lists its own keys in the footer. /help lists the commands."))
	return m.dialog(lines)
}
