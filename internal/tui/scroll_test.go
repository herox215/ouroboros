package tui

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

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

// The box of the input that comes or goes while the history glides to its
// end moves the end: the history goes on to where the end is then, and what
// comes after is glided to as well.
func TestHistoryFollowsItsEndWhileTheInputMoves(t *testing.T) {
	for _, shown := range []bool{true, false} {
		m, _ := newModel(t)
		fill(m, 80)
		m.inputShown = shown
		m.layout()
		say := func(kind plugin.SessionKind, text string) {
			m.Update(eventMsg{id: m.chat.contextID, event: Event{Kind: kind, Text: text, At: m.now()}})
		}
		say(plugin.SessionMessage, "go")
		say(plugin.SessionAnswer, "started")
		m.inputShown = !shown
		for cmd := m.fadeInput(); cmd != nil; {
			_, cmd = m.Update(inputFadeMsg{})
		}
		if !m.followsEnd() {
			t.Fatalf("shown %v: the history no longer follows its end: at %d of %d, %+v", shown, m.history.YOffset(), topLine(&m.history), m.glide)
		}
		say(plugin.SessionAnswer, "one\ntwo\nthree")
		say(plugin.SessionAnswer, "four")
		scroll(t, m, scrollTick())
		if !m.history.AtBottom() {
			t.Fatalf("shown %v: the history rests at %d of %d", shown, m.history.YOffset(), topLine(&m.history))
		}
	}
}

// fades is how a box shows the lines of a viewport, port: a letter a line,
// "e" for one faded at an edge, nearly the color of the background, "d" for
// a dim one and "." for one as it is. shown are the lines of the box from the
// first of the viewport on.
func fades(t *testing.T, m *Model, shown []string, port string) string {
	t.Helper()
	edge := lipgloss.NewStyle().Foreground(fadeColor(m.light))
	var got strings.Builder
	for i, l := range strings.Split(port, "\n") {
		switch {
		case strings.Contains(shown[i], edge.Render(ansi.Strip(l))):
			got.WriteString("e")
		case strings.Contains(shown[i], faint(l)):
			got.WriteString("d")
		case strings.Contains(shown[i], l):
			got.WriteString(".")
		default:
			t.Fatalf("line %d of the viewport is not in the box: %q", i, shown[i])
		}
	}
	return got.String()
}

// The history fades out over two lines at an edge of the box where more of it
// is beyond: at the bottom only at the top, scrolled up at both edges, at the
// top only at the bottom, and the lines between stay as they are. Behind the
// panel of the commands offered for what is typed all of the chat fades.
func TestHistoryFadesAtItsEdges(t *testing.T) {
	m, _ := newModel(t)
	fill(m, 80)
	faded := func() string {
		return fades(t, m, strings.Split(m.chatView(), "\n")[1:], m.history.View()) // below the border
	}
	n := m.history.Height()
	if got, want := faded(), "ed"+strings.Repeat(".", n-2); got != want {
		t.Fatalf("at the bottom only the top should fade: %s, want %s", got, want)
	}
	scroll(t, m, press(m, "pgup"))
	if got, want := faded(), "ed"+strings.Repeat(".", n-4)+"de"; got != want {
		t.Fatalf("scrolled up both edges should fade: %s, want %s", got, want)
	}
	m.history.SetYOffset(0)
	if got, want := faded(), strings.Repeat(".", n-2)+"de"; got != want {
		t.Fatalf("at the top only the bottom should fade: %s, want %s", got, want)
	}
	press(m, "/", "s")
	edge := lipgloss.NewStyle().Foreground(fadeColor(m.light))
	top := m.panelTop(m.panel())
	if top != n-2 {
		t.Fatalf("the panel of four commands begins at row %d of a history of %d", top, n)
	}
	for i, line := range strings.Split(m.chatView(), "\n")[:top] {
		if line != edge.Render(ansi.Strip(line)) {
			t.Fatalf("row %d behind the panel should fade: %q", i, line)
		}
	}
	if got := m.fadeEdges([]string{"a", "b", "c"}, true, true); !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Fatalf("a history of three lines should not fade: %q", got)
	}
	if got := m.fadeEdges([]string{"a", "b", "c", "d"}, true, true); got[0] == "a" || got[3] == "d" {
		t.Fatalf("a history of four lines should fade at both edges: %q", got)
	}
	if fadeColor(true) == fadeColor(false) {
		t.Fatal("the faded edge should follow the background")
	}
}

// A dim line keeps its colors, and stays dim after every sequence in it.
func TestFaintKeepsColors(t *testing.T) {
	got := faint("a\x1b[31mb\x1b[mc")
	want := "\x1b[2ma\x1b[31m\x1b[2mb\x1b[m\x1b[2mc\x1b[22m"
	if got != want {
		t.Fatalf("faint = %q, want %q", got, want)
	}
}

// The status of the node leaves a conversation where it is scrolled to: only
// the empty chat is drawn anew for it.
func TestStatusKeepsTheHistoryInPlace(t *testing.T) {
	m, _ := newModel(t)
	fill(m, 80)
	scroll(t, m, press(m, "pgup"))
	at := m.history.YOffset()
	m.Update(m.refreshStatus())
	if m.history.YOffset() != at || m.history.AtBottom() {
		t.Fatalf("the history went from %d to %d", at, m.history.YOffset())
	}
}

// A glide takes longer the further it goes, up or down, but never longer
// than glideMost.
func TestGlideTakesLongerForALongerWay(t *testing.T) {
	m, _ := newModel(t)
	fill(m, 80)
	for _, tt := range []struct {
		from, to int
		want     time.Duration
	}{
		{20, 23, 105 * time.Millisecond},
		{20, 17, 105 * time.Millisecond},
		{0, 100, 240 * time.Millisecond},
		{100, 0, 240 * time.Millisecond},
	} {
		m.glide = nil
		m.history.SetYOffset(tt.from)
		m.glideTo(&m.history, tt.to)
		if m.glide == nil || m.glide.duration != tt.want {
			t.Errorf("from %d to %d: %+v, want %s", tt.from, tt.to, m.glide, tt.want)
		}
	}
}

// A glide is fast at first and comes slowly to rest: after half its time
// seven eighths of the way are behind it.
func TestGlideEasesOutOverItsTime(t *testing.T) {
	m, _ := newModel(t)
	fill(m, 80)
	start := time.Now()
	for _, tt := range []struct {
		after time.Duration
		at    int
	}{{0, 0}, {60 * time.Millisecond, 46}, {120 * time.Millisecond, 70}, {180 * time.Millisecond, 79}, {240 * time.Millisecond, 80}} {
		m.glide = &glide{view: &m.history, from: 0, target: 80, start: start, duration: 240 * time.Millisecond}
		if more := m.glideOn(start.Add(tt.after)); m.history.YOffset() != tt.at || more != (tt.after < 240*time.Millisecond) {
			t.Errorf("after %s: at line %d, want %d; more frames: %v", tt.after, m.history.YOffset(), tt.at, more)
		}
	}
}
