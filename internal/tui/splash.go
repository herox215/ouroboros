package tui

import (
	"image/color"
	"math"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// splashScreen is the state of the splash, which the UI opens with, and of
// the frames that move it and the setup.
type splashScreen struct {
	show    bool
	start   time.Time
	elapsed time.Duration
	// animating says that frames are coming, for the splash or the setup.
	animating bool
}

// animate makes the frames come unless they do already.
func (s *splashScreen) animate() tea.Cmd {
	if s.animating {
		return nil
	}
	s.animating = true
	return splashTick()
}

// update takes the frame at at: the splash goes on until it is over, and
// then no frame follows.
func (s *splashScreen) update(at time.Time) tea.Cmd {
	if !s.show {
		s.animating = false
		return nil
	}
	s.elapsed = at.Sub(s.start)
	if s.elapsed >= splashDuration {
		s.show, s.animating = false, false
		return nil
	}
	return splashTick()
}

// handleKey handles a key while the splash is shown: any key skips it, and
// ctrl+c quits.
func (s *splashScreen) handleKey(m *Model, msg tea.KeyPressMsg) tea.Cmd {
	if msg.String() == "ctrl+c" {
		return m.leave()
	}
	s.show = false
	return nil
}

// Splash timeline: the name decodes itself letter by letter and the tagline
// is typed. A shine sweeps across the name, and the scene fades out. A splash
// that follows the setup starts at biteAt, where the snake of the setup bites
// its tail.
const (
	splashDuration = 2600 * time.Millisecond
	splashFrame    = 33 * time.Millisecond
	fadeIn         = 400 * time.Millisecond
	fadeOut        = 350 * time.Millisecond
	biteAt         = 1450 * time.Millisecond
	titleAt        = 400 * time.Millisecond
	letterGap      = 60 * time.Millisecond
	scrambleFor    = 300 * time.Millisecond
	shineAt        = 1500 * time.Millisecond
	shineDuration  = 600 * time.Millisecond
	taglineAt      = 1300 * time.Millisecond
	typeSpeed      = 22 * time.Millisecond // per character of the tagline
)

const (
	titleText = "ouroboros"
	tagline   = "a tiny A2A agent node"
	skipHint  = "press any key"
)

// titleFont draws the letters of the title, and of the name of the agent in
// an empty chat, four by six pixels each.
var titleFont = map[rune][6]string{
	'a': {".##.", "#..#", "#..#", "####", "#..#", "#..#"},
	'c': {".###", "#...", "#...", "#...", "#...", ".###"},
	'd': {"###.", "#..#", "#..#", "#..#", "#..#", "###."},
	'e': {"####", "#...", "###.", "#...", "#...", "####"},
	'f': {"####", "#...", "###.", "#...", "#...", "#..."},
	'g': {".###", "#...", "#...", "#.##", "#..#", ".###"},
	'h': {"#..#", "#..#", "####", "#..#", "#..#", "#..#"},
	'i': {"####", ".##.", ".##.", ".##.", ".##.", "####"},
	'j': {"..##", "...#", "...#", "...#", "#..#", ".##."},
	'k': {"#..#", "#.#.", "##..", "##..", "#.#.", "#..#"},
	'l': {"#...", "#...", "#...", "#...", "#...", "####"},
	'm': {"#..#", "####", "####", "#..#", "#..#", "#..#"},
	'n': {"#..#", "##.#", "##.#", "#.##", "#.##", "#..#"},
	'p': {"###.", "#..#", "#..#", "###.", "#...", "#..."},
	'q': {".##.", "#..#", "#..#", "#..#", "#.##", ".###"},
	't': {"####", ".##.", ".##.", ".##.", ".##.", ".##."},
	'v': {"#..#", "#..#", "#..#", "#..#", ".##.", ".##."},
	'w': {"#..#", "#..#", "#..#", "####", "####", "#..#"},
	'x': {"#..#", "#..#", ".##.", ".##.", "#..#", "#..#"},
	'y': {"#..#", "#..#", ".##.", ".##.", ".##.", ".##."},
	'z': {"####", "...#", "..#.", ".#..", "#...", "####"},
	'0': {".##.", "#..#", "#.##", "##.#", "#..#", ".##."},
	'1': {"..#.", ".##.", "..#.", "..#.", "..#.", ".###"},
	'2': {".##.", "#..#", "...#", "..#.", ".#..", "####"},
	'3': {"###.", "...#", ".##.", "...#", "...#", "###."},
	'4': {"#..#", "#..#", "####", "...#", "...#", "...#"},
	'5': {"####", "#...", "###.", "...#", "...#", "###."},
	'6': {".##.", "#...", "###.", "#..#", "#..#", ".##."},
	'7': {"####", "...#", "..#.", "..#.", ".#..", ".#.."},
	'8': {".##.", "#..#", ".##.", "#..#", "#..#", ".##."},
	'9': {".##.", "#..#", "#..#", ".###", "...#", ".##."},
	' ': {"....", "....", "....", "....", "....", "...."},
	'-': {"....", "....", "####", "....", "....", "...."},
	'_': {"....", "....", "....", "....", "....", "####"},
	'.': {"....", "....", "....", "....", "....", ".##."},
	'o': {".##.", "#..#", "#..#", "#..#", "#..#", ".##."},
	'u': {"#..#", "#..#", "#..#", "#..#", "#..#", ".##."},
	'r': {"###.", "#..#", "#..#", "###.", "#.#.", "#..#"},
	'b': {"###.", "#..#", "###.", "#..#", "#..#", "###."},
	's': {".###", "#...", ".##.", "...#", "...#", "###."},
}

// bigTitle is the title in the font: two pixel rows share a cell as half
// blocks, and a blank column follows every letter.
var bigTitle = halfBlocks(titleText)

const bigLetter = 5 // columns of a letter of bigTitle, with its gap

func halfBlocks(text string) []string { return scaledHalfBlocks(text, 1) }

func scaledHalfBlocks(text string, scale int) []string {
	blocks := map[[2]bool]rune{{true, true}: '█', {true, false}: '▀', {false, true}: '▄', {false, false}: ' '}
	lines := make([]string, 3*scale)
	for row := range lines {
		var b strings.Builder
		for i, r := range text {
			if i > 0 {
				b.WriteString(strings.Repeat(" ", scale))
			}
			glyph := titleFont[r]
			for x := range 4 * scale {
				b.WriteRune(blocks[[2]bool{glyph[row*2/scale][x/scale] == '#', glyph[(row*2+1)/scale][x/scale] == '#'}])
			}
		}
		lines[row] = b.String()
	}
	return lines
}

// palette holds the colors of the splash for a dark or a light background.
type palette struct {
	body  []color.RGBA // a cyclic gradient along the snake and the title
	glow  color.RGBA   // what highlights shine toward
	back  color.RGBA   // what fades into the background
	star  color.RGBA
	spark color.RGBA
	text  color.RGBA
}

var (
	darkPalette = palette{
		body:  []color.RGBA{rgb(0x2dd4bf), rgb(0x38bdf8), rgb(0xa78bfa), rgb(0xf472b6)},
		glow:  rgb(0xffffff),
		back:  rgb(0x101018),
		star:  rgb(0xc7d2fe),
		spark: rgb(0xfde68a),
		text:  rgb(0x94a3b8),
	}
	lightPalette = palette{
		body:  []color.RGBA{rgb(0x0f766e), rgb(0x1d4ed8), rgb(0x6d28d9), rgb(0xbe185d)},
		glow:  rgb(0x0b0b10),
		back:  rgb(0xf4f4f5),
		star:  rgb(0x6366f1),
		spark: rgb(0xd97706),
		text:  rgb(0x475569),
	}
)

func rgb(hex uint32) color.RGBA {
	return color.RGBA{uint8(hex >> 16), uint8(hex >> 8), uint8(hex), 0xff}
}

// mix returns the color the share f of the way from a to b.
func mix(a, b color.RGBA, f float64) color.RGBA {
	f = clamp(f)
	channel := func(x, y uint8) uint8 { return uint8(math.Round(float64(x) + (float64(y)-float64(x))*f)) }
	return color.RGBA{channel(a.R, b.R), channel(a.G, b.G), channel(a.B, b.B), 0xff}
}

// gradient returns the color at f of the cyclic gradient, which repeats every 1.
func (p palette) gradient(f float64) color.RGBA {
	pos := (f - math.Floor(f)) * float64(len(p.body))
	i := int(pos)
	return mix(p.body[i], p.body[(i+1)%len(p.body)], pos-float64(i))
}

func clamp(f float64) float64 { return max(0, min(1, f)) }

// ramp returns how far at is through the span that starts at from: 0 before, 1 after.
func ramp(at, from, span time.Duration) float64 {
	return clamp(float64(at-from) / float64(span))
}

// bell is 1 where x is 0 and falls off to both sides with the given width.
func bell(x, width float64) float64 {
	return math.Exp(-(x / width) * (x / width))
}

// hash spreads three numbers over 32 bits, for noise that is the same in every frame.
func hash(x, y, z int) uint32 {
	h := uint32(x)*374761393 + uint32(y)*668265263 + uint32(z)*2246822519
	h = (h ^ h>>13) * 1274126177
	return h ^ h>>16
}

// cell is one character of the splash. A cell without a rune is blank.
type cell struct {
	r    rune
	fg   color.RGBA
	bold bool
}

// canvas is the screen of the splash. Everything drawn on it fades from the
// background in by the share alpha.
type canvas struct {
	w, h  int
	cells []cell
	alpha float64
	back  color.RGBA
}

func (c *canvas) set(x, y int, r rune, fg color.RGBA, bold bool) {
	if x < 0 || y < 0 || x >= c.w || y >= c.h {
		return
	}
	c.cells[y*c.w+x] = cell{r, mix(c.back, fg, c.alpha), bold}
}

// text draws s from x on, every rune in one cell.
func (c *canvas) text(x, y int, s string, fg color.RGBA, bold bool) {
	for i, r := range []rune(s) {
		c.set(x+i, y, r, fg, bold)
	}
}

// String renders the canvas: neighbors of the same color share one style.
func (c *canvas) String() string {
	var b strings.Builder
	for y := range c.h {
		if y > 0 {
			b.WriteByte('\n')
		}
		row := c.cells[y*c.w : (y+1)*c.w]
		for x := 0; x < len(row); {
			end := x + 1
			for end < len(row) && row[end].fg == row[x].fg && row[end].bold == row[x].bold {
				end++
			}
			run := make([]rune, end-x)
			for i, cl := range row[x:end] {
				run[i] = max(cl.r, ' ')
			}
			if row[x].r == 0 {
				b.WriteString(string(run))
			} else {
				b.WriteString(lipgloss.NewStyle().Foreground(row[x].fg).Bold(row[x].bold).Render(string(run)))
			}
			x = end
		}
	}
	return b.String()
}

// colors returns the palette for a light or a dark background.
func colors(light bool) palette {
	if light {
		return lightPalette
	}
	return darkPalette
}

// splashLayout returns what the splash shows in the middle of a screen of w
// by h: the title, the columns of one of its letters, its top row, and its
// rows down to the tagline.
func splashLayout(w, h int) (title []string, letter, top, block int) {
	title, letter = bigTitle, bigLetter
	if w < len([]rune(bigTitle[0]))+4 || h < len(bigTitle)+4 {
		title, letter = []string{titleText}, 1
	}
	block = len(title) + 2 // title, gap, tagline
	return title, letter, (h - block) / 2, block
}

// drawSplash renders the splash for a screen of w by h at the time elapsed
// after its start: the title in the middle of it, and nothing around it.
func drawSplash(w, h int, at time.Duration, light bool) string {
	p := colors(light)
	c := &canvas{w: w, h: h, cells: make([]cell, w*h), back: p.back}
	c.alpha = min(ramp(at, 0, fadeIn), ramp(splashDuration-at, 0, fadeOut))
	title, letter, top, block := splashLayout(w, h)
	drawTitle(c, p, title, letter, top, at)

	typed := min(len(tagline), max(0, int((at-taglineAt)/typeSpeed)))
	x := (w - len(tagline)) / 2
	c.text(x, top+block-1, tagline[:typed], p.text, false)
	if at >= taglineAt && typed < len(tagline) {
		c.set(x+typed, top+block-1, '▌', p.gradient(0), false)
	}
	if h >= block+2 {
		c.text((w-len(skipHint))/2, h-1, skipHint, mix(p.back, p.text, 0.6), false)
	}
	return c.String()
}

// drawStars fills the sky of the setup with two layers of twinkling stars; the nearer one
// drifts faster.
func drawStars(c *canvas, p palette, at time.Duration) {
	s := at.Seconds()
	glyphs := [2][]rune{{'.', '·'}, {'·', '+', '*'}}
	for layer, speed := range []float64{1.5, 4} { // cells per second
		shift := int(s * speed)
		for y := range c.h {
			for x := range c.w {
				n := hash(x+shift, y, layer)
				if n%1000 >= 14 {
					continue
				}
				twinkle := 0.5 + 0.5*math.Sin(s*(2+float64(n>>10%30)/10)+float64(n>>20%628)/100)
				g := glyphs[layer]
				r := g[min(len(g)-1, int(twinkle*float64(len(g))))]
				c.set(x, y, r, mix(p.back, p.star, 0.2+0.6*twinkle*(0.5+0.5*float64(layer))), false)
			}
		}
	}
}

// drawTitle decodes the title letter by letter: each one scrambles for a
// moment before it settles. Then a shine sweeps across it.
func drawTitle(c *canvas, p palette, title []string, letter, top int, at time.Duration) {
	width := len([]rune(title[0]))
	left := (c.w - width) / 2
	shine := ramp(at, shineAt, shineDuration)*float64(width+12) - 6
	scramble := []rune("░▒▓▚▞#%&@")
	frame := int(at / splashFrame)
	for row, line := range title {
		for x, r := range []rune(line) {
			reveal := titleAt + time.Duration(x/letter)*letterGap
			if at < reveal-scrambleFor || x%letter == bigLetter-1 {
				continue // not yet, or the gap after a letter
			}
			hue := float64(x)/float64(width)*0.8 - at.Seconds()*0.3
			if at < reveal {
				n := hash(x, row, frame)
				c.set(left+x, top+row, scramble[n%uint32(len(scramble))], mix(p.back, p.gradient(hue), 0.6), false)
				continue
			}
			if r != ' ' {
				c.set(left+x, top+row, r, mix(p.gradient(hue), p.glow, 0.9*bell(float64(x)-shine, 2.5)), true)
			}
		}
	}
}
