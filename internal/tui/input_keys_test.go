package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestChatNewlineKeys(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  tea.KeyPressMsg
		want string
	}{
		{"alt+enter", tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModAlt}, "hello"},
		{"shift+enter", tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift}, "hello\n"},
		{"ctrl+j", tea.KeyPressMsg{Code: 'j', Mod: tea.ModCtrl}, "hello"}, // it shows the jobs
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := newModel(t)
			m.input.SetValue("hello")
			m.input.CursorEnd()
			m.Update(tc.key)
			if got := m.input.Value(); got != tc.want {
				t.Fatalf("input = %q, want %q", got, tc.want)
			}
			if m.chat.Pending {
				t.Fatal("newline keys should not send a message")
			}
		})
	}
}
