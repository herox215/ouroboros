package tui

import (
	"fmt"
	"testing"
	"time"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"ouroboros/plugin"
)

// maxFrames is how many frames of a glide a test waits for before it gives
// up on one: a glide that does not come to rest is a bug.
const maxFrames = 500

// fill puts n messages into the chat of m and renders it, so that the
// history is longer than the window shows.
func fill(m *Model, n int) {
	for i := range n {
		m.chat.add(entry{text: fmt.Sprintf("line %d", i), at: m.now()})
	}
	m.renderHistory(true)
}

// scroll runs the frames of a scroll to its end, as the program does, only
// without waiting for them.
func scroll(t *testing.T, m *Model, cmd tea.Cmd) {
	t.Helper()
	at := time.Now()
	for frames := 0; cmd != nil; frames++ {
		if frames > maxFrames {
			t.Fatal("a glide should come to rest")
		}
		at = at.Add(scrollFrame)
		_, cmd = m.Update(scrollMsg{at})
	}
}

// wheelOf turns the wheel a step and gives what that returns: nothing, as
// the wheel scrolls at once.
func wheelOf(m *Model, button tea.MouseButton) tea.Cmd {
	_, cmd := m.Update(tea.MouseWheelMsg{Button: button})
	return cmd
}

// turn turns the wheel and runs the frames of the scroll it starts.
func turn(t *testing.T, m *Model, button tea.MouseButton) {
	t.Helper()
	scroll(t, m, wheelOf(m, button))
}

// A page key does not jump to where it means: the viewport comes there over
// the frames of a glide, and rests at the very line it asked for.
func TestScrollingGlides(t *testing.T) {
	m, _ := newModel(t)
	fill(m, 80)
	bottom := m.history.YOffset()
	if bottom != topLine(&m.history) || bottom == 0 {
		t.Fatalf("the history should rest at its bottom: %d of %d", bottom, topLine(&m.history))
	}

	cmd := press(m, "pgup")
	if cmd == nil || m.glide == nil {
		t.Fatal("a page key should start a glide")
	}
	if m.history.YOffset() != bottom {
		t.Fatalf("a page key should not jump: %d", m.history.YOffset())
	}
	scroll(t, m, cmd)
	page := bottom - m.history.Height()
	if got := m.history.YOffset(); got != page || m.glide != nil {
		t.Fatalf("a page up should come a page to %d, came to %d", page, got)
	}
}

// The wheel scrolls at once, a line a step, and needs no frames: the view
// moves under the hand. Turned while a glide runs, it ends the glide where
// the viewport is and takes over from there.
func TestWheelScrollsAtOnce(t *testing.T) {
	m, _ := newModel(t)
	fill(m, 80)
	m.history.GotoTop()

	for i := 1; i <= 3; i++ {
		if cmd := wheelOf(m, tea.MouseWheelDown); cmd != nil || m.glide != nil || m.history.YOffset() != i*scrollWheel {
			t.Fatalf("step %d of the wheel: at %d, %v", i, m.history.YOffset(), cmd)
		}
	}
	if wheelOf(m, tea.MouseWheelUp); m.history.YOffset() != 2*scrollWheel {
		t.Fatalf("the wheel up: at %d", m.history.YOffset())
	}

	press(m, "pgdown")
	at := time.Now()
	for range 3 {
		at = at.Add(scrollFrame)
		m.Update(scrollMsg{at})
	}
	midway := m.history.YOffset()
	if wheelOf(m, tea.MouseWheelUp); m.glide != nil || m.history.YOffset() != midway-scrollWheel {
		t.Fatalf("the wheel should take over the glide at %d: at %d, %+v", midway, m.history.YOffset(), m.glide)
	}
	if _, next := m.Update(scrollMsg{at.Add(scrollFrame)}); next != nil || m.history.YOffset() != midway-scrollWheel {
		t.Fatal("the frames of a glide the wheel ended should move nothing")
	}
}

// A key pressed while a glide runs starts no second chain of frames: each
// frame moves the viewport once, so that pressing it again glides on
// instead of jumping.
func TestOneChainOfFrames(t *testing.T) {
	m, _ := newModel(t)
	fill(m, 200)
	m.history.GotoTop()

	var cmds []tea.Cmd
	for range 3 {
		if cmd := press(m, "pgdown"); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	if len(cmds) != 1 {
		t.Fatalf("three page keys started %d chains of frames", len(cmds))
	}
	if _, ok := cmds[0]().(scrollMsg); !ok {
		t.Fatal("the chain should bring a frame of the glide")
	}
	want := 3 * m.history.Height()
	// The first frame takes a part of the way, as one chain does.
	m.Update(scrollMsg{time.Now()})
	if got := m.history.YOffset(); got <= 0 || got >= want {
		t.Fatalf("after one frame at %d, want on the way to %d", got, want)
	}
	scroll(t, m, scrollTick())
	if got := m.history.YOffset(); got != want {
		t.Fatalf("at rest at %d, want %d", got, want)
	}

	// Once the glide is over, the next key starts a chain again.
	if press(m, "pgdown") == nil {
		t.Fatal("a key after a glide should start its frames")
	}
}

// A scroll that has nowhere to go moves nothing and starts no glide, a wheel
// that is pushed sideways does not scroll, and a frame of a glide that is
// over does nothing.
func TestScrollingAtTheEnds(t *testing.T) {
	m, _ := newModel(t)
	fill(m, 80)
	m.history.GotoTop()
	if cmd := press(m, "pgup"); cmd != nil || m.glide != nil {
		t.Fatalf("a page up at the top is already there: %v", cmd)
	}
	if cmd := wheelOf(m, tea.MouseWheelLeft); cmd != nil || m.glide != nil {
		t.Fatalf("a wheel pushed sideways scrolls nothing: %v", cmd)
	}
	if cmd := wheelOf(m, tea.MouseWheelUp); cmd != nil || m.glide != nil {
		t.Fatal("the wheel up at the top is already there")
	}
	if _, cmd := m.Update(scrollMsg{}); cmd != nil {
		t.Fatal("a frame without a glide does nothing")
	}
	m.history.GotoBottom()
	if cmd := wheelOf(m, tea.MouseWheelDown); cmd != nil || m.glide != nil {
		t.Fatal("the wheel down at the bottom is already there")
	}
}

// A glide whose line is gone – the content became shorter while the frames
// were on their way – comes to rest where the viewport is instead of pulling
// at it for ever.
func TestGlideStopsWhenTheLineIsGone(t *testing.T) {
	m, _ := newModel(t)
	fill(m, 80)
	m.history.GotoTop()
	cmd := press(m, "pgdown")
	m.history.SetContent("one line")
	scroll(t, m, cmd)
	if m.glide != nil || m.history.YOffset() != 0 {
		t.Fatalf("the glide should rest where the viewport is, %d", m.history.YOffset())
	}
}

// glideLines runs the frames of a glide one by one, at the times they are drawn,
// and gives the line at the top after each.
func glideLines(t *testing.T, m *Model, v *viewport.Model, cmd tea.Cmd) []int {
	t.Helper()
	var lines []int
	at := time.Now()
	for cmd != nil {
		if len(lines) > maxFrames {
			t.Fatal("a glide should come to rest")
		}
		at = at.Add(scrollFrame)
		_, cmd = m.Update(scrollMsg{at})
		lines = append(lines, v.YOffset())
	}
	return lines
}

// A glide goes the whole way and never past it, a line at a time or more,
// fast at first and slowly at the end. A short way takes a short time, a
// long one not much longer.
func TestGlideEasesOut(t *testing.T) {
	m, _ := newModel(t)
	fill(m, 200)
	m.history.GotoTop()

	const short = 3
	notch := glideLines(t, m, &m.history, m.scrollBy(&m.history, short))
	page := m.history.YOffset()
	long := glideLines(t, m, &m.history, press(m, "pgdown"))
	for _, tt := range []struct {
		name     string
		lines    []int
		from, to int
	}{
		{"a short way", notch, 0, short},
		{"a page", long, page, page + m.history.Height()},
	} {
		at := tt.from
		for i, line := range tt.lines {
			if line < at || line > tt.to {
				t.Fatalf("%s: frame %d at %d after %d, on the way from %d to %d: %v", tt.name, i, line, at, tt.from, tt.to, tt.lines)
			}
			at = line
		}
		if at != tt.to {
			t.Fatalf("%s should come to %d, came to %d", tt.name, tt.to, at)
		}
	}
	// A short way takes about a tenth of a second, a page at most a quarter.
	if n := len(notch); time.Duration(n)*scrollFrame < glideBase || time.Duration(n-1)*scrollFrame > glideBase+short*glidePerRow {
		t.Errorf("a short way took %d frames: %v", n, notch)
	}
	if n := len(long); time.Duration(n-1)*scrollFrame > glideMost {
		t.Errorf("a page took %d frames: %v", n, long)
	}
	// Eased out: the first half of the time covers most of the way.
	half := long[len(long)/2] - page
	if 2*half <= m.history.Height() {
		t.Errorf("half of the time came %d of %d lines: %v", half, m.history.Height(), long)
	}
}

// A key pressed while a glide runs goes on from where the viewport is: it
// does not jump, back or ahead, and comes to rest where both meant.
func TestGlideGoesOnWithoutAJump(t *testing.T) {
	m, _ := newModel(t)
	fill(m, 200)
	m.history.GotoTop()

	cmd := press(m, "pgdown")
	at := time.Now()
	for range 3 {
		at = at.Add(scrollFrame)
		m.Update(scrollMsg{at})
	}
	midway := m.history.YOffset()
	if again := press(m, "pgdown"); again != nil || m.history.YOffset() != midway {
		t.Fatalf("a key on the way should neither start frames nor jump: at %d, was %d", m.history.YOffset(), midway)
	}
	lines := glideLines(t, m, &m.history, cmd)
	want := 2 * m.history.Height()
	if lines[0] < midway || lines[len(lines)-1] != want {
		t.Fatalf("from %d to %d: %v", midway, want, lines)
	}
}

// A history at its end follows what comes: it glides there instead of
// jumping, and goes on following while a glide is on its way. One scrolled
// up stays where it is.
func TestHistoryGlidesToWhatComes(t *testing.T) {
	m, _ := newModel(t)
	fill(m, 80)
	say := func(text string) {
		m.Update(eventMsg{id: m.chat.contextID, event: Event{Kind: plugin.SessionMessage, Text: text, At: m.now()}})
	}
	bottom := m.history.YOffset()
	say("one\ntwo\nthree")
	if m.history.YOffset() != bottom || m.glide == nil || m.glide.target != topLine(&m.history) || m.glide.target <= bottom {
		t.Fatalf("what came should be glided to, from %d: at %d, %+v", bottom, m.history.YOffset(), m.glide)
	}
	m.Update(scrollMsg{time.Now()}) // on its way, not at the end
	say("four")
	if m.glide == nil || m.glide.target != topLine(&m.history) {
		t.Fatalf("a history on its way to the end should go on to the new end: %+v", m.glide)
	}
	scroll(t, m, scrollTick())
	if !m.history.AtBottom() {
		t.Fatal("the history should rest at its end")
	}

	scroll(t, m, press(m, "pgup"))
	up := m.history.YOffset()
	say("five")
	if m.history.YOffset() != up || m.glide != nil {
		t.Fatalf("a history scrolled up should stay at %d: at %d, %+v", up, m.history.YOffset(), m.glide)
	}
}
