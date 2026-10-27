package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// A dialog is a box in the middle of the chat that lies over it, the chat
// faded behind it: what is chosen in it, like the effort or the model, is
// chosen for the chat it lies over.

// dialogWidth is how wide a dialog is at most, with its border and padding.
const dialogWidth = 80

// The dialog of the conversations stays smaller than the others: it is a
// short list to pick one from, and what is typed filters it.
const (
	sessionsWidth = 64 // with its border and padding
	sessionsRows  = 8  // how many conversations it lists at once
)

// memoryWidth is how wide the dialog of the memories is: a key, what it
// holds and how old it is share a line.
const memoryWidth = 84

// inDialog reports whether a dialog lies over the chat: the chat behind it
// stays as it is, also when the wheel turns.
func (m *Model) inDialog() bool {
	switch m.screen {
	case screenSuggestions, screenEffort, screenJobs, screenModel, screenHelp, screenKeys, screenSettings, screenSessions, screenMemory:
		return true
	}
	return false
}

// dialogSize is how wide and how high a dialog may be in the body, with its
// border and padding: it leaves a margin of the chat around it.
func (m *Model) dialogSize() (width, height int) {
	room := m.height - 4 - m.pendingRows() // a margin, and the line of the keys below the dialog
	width, height = dialogWidth, room
	switch m.screen {
	case screenHelp: // the commands, each with what it does
		width = m.width - 4
	case screenSessions: // as high as the list it shows, not as high as the chat
		width, height = sessionsWidth, sessionsRows+7
	case screenMemory: // a key, what it holds and how old it is share a line
		width = memoryWidth
	}
	return max(1, min(width, m.width-4)), max(1, min(height, room))
}

// dialog renders lines in the box of a dialog, as wide as the body allows.
// Lines that are too long are truncated; what does not fit below is cut
// off above the border.
func (m *Model) dialog(lines []string) string {
	w, h := m.dialogSize()
	clipped := make([]string, len(lines))
	for i, line := range lines {
		clipped[i] = ansi.Truncate(line, max(1, w-4), "…")
	}
	lines = clipped
	inner := lipgloss.NewStyle().Width(max(1, w-4)).MaxHeight(max(1, h-2)).Render(strings.Join(lines, "\n"))
	return box(true, m.light).Width(w).Render(inner)
}

// centered returns lines moved right as one block, to the middle of what
// is inside a dialog: by as much as the widest of them leaves there.
func (m *Model) centered(lines ...string) []string {
	w, _ := m.dialogSize()
	widest := 0
	for _, l := range lines {
		widest = max(widest, lipgloss.Width(l))
	}
	pad := strings.Repeat(" ", max(0, (w-4-widest)/2))
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = pad + l
	}
	return out
}

// overlay lays dialog over the chat, in the middle of the window, as the
// GUI opens its dialogs: the chat faded behind it, so that only the dialog
// stands out, and below it what happened and the keys that do something in
// it.
func (m *Model) overlay(dialog string) string {
	w, h := m.width, m.height
	back := strings.Split(m.chatScreen(), "\n")
	hint := m.footerHint()
	dw, dh := lipgloss.Width(dialog), lipgloss.Height(dialog)
	x := max(0, (w-dw)/2)
	y := max(0, (h-dh-1)/2)
	// The layer of the dialog clears all it covers before it is drawn: the
	// chat does not show through it, and stays whole around it, also where
	// its letters are wide.
	style := lipgloss.NewStyle().Foreground(fadeColor(m.light))
	for i, l := range back {
		back[i] = style.Render(ansi.Strip(l))
	}
	layers := []*lipgloss.Layer{
		lipgloss.NewLayer(strings.Join(back, "\n")),
		lipgloss.NewLayer(dialog).X(x).Y(y).Z(1),
	}
	if hint != "" {
		hint = ansi.Truncate(hint, w, "…")
		layers = append(layers, lipgloss.NewLayer(hint).X(max(0, (w-lipgloss.Width(hint))/2)).Y(min(h-1, y+dh)).Z(1))
	}
	canvas := lipgloss.NewCanvas(w, h)
	canvas.Compose(lipgloss.NewCompositor(layers...))
	// The canvas trims what is blank at the end; the window keeps its height.
	return lipgloss.NewStyle().Width(w).Height(h).MaxHeight(h).Render(canvas.Render())
}
