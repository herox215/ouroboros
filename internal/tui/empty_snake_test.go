package tui

import (
	"github.com/charmbracelet/x/ansi"
	"math"
	"strings"
	"testing"
	"time"
)

func dragonFrame(at float64) *canvas {
	c := &canvas{w: 120, h: 40, cells: make([]cell, 120*40), back: darkPalette.back}
	drawNameSnake(c, darkPalette, 32, 3, at, 1)
	return c
}

func TestDragonOrganicMotionAndCycle(t *testing.T) {
	f := newDragonFlight(120, 40, 32, 3)
	// The body moves even at a fixed position along its route: motion is not
	// merely a rigid sprite translated along a line.
	x, y, _, _ := f.point(f.entry+f.orbit*0.2, 1)
	a, b, _, _ := f.point(f.entry+f.orbit*0.2, 1.7)
	if math.Hypot(x-a, 2*(y-b)) < 0.2 {
		t.Fatal("spine does not undulate")
	}
	// All transitions are continuous; no ninety-degree positional jumps.
	for _, s := range []float64{f.entry, f.entry + f.orbit} {
		x, y, _, _ := f.point(s-0.01, 2)
		a, b, _, _ := f.point(s+0.01, 2)
		if math.Hypot(x-a, 2*(y-b)) > 0.1 {
			t.Fatal("route jumps at a turn")
		}
	}
	at := (f.entry + 8) / f.speed
	c := dragonFrame(at)
	if c.String() != dragonFrame(at+f.duration()+snakeRest).String() {
		t.Fatal("cycle does not repeat")
	}
	for _, at := range []float64{f.duration() + 0.1, f.duration() + snakeRest - 0.1} {
		for _, cl := range dragonFrame(at).cells {
			if cl.r != 0 {
				t.Fatal("dragon visible during rest")
			}
		}
	}
	faded := &canvas{w: 120, h: 40, cells: make([]cell, 120*40), back: darkPalette.back}
	drawNameSnake(faded, darkPalette, 32, 3, at, 0)
	for _, cl := range faded.cells {
		if cl.r != 0 && cl.fg != faded.back {
			t.Fatal("dragon does not fade")
		}
	}
}

func TestDragonReadableHead(t *testing.T) {
	f := newDragonFlight(120, 40, 32, 3)
	c := dragonFrame((f.entry + 8) / f.speed)
	var plain strings.Builder
	for y := 0; y < c.h; y++ {
		for x := 0; x < c.w; x++ {
			r := c.cells[y*c.w+x].r
			if r == 0 {
				r = ' '
			}
			plain.WriteRune(r)
		}
		plain.WriteByte(10)
	}
	got := plain.String()
	if !strings.Contains(got, "(@)") || !strings.Contains(got, "V  V") || !strings.Contains(got, "/|") {
		t.Fatalf("head is not legible:\n%s", got)
	}
	t.Logf("rendered dragon:\n%s", got)
}

func TestDragonKeepsNameTaglineAndViewport(t *testing.T) {
	for _, light := range []bool{false, true} {
		for _, size := range [][2]int{{1, 1}, {32, 3}, {46, 10}, {80, 24}, {120, 40}} {
			w, h := size[0], size[1]
			for _, at := range []time.Duration{0, time.Second, 2 * time.Second, 4 * time.Second, 7 * time.Second, 12 * time.Second, 123 * time.Hour} {
				got := ansi.Strip(drawNameAnimation("Luna", w, h, at, light, nameSnake, 1, "stay curious"))
				lines := strings.Split(got, "\n")
				if len(lines) != h {
					t.Fatal("wrong height")
				}
				for _, line := range lines {
					if ansi.StringWidth(line) != w {
						t.Fatal("wrong width")
					}
				}
				art := fitName("Luna", w, h)
				if art == nil {
					continue
				}
				left, top := (w-len([]rune(art[0])))/2, (h-len(art))/2
				for y, row := range art {
					for x, r := range []rune(row) {
						if r != ' ' && []rune(lines[top+y])[left+x] != '█' {
							t.Fatal("dragon overwrote name")
						}
					}
				}
				if _, ok := emptyTaglineRow(w, h, len(art), "stay curious"); ok && !strings.Contains(got, "stay curious") {
					t.Fatal("dragon overwrote tagline")
				}
			}
		}
	}
}
