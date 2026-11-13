package tui

import (
	"strings"
	"time"

	"charm.land/lipgloss/v2"
)

// streamView is the answer that is still being written, as the chat draws
// it: the blocks of it that stand are rendered once and kept, and only the
// block that is still growing is drawn on every frame. Without it a long
// answer would be formatted and wrapped whole twenty times a second, and the
// UI would lag more the longer the answer got.
type streamView struct {
	text  string // the part of the answer that block holds
	block string // the head of the answer and the rendered text, without the cursor
	width int
	light bool
	name  string
}

// blank is whether s is a line without anything on it.
func blank(s string) bool { return strings.TrimSpace(s) == "" }

// fence is whether s opens or closes a fenced code block.
func fence(s string) bool {
	t := strings.TrimLeft(s, " \t")
	return strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~")
}

// continues is whether the line s continues the block of the line above it
// across a blank line: an indented line, a list item, a quote or a table row.
// Such a line may not begin a new block on its own, so the answer cannot be
// cut before it.
func continues(s string) bool {
	if s == "" || s[0] == ' ' || s[0] == '\t' {
		return true
	}
	switch s[0] {
	case '-', '*', '+', '>', '|':
		return true
	}
	// A numbered list item, like "12. next".
	for i := 0; i < len(s) && i < 9; i++ {
		if s[i] < '0' || s[i] > '9' {
			return strings.HasPrefix(s[i:], ". ")
		}
	}
	return false
}

// streamCut is where the part of the answer that stands ends: just after the
// last blank line that is not inside a fenced code block and that is followed
// by a line which begins a block of its own. from is where to look: the part
// before it is known to stand already. It returns 0 if there is no such line.
func streamCut(s string, from int) int {
	if from < 0 || from > len(s) {
		from = 0
	}
	cut := 0
	open := false // a fenced code block is open, so a blank line is inside it
	for pos := from; pos < len(s); {
		end := strings.IndexByte(s[pos:], '\n')
		if end < 0 {
			break
		}
		line, next := s[pos:pos+end], pos+end+1
		if fence(line) {
			open = !open
		}
		if !open && blank(line) && next < len(s) {
			rest := s[next:]
			if e := strings.IndexByte(rest, '\n'); e >= 0 {
				rest = rest[:e]
			}
			if !blank(rest) && !continues(rest) {
				cut = next
			}
		}
		pos = next
	}
	return cut
}

// streamBlocks draws the answer that is being written, c.partial, in the
// width of the chat: the head of the answer and the blocks that stand, which
// are rendered once and kept, and the block that is still growing, which is
// drawn anew on every frame. The last word is threaded in as far as the
// frames of the answer have brought it.
func (m *Model) streamBlocks(c *conversation, width int) []string {
	s := &c.stream
	name := m.agentName()
	head := boldStyle.Foreground(muted).Render(name + " · writing …")
	if s.width != width || s.light != m.light || s.name != name || len(s.text) > len(c.partial) || !strings.HasPrefix(c.partial, s.text) {
		*s = streamView{width: width, light: m.light, name: name}
	}
	// What begins a block of its own is drawn with the blocks that stand.
	if cut := streamCut(c.partial, len(s.text)); cut > len(s.text) {
		part := lipgloss.Wrap(renderMarkdown(c.partial[len(s.text):cut], width), width, "")
		if s.block == "" {
			s.block = head + "\n" + part
		} else {
			s.block += "\n\n" + part
		}
		s.text = c.partial[:cut]
	}
	rest := c.partial[len(s.text):]
	if s.block == "" {
		return []string{head + "\n" + lipgloss.Wrap(renderMarkdown(threadIn(rest, c.advance()), width), width, "") + mutedStyle.Render(" ▌")}
	}
	if strings.TrimSpace(rest) == "" {
		return []string{s.block + mutedStyle.Render(" ▌")}
	}
	return []string{s.block, lipgloss.Wrap(renderMarkdown(threadIn(rest, c.advance()), width), width, "") + mutedStyle.Render(" ▌")}
}

// answerFrameFor is how often the chat is drawn while the answer c is being
// written. A block that cannot be cut, as one long paragraph or an open code
// block without a line that begins a block, is drawn less often: the frame
// costs what that block is, and the chat is to stay responsive while the
// answer grows.
func answerFrameFor(c *conversation) time.Duration {
	tail := len(c.partial) - len(c.stream.text)
	switch {
	case tail > 64<<10:
		return 250 * time.Millisecond
	case tail > 16<<10:
		return 150 * time.Millisecond
	}
	return answerFrame
}
