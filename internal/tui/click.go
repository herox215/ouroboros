package tui

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// The mouse does what it does in the window of the GUI. Beside the history,
// a click on a session shows it, on a button presses it, on an answer of a
// question gives it, on a command that is offered runs it, and in the
// composer puts the cursor where it is. In a dialog a click on a line
// selects what it shows, and on what is selected already does it, as enter
// does; a list of conversations shows the one clicked at once. A click
// beside a dialog closes it, as esc does.

// hit is a place a click does something on: a line of what is drawn, the
// cells of it from and to, all of them for to 0, and what the click does.
type hit struct {
	line, from, to int
	do             func() tea.Cmd
}

// onLine is a hit on all of the line i.
func onLine(i int, do func() tea.Cmd) hit { return hit{line: i, do: do} }

// hitAt is what a click on the cell col of the line i does, nil for nothing.
func hitAt(hits []hit, i, col int) func() tea.Cmd {
	for _, h := range hits {
		if h.line == i && (h.to == 0 || col >= h.from && col < h.to) {
			return h.do
		}
	}
	return nil
}

// shifted are the hits of lines that were cut from the line first on, put
// at where they are drawn: below the head lines above them.
func shifted(hits []hit, first, head, n int) []hit {
	var out []hit
	for _, h := range hits {
		if h.line >= first && h.line < first+n {
			h.line += head - first
			out = append(out, h)
		}
	}
	return out
}

// choosing is what a click on an entry of a list does: it selects the entry,
// and on the one selected already does what enter does.
func choosing(selected bool, choose func(), enter func() tea.Cmd) func() tea.Cmd {
	return func() tea.Cmd {
		if selected {
			return enter()
		}
		choose()
		return nil
	}
}

// choiceHits are the hits of a row of choices, as choices draws them, on
// the line i from the cell from on: a click on one chooses it and does it.
func choiceHits(i, from int, names []string, choose func(int) tea.Cmd) []hit {
	var hits []hit
	for j, n := range names {
		w := ansi.StringWidth(n) + 2
		hits = append(hits, hit{line: i, from: from, to: from + w, do: func() tea.Cmd { return choose(j) }})
		from += w + 1
	}
	return hits
}

var (
	enterKey = tea.KeyPressMsg{Code: tea.KeyEnter}
	escKey   = tea.KeyPressMsg{Code: tea.KeyEscape}
)

// dialogView is the dialog that lies over the chat, see inDialog, and
// where it is clicked.
func (m *Model) dialogView() (string, []hit) {
	switch m.screen {
	case screenSuggestions:
		return m.suggestionRequests.view(m)
	case screenHelp:
		return m.helpView()
	case screenKeys:
		return m.keysView(), nil
	case screenModel:
		return m.picker.view(m)
	case screenSettings:
		return m.config.view(m)
	case screenSessions:
		return m.sessions.view(m)
	case screenEffort:
		return m.effort.view(m)
	}
	return m.jobs.view(m)
}

// dialogAt is where the dialog lies that overlay lays over the chat: its
// left column and its top row.
func (m *Model) dialogAt(dialog string) (left, top int) {
	return max(0, (m.width-lipgloss.Width(dialog))/2), max(0, (m.height-lipgloss.Height(dialog)-1)/2)
}

// clickDialog does what a click at x and y does to the dialog: inside of
// it what is there, beside it what esc does.
func (m *Model) clickDialog(x, y int) tea.Cmd {
	dialog, hits := m.dialogView()
	left, top := m.dialogAt(dialog)
	if x < left || x >= left+lipgloss.Width(dialog) || y < top || y >= top+lipgloss.Height(dialog) {
		return m.handleKey(escKey)
	}
	// The lines begin inside the border and the padding of the box.
	if do := hitAt(hits, y-top-1, x-left-2); do != nil {
		return do()
	}
	return nil
}

// clickPlugin does what a click on an action of the screen of a plugin
// does: it runs it.
func (m *Model) clickPlugin(x, y int) tea.Cmd {
	actions := m.plugins.screens[m.plugins.open].Actions
	names := make([]string, len(actions))
	for i, a := range actions {
		names[i] = a.Name
	}
	// The actions are on the last line in the box, below the header.
	row := m.bodyHeight() - 1
	do := hitAt(choiceHits(row, 2, names, func(j int) tea.Cmd {
		m.choice = j
		return m.plugins.handleKey(m, enterKey)
	}), y, x)
	if do == nil {
		return nil
	}
	return do()
}

// clickComposer puts the cursor of the composer where it was clicked: on
// the row and the column of what is written, or after its end.
func (m *Model) clickComposer(row, col int) {
	m.input.MoveToBegin()
	for range row {
		m.input.CursorDown()
	}
	m.input.SetCursorColumn(m.input.LineInfo().StartColumn + max(0, col))
}
