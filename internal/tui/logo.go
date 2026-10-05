package tui

import (
	"math"
	"strings"
	"time"
)

// The ouroboros is drawn with braille dots, so every cell holds two by four
// square pixels. The snake lies on a ring: the body starts at the neck and
// runs counterclockwise until the tail ends between the jaws. Its back is
// filled and its belly is only an outline.
const (
	logoCols, logoRows       = 30, 16
	logoPixelsW, logoPixelsH = logoCols * 2, logoRows * 4
	ringX, ringY, ringR      = 30, 34, 25.2
)

var (
	neckAngle  = 130 * math.Pi / 180
	snoutAngle = 86 * math.Pi / 180
	bodyArc    = (360 - 130 + 110) * math.Pi / 180 // the tail ends between the jaws at 110°
)

// Head proportions, as shares of the way from the neck to the snout.
const (
	jawHinge = 0.4
	jawEnd   = 0.88 // the lower jaw is shorter than the upper one
	eyeAt    = 0.47
)

// logoLines is the finished logo: the tail rests in the open mouth.
var logoLines = drawLogo(1, false)

// splashGrown returns the share of the body grown at the time elapsed after
// the start of the splash.
func splashGrown(elapsed time.Duration) float64 {
	grown := min(1, float64(elapsed)/float64(growDuration))
	return 1 - (1-grown)*(1-grown) // ease out: the snake slows down before it bites
}

// drawLogo renders the ouroboros with the given share of the body grown.
// Every frame has the same size, so the logo does not jump while it animates.
func drawLogo(grown float64, shut bool) []string {
	cells := logoCells(grown, shut)
	lines := make([]string, 0, logoRows)
	for _, row := range cells {
		var b strings.Builder
		for _, c := range row {
			if c.dots == 0 {
				b.WriteByte(' ')
			} else {
				b.WriteRune(0x2800 + c.dots)
			}
		}
		lines = append(lines, b.String())
	}
	return lines
}

// logoCell is one braille cell of the logo: its dots, and where on the snake
// they lie, from 0 at the head to 1 at the tip of the fully grown tail.
type logoCell struct {
	dots  rune
	along float64
}

// logoCells draws the ouroboros into braille cells.
func logoCells(grown float64, shut bool) [logoRows][logoCols]logoCell {
	// solid is the silhouette of the snake, px the pixels that are drawn.
	var solid, px [logoPixelsH][logoPixelsW]bool
	length := bodyArc * grown
	for y := range logoPixelsH {
		for x := range logoPixelsW {
			d, angle := ringPoint(x, y)
			t := bodyShare(angle)
			if length > 0 && t <= length {
				thickness := 3.4*(1-t/length) + 0.6 // taper from the neck to the tail
				solid[y][x] = math.Abs(d) <= thickness
			}
			if f, ok := headShare(angle); ok && inHead(f, d, shut) {
				solid[y][x] = true
			}
		}
	}
	for y := range logoPixelsH {
		for x := range logoPixelsW {
			if !solid[y][x] {
				continue
			}
			d, angle := ringPoint(x, y)
			edge := x == 0 || y == 0 || x == logoPixelsW-1 || y == logoPixelsH-1 ||
				!solid[y][x-1] || !solid[y][x+1] || !solid[y-1][x] || !solid[y+1][x]
			px[y][x] = edge || d > 0.2 // the back is the outer side of the ring
			if f, ok := headShare(angle); ok {
				eye := math.Hypot((f-eyeAt)*headLength(), d-4)
				if eye < 1.9 {
					px[y][x] = eye < 0.6 // a pupil in a hollow eye
				}
			}
		}
	}

	// The dots of a braille cell, by row and column.
	dots := [4][2]rune{{0x01, 0x08}, {0x02, 0x10}, {0x04, 0x20}, {0x40, 0x80}}
	var cells [logoRows][logoCols]logoCell
	for row := range logoRows {
		for col := range logoCols {
			c := &cells[row][col]
			n := 0
			for j := range 4 {
				for i := range 2 {
					x, y := col*2+i, row*4+j
					if !px[y][x] {
						continue
					}
					c.dots |= dots[j][i]
					if _, angle := ringPoint(x, y); !isHead(angle) {
						c.along += bodyShare(angle) / bodyArc
					}
					n++
				}
			}
			if n > 0 {
				c.along /= float64(n)
			}
		}
	}
	return cells
}

// bodyShare returns how far the angle lies along the body from the neck, in
// radians counterclockwise.
func bodyShare(angle float64) float64 {
	return math.Mod(angle-neckAngle+4*math.Pi, 2*math.Pi)
}

// isHead reports whether the angle lies on the head.
func isHead(angle float64) bool {
	_, ok := headShare(angle)
	return ok
}

// ringPoint returns where the pixel lies relative to the ring: its distance
// outward from the ring line and its angle around the center.
func ringPoint(x, y int) (d, angle float64) {
	dx, dy := float64(x)+0.5-ringX, ringY-float64(y)-0.5
	return math.Hypot(dx, dy) - ringR, math.Atan2(dy, dx)
}

// headLength is the length of the head along the ring in pixels.
func headLength() float64 {
	return (neckAngle - snoutAngle) * ringR
}

// headShare returns how far along the head the angle lies, from 0 at the neck
// to 1 at the snout. It reports false outside the head.
func headShare(angle float64) (float64, bool) {
	f := (neckAngle - angle) / (neckAngle - snoutAngle)
	return f, f >= 0 && f <= 1
}

// inHead reports whether the head covers the point at share f and distance d.
func inHead(f, d float64, shut bool) bool {
	const center = 0.8 // the head sits slightly outside the ring line
	if f < jawHinge {
		// The skull widens from the neck to the jaw hinge.
		return math.Abs(d-center) <= 4+2.6*math.Sin(f/jawHinge*math.Pi/2)
	}
	g := (f - jawHinge) / (1 - jawHinge)
	width := 6.6 * math.Sqrt(1-g*g) // both jaws narrow toward the snout
	if shut {
		return d <= center+width && d >= center-width*0.85 && (d >= -0.2 || f < jawEnd)
	}
	// The open jaws leave room for the tail; the lower one swings inward.
	upper := d >= 1.3 && d <= center+width
	swung := d + 5*g
	lower := f < jawEnd && swung <= -1.3 && swung >= center-width*0.85
	return upper || lower
}
