package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// streamCut is where a streamed answer can be cut without changing what it
// reads: after a blank line that begins a block of its own, and never inside
// a fenced code block or before a line that continues the block above it.
func TestStreamCut(t *testing.T) {
	for _, tt := range []struct {
		text string
		from int
		want int
	}{
		{"a\n\nb", 0, 3},
		{"a\n\nb\n\nc", 0, 6},
		{"a\n\n\nb", 0, 4},
		{"a\n\n", 0, 0},
		{"a\n\n- b", 0, 0},
		{"a\n\n12. b", 0, 0},
		{"a\n\n1234567890. b", 0, 3},
		{"a\n\n  b", 0, 0},
		{"a\n\n> b", 0, 0},
		{"a\n\n| b |", 0, 0},
		{"a\n\n```\nb\n\nc\n```", 0, 3},
		{"a\n\n```\nb\n```\n\nc", 0, 14},
		{"a\n\n```\nb\n\nc\n```\n\nd", 0, 17},
		{"a\n\nb\n\nc", 6, 0},
		{"a\n\nb\n\nc", 3, 6},
		{"a\n\nb", -1, 3},
		{"a\n\nb", 99, 3},
	} {
		if got := streamCut(tt.text, tt.from); got != tt.want {
			t.Errorf("streamCut(%q, %d) = %d, want %d", tt.text, tt.from, got, tt.want)
		}
	}
}

// A streamed answer reads as the whole of it does, however the stream broke
// it into pieces: the blocks that stand are kept, the one that grows is drawn,
// and markdown that is still open is not cut.
func TestStreamedAnswerMatchesTheWhole(t *testing.T) {
	for _, tt := range []struct {
		name   string
		answer string
		width  int
		cut    bool
	}{
		{"paragraphs", "First paragraph.\n\nSecond paragraph, a little longer than the first one.\n\nThird.", 50, true},
		{"code", "Before.\n\n```go\nfmt.Println(1)\n\nfmt.Println(2)\n```\n\nAfter.", 50, true},
		{"list", "Intro.\n\n- one\n- two\n\nEnd.", 50, true},
		{"quote", "> quoted\n\n> more\n\nAfter.", 50, true},
		{"table", "| a | b |\n|---|---|\n| 1 | 2 |\n\nAfter.", 50, true},
		{"one block", "a single paragraph that never ends.\n", 30, false},
		{"narrow", "First.\n\nSecond, which wraps on the narrow screen.\n\nThird.", 20, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			whole, _ := newModel(t)
			whole.chat.entries = []entry{{mine: true, text: "go", at: time.Unix(100, 0)}}
			whole.chat.pending = true
			whole.chat.partial = tt.answer
			whole.Update(makeWindow(tt.width, 30))
			whole.chat.thread = thread{newest: newestWord(tt.answer), frame: settleFrames}
			whole.renderHistory(true)

			streamed, _ := newModel(t)
			streamed.chat.entries = []entry{{mine: true, text: "go", at: time.Unix(100, 0)}}
			streamed.chat.pending = true
			streamed.Update(makeWindow(tt.width, 30))
			for i := 1; i <= len(tt.answer); i++ {
				streamed.chat.partial = tt.answer[:i]
				streamed.chat.thread = thread{newest: newestWord(streamed.chat.partial), frame: settleFrames}
				streamed.renderHistory(false)
			}
			if got, want := ansiStrip(streamed.history.GetContent()), ansiStrip(whole.history.GetContent()); got != want {
				t.Fatalf("streamed answer:\n%s\nwhole answer:\n%s", got, want)
			}
			// The blocks that stood were not drawn again: the cache holds
			// them, and a frame adds only what came after them.
			if (streamed.chat.stream.text != "") != tt.cut {
				t.Fatalf("the answer was cut (%q), want %v", streamed.chat.stream.text, tt.cut)
			}
		})
	}
}

// A new answer, another width or another palette draws the answer anew
// instead of keeping blocks of the one before.
func TestStreamedAnswerIsDrawnAnew(t *testing.T) {
	m, _ := newModel(t)
	m.chat.entries = []entry{{mine: true, text: "go", at: time.Unix(100, 0)}}
	m.chat.pending = true
	m.chat.partial = "one\n\ntwo"
	m.renderHistory(true)
	if m.chat.stream.text != "one\n\n" {
		t.Fatalf("the prefix: %q", m.chat.stream.text)
	}
	// A partial that does not continue what stood begins anew.
	m.chat.partial = "another one"
	m.renderHistory(false)
	if m.chat.stream.text != "" || !strings.Contains(ansiStrip(m.history.GetContent()), "another") {
		t.Fatalf("a new answer kept the old blocks: %q", m.chat.stream.text)
	}
	// A resize draws what stands at the new width.
	m.chat.partial = "one\n\ntwo"
	m.renderHistory(false)
	m.Update(makeWindow(40, 30))
	if m.chat.stream.width != m.transcriptWidth() {
		t.Fatalf("the cache is for %d columns, the chat for %d", m.chat.stream.width, m.transcriptWidth())
	}
	// The end of the answer drops the cache.
	m.chat.partial = ""
	m.chat.pending = false
	m.renderHistory(true)
	if m.chat.stream != (streamView{}) {
		t.Fatalf("the cache outlived the answer: %+v", m.chat.stream)
	}
}

// makeWindow is a window size message.
func makeWindow(w, h int) tea.WindowSizeMsg { return tea.WindowSizeMsg{Width: w, Height: h} }

// ansiStrip is text without its colors, for comparing what is read.
func ansiStrip(s string) string { return ansi.Strip(s) }

// What the kept blocks and the growing one say together is exactly the frame
// the answer would be drawn as whole, with the cursor after its last line.
func TestStreamedBlockMatchesTheWholeFrame(t *testing.T) {
	m, _ := newModel(t)
	m.chat.entries = []entry{{mine: true, text: "go", at: time.Unix(100, 0)}}
	m.chat.pending = true
	m.chat.partial = "First.\n\n```go\none\n\ntwo\n```\n\nLast words."
	width := m.transcriptWidth()
	m.chat.thread = thread{newest: newestWord(m.chat.partial), frame: settleFrames}
	got := strings.Join(m.streamBlocks(m.chat, width), "\n\n")
	head := boldStyle.Foreground(muted).Render(m.agentName() + " · writing …")
	want := lipgloss.Wrap(head+"\n"+renderMarkdown(m.chat.partial, width), width, "") + mutedStyle.Render(" ▌")
	if got != want {
		t.Fatalf("streamed frame:\n%s\nwhole frame:\n%s", want, got)
	}
}

// The clock of a streamed answer slows down when the block that grows is so
// long that drawing it would cost more than the eye follows; what stands does
// not count, so a long answer of paragraphs keeps the fast clock.
func TestAnswerFrameForALongBlock(t *testing.T) {
	c := &conversation{partial: strings.Repeat("x", 10<<10)}
	if got := answerFrameFor(c); got != answerFrame {
		t.Fatalf("a short block is drawn every %v, want %v", got, answerFrame)
	}
	c.partial = strings.Repeat("x", 20<<10)
	if got := answerFrameFor(c); got != 150*time.Millisecond {
		t.Fatalf("20 KiB in one block: %v", got)
	}
	c.partial = strings.Repeat("x", 70<<10)
	if got := answerFrameFor(c); got != 250*time.Millisecond {
		t.Fatalf("70 KiB in one block: %v", got)
	}
	c.stream.text = c.partial[:len(c.partial)-100]
	if got := answerFrameFor(c); got != answerFrame {
		t.Fatalf("100 bytes after what stands: %v", got)
	}
}

// A block that stands with nothing growing after it keeps its cursor at its
// end, as a stream that has not brought the next word yet.
func TestStreamedBlockWithoutATail(t *testing.T) {
	m, _ := newModel(t)
	width := m.transcriptWidth()
	m.chat.partial = "one\n\n"
	m.chat.stream = streamView{text: "one\n\n", block: "kept", width: width, light: m.light, name: m.agentName()}
	blocks := m.streamBlocks(m.chat, width)
	if len(blocks) != 1 || !strings.HasPrefix(blocks[0], "kept") || !strings.Contains(blocks[0], "▌") {
		t.Fatalf("the standing block: %q", blocks)
	}
}
