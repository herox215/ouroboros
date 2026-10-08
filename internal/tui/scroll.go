package tui

import (
	"math"
	"time"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
)

// The wheel scrolls at once, a line a step, under the hand: a touchpad sends
// many steps for a fast stroke and few for a slow one, so the view moves as
// the fingers do, and nothing goes on after they stop. The page keys and the
// arrows, and the history following what comes, do not jump that far: they
// name the line the viewport is to come to rest at, and the frames of a glide
// carry it there over a short time, fast at first and slowly at the end, so
// that the eye can follow it. A cell of a terminal is the smallest step there
// is, and the time spreads the steps evenly. A key pressed again while the
// frames are still moving adds to what the one before asked for, and the
// glide goes on from where it is, without a jump; the wheel ends a glide and
// takes over.
const (
	scrollFrame = time.Second / 60       // one frame of a glide: as often as the terminal is drawn
	scrollWheel = 1                      // lines a step of the wheel scrolls
	glideBase   = 90 * time.Millisecond  // how long a glide takes at least
	glidePerRow = 5 * time.Millisecond   // and longer for every line of its way
	glideMost   = 240 * time.Millisecond // but no longer than this, however far it goes
)

// scrollMsg is one frame of a glide, at the time it is drawn.
type scrollMsg struct{ at time.Time }

// glide is a viewport on its way to a line of its content.
type glide struct {
	view     *viewport.Model
	from     int       // the line at the top when the glide began
	target   int       // the line of the content that is to be at the top
	start    time.Time // when it began; the zero time until its first frame
	duration time.Duration
}

// scrollBy asks the viewport v to move by lines and returns the frame that
// carries it there; up is negative. A viewport that cannot go that way stays
// where it is and needs no frame.
func (m *Model) scrollBy(v *viewport.Model, lines int) tea.Cmd {
	from := v.YOffset()
	if m.glide != nil && m.glide.view == v {
		// A wheel that is turned again adds to what the notch before it
		// asked for and not to where the frames have come: nothing of a
		// spun wheel is lost.
		from = m.glide.target
	}
	return m.glideTo(v, min(max(from+lines, 0), topLine(v)))
}

// glideTo starts the viewport v on its way to the line to, from where it is
// now, and returns the frame that carries it there; nil when it is there.
// One chain of frames carries a glide: one that starts while another runs
// takes its place and starts no second chain, which would move the viewport
// twice a frame and make a spun wheel jump.
func (m *Model) glideTo(v *viewport.Model, to int) tea.Cmd {
	at := v.YOffset()
	if to == at {
		m.stopGlide(v) // it is there, or all of it is in sight already
		return nil
	}
	running := m.glide != nil
	way := time.Duration(max(to-at, at-to))
	m.glide = &glide{view: v, from: at, target: to, duration: min(glideBase+way*glidePerRow, glideMost)}
	if running {
		return nil
	}
	return scrollTick()
}

// followsEnd reports whether the history follows its end: it is at the
// bottom, or on its way there.
func (m *Model) followsEnd() bool {
	g := m.glide
	return m.history.AtBottom() || (g != nil && g.view == &m.history && g.target == topLine(&m.history))
}

// topLine is the last line of v that can be at the top of it: how far it
// scrolls, 0 when all of it fits.
func topLine(v *viewport.Model) int { return max(0, v.TotalLineCount()-v.Height()) }

// stopGlide ends a glide of the viewport v, so that one that is not shown
// any more, or has moved on, is not carried on.
func (m *Model) stopGlide(v *viewport.Model) {
	if m.glide != nil && m.glide.view == v {
		m.glide = nil
	}
}

// glideOn moves the viewport to where its glide is at the time now, and says
// whether another frame is needed. The way is eased out: the viewport goes
// fast at first and comes slowly to rest.
func (m *Model) glideOn(now time.Time) bool {
	g := m.glide
	if g == nil {
		return false
	}
	if g.start.IsZero() {
		// The time of a glide counts from its first frame: what came before
		// it, like the work of the key that started it, is no part of it.
		g.start = now.Add(-scrollFrame)
	}
	done := min(float64(now.Sub(g.start))/float64(g.duration), 1)
	eased := 1 - (1-done)*(1-done)*(1-done)
	// A line that is gone, as the content became shorter meanwhile, is not
	// reached: the viewport stays at the last there is, and the glide ends
	// on time all the same.
	g.view.SetYOffset(g.from + int(math.Round(float64(g.target-g.from)*eased)))
	if done == 1 {
		m.glide = nil
		return false
	}
	return true
}

// wheel scrolls the viewport v by a step of the wheel, at once: a glide of
// it ends there, and the hand takes over.
func (m *Model) wheel(v *viewport.Model, msg tea.MouseWheelMsg) {
	m.stopGlide(v)
	v.SetYOffset(v.YOffset() + wheelLines(msg))
}

// wheelLines is what a step of the wheel scrolls: up is negative, and a
// wheel that is pushed sideways does not scroll at all.
func wheelLines(msg tea.MouseWheelMsg) int {
	switch msg.Button {
	case tea.MouseWheelUp:
		return -scrollWheel
	case tea.MouseWheelDown:
		return scrollWheel
	}
	return 0
}

// scrollTick is the frame after this one.
func scrollTick() tea.Cmd {
	return tea.Tick(scrollFrame, func(at time.Time) tea.Msg { return scrollMsg{at} })
}
