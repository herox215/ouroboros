package tui

import (
	"slices"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// modeNames are the modes of the node, as /mode offers them, in the order of
// the row: the ordinary one first.
var modeNames = []string{"off", "yolo", "cyberpsycho"}

// modeAbout says what each mode does, a line at a time, for the dialog.
var modeAbout = map[string][]string{
	"off": {"A rule of tools.ask asks you before a tool call that needs it."},
	"yolo": {
		"No rule asks before a tool call.",
		"What the review of an answer proposes still waits in /suggestions.",
	},
	"cyberpsycho": {
		"No rule asks, and the review's change is made at once.",
		"Changes stay in the working tree,",
		"nothing is committed or pushed.",
	},
}

// modeScreen is the dialog of /mode: the modes of the node in a row, the one
// the dialog is at marked, and what it does below. Yolo and cyberpsycho
// exclude each other, so the node is in one of the three at a time.
type modeScreen struct {
	at int // of modeNames, the one the dialog is at
}

// modeOf is the mode the status says is on, as modeNames names it.
func modeOf(s Status) string {
	switch {
	case s.Cyberpsycho:
		return "cyberpsycho"
	case s.Yolo:
		return "yolo"
	}
	return "off"
}

// openMode opens the dialog at the mode of the node.
func (m *Model) openMode() tea.Cmd {
	m.mode.at = max(0, slices.Index(modeNames, modeOf(m.status)))
	m.screen = screenMode
	return nil
}

// modeArg is /mode with what follows it: it sets that mode, or opens the
// dialog when nothing follows. A name that is no mode is noticed.
func (m *Model) modeArg(arg string) tea.Cmd {
	if arg == "" {
		return m.openMode()
	}
	if !slices.Contains(modeNames, arg) {
		m.setNotice("no mode "+arg+": off, yolo or cyberpsycho", true)
		return nil
	}
	return m.setMode(arg)
}

// setMode makes mode the mode of the node: one of modeNames. The node keeps
// one mode at a time, so setting one leaves the other.
func (m *Model) setMode(mode string) tea.Cmd {
	done := "mode off"
	switch mode {
	case "yolo":
		done = "yolo mode on: no permission is asked before a tool call"
	case "cyberpsycho":
		done = "cyberpsycho mode on: no permission is asked, and what the review proposes is made at once; changes stay in the working tree, nothing is committed or pushed"
	case "off":
		switch {
		case m.status.Yolo:
			done = "yolo mode off"
		case m.status.Cyberpsycho:
			done = "cyberpsycho mode off"
		}
	}
	return func() tea.Msg { return actionMsg{done, m.backend.SetMode(mode)} }
}

// choose is what enter in the dialog and a click on a mode do: the mode the
// dialog is at is the mode of the node, and the dialog closes.
func (s *modeScreen) choose(m *Model) tea.Cmd {
	m.screen = screenChat
	return m.setMode(modeNames[s.at])
}

func (s *modeScreen) handleKey(m *Model, msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "left", "h", "up", "k":
		s.at = cycle(s.at, -1, len(modeNames))
	case "right", "l", "down", "j":
		s.at = cycle(s.at, 1, len(modeNames))
	case "esc":
		m.screen = screenChat
	case "enter":
		return s.choose(m)
	}
	return nil
}

// view shows the modes in a small dialog: a row to choose from, and what the
// mode at is does.
func (s *modeScreen) view(m *Model) (string, []hit) {
	lines := []string{
		boldStyle.Render("Mode"), "",
		mutedStyle.Render("What your agent may do without asking you."), "",
	}
	row := choices(modeNames, s.at)
	w, _ := m.dialogSize()
	rowLine := len(lines)
	lines = append(lines, m.centered(row)...)
	lines = append(lines, "")
	for _, line := range modeAbout[modeNames[s.at]] {
		lines = append(lines, m.centered(mutedStyle.Render(line))...)
	}
	lines = append(lines, "",
		mutedStyle.Render("Yolo and cyberpsycho exclude each other:"),
		mutedStyle.Render("choosing one leaves the other."),
	)
	hits := choiceHits(rowLine, max(0, (w-4-lipgloss.Width(row))/2), modeNames, func(at int) tea.Cmd {
		s.at = at
		return s.choose(m)
	})
	return m.dialog(lines), hits
}
