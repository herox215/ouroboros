package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func TestRainFallsAndSplashesDeterministically(t *testing.T) {
	frame := func(at, shown float64) *canvas {
		c := &canvas{w: 80, h: 24, cells: make([]cell, 80*24), back: darkPalette.back}
		drawNameRain(c, at, shown)
		return c
	}
	a, b := frame(1, 1), frame(1.2, 1)
	falling, splashes := 0, 0
	for x := 0; x < a.w; x++ {
		for y := 0; y < a.h-4; y++ {
			if a.cells[y*a.w+x].r != '│' {
				continue
			}
			for dy := 1; dy <= 3; dy++ {
				if b.cells[(y+dy)*b.w+x].r == '│' {
					falling++
				}
			}
		}
	}
	for step := 0; step < 40; step++ {
		c := frame(float64(step)/10, 1)
		for _, cl := range c.cells[(c.h-1)*c.w:] {
			if cl.r == '·' {
				splashes++
			}
		}
	}
	if falling == 0 || splashes == 0 {
		t.Fatalf("no falling drops (%d) or splashes (%d)", falling, splashes)
	}
	if a.String() != frame(1, 1).String() {
		t.Fatal("rain depends on rendering history")
	}
	faded := frame(1, 0)
	for _, cl := range faded.cells {
		if cl.r != 0 && cl.fg != faded.back {
			t.Fatal("rain did not fade with the name")
		}
	}
}

func TestRainKeepsNameTaglineAndViewport(t *testing.T) {
	for _, light := range []bool{false, true} {
		for _, size := range [][2]int{{1, 1}, {32, 3}, {46, 10}, {80, 24}} {
			w, h := size[0], size[1]
			for _, at := range []time.Duration{0, time.Second, 123 * time.Hour} {
				got := ansi.Strip(drawNameAnimation("Luna", w, h, at, light, nameRain, 1, "stay curious"))
				lines := strings.Split(got, "\n")
				if len(lines) != h {
					t.Fatalf("got %d rows, want %d", len(lines), h)
				}
				for _, line := range lines {
					if ansi.StringWidth(line) != w {
						t.Fatal("rain exceeded the viewport")
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
							t.Fatal("rain overwrote the name")
						}
					}
				}
				if _, ok := emptyTaglineRow(w, h, len(art), "stay curious"); ok && !strings.Contains(got, "stay curious") {
					t.Fatal("rain overwrote the tagline")
				}
			}
		}
	}
}

// A canvas without room gets no rain, and is left as it was.
func TestRainWithoutRoom(t *testing.T) {
	for _, size := range [][2]int{{0, 4}, {4, 0}} {
		c := &canvas{w: size[0], h: size[1], back: darkPalette.back}
		drawNameRain(c, 1, 1)
		if len(c.cells) != 0 || c.alpha != 0 {
			t.Fatalf("%v: %d cells, alpha %v", size, len(c.cells), c.alpha)
		}
	}
}
