package tui

import (
	"fmt"
	"image/color"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// The tests see the name shine unless they pick a look themselves: a glitch
// tears the name they look for. And they see a chat that has just begun
// without the name fading out behind it, unless that is what they test.
func init() {
	pickNameLook = func(int) int { return int(nameShine) }
	nameFade = 0
}

var sgrParams = regexp.MustCompile(`^\x1b\[([0-9;]*)m`)

// colorsOf are the colors a frame shows the name in, as the terminal is
// told them, by the column and the row of every cell of it.
func colorsOf(frame string) map[[2]int]string {
	colors := map[[2]int]string{}
	for y, line := range strings.Split(frame, "\n") {
		fg := ""
		for x := 0; len(line) > 0; {
			if found := sgrParams.FindStringSubmatch(line); found != nil {
				fg, line = foreground(found[1]), line[len(found[0]):]
				continue
			}
			r, size := utf8.DecodeRuneInString(line)
			if r != ' ' {
				colors[[2]int{x, y}] = fg
			}
			x, line = x+1, line[size:]
		}
	}
	return colors
}

// foreground is the parameters that color a cell, without the background a
// block of the name fills its cell with.
func foreground(params string) string {
	if i := strings.Index(params, "48;2;"); i >= 0 {
		return strings.TrimSuffix(params[:i], ";")
	}
	return params
}

// told is c as the terminal is told it.
func told(c color.RGBA) string { return fmt.Sprintf("38;2;%d;%d;%d", c.R, c.G, c.B) }

// seconds is the time s seconds after the animation began.
func seconds(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }

// The animation leaves every cell of the name where an empty chat has it at
// rest, frame by frame: in both sizes of the font, on a dark and on a light
// background, and in plain letters where the artwork does not fit. A block
// is one column of the artwork, though it is three bytes of its row.
func TestNameAnimationLeavesTheNameInPlace(t *testing.T) {
	for _, c := range []struct {
		name string
		w, h int
	}{
		{"Luna", 80, 21}, {"Luna", 38, 6}, {"Luna", 27, 7}, {"Luna", 19, 3}, {"Luna", 12, 4}, {"Luna", 1, 1},
		{"herox215", 84, 8}, {"herox215", 44, 7}, {"René", 40, 8},
	} {
		still := ansi.Strip(emptyChat(c.name, c.w, c.h))
		for _, light := range []bool{false, true} {
			for _, at := range []float64{0, 0.08, 0.5, 1.7, 60} {
				if got := ansi.Strip(drawNameAnimation(c.name, c.w, c.h, seconds(at), light, nameShine, 1)); got != still {
					t.Errorf("%s in %dx%d after %v seconds:\n%s\nand at rest the name is\n%s", c.name, c.w, c.h, at, got, still)
				}
			}
		}
	}
}

// The gradient of the splash colors the name column by column, each column
// in one color from top to bottom while no shine is on it, as matte as the
// name is: it begins with the first color of the gradient at the left, on a
// light background with the one for that. The colors flow, and come round
// every 25 ninths of a second. For Luna at full size a shine sets out every
// 3.85 seconds, and has left the name after two of them.
func TestNameAnimationFlowsThroughTheColumns(t *testing.T) {
	columns := func(at time.Duration, light bool) map[int]string {
		t.Helper()
		columns := map[int]string{}
		for cell, fg := range colorsOf(drawNameAnimation("Luna", 46, 10, at, light, nameShine, 1)) {
			if cell[1] >= 8 { // below the six rows of the name: the tagline
				continue
			}
			if other, ok := columns[cell[0]]; ok && other != fg {
				t.Fatalf("the column %d has the colors %s and %s", cell[0], other, fg)
			}
			columns[cell[0]] = fg
		}
		return columns
	}
	for light, p := range map[bool]palette{false: darkPalette, true: lightPalette} {
		if first, want := columns(0, light)[4], told(mix(p.back, p.body[0], nameMatte)); first != want {
			t.Errorf("light %v: the name begins with %s at the left, want %s", light, first, want)
		}
	}
	now := columns(seconds(2), false)
	colors := map[string]bool{}
	for _, fg := range now {
		colors[fg] = true
	}
	if len(now) != 32 || len(colors) != len(now) { // every column of the four letters
		t.Fatalf("%d colors in %d columns", len(colors), len(now))
	}
	later, rounds := columns(seconds(2+25.0/18), false), columns(seconds(2+50.0/9), false)
	for x, fg := range now {
		if later[x] == fg || rounds[x] != fg {
			t.Fatalf("the column %d is %s, then %s, and %s after two rounds", x, fg, later[x], rounds[x])
		}
	}
}

// The shine is a streak of light on the name, leaning to the right: where
// it is the name is nearly white, on the top row further right than on the
// bottom row, and it moves to the right.
func TestNameAnimationShineCrossesTheName(t *testing.T) {
	// brightest is the column of the brightest cell of the row y, and how bright it is.
	brightest := func(at float64, y int) (column, sum int) {
		for cell, fg := range colorsOf(drawNameAnimation("Luna", 46, 10, seconds(at), false, nameShine, 1)) {
			var r, g, b int
			fmt.Sscanf(fg, "38;2;%d;%d;%d", &r, &g, &b)
			if cell[1] == y && r+g+b > sum {
				column, sum = cell[0], r+g+b
			}
		}
		return column, sum
	}
	top, light := brightest(1, 2)
	bottom, _ := brightest(1, 7)
	if _, rest := brightest(2, 2); light < 600 || rest > 500 || top <= bottom {
		t.Fatalf("the shine: %d bright at the column %d, below at %d; at rest %d", light, top, bottom, rest)
	}
	if on, _ := brightest(1.2, 2); on <= top {
		t.Fatalf("the shine went from the column %d to %d", top, on)
	}
}

// A glitch tears the name for a third of a second, another way with every
// step, and leaves it where it was: in rows as wide as before, and whole
// again before and after. Every visit of an empty chat picks its look by
// chance.
func TestNameAnimationGlitchTearsTheName(t *testing.T) {
	still := ansi.Strip(emptyChat("Luna", 46, 10))
	frame := func(at float64) string {
		return ansi.Strip(drawNameAnimation("Luna", 46, 10, seconds(at), false, nameGlitch, 1))
	}
	for n := range 5 {
		at := glitchAt(n)
		if at < float64(n)*glitchEvery+0.5 || at+glitchFor > float64(n+1)*glitchEvery {
			t.Fatalf("the glitch %d is at %v", n, at)
		}
		first, second := frame(at+0.01), frame(at+glitchStep+0.01)
		if first == still || second == still || first == second || !strings.Contains(first, "▒") {
			t.Fatalf("the glitch %d does not tear the name:\n%s\n%s", n, first, second)
		}
		for _, line := range strings.Split(first, "\n") {
			if ansi.StringWidth(line) != 46 {
				t.Fatalf("a row of %d columns: %q", ansi.StringWidth(line), line)
			}
		}
		if before, after := frame(at-0.01), frame(at+glitchFor+0.01); before != still || after != still {
			t.Fatalf("around the glitch %d the name is\n%s\n%s", n, before, after)
		}
	}

	was := pickNameLook
	defer func() { pickNameLook = was }()
	for _, look := range []nameLook{nameGlitch, nameShine} {
		pickNameLook = func(n int) int {
			if n != 2 {
				t.Fatalf("a look out of %d", n)
			}
			return int(look)
		}
		m, _ := newModel(t)
		m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
		if m.nameAnimation.look != look || m.history.GetContent() != drawNameAnimation(m.agentName(), 76, 20, 0, false, look, 1, m.nameAnimation.tagline) {
			t.Fatalf("the visit is not in the look %d", look)
		}
	}
}

// The first message of a chat does not put the name out at once: the name
// fades out behind it, on the rows the message leaves blank, and is gone
// after nameFade. Another chat shows nothing of it.
func TestNameFadesOutBehindTheFirstMessage(t *testing.T) {
	nameFade = 450 * time.Millisecond
	defer func() { nameFade = 0 }()
	m, b := newModel(t)
	at := time.Unix(100, 0)
	m.now = func() time.Time { return at }
	b.status.Name = "Luna"
	m.Update(m.refreshStatus())
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	m.chat.entries = []entry{{mine: true, text: "hello"}}
	m.renderHistory(true)
	m.Update(spinner.TickMsg{ID: m.spinner.ID()})
	shown := func() (name, message bool) {
		content := ansi.Strip(m.history.GetContent())
		return strings.Contains(content, nameRow("Luna")), strings.Contains(content, "hello")
	}
	if name, message := shown(); !name || !message || m.nameAnimation.active {
		t.Fatalf("the name %v behind the message %v:\n%s", name, message, ansi.Strip(m.history.GetContent()))
	}
	full := m.history.GetContent()
	at = at.Add(nameFade / 2)
	m.Update(spinner.TickMsg{ID: m.spinner.ID()})
	if name, _ := shown(); !name || m.history.GetContent() == full {
		t.Fatal("the name does not fade")
	}
	was := m.chat
	m.chat = m.conversation("other")
	m.chat.entries = []entry{{mine: true, text: "there"}}
	m.renderHistory(true)
	if name, _ := shown(); name {
		t.Fatal("the name is behind another chat")
	}
	m.chat = was
	at = at.Add(nameFade / 2)
	m.Update(spinner.TickMsg{ID: m.spinner.ID()})
	if name, message := shown(); name || !message || !m.nameAnimation.fading.IsZero() {
		t.Fatalf("after the fade the name is shown %v, the message %v", name, message)
	}
}

func TestNameAnimationVisitLifecycle(t *testing.T) {
	m, b := newModel(t)
	at := time.Unix(100, 0)
	m.now = func() time.Time { return at }
	b.status.Name = "Luna"
	m.Update(m.refreshStatus())
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	if !m.nameAnimation.active {
		t.Fatal("empty chat should start the animation")
	}
	at = at.Add(time.Second)
	m.Update(spinner.TickMsg{ID: m.spinner.ID()})
	if m.nameAnimation.elapsed != time.Second {
		t.Fatal("a tick should move the animation on")
	}
	m.screen = screenHelp
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	if m.nameAnimation.active {
		t.Fatal("hidden chat should stop")
	}
	at = at.Add(time.Second)
	m.screen = screenChat
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	if !m.nameAnimation.active || m.nameAnimation.start != at || m.nameAnimation.elapsed != 0 {
		t.Fatal("reentry should restart")
	}
	m.chat.entries = []entry{{mine: true, text: "hello"}}
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	if m.nameAnimation.active {
		t.Fatal("messages replace animation")
	}
}

// The chat shows the animation as far as it has come with every tick of
// the spinner of the UI, in the colors for the background of the terminal,
// and from its beginning when the visit begins.
func TestNameAnimationIsDrawnWithTheTicks(t *testing.T) {
	m, b := newModel(t)
	at := time.Unix(100, 0)
	m.now = func() time.Time { return at }
	b.status.Name = "Luna"
	m.Update(m.refreshStatus())
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	a := &m.nameAnimation
	shows := func(elapsed time.Duration, light bool) {
		t.Helper()
		if w, h := m.history.Width(), m.history.Height(); w != 76 || h != 20 || m.history.GetContent() != drawNameAnimation("Luna", w, h, elapsed, light, a.look, 1, a.tagline) {
			t.Fatalf("after %v in %dx%d, light %v:\n%s", elapsed, w, h, light, ansi.Strip(m.history.GetContent()))
		}
		if !strings.Contains(ansi.Strip(view(m)), nameRow("Luna")) {
			t.Fatalf("the chat does not show the name:\n%s", view(m))
		}
	}
	shows(0, false)
	at = at.Add(1300 * time.Millisecond)
	m.Update(spinner.TickMsg{ID: m.spinner.ID() + 1}) // of another spinner
	shows(0, false)
	m.Update(spinner.TickMsg{ID: m.spinner.ID()})
	shows(1300*time.Millisecond, false)
	m.light = true
	at = at.Add(700 * time.Millisecond)
	m.Update(spinner.TickMsg{ID: m.spinner.ID()})
	shows(2*time.Second, true)

	// Another name, and another conversation, begin anew.
	b.status.Name = "Terra"
	m.Update(m.refreshStatus())
	if a.name != "Terra" || a.start != at || a.elapsed != 0 || m.history.GetContent() != drawNameAnimation("Terra", 76, 20, 0, true, a.look, 1, a.tagline) {
		t.Fatalf("after another name: %+v", *a)
	}
	at = at.Add(time.Second)
	m.Update(spinner.TickMsg{ID: m.spinner.ID()})
	was := m.chat
	drive(t, m, run(m, "/new"), func() bool { return true })
	if m.chat == was || a.chat != m.chat || a.start != at || a.elapsed != 0 {
		t.Fatalf("after /new: %+v", *a)
	}
}

// The name moves only in an empty chat of the owner that is on the screen:
// not behind the splash or the setup, not on another screen, not before the
// node is set up, not in a conversation of a peer, which is only to read,
// and not once there is a message.
func TestNameAnimationNeedsAnEmptyChatOnTheScreen(t *testing.T) {
	for what, change := range map[string]func(m *Model) (undo func()){
		"the splash":       func(m *Model) func() { m.splash.show = true; return func() { m.splash.show = false } },
		"the setup":        func(m *Model) func() { m.onboard = &onboarding{}; return func() { m.onboard = nil } },
		"another screen":   func(m *Model) func() { m.screen = screenSessions; return func() { m.screen = screenChat } },
		"no configuration": func(m *Model) func() { m.status.Configured = false; return func() { m.status.Configured = true } },
		"a peer":           func(m *Model) func() { m.chat.peer = "beta"; return func() { m.chat.peer = "" } },
		"a message":        func(m *Model) func() { m.chat.entries = []entry{{text: "hi"}}; return func() { m.chat.entries = nil } },
	} {
		m, _ := newModel(t)
		if !m.nameAnimation.active {
			t.Fatalf("%s: the name does not move in an empty chat", what)
		}
		undo := change(m)
		if m.syncNameAnimation(); m.nameAnimation.active {
			t.Errorf("%s: the name still moves", what)
		}
		undo()
		if m.syncNameAnimation(); !m.nameAnimation.active {
			t.Errorf("after %s the name does not move again", what)
		}
	}
}

// Of the profiles of a node only the one shown moves its name: the one the
// UI opens on, the next one that shift+tab shows, and the one the UI goes
// on with after a new build.
func TestNameMovesOnlyForTheProfileShown(t *testing.T) {
	root, terra, luna, _ := profiles(t)
	// moving are the profiles whose name moves, once each has had a tick of its spinner.
	moving := func() [2]bool {
		terra.Update(spinner.TickMsg{ID: terra.spinner.ID()})
		luna.Update(spinner.TickMsg{ID: luna.spinner.ID()})
		return [2]bool{terra.nameAnimation.active, luna.nameAnimation.active}
	}
	root.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if got := moving(); got != [2]bool{true, false} {
		t.Fatalf("the UI opens on Terra, and the names move: %v", got)
	}
	root.Update(shiftTab)
	luna.Update(luna.refreshStatus())
	if got := moving(); got != [2]bool{false, true} {
		t.Fatalf("Luna is shown, and the names move: %v", got)
	}
	root.SelectProfile("aaa111")
	if got := moving(); got != [2]bool{true, false} {
		t.Fatalf("the UI goes on with Terra, and the names move: %v", got)
	}
	root.SelectProfile("bbb222")
	if got := moving(); got != [2]bool{false, true} {
		t.Fatalf("the UI goes on with Luna, and the names move: %v", got)
	}
}

func TestEmptyTaglineSelectionStaysDuringAnimation(t *testing.T) {
	old := pickEmptyTagline
	t.Cleanup(func() { pickEmptyTagline = old })
	calls := 0
	pickEmptyTagline = func(n int) int { calls++; return n - 1 }
	m, _ := newModel(t)
	m.splash.show = false
	m.status.Configured = true
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	m.syncNameAnimation()
	m.syncNameAnimation()
	if calls != 1 || m.nameAnimation.tagline != emptyTaglines[len(emptyTaglines)-1] {
		t.Fatalf("selection: %d calls, %q", calls, m.nameAnimation.tagline)
	}
	if !strings.Contains(ansi.Strip(m.history.GetContent()), m.nameAnimation.tagline) {
		t.Fatal("selected tagline not shown")
	}
}

func TestResumeWaitsForHistoryBeforeEmptyArtwork(t *testing.T) {
	m, _ := newModel(t)
	m.status.Configured = true
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	m.Resume("resumed")
	m.syncNameAnimation()
	if m.nameAnimation.active || strings.TrimSpace(m.history.GetContent()) != "" {
		t.Fatal("artwork shown before resumed history arrived")
	}
	m.watched(watchedMsg{id: "resumed", err: fmt.Errorf("retry")})
	if m.resuming != m.chat {
		t.Fatal("failed load released artwork")
	}
	m.watched(watchedMsg{id: "resumed"})
	m.syncNameAnimation()
	if m.resuming != nil || !m.nameAnimation.active {
		t.Fatal("a truly empty resumed chat should show artwork after loading")
	}
}
