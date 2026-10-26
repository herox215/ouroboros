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
// there, and it shines, glitches, waves, floats, flares, flickers, beams
// a torch over itself, is engraved by a laser, stands in rain or is encircled
// by an ASCII snake. A visit of an
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
		m.status.Configured && m.chat.Peer == "" && m.resuming != m.chat
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
	nameWave                   // bands of light roll through the name
	nameFloat                  // the letters of the name bob up and down
	nameFlare                  // a letter of the name lights up like a lamp
	nameNeon                   // all of the name is lit, one letter flickers and throws sparks
	nameBeam                   // a torch wanders over the name in the dark
	nameLaser                  // an off-screen laser engraves the letters and throws sparks
	nameRain                   // rain falls behind the name and splashes at the bottom
	nameSnake                  // an ASCII snake flies in, circles the name and leaves
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

// A wave rolls bands of light through the name: they travel from its left
// to its right, leaning to the right like the shine, and every cell of the
// name breathes between the nameMatte it rests in and the light of a crest.
const (
	waveSpeed  = 9.0  // columns a crest travels per second
	waveLength = 1.4  // crests over the width of the name at once
	waveHeight = 0.55 // how bright a crest is, from 0 to 1
	waveLean   = 1.2  // columns a row is ahead of the one below it
)

// A float lets the letters of the name bob up and down, a row at most: a
// wave of the bob travels through the letters, and every letter follows it
// in its own time, so the name itself moves, gently.
const (
	floatPeriod = 2.8  // seconds a wave of the bob through the letters takes
	floatBob    = 0.55 // rows a letter bobs at most
)

// A flare lights a letter of the name up like a lamp: it glows up and dims
// again in its time, and the letters around it catch some of its light,
// the nearer the more. The light goes round the name, a letter after the
// other.
const (
	flareEvery  = 1.9 // seconds a letter holds the light
	flareReach  = 0.7 // the share of its light the letters around it catch
	flareSpread = 3.0 // columns the light falls off over, in the font at its size
)

// A neon lights all of the name up, and one letter of it, taken at random,
// flickers like a defective tube: the whole letter stutters out and burns
// again together. The letter is taken with the tagline the visit shows, and
// stays with it. Where it stutters out, little sparks fly, like shorts at
// the tube.
const (
	neonStep = 0.07 // seconds between two steps of the flicker
	neonOdds = 5    // a cell burns this many steps of the flicker for one it stutters out
	neonOut  = 0.07 // how bright a cell is where the tube is out
)

// A beam wanders over the name like a torch: a round spot of light that
// travels from its left to its right, swaying a row up and down, and lights
// the letters the nearer it stands to them. Outside of it the name rests in
// the dark.
const (
	beamSpeed = 9.0 // columns the spot travels per second
	beamWidth = 1.6 // columns the light falls off over
	beamLead  = 5   // columns it starts before the name, and goes on behind it
	beamRest  = 2.2 // seconds it rests before it sets out again
	beamSway  = 0.8 // rows the spot sways up and down
	beamSwayP = 4.2 // seconds a sway takes
)

// A spark of the defect burns for a few steps of the flicker: how many of
// them one step that stutters out throws, how long one lives, how far it
// flies at most from the middle of the letter, and what it shows, large to
// small as it burns out.
const (
	sparkMany  = 3
	sparkLife  = 3
	sparkReach = 4
)

var sparkArt = [...]rune{'✦', '•', '·'}

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
	if l == nameWave {
		spacing := float64(w) / waveLength
		at := t * waveSpeed
		return waveHeight * (0.5 + 0.5*math.Sin(2*math.Pi*(float64(x)-at-float64(h-1-y)*waveLean)/spacing))
	}
	if l == nameShine {
		scale := float64(h) / 3
		every := (float64(w)+2*shineLead*scale)/(shineSpeed*scale) + shineRest
		at := math.Mod(t, every)*shineSpeed*scale - shineLead*scale
		ahead := (float64(h-1)/2 - float64(y)) * shineLean
		return bell(float64(x)-at-ahead, shineWidth*scale)
	}
	if l != nameBeam {
		return 0
	}
	// The spot of the torch wanders over the name, and sways a row up and
	// down as it goes. Cells are taller than wide: a row weighs twice a
	// column, so that the spot stays round.
	scale := float64(h) / 3
	every := (float64(w)+2*beamLead*scale)/(beamSpeed*scale) + beamRest
	at := math.Mod(t, every)*beamSpeed*scale - beamLead*scale
	mid := float64(h-1) / 2
	sway := beamSway * scale * math.Sin(2*math.Pi*t/beamSwayP)
	dx := (float64(x) - at) / (beamWidth * scale)
	dy := 2 * (float64(y) - mid - sway) / (beamWidth * scale)
	return bell(math.Sqrt(dx*dx+dy*dy), 1)
}

// letterSpans are the first and the last column of every letter of an
// artwork: letters are apart by columns that every row of it leaves blank.
func letterSpans(art []string) [][2]int {
	var spans [][2]int
	w := len([]rune(art[0]))
	used := make([]bool, w)
	for _, row := range art {
		for x, r := range []rune(row) {
			if r != ' ' {
				used[x] = true
			}
		}
	}
	for x := 0; x < w; x++ {
		if !used[x] {
			continue
		}
		if n := len(spans); n > 0 && spans[n-1][1] == x-1 {
			spans[n-1][1] = x
		} else {
			spans = append(spans, [2]int{x, x})
		}
	}
	return spans
}

// neonFlicker is how the column x and the row y of the defect, the nth
// letter of spans, flickers at t: 1 where the tube burns, and dim where it
// stutters out. The whole letter flickers together, and cells outside of
// it burn steadily.
func neonFlicker(x, y, n int, spans [][2]int, t float64) float64 {
	if n < 0 || x < spans[n][0] || x > spans[n][1] {
		return 1
	}
	if hash(int(t/neonStep), n, 3)%neonOdds == 0 {
		return neonOut
	}
	return 1
}

// drawNameAnimation draws name where emptyChat has it, in the middle of
// width by height, with the gradient of the splash flowing through it and
// look moving it: as far as they have come in the time elapsed. It fills all
// of width by height, and nothing of the name moves but in a glitch or a
// float. A name
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
	if look == nameRain {
		drawNameRain(c, elapsed.Seconds(), shown)
	}
	if look == nameSnake {
		drawNameSnake(c, p, w, len(art), elapsed.Seconds(), shown)
	}
	torn := 0
	if look == nameGlitch {
		torn = glitch(elapsed.Seconds())
	}
	// A float bobs the letters of the name up and down: one row at most, a
	// wave of the bob through them.
	bob := make([]int, w)
	if look == nameFloat {
		spans := letterSpans(art)
		for i, span := range spans {
			dy := int(math.Round(floatBob * math.Sin(2*math.Pi*(elapsed.Seconds()/floatPeriod-float64(i)/float64(len(spans))))))
			for x := span[0]; x <= span[1]; x++ {
				bob[x] = dy
			}
		}
	}
	// A flare lights a letter of the name up: the letter glows up and dims
	// again, and the letters around it catch some of its light.
	lamp := make([]float64, w)
	if look == nameFlare {
		spans := letterSpans(art)
		into := math.Mod(elapsed.Seconds(), flareEvery*float64(len(spans)))
		n := int(into/flareEvery) % len(spans)
		share := math.Sin(math.Pi * (into/flareEvery - float64(n)))
		span := spans[n]
		scale := float64(len(art)) / 3
		for x := 0; x < w; x++ {
			if x < span[0] {
				lamp[x] = flareReach * share * bell(float64(span[0]-x), flareSpread*scale)
			} else if x > span[1] {
				lamp[x] = flareReach * share * bell(float64(x-span[1]), flareSpread*scale)
			} else {
				lamp[x] = share
			}
		}
	}
	// A neon lights all of the name up, and the defect flickers on one
	// letter of it, taken at random with the tagline the visit shows, and
	// staying with it.
	defect, neonSpans := -1, [][2]int{}
	if look == nameNeon {
		neonSpans = letterSpans(art)
		tag := taglineText(selected)
		seed := len(tag)
		if len(tag) > 0 {
			seed += int(tag[0])
		}
		defect = int(hash(seed, 0, 5)) % len(neonSpans)
	}
	if look == nameLaser {
		drawNameLaser(c, p, art, left, top, elapsed.Seconds(), shown)
	}
	for y, row := range art {
		if look == nameLaser {
			break
		}
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
			if look == nameFlare {
				lit = lamp[x]
			}
			if look == nameNeon {
				lit = neonFlicker(x, y, defect, neonSpans, elapsed.Seconds())
			}
			c.alpha = (nameMatte + (1-nameMatte)*lit) * shown
			fg := mix(p.gradient(float64(x)/float64(w)*0.8-elapsed.Seconds()*0.36), p.glow, shineGlow*lit)
			if jump != 0 {
				fg = p.body[tint>>8%uint32(len(p.body))]
			}
			if torn != 0 && hash(x, y, torn)%9 == 0 {
				c.set(left+x+jump, top+y, '▒', fg, false)
				continue
			}
			c.block(left+x+jump, top+y+bob[x], fg)
		}
	}
	// Sparks fly where the defect stutters out, like shorts at the tube: a
	// few of them a step that goes out throws, from the middle of the
	// letter out, and dim as they burn. They stay in the gaps between and
	// around the letters, never over one of them.
	if look == nameNeon && defect >= 0 {
		span, step := neonSpans[defect], int(elapsed.Seconds()/neonStep)
		mid := left + (span[0]+span[1])/2
		for age := 0; age < sparkLife; age++ {
			born := step - age
			if born < 0 || hash(born, defect, 3)%neonOdds != 0 {
				continue
			}
			for i := 0; i < sparkMany; i++ {
				h := hash(born, i, 11)
				if h%3 == 0 {
					continue
				}
				x := mid + (int(h>>4%uint32(2*sparkReach+1))-sparkReach)*(age+1)
				y := top + len(art)/2 + (int(h>>12%uint32(2*sparkReach+1))-sparkReach)*(age+1)
				if x < 0 || x >= width || y < 0 || y >= height {
					continue
				}
				if y >= top && y < top+len(art) {
					in := false
					for _, s := range neonSpans {
						if x >= left+s[0] && x <= left+s[1] {
							in = true
							break
						}
					}
					if in {
						continue
					}
				}
				c.alpha = shown * (1 - 0.7*float64(age)/float64(sparkLife))
				c.set(x, y, sparkArt[age], p.glow, false)
			}
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
