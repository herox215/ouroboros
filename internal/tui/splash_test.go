package tui

import (
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

var updateGolden = flag.Bool("update", false, "write the golden files in testdata anew")

// golden compares got with the file testdata/name.golden. With -update it
// writes the file instead: go test ./internal/tui -run Golden -update.
// The frames are kept with their colors; cat shows them in a terminal.
func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (write it with -update)", err)
	}
	if got != string(want) {
		t.Errorf("%s differs from what the test draws now:\n%s", path, ansi.Strip(got))
	}
}

// The times the frames of the golden splash are taken at: every part of the
// timeline, and the moments where one part ends and the next one starts.
var splashMoments = []time.Duration{
	0,
	fadeIn / 2,
	fadeIn,
	titleAt - scrambleFor,
	titleAt + letterGap/2,
	growDuration,
	biteAt - time.Millisecond,
	biteAt,
	biteAt + flashDuration/2,
	1700 * time.Millisecond,
	taglineAt,
	taglineAt + typeSpeed*5,
	biteAt + sparkLife - time.Millisecond,
	biteAt + sparkLife,
	shineAt + shineDuration/2,
	2900 * time.Millisecond,
	splashDuration - fadeOut/2,
	splashDuration,
}

// The splash is drawn the same as it was looked at, frame by frame: on a
// screen just large enough for all of it, on one too narrow for the big
// title, on one too low for the hint, and on one smaller than the logo,
// where the sparks and the snake run over the edges.
func TestGoldenSplashFrames(t *testing.T) {
	for _, s := range []struct {
		name string
		w, h int
	}{
		{"splash-50x24", 50, 24},
		{"splash-40x24", 40, 24},
		{"splash-70x21", 70, 21},
		{"splash-20x8", 20, 8},
	} {
		var b strings.Builder
		for _, at := range splashMoments {
			fmt.Fprintf(&b, "--- %v\n%s\n", at, drawSplash(s.w, s.h, at, at, false))
		}
		golden(t, s.name, b.String())
	}
}

// The stars are where the sky time puts them, whatever the time of the splash.
func TestGoldenSplashSky(t *testing.T) {
	golden(t, "splash-sky", drawSplash(60, 26, biteAt, 7*time.Second, false))
}

// The big title needs a screen 48 columns wide and 24 rows high; on a
// smaller one the title is in plain letters.
func TestSplashLayoutFitsTheScreen(t *testing.T) {
	for _, c := range []struct {
		w, h                     int
		big                      bool
		letter, left, top, block int
	}{
		{48, 24, true, bigLetter, 9, 1, 22},
		{47, 24, false, 1, 8, 2, 20},
		{48, 23, false, 1, 9, 1, 20},
		{100, 30, true, bigLetter, 35, 4, 22},
		{20, 8, false, 1, -5, -6, 20},
	} {
		title, letter, left, top, block := splashLayout(c.w, c.h)
		if big := len(title) == len(bigTitle); big != c.big || letter != c.letter || left != c.left || top != c.top || block != c.block {
			t.Errorf("%dx%d: big %v, letter %d, left %d, top %d, block %d; want %v, %d, %d, %d, %d",
				c.w, c.h, big, letter, left, top, block, c.big, c.letter, c.left, c.top, c.block)
		}
	}
}

// The hint to skip is on the last row when there is a free row between it
// and the tagline.
func TestSplashHintNeedsARowOfItsOwn(t *testing.T) {
	for h, want := range map[int]bool{21: false, 22: true} {
		lines := strings.Split(ansi.Strip(drawSplash(40, h, fadeIn, fadeIn, false)), "\n")
		if got := strings.Contains(lines[h-1], skipHint); got != want {
			t.Errorf("40x%d: hint shown %v, want %v:\n%s", h, got, want, strings.Join(lines, "\n"))
		}
	}
}

// A canvas keeps what is drawn inside it, up to its first and last cells,
// and drops what lies outside.
func TestCanvasDrawsUpToItsEdges(t *testing.T) {
	c := &canvas{w: 3, h: 2, cells: make([]cell, 6), alpha: 1}
	for _, p := range [][2]int{{0, 0}, {2, 1}, {-1, 0}, {3, 0}, {0, -1}, {0, 2}} {
		c.set(p[0], p[1], 'x', darkPalette.star, false)
	}
	if got := ansi.Strip(c.String()); got != "x  \n  x" {
		t.Fatalf("canvas:\n%q", got)
	}
}

// A star at the very top of its twinkle shows the brightest glyph of its
// layer. The sine is exactly 1 for some nanoseconds around its top, so the
// test looks for a star of the near layer that is there then.
func TestStarAtTheTopOfItsTwinkle(t *testing.T) {
	const w, h, speed = 60, 20, 4 // the near layer drifts 4 cells per second
	for shift := 0; shift < 400; shift++ {
		from := float64(shift) / speed // the stars lie still until the next shift
		for y := range h {
			for x := range w {
				n := hash(x+shift, y, 1)
				if n%1000 >= 14 {
					continue
				}
				a, b := 2+float64(n>>10%30)/10, float64(n>>20%628)/100
				top := math.Ceil((from*a+b-math.Pi/2)/(2*math.Pi))*2*math.Pi + math.Pi/2
				s := (top - b) / a
				if s+1e-6 >= float64(shift+1)/speed {
					continue
				}
				for ns := int64(s*1e9) - 50; ns <= int64(s*1e9)+50; ns++ {
					c := &canvas{w: w, h: h, cells: make([]cell, w*h), alpha: 1}
					drawStars(c, darkPalette, time.Duration(ns))
					if r := c.cells[y*w+x].r; r != '*' {
						t.Fatalf("the star at %d,%d at %dns: %q", x, y, ns, r)
					}
				}
				return
			}
		}
	}
	t.Fatal("no star at the top of its twinkle")
}
