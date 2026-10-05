package tui

import (
	"image/color"
	"math"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
)

// Splash timeline: the stars come up, the body grows around the ring and the
// jaw snaps shut on the tail with a burst of sparks. Then a glow runs along
// the body, the colors start to flow, the name decodes itself letter by
// letter, and the scene fades out.
const (
	splashDuration = 3200 * time.Millisecond
	splashFrame    = 33 * time.Millisecond
	fadeIn         = 400 * time.Millisecond
	fadeOut        = 350 * time.Millisecond
	growDuration   = 1300 * time.Millisecond
	biteAt         = 1450 * time.Millisecond
	flashDuration  = 150 * time.Millisecond
	pulseDuration  = 900 * time.Millisecond
	sparkLife      = 700 * time.Millisecond
	titleAt        = 1000 * time.Millisecond
	letterGap      = 60 * time.Millisecond
	scrambleFor    = 300 * time.Millisecond
	shineAt        = 2100 * time.Millisecond
	shineDuration  = 600 * time.Millisecond
	taglineAt      = 1900 * time.Millisecond
	typeSpeed      = 22 * time.Millisecond // per character of the tagline
)

const (
	titleText = "ouroboros"
	tagline   = "a tiny A2A agent node"
	skipHint  = "press any key"
)

// titleFont draws the letters of the title, four by six pixels each.
var titleFont = map[rune][6]string{
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

func halfBlocks(text string) []string {
	blocks := map[[2]bool]rune{{true, true}: '█', {true, false}: '▀', {false, true}: '▄', {false, false}: ' '}
	lines := make([]string, 3)
	for row := range lines {
		var b strings.Builder
		for i, r := range text {
			if i > 0 {
				b.WriteByte(' ')
			}
			glyph := titleFont[r]
			for x := range 4 {
				b.WriteRune(blocks[[2]bool{glyph[row*2][x] == '#', glyph[row*2+1][x] == '#'}])
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

// clear blanks the cells from x0, y0 up to x1, y1, which are left out.
func (c *canvas) clear(x0, y0, x1, y1 int) {
	for y := max(0, y0); y < min(c.h, y1); y++ {
		for x := max(0, x0); x < min(c.w, x1); x++ {
			c.cells[y*c.w+x] = cell{}
		}
	}
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

// drawSplash renders the splash for a screen of w by h at the time elapsed
// after its start.
func drawSplash(w, h int, at time.Duration, light bool) string {
	p := darkPalette
	if light {
		p = lightPalette
	}
	c := &canvas{w: w, h: h, cells: make([]cell, w*h), back: p.back}
	c.alpha = min(ramp(at, 0, fadeIn), ramp(splashDuration-at, 0, fadeOut))

	title, letter := bigTitle, bigLetter
	if w < len([]rune(bigTitle[0]))+4 || h < logoRows+len(bigTitle)+5 {
		title, letter = []string{titleText}, 1
	}
	block := logoRows + 1 + len(title) + 2 // logo, gap, title, gap, tagline
	top, left := (h-block)/2, (w-logoCols)/2

	drawStars(c, p, at)
	// The jaws close at the left of the snout.
	biteX := float64(left) + (ringX+ringR*math.Cos(110*math.Pi/180))/2
	biteY := float64(top) + (ringY-ringR*math.Sin(110*math.Pi/180))/4
	drawSparks(c, p, biteX, biteY, at)
	drawSnake(c, p, left, top, at)
	// The text stays clear of the stars.
	titleLeft := (w - len([]rune(title[0]))) / 2
	c.clear(titleLeft-1, top+logoRows+1, w-titleLeft+1, top+block)
	drawTitle(c, p, title, letter, top+logoRows+1, at)

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

// drawStars fills the sky with two layers of twinkling stars; the nearer one
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

// drawSparks bursts sparks out of the bite, which slow down and die out.
func drawSparks(c *canvas, p palette, x0, y0 float64, at time.Duration) {
	if at < biteAt || at >= biteAt+sparkLife {
		return
	}
	age := ramp(at, biteAt, sparkLife)
	glyphs := []rune{'✦', '*', '+', '·'}
	const count = 18
	for i := range count {
		n := hash(i, 7, 3)
		dir := (float64(i) + float64(n%100)/100) / count * 2 * math.Pi
		reach := (5 + float64(n>>8%50)/10) * (1 - (1-age)*(1-age))
		x := int(math.Round(x0 + math.Cos(dir)*reach*2)) // a cell is twice as high as wide
		y := int(math.Round(y0 - math.Sin(dir)*reach))
		c.set(x, y, glyphs[int(age*float64(len(glyphs)))], mix(p.spark, p.back, age*age), false)
	}
}

// drawSnake draws the ouroboros in the gradient. While it grows, the tip of
// its tail shines like a comet; when it bites, it flashes, a glow runs from
// its head to its tail, and its colors start to flow.
func drawSnake(c *canvas, p palette, left, top int, at time.Duration) {
	grown := splashGrown(at)
	bitten := at >= biteAt
	since := (at - biteAt).Seconds()
	pulse := ramp(at, biteAt, pulseDuration)*1.3 - 0.15
	for row, line := range logoCells(grown, bitten) {
		for col, cl := range line {
			if cl.dots == 0 {
				continue
			}
			hue, glow := cl.along*0.9, 0.85*bell(grown-cl.along, 0.05)
			if bitten {
				hue -= since * 0.45
				glow = max(0.7*(1-ramp(at, biteAt, flashDuration)), 0.8*bell(cl.along-pulse, 0.07))
			}
			c.set(left+col, top+row, 0x2800+cl.dots, mix(p.gradient(hue), p.glow, glow), false)
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
