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

// inDialog reports whether a dialog lies over the chat: the chat behind it
// stays as it is, also when the wheel turns.
func (m *Model) inDialog() bool {
	return m.screen == screenEffort || m.screen == screenModel
}

// dialogSize is how wide and how high a dialog may be in the body, with its
// border and padding: it leaves a margin of the chat around it.
func (m *Model) dialogSize() (width, height int) {
	return max(1, min(dialogWidth, m.width-4)), max(1, m.bodyHeight()-2)
}

// dialog renders lines in the box of a dialog, as wide as the body allows.
// Lines that are too long wrap; what does not fit below is cut off, above
// the border.
func (m *Model) dialog(lines []string) string {
	w, h := m.dialogSize()
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

// overlay lays dialog over the chat, in the middle of the body. The chat is
// faded behind it, so that only the dialog stands out.
func (m *Model) overlay(dialog string) string {
	w, h := m.width, m.bodyHeight()
	back := strings.Split(m.chatView(), "\n")
	style := lipgloss.NewStyle().Foreground(fadeColor(m.light))
	for i, l := range back {
		back[i] = style.Render(ansi.Strip(l))
	}
	x := max(0, (w-lipgloss.Width(dialog))/2)
	y := max(0, (h-lipgloss.Height(dialog))/2)
	canvas := lipgloss.NewCanvas(w, h)
	canvas.Compose(lipgloss.NewCompositor(
		lipgloss.NewLayer(strings.Join(back, "\n")),
		lipgloss.NewLayer(dialog).X(x).Y(y).Z(1),
	))
	// The canvas trims what is blank at the end; the body keeps its height.
	return lipgloss.NewStyle().Width(w).Height(h).MaxHeight(h).Render(canvas.Render())
}
