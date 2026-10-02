package tui

import (
	"math"
	"strings"
	"time"
)

// The ouroboros is drawn with half blocks, so every cell holds two square
// pixels on top of each other. The head is a fixed sprite. The body is a ring
// that starts at the neck and runs counterclockwise until the tail ends in the
// mouth.
const (
	logoPixelsW, logoPixelsH = 36, 34
	ringX, ringY, ringR      = 18, 18.5, 12.5
	headX, headY             = 5, 0
)

var (
	neckAngle = 128 * math.Pi / 180
	bodyArc   = (360 - 128 + 97) * math.Pi / 180 // the tail ends inside the mouth at 97°
)

// The head sprites: # is filled, o is the eye, m is the mouth where the tail
// shows through.
var (
	headOpen = []string{
		"...######.......",
		".##########.....",
		"###oo#######....",
		"###oo########...",
		"##############..",
		"#######mmmmmmmm.",
		"######mmmmmmmmm.",
		"#######mmmmmmm..",
		"###########.....",
		".#########......",
		"..######........",
	}
	headShut = []string{
		"...######.......",
		".##########.....",
		"###oo#######....",
		"###oo########...",
		"##############..",
		"##############m.",
		"######mmmmmmmmm.",
		"##############..",
		"############....",
		".#########......",
		"..######........",
	}
)

// Splash timeline: the body grows around the ring, the tail reaches the
// mouth, then the jaw snaps shut.
const (
	splashDuration = 2 * time.Second
	splashFrame    = 40 * time.Millisecond
	growDuration   = 1300 * time.Millisecond
	biteAt         = 1500 * time.Millisecond
)

// logoLines is the finished logo: the tail rests in the open mouth.
var logoLines = drawLogo(1, false)

// splashLogo returns the animation frame elapsed after the start.
func splashLogo(elapsed time.Duration) []string {
	grown := min(1, float64(elapsed)/float64(growDuration))
	grown = 1 - (1-grown)*(1-grown) // ease out: the snake slows down before it bites
	return drawLogo(grown, elapsed >= biteAt)
}

// drawLogo renders the ouroboros with the given share of the body grown.
// Every frame has the same size, so the logo does not jump while it animates.
func drawLogo(grown float64, shut bool) []string {
	var px [logoPixelsH][logoPixelsW]bool
	length := bodyArc * grown
	for y := range logoPixelsH {
		for x := range logoPixelsW {
			dx, dy := float64(x)+0.5-ringX, ringY-float64(y)-0.5
			t := math.Mod(math.Atan2(dy, dx)-neckAngle+4*math.Pi, 2*math.Pi)
			if length == 0 || t > length {
				continue
			}
			thickness := 2.3*(1-t/length) + 0.8 // taper from the neck to the tail
			px[y][x] = math.Abs(math.Hypot(dx, dy)-ringR) <= thickness
		}
	}
	head := headOpen
	if shut {
		head = headShut
	}
	for j, row := range head {
		for i, c := range row {
			switch c {
			case '#':
				px[headY+j][headX+i] = true
			case 'o':
				px[headY+j][headX+i] = false
			}
		}
	}

	// Crop to the finished logo, which is the widest frame.
	const left, right = 3, 32
	lines := make([]string, 0, logoPixelsH/2)
	for y := 0; y < logoPixelsH; y += 2 {
		var b strings.Builder
		for x := left; x < right; x++ {
			top, bottom := px[y][x], px[y+1][x]
			switch {
			case top && bottom:
				b.WriteString("█")
			case top:
				b.WriteString("▀")
			case bottom:
				b.WriteString("▄")
			default:
				b.WriteByte(' ')
			}
		}
		lines = append(lines, b.String())
	}
	return lines
}
