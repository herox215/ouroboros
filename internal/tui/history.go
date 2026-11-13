package tui

import "strings"

// historyView is the scrollback of the conversation: the lines of what it
// shows as far as they cannot change anymore, the lines of the answer that is
// still being written and of what waits below it, and the window on them.
//
// It takes the place of the viewport of bubbles, which measured every line it
// was given on every frame to find the widest one: a long conversation then
// cost more the older it was, on every piece of an answer. This view knows
// the width its lines are wrapped to, and keeps what changes apart from what
// stands, so that a frame of an answer only touches the answer.
type historyView struct {
	settled []string // the lines of the conversation as far as they stay
	live    []string // the lines of the answer on its way and what waits
	width   int
	height  int
	yOffset int

	// content caches what GetContent returns, until the lines change.
	content string
	cached  bool
}

// Width is how wide the history is: the width its lines were wrapped to.
func (v *historyView) Width() int { return v.width }

// Height is how many rows of it the window shows.
func (v *historyView) Height() int { return v.height }

// SetWidth gives the history the width the chat wraps to.
func (v *historyView) SetWidth(w int) { v.width = w }

// SetHeight gives the history the rows the window has for it.
func (v *historyView) SetHeight(h int) {
	v.height = h
	v.clamp()
}

// SetLines puts the lines of the settled conversation and those of what is
// still coming into the history: a blank line parts the two where both have
// some.
func (v *historyView) SetLines(settled, live []string) {
	v.settled, v.live = settled, live
	v.cached = false
	v.clamp()
}

// SetContent puts s into the history as lines that all stand.
func (v *historyView) SetContent(s string) {
	if s == "" {
		v.SetLines(nil, nil)
		return
	}
	v.SetLines(strings.Split(s, "\n"), nil)
}

// GetContent is all of the history as one string, as the chat would show it
// without its window.
func (v *historyView) GetContent() string {
	if v.cached {
		return v.content
	}
	var b strings.Builder
	for i := range v.TotalLineCount() {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(v.line(i))
	}
	v.content, v.cached = b.String(), true
	return v.content
}

// TotalLineCount is how many lines the history holds.
func (v *historyView) TotalLineCount() int {
	n := len(v.settled) + len(v.live)
	if len(v.settled) > 0 && len(v.live) > 0 {
		n++ // the blank line between what stands and what is still coming
	}
	return n
}

// line is the line i of the history, "" beyond its end.
func (v *historyView) line(i int) string {
	n := len(v.settled)
	if i < n {
		if i < 0 {
			return ""
		}
		return v.settled[i]
	}
	if len(v.live) == 0 {
		return ""
	}
	if n > 0 {
		if i == n {
			return "" // the line that parts the two
		}
		i--
	}
	if i -= n; i >= 0 && i < len(v.live) {
		return v.live[i]
	}
	return ""
}

// maxYOffset is how far the history scrolls: the last line of it that can be
// at the top, 0 when all of it fits.
func (v *historyView) maxYOffset() int { return max(0, v.TotalLineCount()-v.height) }

// YOffset is the line of the history that is at the top of the window.
func (v *historyView) YOffset() int { return v.yOffset }

// SetYOffset moves the window to line i, within the history.
func (v *historyView) SetYOffset(i int) {
	v.yOffset = i
	v.clamp()
}

// GotoTop shows the history from its first line.
func (v *historyView) GotoTop() { v.SetYOffset(0) }

// GotoBottom shows the history down to its last line.
func (v *historyView) GotoBottom() { v.SetYOffset(v.maxYOffset()) }

// AtBottom reports whether the window is at the end of the history.
func (v *historyView) AtBottom() bool { return v.yOffset >= v.maxYOffset() }

// clamp keeps the window inside the history, as it grows or shrinks.
func (v *historyView) clamp() {
	v.yOffset = max(0, min(v.yOffset, v.maxYOffset()))
}

// View is what the window shows of the history: as many lines as it is high.
// The chat pads them to its width, so that a frame does not measure every line
// of it again.
func (v *historyView) View() string {
	if v.width <= 0 || v.height <= 0 {
		return ""
	}
	var b strings.Builder
	for i := range v.height {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(v.line(v.yOffset + i))
	}
	return b.String()
}
