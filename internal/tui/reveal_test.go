package tui

import (
	"strings"
	"testing"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"ouroboros/plugin"
)

func TestThreadAnswerReveal(t *testing.T) {
	text := strings.Repeat("Grüße 世界, fertig! ", 20) + "ENDE"
	m, _ := threadModel(t, "", nil)
	c := m.chat
	m.event(eventMsg{id: c.contextID, event: Event{Kind: plugin.SessionAnswer, Text: text}})
	r := c.entries[0].reveal
	if r == nil || r.shown == 0 || r.shown >= len(r.runes) || c.entries[0].text != text {
		t.Fatal("answer did not begin revealing with its complete text preserved")
	}
	before := ansi.Strip(m.history.GetContent())
	if strings.Contains(before, "ENDE") || !utf8.ValidString(before) {
		t.Fatalf("initial frame: %q", before)
	}
	msg := revealMsg{c, 0, r}
	for n := 0; n < 50 && c.entries[0].reveal != nil; n++ {
		m.Update(msg)
	}
	if c.entries[0].reveal != nil || !strings.Contains(ansi.Strip(m.history.GetContent()), "ENDE") {
		t.Fatal("answer did not finish revealing")
	}
	m.replay(c, []Event{{Kind: plugin.SessionAnswer, Text: text}})
	m.Update(msg) // a stale timer must not animate replayed history
	if c.entries[0].reveal != nil || !strings.Contains(ansi.Strip(m.history.GetContent()), "ENDE") {
		t.Fatal("replayed history was animated")
	}
	c.partial = "already streamed"
	m.event(eventMsg{id: c.contextID, event: Event{Kind: plugin.SessionAnswer, Text: text}})
	if c.entries[1].reveal != nil {
		t.Fatal("streamed answer was animated twice")
	}
	topic := m.conversation("topic")
	m.event(eventMsg{id: topic.contextID, event: Event{Kind: plugin.SessionAnswer, Text: text}})
	if topic.entries[0].reveal != nil {
		t.Fatal("background answer was animated")
	}
}

// advance reveals a fortieth of a long answer a frame, so that even a long
// one is done in about two seconds.
func TestAdvanceRevealsAFortiethOfALongAnswer(t *testing.T) {
	r := &textReveal{runes: []rune(strings.Repeat("Wort ", 800))} // 4000 runes
	r.advance()
	if r.shown < 100 {
		t.Fatalf("the first frame shows %d runes of %d", r.shown, len(r.runes))
	}
}

// A word longer than what a frame adds does not hold the reveal back: twelve
// runes past the end of the chunk it stops, as in a language without spaces.
func TestAdvanceStopsTwelveRunesIntoALongWord(t *testing.T) {
	r := &textReveal{runes: []rune(strings.Repeat("語", 100))}
	r.advance()
	if r.shown != 20 {
		t.Fatalf("the frame shows %d runes, not the eight of the chunk and the twelve of the word", r.shown)
	}
}

// An answer that came in one piece is revealed over the frames that follow,
// not at once, and its end shows only once the last one has come.
func TestLongAnswerRevealsOverManyFrames(t *testing.T) {
	text := strings.Repeat("Wort ", 800) + "ENDE" // 4004 runes
	m, _ := threadModel(t, "", nil)
	c := m.chat
	m.event(eventMsg{id: c.contextID, event: Event{Kind: plugin.SessionAnswer, Text: text}})
	r := c.entries[0].reveal
	if r == nil {
		t.Fatal("the answer did not begin revealing")
	}
	msg := revealMsg{c, 0, r}
	for n := 1; n <= 3; n++ {
		m.Update(msg)
		if c.entries[0].reveal == nil || strings.Contains(ansi.Strip(m.history.GetContent()), "ENDE") {
			t.Fatalf("the whole answer is shown after %d frames", n)
		}
	}
	for n := 0; n < 200 && c.entries[0].reveal != nil; n++ {
		m.Update(msg)
	}
	if c.entries[0].reveal != nil || !strings.Contains(ansi.Strip(m.history.GetContent()), "ENDE") {
		t.Fatal("the answer did not finish revealing")
	}
}

// A frame of an entry that a replay took away draws nothing and asks for
// nothing: the entries are not those the frame was scheduled for.
func TestRevealFrameOfAnEntryThatIsGone(t *testing.T) {
	m, _ := threadModel(t, "", nil)
	c := m.chat
	m.event(eventMsg{id: c.contextID, event: Event{Kind: plugin.SessionAnswer, Text: strings.Repeat("Wort ", 400)}})
	r := c.entries[0].reveal
	if r == nil {
		t.Fatal("the answer did not begin revealing")
	}
	m.replay(c, nil)
	if cmd := m.revealFrame(revealMsg{c, len(c.entries), r}); cmd != nil {
		t.Fatalf("a frame of an entry that is gone asked for %v", cmd)
	}
}

// asksForAFrame runs cmd as the program does and says whether what it brings
// is the next frame of a reveal.
func asksForAFrame(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	frame := func(msg tea.Msg) bool {
		_, ok := msg.(revealMsg)
		return ok
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			if c != nil && frame(c()) {
				return true
			}
		}
		return false
	default:
		return frame(msg)
	}
}

// The frames of a reveal end with the answer: each frame of one still coming
// asks for the next, and the frame that completes it asks for none.
func TestRevealFramesEndWithTheAnswer(t *testing.T) {
	text := strings.Repeat("Wort ", 800) + "ENDE"
	m, _ := threadModel(t, "", nil)
	c := m.chat
	m.event(eventMsg{id: c.contextID, event: Event{Kind: plugin.SessionAnswer, Text: text}})
	r := c.entries[0].reveal
	if r == nil {
		t.Fatal("the answer did not begin revealing")
	}
	msg := revealMsg{c, 0, r}
	if !asksForAFrame(m.revealFrame(msg)) {
		t.Fatal("a frame of an answer still coming asks for no frame after it")
	}
	var last tea.Cmd
	for n := 0; n < 200 && c.entries[0].reveal != nil; n++ {
		last = m.revealFrame(msg)
	}
	if c.entries[0].reveal != nil {
		t.Fatal("the answer did not finish revealing")
	}
	if asksForAFrame(last) {
		t.Fatal("the frame that finished the answer asks for another one")
	}
}
