package tui

import (
	"slices"
	"strings"

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
// mode at is does. While it is at the cyberpsycho mode, the dialog comes
// apart a little: its heading flickers, the name is drawn in the colors of a
// signal that cannot settle, and a broken line runs under the row.
func (s *modeScreen) view(m *Model) (string, []hit) {
	cyber := modeNames[s.at] == "cyberpsycho"
	heading := boldStyle.Render("Mode")
	if cyber {
		heading = glitchTitle(m.ticks)
	}
	lines := []string{
		heading, "",
		mutedStyle.Render("What your agent may do without asking you."), "",
	}
	row := s.row(m.ticks)
	w, _ := m.dialogSize()
	rowLine := len(lines)
	lines = append(lines, m.centered(row)...)
	lines = append(lines, "")
	if cyber {
		lines = append(lines, m.centered(glitchStrip(min(w-4, 48), m.ticks))...)
		lines = append(lines, "")
	}
	var about []string
	for _, line := range modeAbout[modeNames[s.at]] {
		if cyber {
			line = lipgloss.NewStyle().Foreground(bad).Render("▌ ") + mutedStyle.Render(line)
		} else {
			line = mutedStyle.Render(line)
		}
		about = append(about, line)
	}
	lines = append(lines, m.centered(about...)...)
	lines = append(lines, "")
	lines = append(lines, m.centered(mutedStyle.Render("Yolo and cyberpsycho exclude each other:"),
		mutedStyle.Render("choosing one leaves the other."))...)
	hits := choiceHits(rowLine, max(0, (w-4-lipgloss.Width(row))/2), modeNames, func(at int) tea.Cmd {
		s.at = at
		return s.choose(m)
	})
	return m.dialog(lines), hits
}

// row is the row of the modes: the one at marked, and the cyberpsycho mode
// in the colors of the warning and of the UI, one letter after the other.
func (s *modeScreen) row(tick int) string {
	parts := make([]string, len(modeNames))
	for i, n := range modeNames {
		switch {
		case i != s.at:
			parts[i] = mutedStyle.Render(" " + n + " ")
		case n == "cyberpsycho":
			parts[i] = glitchName(n, tick)
		default:
			parts[i] = boldStyle.Foreground(accent).Render("[" + n + "]")
		}
	}
	return strings.Join(parts, " ")
}

// glitchRunes are what the glitch of the cyberpsycho mode is made of: blocks
// and marks that stand where a letter or a cell of signal came apart.
var glitchRunes = []rune{'▓', '▒', '░', '▚', '▞', '▙', '▟', '#', '¬'}

// glitchCell is one cell of the glitch at a place of a run, chosen by the
// tick of the UI: one frame keeps what it drew until the next tick.
func glitchCell(tick, at int) string {
	return string(glitchRunes[(tick*7+at*11)%len(glitchRunes)])
}

// glitchTitle is the heading while the cyberpsycho mode is chosen: the o of
// Mode comes apart for a frame or two, as a signal does.
func glitchTitle(tick int) string {
	switch tick % 6 {
	case 1:
		return boldStyle.Render("M") + lipgloss.NewStyle().Foreground(bad).Render("0") + boldStyle.Render("DE")
	case 3:
		return boldStyle.Render("M") + lipgloss.NewStyle().Foreground(bad).Render(glitchCell(tick, 0)) + boldStyle.Render("DE")
	}
	return boldStyle.Render("Mode")
}

// glitchName is the name of the mode that does not leave the mind as it was:
// its letters in the two colors, so that it reads as a signal that cannot
// settle.
func glitchName(name string, tick int) string {
	var b strings.Builder
	for i, r := range name {
		style := lipgloss.NewStyle().Foreground(bad)
		if (i+tick)%3 == 0 {
			style = lipgloss.NewStyle().Foreground(accent)
		}
		b.WriteString(style.Render(string(r)))
	}
	return boldStyle.Render("[") + b.String() + boldStyle.Render("]")
}

// glitchStrip is a line of broken signal under the row while the
// cyberpsycho mode is chosen: sparse blocks that move with the tick.
func glitchStrip(w, tick int) string {
	var b strings.Builder
	for i := 0; i < w; i++ {
		if (i*5+tick*3)%7 >= 2 {
			b.WriteString(" ")
			continue
		}
		style := mutedStyle
		if i%2 == 0 {
			style = lipgloss.NewStyle().Foreground(bad)
		}
		b.WriteString(style.Render(glitchCell(tick, i)))
	}
	return b.String()
}
