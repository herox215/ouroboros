package tui

import (
	"math"
	"strings"
	"time"
)

// art is a picture that moves behind what the agent is waiting for, a row of
// cells. at says how much there is at the cell x of a row of w cells at the
// tick t, from 0 for nothing to 1 for the most, and glyphs draws that: the
// first for the least, the last for the most.
type art struct {
	glyphs []rune
	at     func(x, w, t float64) float64
}

// arts are the pictures a wait is shown with, one after the other: each
// message of the owner gets the next.
var arts = []art{
	// tide: waves that roll to the right.
	{[]rune(" ▁▂▃▄▅▆▇"), func(x, w, t float64) float64 {
		return 0.5 + 0.5*math.Sin(x/5-t/6)
	}},
	// plasma: three waves that run through each other.
	{[]rune(" .·:;+*"), func(x, w, t float64) float64 {
		return (3 + math.Sin(x/6+t/9) - math.Sin(t/11) + math.Sin(x/7+t/13)) / 6
	}},
	// ripple: rings that leave the middle.
	{[]rune(" ·∙•"), func(x, w, t float64) float64 {
		return 0.5 + 0.5*math.Sin(math.Abs(x-(w-1)/2)/2-t/4)
	}},
	// scanner: a light that goes from side to side.
	{[]rune(" ░▒▓█"), func(x, w, t float64) float64 {
		return 1 - math.Abs(x-(w-1)*(0.5+0.5*math.Sin(t/10)))/10
	}},
	// stars: a few cells that twinkle, each in its own time.
	{[]rune(" .+*"), func(x, w, t float64) float64 {
		star := noise(x)
		if star < 0.8 {
			return 0
		}
		return 0.5 + 0.5*math.Sin(t/5+star*400)
	}},
	// helix: two strands that wind around each other, seen from the side.
	{[]rune(" ·•●"), func(x, w, t float64) float64 {
		return 1 - 0.75*math.Abs(math.Sin(x/4-t/5))
	}},
	// rain: drops that fall through the row, each in its own time.
	{[]rune(" ·╎│"), func(x, w, t float64) float64 {
		return 1 - frac(noise(x)+t/24)*5/3
	}},
	// weave: dots that go around in every cell, each a step after the last.
	{[]rune("⠁⠂⠄⡀⢀⠠⠐⠈"), func(x, w, t float64) float64 {
		return frac((x - t) / 8)
	}},
	// zigzag: a line of steps that marches to the right.
	{[]rune("╱‾╲_"), func(x, w, t float64) float64 {
		return frac((x - math.Floor(t/3)) / 4)
	}},
	// snake: it runs through the row after its own tail, as an ouroboros does.
	{[]rune(" ·∙•●"), func(x, w, t float64) float64 {
		return 1 - math.Mod(w+math.Mod(t, w)-x, w)/w
	}},
}

// frac is what of v is left after its whole part, from 0 to below 1.
func frac(v float64) float64 { return v - math.Floor(v) }

// noise is a number from 0 to below 1 that the cell x always gets, and that
// says nothing about the one of the cell next to it.
func noise(x float64) float64 { return frac(math.Sin(x*12.9898) * 43758.5453) }

// row draws the picture in w cells at the tick t.
func (a art) row(w, t int) []rune {
	row := make([]rune, w)
	for x := range row {
		much := a.at(float64(x), float64(w), float64(t))
		row[x] = a.glyphs[min(max(0, int(much*float64(len(a.glyphs)))), len(a.glyphs)-1)]
	}
	return row
}

// waiting is what the agent of the session shown is waiting for, as the chat
// says it, with the jobs that run in the background for the answer and the
// tasks it delegated, unless they are listed on the right; "" when no
// answer is on its way.
func (m *Model) waiting() string {
	label := m.waitingFor()
	if label != "" && len(m.chat.jobs) > 0 && !m.jobsShown {
		label += " · " + strings.Join(m.chat.jobs, " · ")
	}
	if label != "" && len(m.chat.tasks) > 0 && !m.jobsShown {
		label += " · " + strings.Join(m.chat.tasks, " · ")
	}
	return label
}

// waitingFor is what the agent of the session shown is waiting for, "" when
// no answer is on its way.
//
// A tool shows once it has run for a while: one that ends at once, as most
// commands and reads do, would flash its name for a moment and make the line
// flicker, so the agent is thinking until then.
func (m *Model) waitingFor() string {
	c := m.chat
	switch {
	case c.asking != nil:
		return m.agentName() + " waits for your answer"
	case c.calling != "":
		// Nothing of a call shows until it is written: what grows is
		// what tells that the model is at work.
		return m.agentName() + " is preparing an action · " + c.calling
	case c.partial != "":
		return m.agentName() + " is writing"
	case c.doing != "" && m.now().Sub(c.since) >= toolShown:
		return c.doing
	case c.doing != "" || c.pending:
		return m.agentName() + " is thinking"
	}
	return ""
}

// toolShown is how long a tool runs before the chat names it.
const toolShown = 300 * time.Millisecond
