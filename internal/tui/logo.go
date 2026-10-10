package tui

import (
	"math"
	"strings"
)

// The ouroboros is drawn with braille dots, so every cell holds two by four
// square pixels. The snake lies on a ring: the body starts at the neck and
// runs counterclockwise until the tail ends between the jaws. Its back is
// armored with chevron scales; fine ribs pick out the outlined belly.
const (
	logoCols, logoRows  = 30, 16
	ringX, ringY, ringR = 30, 34, 25.2
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

// pose is how the snake lies. Its body always has the same length: curled up
// less, it lies on a wider ring that is open at the top, with the gap between
// the snout and the tip of the tail where the jaws close on it.
type pose struct {
	curl  float64 // the angle the body sweeps: bodyArc when the tail is in the mouth
	turn  float64 // how far the snake is turned counterclockwise
	scale float64 // pixels per pixel of the logo
	// x, y is where the center of the ring is once the snake is curled up,
	// in pixels from the top left. Uncurled, the ring keeps its bottom there.
	x, y float64
}

// logoPose is the snake of the logo: curled up, at the size of the logo.
var logoPose = pose{curl: bodyArc, scale: 1, x: ringX, y: ringY}

// logoCells draws the ouroboros of the logo into braille cells.
func logoCells(grown float64, shut bool) [][]logoCell {
	return poseCells(logoPose, grown, shut, logoCols, logoRows)
}

// ring is where the snake lies in a pose, in pixels of the logo.
type ring struct {
	r      float64 // radius
	cx, cy float64 // center, in pixels of the screen
	neck   float64 // angle of the neck; the body runs counterclockwise from it
	head   float64 // angle the head takes, clockwise from the neck
	scale  float64
}

// headArc is the angle the head takes on the ring of the logo.
var headArc = neckAngle - snoutAngle

func (p pose) ring() ring {
	r := ringR * bodyArc / p.curl
	head := headArc * ringR / r // the head keeps its length too
	// The gap stays centered where it is when the snake is curled up.
	gap := neckAngle - headArc + (headArc+bodyArc)/2
	return ring{
		r:     r,
		cx:    p.x,
		cy:    p.y - (r-ringR)*p.scale,
		neck:  gap - (head+p.curl)/2 + head + p.turn,
		head:  head,
		scale: p.scale,
	}
}

// point returns where the pixel lies on the ring: its distance d outward
// from the ring line, how far u along the body from the neck in radians of
// the logo's ring, and how far f along the head, from 0 at the neck to 1 at
// the snout.
func (g ring) point(x, y int) (d, u, f float64) {
	dx, dy := (float64(x)+0.5-g.cx)/g.scale, (g.cy-float64(y)-0.5)/g.scale
	angle := math.Atan2(dy, dx)
	return math.Hypot(dx, dy) - g.r, wrap(angle-g.neck) * g.r / ringR, wrap(g.neck-angle) / g.head
}

// wrap returns the angle a in [0, 2π).
func wrap(a float64) float64 { return a - 2*math.Pi*math.Floor(a/(2*math.Pi)) }

// reach is how far from the ring line the snake ever lies, in pixels of the logo.
const reach = 8

// poseCells draws the snake in pose p, with the given share of the body
// grown, into cols by rows braille cells.
func poseCells(p pose, grown float64, shut bool, cols, rows int) [][]logoCell {
	g := p.ring()
	w, h := cols*2, rows*4
	// solid is the silhouette of the snake, px the pixels that are drawn.
	solid, px := grid(w, h), grid(w, h)
	length := bodyArc * grown
	for y := range h {
		for x := range w {
			d, u, f := g.point(x, y)
			if math.Abs(d) > reach {
				continue
			}
			if length > 0 && u <= length {
				thickness := 3.4*(1-u/length) + 0.6 // taper from the neck to the tail
				solid[y][x] = math.Abs(d) <= thickness
			}
			if f <= 1 && inHead(f, d, shut) {
				solid[y][x] = true
			}
		}
	}
	for y := range h {
		for x := range w {
			if !solid[y][x] {
				continue
			}
			d, u, f := g.point(x, y)
			edge := x == 0 || y == 0 || x == w-1 || y == h-1 ||
				!solid[y][x-1] || !solid[y][x+1] || !solid[y-1][x] || !solid[y+1][x]
			px[y][x] = edge || bodyInk(u, d)
			if f <= 1 {
				px[y][x] = edge || d > 0.2
				// A long brow frames a vertical slit rather than a round pupil.
				ex, ey := (f-eyeAt)*headLength(), d-4
				if ex*ex/(2.3*2.3)+ey*ey/(1.6*1.6) < 1 {
					px[y][x] = math.Abs(ex) < 0.45 && math.Abs(ey) < 1.2
				}
			}
		}
	}

	// The dots of a braille cell, by row and column.
	dots := [4][2]rune{{0x01, 0x08}, {0x02, 0x10}, {0x04, 0x20}, {0x40, 0x80}}
	cells := make([][]logoCell, rows)
	for row := range rows {
		cells[row] = make([]logoCell, cols)
		for col := range cols {
			c := &cells[row][col]
			n := 0
			for j := range 4 {
				for i := range 2 {
					x, y := col*2+i, row*4+j
					if !px[y][x] {
						continue
					}
					c.dots |= dots[j][i]
					if _, u, f := g.point(x, y); f > 1 {
						c.along += u / bodyArc
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

// bodyInk carves staggered chevrons into the back and adds sparse belly
// ribs. Coordinates follow the ring, so the armor turns and uncurls with it.
func bodyInk(u, d float64) bool {
	along := u * ringR
	if d > 0.2 {
		seam := math.Mod(along+1.4*math.Abs(d-1.8), 5.5)
		return seam > 0.85
	}
	return math.Mod(along-0.7*d, 5.5) < 0.8
}

func grid(w, h int) [][]bool {
	g := make([][]bool, h)
	for y := range g {
		g[y] = make([]bool, w)
	}
	return g
}

// headLength is the length of the head along the ring in pixels.
func headLength() float64 {
	return headArc * ringR
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
	// Two tapered fangs hang from the upper jaw into the open mouth.
	// They stay inside the old silhouette's reach, clear of the lower jaw.
	fang := false
	for _, at := range [...]float64{0.64, 0.81} {
		depth := (1.3 - d) / 3.0
		if depth >= 0 && depth <= 1 && math.Abs((f-at)*headLength()) <= 0.9*(1-depth) {
			fang = true
		}
	}
	return upper || lower || fang
}
