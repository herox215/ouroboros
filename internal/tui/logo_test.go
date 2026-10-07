package tui

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
)

// poseText writes the cells of a snake down: the braille dots, then for
// every cell with dots how far along the snake they lie.
func poseText(cells [][]logoCell) string {
	dots, along := poseParts(cells)
	return dots + along
}

func poseParts(cells [][]logoCell) (string, string) {
	var dots, along strings.Builder
	for _, row := range cells {
		for _, c := range row {
			if c.dots == 0 {
				dots.WriteByte(' ')
				along.WriteString("   .  ")
			} else {
				dots.WriteRune(0x2800 + c.dots)
				fmt.Fprintf(&along, " %.3f", c.along)
			}
		}
		dots.WriteByte('\n')
		along.WriteByte('\n')
	}
	return dots.String(), along.String()
}

// The logo is drawn the same as it was looked at while it grows, with the
// jaws open and shut.
func TestGoldenLogoGrows(t *testing.T) {
	var b strings.Builder
	for _, grown := range []float64{0, 0.25, 0.6, 1} {
		for _, shut := range []bool{false, true} {
			fmt.Fprintf(&b, "--- grown %v, shut %v\n%s", grown, shut, poseText(logoCells(grown, shut)))
			dots, _ := poseParts(logoCells(grown, shut))
			if got := strings.Join(drawLogo(grown, shut), "\n") + "\n"; got != dots {
				t.Fatalf("grown %v, shut %v: drawLogo draws\n%s", grown, shut, got)
			}
		}
	}
	golden(t, "logo-grows", b.String())
	if got := strings.Join(drawLogo(1, false), "\n"); got != strings.Join(logoLines, "\n") {
		t.Fatalf("the logo is the snake fully grown with the jaws open:\n%s", got)
	}
}

// A snake that is uncurled, turned and scaled lies on a wider ring, as the
// setup shows it.
func TestGoldenLogoPoses(t *testing.T) {
	var b strings.Builder
	for _, p := range []pose{
		{curl: bodyArc * 0.75, scale: 1.4, x: 44, y: 40},
		{curl: bodyArc * 0.9, turn: 1.2, scale: 0.8, x: 30, y: 30},
		{curl: bodyArc, turn: math.Pi, scale: 1, x: ringX, y: ringY},
	} {
		fmt.Fprintf(&b, "--- %+v\n%s", p, poseText(poseCells(p, 1, false, 44, 22)))
	}
	golden(t, "logo-poses", b.String())
}

// The body grows fast at first and slows down until it is grown.
func TestSplashGrownEasesOut(t *testing.T) {
	for _, c := range []struct {
		at   time.Duration
		want float64
	}{
		{0, 0},
		{growDuration / 4, 0.4375},
		{growDuration / 2, 0.75},
		{growDuration, 1},
		{2 * growDuration, 1},
	} {
		if got := splashGrown(c.at); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("grown at %v: %v, want %v", c.at, got, c.want)
		}
	}
}

// The edges of the head belong to it: the skull up to the jaw hinge, the shut
// jaws, the open upper jaw and the lower one that swings inward.
func TestHeadIncludesItsEdges(t *testing.T) {
	center, width := 0.8, 6.6 // at the jaw hinge the jaws are widest
	for _, c := range []struct {
		f, d float64
		shut bool
		want bool
	}{
		{0, -3.2, true, true},                       // |d-center| is 4, the width of the skull at the neck
		{jawHinge, -5, true, false},                 // the jaws begin at the hinge, narrower than the skull
		{jawHinge, center + width, true, true},      // the top of the shut jaws
		{jawHinge, center - width*0.85, true, true}, // their bottom
		{jawEnd, -0.2, true, true},                  // at the end of the lower jaw the upper one goes down to -0.2
		{jawEnd, -1, true, false},
		{jawHinge, 1.3, false, true}, // the open upper jaw starts at 1.3
		{jawHinge, center + width, false, true},
		{jawHinge, -1.3, false, true}, // the open lower jaw ends at -1.3
		{jawHinge, center - width*0.85, false, true},
		{jawEnd, -6, false, false}, // the open lower jaw ends at jawEnd
	} {
		if got := inHead(c.f, c.d, c.shut); got != c.want {
			t.Errorf("inHead(%v, %v, shut %v) = %v, want %v", c.f, c.d, c.shut, got, c.want)
		}
	}
}
