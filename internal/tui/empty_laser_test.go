package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func TestLaserPathFollowsLettersAndCoversInk(t *testing.T) {
	art := fitName("Luna", 80, 16)
	path := laserPath(art)
	seen := map[[2]int]bool{}
	last := -1
	spans := letterSpans(art)
	for _, point := range path {
		if seen[point] || []rune(art[point[1]])[point[0]] == ' ' {
			t.Fatalf("invalid cut at %v", point)
		}
		seen[point] = true
		for n, span := range spans {
			if point[0] >= span[0] && point[0] <= span[1] {
				if n < last {
					t.Fatal("laser returned to a completed letter")
				}
				last = n
			}
		}
	}
	for y, row := range art {
		for x, r := range []rune(row) {
			if r != ' ' && !seen[[2]int{x, y}] {
				t.Fatalf("uncut ink at %d,%d", x, y)
			}
		}
	}
}

func TestLaserRevealsNameAndRestsWithoutBeamOrParticles(t *testing.T) {
	for _, light := range []bool{false, true} {
		frame := func(at float64) string {
			return drawNameAnimation("Luna", 80, 16, time.Duration(at*float64(time.Second)), light, nameLaser, 1)
		}
		path := laserPath(fitName("Luna", 80, 16))
		middle := ansi.Strip(frame(float64(len(path)/2) * laserStep))
		doneAt := float64(len(path))*laserStep + 1
		done := ansi.Strip(frame(doneAt))
		cuts := func(at float64) int {
			p := colors(light)
			c := &canvas{w: 80, h: 16, cells: make([]cell, 80*16), back: p.back}
			art := fitName("Luna", 80, 16)
			drawNameLaser(c, p, art, (80-len([]rune(art[0])))/2, (16-len(art))/2, at, 1)
			n := 0
			for _, cl := range c.cells {
				if cl.bg != (cell{}).bg {
					n++
				}
			}
			return n
		}
		if cuts(0) != 1 || cuts(float64(len(path)/2)*laserStep) <= 1 || cuts(float64(len(path)/2)*laserStep) >= len(path) {
			t.Fatal("name was not revealed progressively")
		}
		if done != ansi.Strip(emptyChat("Luna", 80, 16)) {
			t.Fatalf("finished cut does not match the name:\n%s", done)
		}
		if !strings.Contains(middle, "▀") || !strings.ContainsAny(middle, "✦•·") {
			t.Fatal("cut has no beam or sparks")
		}
		if again := frame(0.5); frame(0.5) != again {
			t.Fatal("rendering depends on frame history")
		}
		for _, later := range []float64{doneAt + 3, doneAt + 30, doneAt + 3600} {
			if ansi.Strip(frame(later)) != done {
				t.Fatal("finished laser restarted or left particles behind")
			}
		}
	}
}

func TestLaserFitsSmallWindowsAndKeepsTagline(t *testing.T) {
	for _, size := range [][2]int{{1, 1}, {32, 3}, {46, 10}, {80, 24}} {
		got := ansi.Strip(drawNameAnimation("Luna", size[0], size[1], time.Second, false, nameLaser, 1, "stay curious"))
		lines := strings.Split(got, "\n")
		if len(lines) != size[1] {
			t.Fatalf("height %d: %d rows", size[1], len(lines))
		}
		for _, line := range lines {
			if ansi.StringWidth(line) > size[0] {
				t.Fatal("laser overflowed the viewport")
			}
		}
		if size[1] >= 10 && !strings.Contains(got, "stay curious") {
			t.Fatal("laser lost the selected tagline")
		}
	}
	if got := drawNameAnimation("日本語", 30, 10, time.Second, false, nameLaser, 1); got != emptyChat("日本語", 30, 10) {
		t.Fatal("unsupported font should retain the plain name")
	}
}

func TestLaserSourcesChangeDirectionAndStayOffScreen(t *testing.T) {
	seen := map[[2]float64]bool{}
	for letter := 0; letter < 20; letter++ {
		x, y := laserSource(80, 16, letter)
		if x >= 0 && x < 80 && y >= 0 && y < 32 {
			t.Fatal("source is inside the viewport")
		}
		if seen[[2]float64{x, y}] {
			t.Fatal("letters share a source")
		}
		seen[[2]float64{x, y}] = true
		c := &canvas{w: 80, h: 16, cells: make([]cell, 80*16), back: darkPalette.back}
		drawLaserRay(c, darkPalette, x, y, 40.5, 16, 1)
		pixels, shaded := 0, 0
		for _, cl := range c.cells {
			if cl.r != 0 {
				pixels++
				if cl.fg != cl.bg {
					shaded++
				}
				if cl.r != '▀' && cl.r != '▄' && cl.r != '█' {
					t.Fatal("ray is not rendered as half pixels")
				}
			}
		}
		if pixels < 8 || shaded == 0 {
			t.Fatal("beam did not reach from the edge to the cutting head")
		}
	}
}

func TestLaserRayUsesMutedYellowWithoutPaletteGradient(t *testing.T) {
	for _, light := range []bool{false, true} {
		p := colors(light)
		c := &canvas{w: 8, h: 4, cells: make([]cell, 32), back: p.back}
		drawLaserRay(c, p, -8, 0.5, 7.5, 0.5, 1)
		want := mix(p.back, rgb(0xe6d477), 0.65)
		if c.cells[0].fg != want {
			t.Fatalf("beam core is not muted yellow: got %v, want %v", c.cells[0].fg, want)
		}
		other := &canvas{w: 8, h: 4, cells: make([]cell, 32), back: p.back}
		p.body, p.glow = nil, rgb(0xff0000)
		drawLaserRay(other, p, -8, 0.5, 7.5, 0.5, 1)
		for i, cl := range c.cells {
			if cl != other.cells[i] {
				t.Fatal("beam color depends on the animation palette")
			}
		}
	}
}

func TestLaserRayHasHardEdges(t *testing.T) {
	for _, light := range []bool{false, true} {
		p := colors(light)
		c := &canvas{w: 40, h: 12, cells: make([]cell, 480), back: p.back}
		drawLaserRay(c, p, -8, -5, 35.5, 20.5, 1)
		ink := mix(p.back, rgb(0xe6d477), 0.65)
		painted := 0
		for _, cl := range c.cells {
			if cl.r == 0 {
				continue
			}
			painted++
			if cl.fg != ink || cl.bg != (cell{}).bg {
				t.Fatal("beam has a soft edge, halo or background outline")
			}
		}
		if painted == 0 {
			t.Fatal("beam is missing")
		}
	}
}
