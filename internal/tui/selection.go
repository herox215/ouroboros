package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/atotto/clipboard"
	"github.com/charmbracelet/x/ansi"

	"ouroboros/plugin"
)

// The UI takes the mouse, for the wheel, so the terminal does not select
// text itself. The chat does instead: the mouse pressed on the history and
// dragged selects from there, and once it is released the text selected goes
// to the clipboard. A click selects nothing, and a key drops the selection.
// Nothing is marked before the drag begins, so a press alone leaves no mark
// under the pointer.

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
	row := min(max(0, y-m.historyTop()), max(0, m.history.Height()-1))
	return spot{m.history.YOffset() + row, max(0, x-m.transcriptLeft())}
}

// onHistory tells if x and y are on the history of the chat.
func (m *Model) onHistory(x, y int) bool {
	left, top := m.transcriptLeft(), m.historyTop()
	return x >= left && x < left+m.history.Width() && y >= top && y < top+m.history.Height()
}

// mouse selects text of the history with the mouse and copies it. A press
// only remembers where it went down: the selection begins with the drag, so
// a click marks nothing.
func (m *Model) mouse(msg tea.MouseMsg) tea.Cmd {
	if m.onboard != nil || m.splash.show {
		return nil
	}
	at := msg.Mouse()
	if _, ok := msg.(tea.MouseClickMsg); ok && at.Button == tea.MouseLeft {
		switch {
		case m.inDialog():
			return m.clickDialog(at.X, at.Y)
		case m.screen == screenPlugin:
			return m.clickPlugin(at.X, at.Y)
		}
	}
	if m.screen != screenChat || !m.status.Configured {
		return nil
	}
	if _, ok := msg.(tea.MouseMotionMsg); !ok || m.pressed != nil {
		if gone := m.unhover(); gone != nil {
			return tea.Batch(gone, m.mouseOnChat(msg))
		}
	}
	return m.mouseOnChat(msg)
}

// mouseOnChat does what the mouse does on the chat, see mouse.
func (m *Model) mouseOnChat(msg tea.MouseMsg) tea.Cmd {
	at := msg.Mouse()
	switch msg.(type) {
	case tea.MouseClickMsg:
		m.selected, m.pressed = nil, nil
		if at.Button == tea.MouseLeft {
			if cmd, ok := m.click(at.X, at.Y); ok {
				return cmd
			}
		}
		if at.Button == tea.MouseLeft && m.onHistory(at.X, at.Y) {
			s := m.historySpot(at.X, at.Y)
			m.pressed = &s // the drag has not begun, so nothing is marked yet
		}
	case tea.MouseMotionMsg:
		if m.pressed == nil {
			return m.rest(at.X, at.Y)
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

// click does what is clicked beside the history, as in the GUI: a session
// of the sidebar or of the column on the right shows it, the button of the
// settings opens them, and those of the head and the composer take the owner
// to the thread, stop the answer and send. What is asked or offered above
// the composer is answered or run, and a click into the composer puts its
// cursor there. It reports whether something was there.
func (m *Model) click(x, y int) (tea.Cmd, bool) {
	side := m.sideWidth()
	if x < side {
		if c := m.sidebarAt(y); c != nil {
			return m.switchTo(c), true
		}
		if y == m.height-2 && x >= columnPad && x < columnPad+2 {
			return m.openSettings(), true
		}
		return nil, false
	}
	// The name of the conversation, and the button beside it, are in the
	// middle of the head.
	right := side + m.chatWidth() - columnPad
	if y == m.headerHeight()/2 && m.chat.contextID != plugin.ThreadID && x < right && x >= right-lipgloss.Width(threadButton()) {
		return m.openThread(), true
	}
	if m.onHistory(x, y) {
		if path, ok := m.soundLines[m.historySpot(x, y).line]; ok {
			return m.toggleSound(path), true
		}
	}
	left := m.transcriptLeft()
	row := m.historyTop() + m.chatHeight() + 2 // below the status and the line above the composer
	if panel, hits := m.panel(); len(panel) > 0 {
		if do := hitAt(hits, y-row, x-left); do != nil {
			return do(), true
		}
		row += len(panel) + 1
	}
	if r, col := y-row, x-left-promptWidth; r >= 0 && r < m.composerRows() && col >= 0 && col < m.composerInner() {
		m.clickComposer(r, col)
		return nil, true
	}
	if x >= side+m.chatWidth() {
		if c := m.railAt(y); c != nil {
			return m.switchTo(c), true
		}
	}
	if y == m.height-2-m.pendingRows() {
		stop := left + promptWidth + m.composerInner() + 1
		switch {
		case x >= stop && x < stop+stopWidth && m.chat.pending && m.chat.peer == "":
			return m.cancel(), true
		case x >= stop+stopWidth+1 && x < stop+stopWidth+1+sendWidth:
			return m.send(), true
		}
	}
	return nil, false
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
