package tui

import (
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
)

// textReveal only limits what is drawn; the complete answer stays in history.
type textReveal struct {
	runes []rune
	shown int
}

type revealMsg struct {
	c      *conversation
	index  int
	reveal *textReveal
}

func revealTick(msg revealMsg) tea.Cmd {
	return tea.Tick(answerFrame, func(time.Time) tea.Msg { return msg })
}

// revealAnswer starts a fast local stream for an answer delivered in one event.
func (m *Model) revealAnswer(c *conversation) tea.Cmd {
	i := len(c.entries) - 1
	r := &textReveal{runes: []rune(c.entries[i].text)}
	c.entries[i].reveal = r
	r.advance()
	return revealTick(revealMsg{c, i, r})
}

// advance reveals small chunks, finishing even long answers in about two seconds.
// Prefer word boundaries, but do not wait for whitespace in unspaced languages.
func (r *textReveal) advance() {
	end := min(len(r.runes), r.shown+max(8, (len(r.runes)+39)/40))
	for n := 0; end < len(r.runes) && n < 12 && !unicode.IsSpace(r.runes[end]); n++ {
		end++
	}
	r.shown = end
}

func (m *Model) revealFrame(msg revealMsg) tea.Cmd {
	c, i, r := msg.c, msg.index, msg.reveal
	// A replay may have replaced the entries since this frame was scheduled.
	if i >= len(c.entries) || c.entries[i].reveal != r {
		return nil
	}
	r.advance()
	if r.shown == len(r.runes) || c != m.chat {
		c.entries[i].reveal = nil
	}
	var glide tea.Cmd
	if c == m.chat {
		following := m.followsEnd()
		m.renderHistory(false)
		if following {
			glide = m.glideTo(&m.history, topLine(&m.history))
		}
	}
	if c.entries[i].reveal != nil {
		return tea.Batch(revealTick(msg), glide)
	}
	return glide
}
