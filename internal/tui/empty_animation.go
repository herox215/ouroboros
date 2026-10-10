package tui

import (
	"math"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// nameAnimation is how the name of the agent moves in an empty chat: the
// gradient of the splash flows through it, as it does through the title
// there, and it shines or glitches, as its look is. A visit of an
// empty chat begins it anew, in a look picked by chance, and the first
// message of the chat fades the name out behind it.
type nameAnimation struct {
	look    nameLook
	active  bool
	chat    *conversation // what it moves for: another chat or another name starts anew
	tagline string
	name    string
	start   time.Time
	elapsed time.Duration
	// fading is when the first message came, while the name fades out.
	fading time.Time
}

// nameFade is how long the name takes to fade out behind the first message.
var nameFade = 450 * time.Millisecond

// faded is the share of the name that is left at at: 0 where none fades.
func (a *nameAnimation) faded(at time.Time) float64 {
	if a.fading.IsZero() || nameFade == 0 {
		return 0
	}
	return 1 - ramp(at.Sub(a.fading), 0, nameFade)
}

// behind puts the name, as much as is left of it, behind the content of a
// chat that has just begun: on the rows the content leaves blank. Content
// that fills the viewport has no room for it.
func (m *Model) behind(content string) string {
	a := &m.nameAnimation
	left := a.faded(m.now())
	lines := strings.Split(content, "\n")
	if left == 0 || a.chat != m.chat || len(lines) > m.history.Height() {
		return content
	}
	name := strings.Split(drawNameAnimation(a.name, m.history.Width(), m.history.Height(), a.elapsed, m.light, a.look, left, a.tagline), "\n")
	for y, line := range lines {
		if strings.TrimSpace(ansi.Strip(line)) != "" {
			name[y] = line
		}
	}
	return strings.Join(name, "\n")
}

// syncNameAnimation starts the animation where an empty chat of the owner is
// on the screen, and ends it where none is.
func (m *Model) syncNameAnimation() {
	a := &m.nameAnimation
	onScreen := m.shown() && !m.splash.show && m.onboard == nil && m.screen == screenChat &&
		m.status.Configured && m.chat.peer == "" && m.resuming != m.chat
	if !onScreen || len(m.chat.entries) > 0 {
		if a.active && onScreen && a.chat == m.chat {
			a.fading = m.now()
			m.renderHistory(false)
		}
		a.active = false
		return
	}
	if a.active && a.chat == m.chat && a.name == m.agentName() {
		return
	}
	a.active, a.chat, a.name = true, m.chat, m.agentName()
	a.start, a.elapsed, a.fading = m.now(), 0, time.Time{}
	a.tagline = emptyTaglines[pickEmptyTagline(len(emptyTaglines))]
	a.look = nameLook(pickNameLook(int(nameLooks)))
	m.renderHistory(false)
}

// nameMatte is the share of its colors the name is drawn in, the rest being
// the background: in all of them it would outshine the chat it stands in.
const nameMatte = 0.45

// nameLook is what happens to the name while the gradient flows.
type nameLook int

const (
	nameShine  nameLook = iota // a streak of light crosses the name
	nameGlitch                 // the name is torn for a moment now and then
	nameLooks                  // how many there are
)

// pickNameLook picks one of n looks by chance.
var pickNameLook = rand.IntN

var pickEmptyTagline = rand.IntN

// A shine crosses the name from left to right, leaning to the right, and
// sets out again a while after it has left. Its measures are for the font
// at its own size, and grow with it.
const (
	shineSpeed = 21.0 // columns per second
	shineWidth = 2.2  // columns
	shineLead  = 12   // columns it starts before the name, and goes on behind it
	shineRest  = 1.8  // seconds between two of them
	shineLean  = 2.0  // columns a row is ahead of the one below it
	shineGlow  = 0.85 // the share of the glow where it is
)

// A glitch tears the name for a moment: some of its rows jump to the side
// in a plain color of the gradient, and some of its cells are noise. Every
// glitchStep it is torn another way. One glitch comes in every glitchEvery,
// at a moment of its own.
const (
	glitchEvery = 2.4  // seconds
	glitchFor   = 0.34 // seconds a glitch lasts
	glitchStep  = 0.085
	glitchJump  = 3 // columns a row jumps at most, in the font at its own size
)

// glitchAt is when the glitch of the nth span of glitchEvery begins, in
// seconds from the beginning of the animation.
func glitchAt(n int) float64 {
	return float64(n)*glitchEvery + 0.5 + float64(hash(n, 1, 2)%1000)/1000*(glitchEvery-glitchFor-0.5)
}

// glitch returns how the glitch tears the name at t: 0 where there is none.
func glitch(t float64) int {
	n := int(t / glitchEvery)
	if into := t - glitchAt(n); into >= 0 && into < glitchFor {
		return 1 + n*8 + int(into/glitchStep)
	}
	return 0
}

// lit returns how much of the shine is on the column x and the row y of a
// name of w columns and h rows at the time t in seconds, from 0 to 1.
func (l nameLook) lit(x, y, w, h int, t float64) float64 {
	if l != nameShine {
		return 0
	}
	scale := float64(h) / 3
	every := (float64(w)+2*shineLead*scale)/(shineSpeed*scale) + shineRest
	at := math.Mod(t, every)*shineSpeed*scale - shineLead*scale
	ahead := (float64(h-1)/2 - float64(y)) * shineLean
	return bell(float64(x)-at-ahead, shineWidth*scale)
}

// drawNameAnimation draws name where emptyChat has it, in the middle of
// width by height, with the gradient of the splash flowing through it and
// look moving it: as far as they have come in the time elapsed. It fills all
// of width by height, and nothing of the name moves but in a glitch. A name
// too large for the artwork rests as emptyChat shows it. Of a name that
// fades out the share shown is left.
func drawNameAnimation(name string, width, height int, elapsed time.Duration, light bool, look nameLook, shown float64, selected ...string) string {
	art := fitName(name, width, height)
	if art == nil {
		return emptyChat(name, width, height, selected...)
	}
	p := colors(light)
	c := &canvas{w: width, h: height, cells: make([]cell, width*height), back: p.back}
	w := len([]rune(art[0]))
	left, top := (width-w)/2, (height-len(art))/2
	torn := 0
	if look == nameGlitch {
		torn = glitch(elapsed.Seconds())
	}
	for y, row := range art {
		jump, tint := 0, hash(y, torn, 7)
		if torn != 0 && tint%3 != 0 {
			jump = (int(tint>>4%(2*glitchJump+1)) - glitchJump) * len(art) / 3
		}
		// The columns of a row are its runes: a block is three bytes long.
		for x, r := range []rune(row) {
			if r == ' ' {
				continue
			}
			lit := look.lit(x, y, w, len(art), elapsed.Seconds())
			c.alpha = (nameMatte + (1-nameMatte)*lit) * shown
			fg := mix(p.gradient(float64(x)/float64(w)*0.8-elapsed.Seconds()*0.36), p.glow, shineGlow*lit)
			if jump != 0 {
				fg = p.body[tint>>8%uint32(len(p.body))]
			}
			if torn != 0 && hash(x, y, torn)%9 == 0 {
				r = '▒'
			}
			c.set(left+x+jump, top+y, r, fg, false)
		}
	}
	if row, ok := emptyTaglineRow(width, height, len(art), selected...); ok {
		c.alpha = nameMatte * shown
		tagline := taglineText(selected)
		for x, r := range tagline {
			c.set((width-len(tagline))/2+x, row, r, p.body[0], false)
		}
	}
	return c.String()
}
