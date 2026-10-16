package tui

import "math"

const snakeRest = 3.0

// dragonFlight follows a rounded orbit rather than snapping around corners.
// Distances use terminal-cell proportions: a row is roughly two columns tall.
type dragonFlight struct {
	cx, cy, rx, ry, entry, orbit, exit, length, speed float64
}

func newDragonFlight(w, h, nw, nh int) dragonFlight {
	rx := float64(nw)/2 + 10
	ry := float64(nh)/2 + 5
	cx, cy := float64(w-1)/2, float64(h-1)/2
	// Approximate ellipse circumference in physical terminal-cell units.
	a, b := rx, ry*2
	orbit := math.Pi * (3*(a+b) - math.Sqrt((3*a+b)*(a+3*b)))
	entry := cx + 24
	exit := float64(w) - cx + 36
	return dragonFlight{cx, cy, rx, ry, entry, orbit, exit,
		orbit * 0.8, math.Max(18, float64(w)*0.25)}
}

func (f dragonFlight) duration() float64 {
	return (f.entry + f.orbit + f.exit + f.length) / f.speed
}

// point returns a continuous centreline and its outward normal. A travelling
// wave moves through the spine, with a second smaller wave for a living sway.
func (f dragonFlight) point(s, t float64) (x, y, nx, ny float64) {
	if s < f.entry {
		x, y, nx, ny = -24+s, f.cy-f.ry, 0, -1
	} else if s < f.entry+f.orbit {
		a := -math.Pi/2 + (s-f.entry)/f.orbit*2*math.Pi
		x, y = f.cx+f.rx*math.Cos(a), f.cy+f.ry*math.Sin(a)
		nx, ny = math.Cos(a), math.Sin(a)
	} else {
		x, y, nx, ny = f.cx+s-f.entry-f.orbit, f.cy-f.ry, 0, -1
	}
	wave := math.Sin(s*0.19-t*2.8) + 0.3*math.Sin(s*0.37-t*1.7)
	x += nx * wave * 1.5
	y += ny * wave * 0.8
	return
}

// The broad, side-on head stays readable during turns instead of rotating
// ASCII glyphs into a vertical jumble. Coordinates are relative to the neck.
var dragonHead = []string{
	"     /|       /|          ",
	"  __/ |__  __/ |          ",
	" /       \\/    /  __      ",
	"/  /~~~\\  ___ /__/  \\__   ",
	"\\_/  (@)  \\___     o  _\\  ",
	" ~\\   ___/     \\_____/==> ",
	"   \\_/  V  V___/   ~~~    ",
	"    /__/\\___/   ~~~~~     ",
	"      /_/   ~~~           ",
}

func drawNameSnake(c *canvas, p palette, nw, nh int, t, shown float64) {
	// A tiny viewport has no room for a recognizable dragon.
	if c.w < nw+16 || c.h < nh+12 {
		return
	}
	f := newDragonFlight(c.w, c.h, nw, nh)
	into := math.Mod(math.Max(0, t), f.duration()+snakeRest)
	if into >= f.duration() {
		return
	}
	head := into * f.speed
	left, top := (c.w-nw)/2, (c.h-nh)/2
	tagrow, tagok := emptyTaglineRow(c.w, c.h, nh)
	paint := func(x, y int, r rune, shade float64, gold bool) {
		if x >= left-1 && x <= left+nw && y >= top-1 && y <= top+nh {
			return
		}
		if tagok && y == tagrow && x >= (c.w-40)/2 && x <= (c.w+40)/2 {
			return
		}
		ink := rgb(0x63cba5)
		if gold {
			ink = rgb(0xf0c46b)
		}
		c.alpha = shown * shade
		c.set(x, y, r, mix(ink, p.glow, 0.08), false)
	}
	// Draw tail to neck. Dense samples join smoothly even at steep turns;
	// the taper, scales, belly and mane all follow the same moving spine.
	for age := f.length; age >= 2; age -= 0.65 {
		s := head - age
		x, y, nx, ny := f.point(s, into)
		life := 1 - age/f.length
		width := math.Min(1, life*5)
		for band := -1; band <= 1; band++ {
			px := int(math.Round(x + float64(band)*nx*width*1.8))
			py := int(math.Round(y + float64(band)*ny*width))
			r := ')'
			if int(age/2)%2 == 0 {
				r = '('
			}
			if band == -1 {
				r = '='
			}
			paint(px, py, r, 0.65+0.25*life, band == -1)
		}
		if int(age)%4 == 0 && life > 0.12 {
			paint(int(math.Round(x+nx*3)), int(math.Round(y+ny*1.8)), '^', 0.9, true)
		}
		// Four legs flex independently with the same travelling wave.
		for _, leg := range []float64{14, 23, 53, 62} {
			if math.Abs(age-leg) < 0.34 {
				bend := math.Sin(into*3+leg) * 1.5
				for j, r := range []rune{'/', ')', 'w'} {
					paint(int(math.Round(x+nx*float64(j+3)+bend)), int(math.Round(y+ny*float64(j+2))), r, 0.95, true)
				}
			}
		}
	}
	x, y, _, _ := f.point(head, into)
	px, _, _, _ := f.point(head-1, into)
	direction := 1
	if x < px {
		direction = -1
	}
	for row, line := range dragonHead {
		for col, r := range line {
			if r == ' ' {
				continue
			}
			if direction < 0 {
				switch r {
				case '/':
					r = '\\'
				case '\\':
					r = '/'
				case '(':
					r = ')'
				case ')':
					r = '('
				case '>':
					r = '<'
				}
			}
			// Whiskers trail and sway, while the skull stays coherent.
			bob := 0
			if r == '~' && row > 5 {
				bob = int(math.Round(math.Sin(into*4 + float64(col)*0.4)))
			}
			paint(int(math.Round(x))+direction*(col-6), int(math.Round(y))+row-4+bob, r, 1, row < 3 || row > 5 || r == '@')
		}
	}
}
