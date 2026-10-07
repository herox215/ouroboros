package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/atotto/clipboard"
	"github.com/charmbracelet/x/ansi"
)

// The UI takes the mouse, for the wheel, so the terminal does not select
// text itself. The chat does instead: the mouse pressed on the history and
// dragged selects from there, and once it is released the text selected goes
// to the clipboard. A click selects nothing, and a key drops the selection.
// Nothing is marked before the drag begins, so a press alone leaves no mark
// under the pointer.

// The history starts below the header, the top of the box and its title, and
// right of the border and padding of the box.
const historyTop, historyLeft = 3, 2

// spot is a cell of the content of the history: its line and column.
type spot struct{ line, col int }

// selection is the text selected in the history, from where the mouse was
// pressed to where it is or was released.
type selection struct {
	from, to spot
	dragging bool // the button is still down
}

// writeClipboard puts text on the clipboard of the system. The terminal is
// asked to as well, which also works over SSH.
var writeClipboard = clipboard.WriteAll

// span is the selection in the order of reading: its first and its last cell.
func (s *selection) span() (first, last spot) {
	first, last = s.from, s.to
	if last.line < first.line || last.line == first.line && last.col < first.col {
		first, last = last, first
	}
	return first, last
}

// cols is the columns of line that are selected, from and to, to excluded.
func (s *selection) cols(line int) (from, to int, ok bool) {
	first, last := s.span()
	if line < first.line || line > last.line {
		return 0, 0, false
	}
	from, to = 0, -1 // to the end
	if line == first.line {
		from = first.col
	}
	if line == last.line {
		to = last.col + 1
	}
	return from, to, true
}

// historySpot is the cell of the history at x and y on the screen, the rows
// above and below the history taken as its first and its last.
func (m *Model) historySpot(x, y int) spot {
	hidden := len(m.suggestionLines(m.chatHeight() - 3)) // the rows the history gives up to them
	row := min(max(0, y-historyTop), max(0, m.history.Height()-hidden-1))
	return spot{m.history.YOffset() + hidden + row, max(0, x-historyLeft)}
}

// onHistory tells if x and y are on the history of the chat.
func (m *Model) onHistory(x, y int) bool {
	hidden := len(m.suggestionLines(m.chatHeight() - 3))
	return x >= historyLeft && x < historyLeft+m.history.Width() &&
		y >= historyTop && y < historyTop+m.history.Height()-hidden
}

// mouse selects text of the history with the mouse and copies it. A press
// only remembers where it went down: the selection begins with the drag, so
// a click marks nothing.
func (m *Model) mouse(msg tea.MouseMsg) tea.Cmd {
	if m.onboard != nil || m.showSplash || m.screen != screenChat || !m.status.Configured {
		return nil
	}
	at := msg.Mouse()
	switch msg.(type) {
	case tea.MouseClickMsg:
		m.selected, m.pressed = nil, nil
		if at.Button == tea.MouseLeft && m.onHistory(at.X, at.Y) {
			s := m.historySpot(at.X, at.Y)
			m.pressed = &s // the drag has not begun, so nothing is marked yet
		}
	case tea.MouseMotionMsg:
		if m.pressed == nil {
			return nil
		}
		to := m.historySpot(at.X, at.Y)
		if m.selected == nil {
			m.selected = &selection{from: *m.pressed, to: to, dragging: true}
			return nil
		}
		m.selected.to = to
	case tea.MouseReleaseMsg:
		m.pressed = nil
		if m.selected == nil || !m.selected.dragging {
			return nil
		}
		m.selected.to, m.selected.dragging = m.historySpot(at.X, at.Y), false
		if m.selected.from == m.selected.to {
			m.selected = nil // a click
			return nil
		}
		return m.copySelection()
	}
	return nil
}

// copySelection puts the text selected on the clipboard, without its styles
// and without the spaces at the ends of its lines.
func (m *Model) copySelection() tea.Cmd {
	content := strings.Split(m.history.GetContent(), "\n")
	first, last := m.selected.span()
	var lines []string
	for i := first.line; i <= min(last.line, len(content)-1); i++ {
		from, to, _ := m.selected.cols(i)
		line := ansi.Strip(content[i])
		if to < 0 {
			to = ansi.StringWidth(line)
		}
		lines = append(lines, strings.TrimRight(ansi.Cut(line, from, to), " "))
	}
	text := strings.Join(lines, "\n")
	m.setNotice("copied to the clipboard", false)
	return tea.Batch(tea.SetClipboard(text), func() tea.Msg {
		_ = writeClipboard(text) // the terminal may have it anyway
		return nil
	})
}

// highlight marks what is selected of the lines the history shows.
func (m *Model) highlight(lines []string) []string {
	if m.selected == nil {
		return lines
	}
	marked := lipgloss.NewStyle().Reverse(true)
	for i, l := range lines {
		from, to, ok := m.selected.cols(m.history.YOffset() + i)
		if !ok {
			continue
		}
		// The spaces at the end are not copied, so they are not marked.
		end := ansi.StringWidth(strings.TrimRight(ansi.Strip(l), " "))
		if to < 0 || to > end {
			to = end
		}
		if from >= to {
			continue
		}
		lines[i] = ansi.Cut(l, 0, from) + marked.Render(ansi.Strip(ansi.Cut(l, from, to))) + ansi.Cut(l, to, ansi.StringWidth(l))
	}
	return lines
}
