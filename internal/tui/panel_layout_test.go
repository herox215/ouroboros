package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// Questions give way to the composer, including on short windows and when
// every answer needs a separate row. Keyboard selection scrolls the buttons.
func TestQuestionPanelPreservesComposer(t *testing.T) {
	for _, size := range []struct{ height, options int }{{12, 2}, {24, 15}} {
		t.Run(fmt.Sprintf("height%d", size.height), func(t *testing.T) {
			m, _ := newModel(t)
			m.Update(tea.WindowSizeMsg{Width: 80, Height: size.height})
			options := make([]string, size.options)
			for i := range options {
				options[i] = fmt.Sprintf("Option %02d %s", i, strings.Repeat("answer ", 6))
			}
			a := &asking{question: "Choose a mode", options: options}
			m.chat.asking = a
			m.input.SetValue("UNIQUE_COMPOSER_TEXT")
			for selected := range options {
				a.sel = selected
				m.layout()
				view := strip(m.mainView())
				if !strings.Contains(view, "UNIQUE_COMPOSER_TEXT") {
					t.Fatalf("composer hidden:\n%s", view)
				}
				lines, hits := m.panel()
				if len(lines) > m.panelHeight() || m.headerHeight()+m.chatHeight()+m.bottomRows() > m.height {
					t.Fatal("panel exceeded its height budget")
				}
				label := fmt.Sprintf("Option %02d", selected)
				found := false
				for _, h := range hits {
					if strings.Contains(strip(lines[h.line]), label) {
						found = true
						if h.do() == nil || !a.sent {
							t.Fatal("visible option has no working click target")
						}
						a.sent = false
						break
					}
				}
				if !found {
					t.Fatalf("selected answer %d is not visible and clickable: %v", selected, lines)
				}
			}
		})
	}
}
