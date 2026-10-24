package tui

import (
	"fmt"
	"image/color"
	"maps"
	"math"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// The tests see the name shine unless they pick a look themselves: a glitch
// tears the name they look for. And they see a chat that has just begun
// without the name fading out behind it, unless that is what they test.
func init() {
	pickNameLook = func(int) int { return int(nameShine) }
	nameFade = 0
}

var sgrParams = regexp.MustCompile(`^\x1b\[([0-9;]*)m`)

// colorsOf are the colors a frame shows the name in, as the terminal is
// told them, by the column and the row of every cell of it.
func colorsOf(frame string) map[[2]int]string {
	colors := map[[2]int]string{}
	for y, line := range strings.Split(frame, "\n") {
		fg := ""
		for x := 0; len(line) > 0; {
			if found := sgrParams.FindStringSubmatch(line); found != nil {
				fg, line = foreground(found[1]), line[len(found[0]):]
				continue
			}
			r, size := utf8.DecodeRuneInString(line)
			if r != ' ' {
				colors[[2]int{x, y}] = fg
			}
			x, line = x+1, line[size:]
		}
	}
	return colors
}

// foreground is the parameters that color a cell, without the background a
// block of the name fills its cell with.
func foreground(params string) string {
	if i := strings.Index(params, "48;2;"); i >= 0 {
		return strings.TrimSuffix(params[:i], ";")
	}
	return params
}

// told is c as the terminal is told it.
func told(c color.RGBA) string { return fmt.Sprintf("38;2;%d;%d;%d", c.R, c.G, c.B) }

// alike says whether two colors as the terminal is told them are the same,
// but for a step of rounding in a channel.
func alike(a, b string) bool {
	var ar, ag, ab, br, bg, bb int
	if n, _ := fmt.Sscanf(a, "38;2;%d;%d;%d", &ar, &ag, &ab); n != 3 {
		return false
	}
	if n, _ := fmt.Sscanf(b, "38;2;%d;%d;%d", &br, &bg, &bb); n != 3 {
		return false
	}
	return max(ar-br, br-ar, ag-bg, bg-ag, ab-bb, bb-ab) <= 1
}

// seconds is the time s seconds after the animation began.
func seconds(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }

// The animation leaves every cell of the name where an empty chat has it at
// rest, frame by frame: in both sizes of the font, on a dark and on a light
// background, and in plain letters where the artwork does not fit. A block
// is one column of the artwork, though it is three bytes of its row.
func TestNameAnimationLeavesTheNameInPlace(t *testing.T) {
	for _, c := range []struct {
		name string
		w, h int
	}{
		{"Luna", 80, 21}, {"Luna", 38, 6}, {"Luna", 27, 7}, {"Luna", 19, 3}, {"Luna", 12, 4}, {"Luna", 1, 1},
		{"herox215", 84, 8}, {"herox215", 44, 7}, {"René", 40, 8},
	} {
		still := ansi.Strip(emptyChat(c.name, c.w, c.h))
		for _, light := range []bool{false, true} {
			for _, at := range []float64{0, 0.08, 0.5, 1.7, 60} {
				if got := ansi.Strip(drawNameAnimation(c.name, c.w, c.h, seconds(at), light, nameShine, 1)); got != still {
					t.Errorf("%s in %dx%d after %v seconds:\n%s\nand at rest the name is\n%s", c.name, c.w, c.h, at, got, still)
				}
			}
		}
	}
}

// The gradient of the splash colors the name column by column, each column
// in one color from top to bottom while no shine is on it, as matte as the
// name is: it begins with the first color of the gradient at the left, on a
// light background with the one for that. The colors flow, and come round
// every 25 ninths of a second. For Luna at full size a shine sets out every
// 3.85 seconds, and has left the name after two of them.
func TestNameAnimationFlowsThroughTheColumns(t *testing.T) {
	columns := func(at time.Duration, light bool) map[int]string {
		t.Helper()
		columns := map[int]string{}
		for cell, fg := range colorsOf(drawNameAnimation("Luna", 46, 10, at, light, nameShine, 1)) {
			if cell[1] >= 8 { // below the six rows of the name: the tagline
				continue
			}
			if other, ok := columns[cell[0]]; ok && other != fg {
				t.Fatalf("the column %d has the colors %s and %s", cell[0], other, fg)
			}
			columns[cell[0]] = fg
		}
		return columns
	}
	for light, p := range map[bool]palette{false: darkPalette, true: lightPalette} {
		if first, want := columns(0, light)[4], told(mix(p.back, p.body[0], nameMatte)); first != want {
			t.Errorf("light %v: the name begins with %s at the left, want %s", light, first, want)
		}
	}
	now := columns(seconds(2), false)
	colors := map[string]bool{}
	for _, fg := range now {
		colors[fg] = true
	}
	if len(now) != 32 || len(colors) != len(now) { // every column of the four letters
		t.Fatalf("%d colors in %d columns", len(colors), len(now))
	}
	later, rounds := columns(seconds(2+25.0/18), false), columns(seconds(2+50.0/9), false)
	for x, fg := range now {
		if later[x] == fg || rounds[x] != fg {
			t.Fatalf("the column %d is %s, then %s, and %s after two rounds", x, fg, later[x], rounds[x])
		}
	}
}

// The shine is a streak of light on the name, leaning to the right: where
// it is the name is nearly white, on the top row further right than on the
// bottom row, and it moves to the right.
func TestNameAnimationShineCrossesTheName(t *testing.T) {
	// brightest is the column of the brightest cell of the row y, and how bright it is.
	brightest := func(at float64, y int) (column, sum int) {
		for cell, fg := range colorsOf(drawNameAnimation("Luna", 46, 10, seconds(at), false, nameShine, 1)) {
			var r, g, b int
			fmt.Sscanf(fg, "38;2;%d;%d;%d", &r, &g, &b)
			if cell[1] == y && r+g+b > sum {
				column, sum = cell[0], r+g+b
			}
		}
		return column, sum
	}
	top, light := brightest(1, 2)
	bottom, _ := brightest(1, 7)
	if _, rest := brightest(2, 2); light < 600 || rest > 500 || top <= bottom {
		t.Fatalf("the shine: %d bright at the column %d, below at %d; at rest %d", light, top, bottom, rest)
	}
	if on, _ := brightest(1.2, 2); on <= top {
		t.Fatalf("the shine went from the column %d to %d", top, on)
	}
}

// The beam is a round spot of light that wanders over the name like a
// torch: where it stands the letters are nearly white, the light falls off
// around it in a circle, and it moves to the right while the name itself
// stays where it was.
func TestNameAnimationBeamWandersOverTheName(t *testing.T) {
	still := ansi.Strip(emptyChat("Luna", 46, 10))
	for _, at := range []float64{0, 1, 2.5, 4} {
		if got := ansi.Strip(drawNameAnimation("Luna", 46, 10, seconds(at), false, nameBeam, 1)); got != still {
			t.Fatalf("after %v seconds the beam moved the name:\n%s", at, got)
		}
	}
	// brightest is the column of the brightest cell of the whole frame, and
	// how bright it is.
	brightest := func(at float64) (column, sum int) {
		for cell, fg := range colorsOf(drawNameAnimation("Luna", 46, 10, seconds(at), false, nameBeam, 1)) {
			var r, g, b int
			fmt.Sscanf(fg, "38;2;%d;%d;%d", &r, &g, &b)
			if r+g+b > sum {
				column, sum = cell[0], r+g+b
			}
		}
		return column, sum
	}
	peak, light := brightest(2)
	if light < 600 {
		t.Fatalf("the beam stands at %d bright, want it lit up", light)
	}
	// The light falls off in a circle: far from the spot, in its row and on
	// the rows above and below it, the name rests in the dark.
	left, right := (46-32)/2, (46-32)/2+32 // the columns of the four letters
	dark := 0
	for cell, fg := range colorsOf(drawNameAnimation("Luna", 46, 10, seconds(2), false, nameBeam, 1)) {
		var r, g, b int
		fmt.Sscanf(fg, "38;2;%d;%d;%d", &r, &g, &b)
		if cell[0] < left || cell[0] >= right || cell[0] > peak-6 && cell[0] < peak+6 {
			continue
		}
		if r+g+b > dark {
			dark = r + g + b
		}
	}
	if dark > 500 {
		t.Fatalf("the beam lights the letters far from its spot to %d, want them in the dark", dark)
	}
	if on, _ := brightest(2.3); on <= peak {
		t.Fatalf("the beam went from the column %d to %d", peak, on)
	}
	// Over a name of 34 columns and 6 rows the spot sets out 10 columns
	// before it, crosses it at 18 columns a second, out to 10 columns behind
	// it, and rests 2.2 seconds: it comes round again after 5.2 seconds.
	spot := func(at float64) int {
		best, top := 0, -1.0
		for x := range 34 {
			if lit := nameBeam.lit(x, 2, 34, 6, at); lit > top {
				best, top = x, lit
			}
		}
		return best
	}
	for _, tt := range []struct {
		at   float64
		want int
	}{{1, 8}, {1.5, 17}, {6.2, 8}, {6.7, 17}} {
		if got := spot(tt.at); got != tt.want {
			t.Errorf("after %v seconds the spot is at the column %d, want %d", tt.at, got, tt.want)
		}
	}
}

// The spot of the beam is round, in cells that are twice as high as wide:
// two columns from its middle are as dark as one row. On a name of 9 rows it
// is 4.8 columns wide, sets out 15 columns before the name in its middle
// row, and sways 2.4 rows down and up again every 4.2 seconds.
func TestNameAnimationBeamSpot(t *testing.T) {
	lit := func(x, y int, at float64) float64 { return nameBeam.lit(x, y, 40, 9, at) }
	if got := lit(-15, 4, 0); got != 1 {
		t.Fatalf("the middle of the spot is %f bright", got)
	}
	for _, c := range [][2]int{{-13, 4}, {-17, 4}, {-15, 5}, {-15, 3}} {
		if got, want := lit(c[0], c[1], 0), math.Exp(-(2/4.8)*(2/4.8)); math.Abs(got-want) > 1e-12 {
			t.Errorf("at %v the spot is %f bright, want %f", c, got, want)
		}
	}
	if got, want := lit(-12, 4, 0), math.Exp(-(3/4.8)*(3/4.8)); math.Abs(got-want) > 1e-12 {
		t.Errorf("three columns from its middle the spot is %f bright, want %f", got, want)
	}
	// row is the brightest row of the column x at the time at.
	row := func(x int, at float64) int {
		best, top := 0, -1.0
		for y := range 9 {
			if l := lit(x, y, at); l > top {
				best, top = y, l
			}
		}
		return best
	}
	if down, up := row(13, 1.05), row(70, 3.15); down != 6 || up != 2 {
		t.Errorf("swayed down to the row %d, up to %d; want 6 and 2", down, up)
	}
}

// The wave rolls bands of light through the name, leaning to the right:
// the brightest column of a row moves to the right with the time, the top
// row ahead of the bottom row, and the name stays where it was.
func TestNameAnimationWaveRollsThroughTheName(t *testing.T) {
	// peak is the column of the row y where the wave is brightest, over one
	// crest of it, which a length of the name holds more than one of.
	peak := func(at float64, y int) int {
		best, top := 0, -1.0
		for x := 0; float64(x) < 46/waveLength; x++ {
			if lit := nameWave.lit(x, y, 46, 6, at); lit > top {
				best, top = x, lit
			}
		}
		return best
	}
	still := ansi.Strip(emptyChat("Luna", 46, 10))
	for _, at := range []float64{0, 0.4, 1.3, 3.1} {
		if got := ansi.Strip(drawNameAnimation("Luna", 46, 10, seconds(at), false, nameWave, 1)); got != still {
			t.Fatalf("after %v seconds the wave moved the name:\n%s", at, got)
		}
	}
	first, later := peak(0, 2), peak(0.6, 2)
	top, bottom := peak(0, 0), peak(0, 5)
	if lit := nameWave.lit(3, 2, 46, 6, 0); lit < 0 || lit > waveHeight {
		t.Fatalf("the wave is %f bright, from 0 to %f", lit, waveHeight)
	}
	if later <= first || top <= bottom {
		t.Fatalf("the wave: a crest at the column %d, later at %d; the top row at %d, below at %d", first, later, top, bottom)
	}
	// A name of 14 columns holds 1.4 crests: one every 10 columns. Of six
	// rows, the top one is 5 × 1.2 columns ahead of the bottom one, where
	// the wave starts at half its height at first.
	for x := range 4 {
		if a, b := nameWave.lit(x, 5, 14, 6, 0.3), nameWave.lit(x+10, 5, 14, 6, 0.3); math.Abs(a-b) > 1e-9 {
			t.Fatalf("the columns %d and %d are %f and %f bright, a crest apart", x, x+10, a, b)
		}
		if a, b := nameWave.lit(x, 5, 14, 6, 0.3), nameWave.lit(x+6, 0, 14, 6, 0.3); math.Abs(a-b) > 1e-9 {
			t.Fatalf("the column %d of the bottom row is %f bright, %d of the top row %f", x, a, x+6, b)
		}
	}
	if lit := nameWave.lit(0, 5, 14, 6, 0); math.Abs(lit-waveHeight/2) > 1e-9 {
		t.Fatalf("the wave starts %f bright", lit)
	}
}

// The float bobs the letters of the name up and down, a row at most: a
// wave of the bob travels through the letters, and every letter moves
// whole, without tearing.
func TestNameAnimationFloatBobsTheLetters(t *testing.T) {
	art := nameArt("Luna", nameScale)
	_, h := len([]rune(art[0])), len(art)
	top := (10 - h) / 2
	spans := letterSpans(art)
	frame := func(at float64) []string {
		return strings.Split(ansi.Strip(drawNameAnimation("Luna", 46, 10, seconds(at), false, nameFloat, 1)), "\n")
	}
	// down is the letter whose bottom row shows below the name, -1 where
	// none does.
	down := func(at float64) int {
		lines := frame(at)
		for i, span := range spans {
			for _, r := range []rune(lines[top+h])[span[0] : span[1]+1] {
				if r != ' ' {
					return i
				}
			}
		}
		return -1
	}
	// whole counts the cells of every letter: a letter that moves whole
	// shows all of them, wherever it bobs.
	whole := func(at float64) map[int]int {
		counts := map[int]int{}
		for _, line := range frame(at) {
			for i, span := range spans {
				for _, r := range []rune(line)[span[0] : span[1]+1] {
					if r != ' ' {
						counts[i]++
					}
				}
			}
		}
		return counts
	}
	atRest := whole(0)
	for _, at := range []float64{0, 0.4, 0.7, 1.4, 2.1} {
		if got := whole(at); !maps.Equal(got, atRest) {
			t.Fatalf("after %v seconds the float tears a letter: %v, want %v", at, got, atRest)
		}
	}
	// Every column of a letter, its last one too, bobs as far as the letter.
	first := func(lines []string, x int) int {
		for y, line := range lines {
			if r := []rune(line); x < len(r) && r[x] != ' ' {
				return y
			}
		}
		return -1
	}
	rest := frame(0)
	for _, at := range []float64{0.4, 0.7, 1.4, 2.1} {
		lines := frame(at)
		for i, span := range spans {
			left := (46 - len([]rune(art[0]))) / 2
			bob := first(lines, left+span[0]) - first(rest, left+span[0])
			for x := span[0]; x <= span[1]; x++ {
				if got := first(lines, left+x) - first(rest, left+x); got != bob {
					t.Fatalf("after %v seconds the column %d of the letter %d bobs %d rows, the letter %d", at, x, i, got, bob)
				}
			}
		}
	}
	if down(0) != 3 || down(floatPeriod/4) != 0 || down(floatPeriod/2) != 1 {
		t.Fatalf("the wave of the bob: at 0 below %d, at a quarter %d, at half %d", down(0), down(floatPeriod/4), down(floatPeriod/2))
	}
}

// The flare lights a letter of the name up like a lamp: the letter glows up
// and dims again in its time, and the light goes round the name, a letter
// after the other. The name stays where it was.
func TestNameAnimationFlareLightsTheLetters(t *testing.T) {
	art := nameArt("Luna", nameScale)
	_, h := len([]rune(art[0])), len(art)
	top := (10 - h) / 2
	spans := letterSpans(art)
	// bright sums the brightness of the cells of the letter i at at.
	bright := func(at float64, i int) int {
		sum := 0
		for cell, fg := range colorsOf(drawNameAnimation("Luna", 46, 10, seconds(at), false, nameFlare, 1)) {
			if cell[1] < top || cell[1] >= top+h || cell[0] < spans[i][0] || cell[0] > spans[i][1] {
				continue
			}
			var r, g, b int
			fmt.Sscanf(fg, "38;2;%d;%d;%d", &r, &g, &b)
			sum += r + g + b
		}
		return sum
	}
	still := ansi.Strip(emptyChat("Luna", 46, 10))
	for _, at := range []float64{0, 0.5, 2.3, 4.4} {
		if got := ansi.Strip(drawNameAnimation("Luna", 46, 10, seconds(at), false, nameFlare, 1)); got != still {
			t.Fatalf("after %v seconds the flare moved the name:\n%s", at, got)
		}
	}
	// Every cell shows as lit as the flare says: the letter of its time
	// with the sine of its time, and the columns around it with 0.7 of
	// that, falling off over 3 columns of the font at its size, 6 here.
	w, p := len([]rune(art[0])), colors(false)
	left := (46 - w) / 2
	for _, at := range []float64{0.95, 2.2, 5, 6.1} {
		now := seconds(at).Seconds()
		into := math.Mod(now, flareEvery*float64(len(spans)))
		n := int(into/flareEvery) % len(spans)
		share := math.Sin(math.Pi * (into/flareEvery - float64(n)))
		cells := colorsOf(drawNameAnimation("Luna", 46, 10, seconds(at), false, nameFlare, 1))
		for y, row := range art {
			for x, r := range []rune(row) {
				if r == ' ' {
					continue
				}
				lit := share
				if x < spans[n][0] {
					lit = 0.7 * share * bell(float64(spans[n][0]-x), 6)
				} else if x > spans[n][1] {
					lit = 0.7 * share * bell(float64(x-spans[n][1]), 6)
				}
				fg := mix(p.gradient(float64(x)/float64(w)*0.8-now*0.36), p.glow, shineGlow*lit)
				if got, want := cells[[2]int{left + x, top + y}], told(mix(p.back, fg, nameMatte+(1-nameMatte)*lit)); !alike(got, want) {
					t.Fatalf("after %v seconds the cell %d, %d is %s, want %s: lit %f by the letter %d", at, x, y, got, want, lit, n)
				}
			}
		}
	}
	peak, dim := flareEvery/2, flareEvery*1.5
	if bright(peak, 0) <= bright(dim, 0) {
		t.Fatalf("the letter 0 does not flare up in its time: %d bright at its peak, %d while another letter is the lamp", bright(peak, 0), bright(dim, 0))
	}
	if bright(dim, 1) <= bright(peak, 1) {
		t.Fatalf("the light does not go round the name: the letter 1 is %d bright while the letter 0 flares, %d while it flares itself", bright(peak, 1), bright(dim, 1))
	}
}

// The neon lights all of the name up, and one letter of it, taken at
// random, flickers like a defective tube: the whole letter stutters out and
// burns again together, and stays the letter of the whole visit. Another
// visit, with another tagline, flickers on another letter. The name stays
// where it was.
func TestNameAnimationNeonFlickers(t *testing.T) {
	art := nameArt("Luna", nameScale)
	_, h := len([]rune(art[0])), len(art)
	top := (10 - h) / 2
	spans := letterSpans(art)
	// frame is how bright every cell of the name shows at at, in the visit
	// of a tagline of its own.
	frame := func(at float64, tagline ...string) map[[2]int]int {
		bright := map[[2]int]int{}
		for c, fg := range colorsOf(drawNameAnimation("Luna", 46, 10, seconds(at), false, nameNeon, 1, tagline...)) {
			var r, g, b int
			fmt.Sscanf(fg, "38;2;%d;%d;%d", &r, &g, &b)
			bright[c] = r + g + b
		}
		return bright
	}
	still := ansi.Strip(emptyChat("Luna", 46, 10))
	// sameName says whether every letter of still shows where it was in got:
	// the sparks around them may come and go, the letters may not.
	sameName := func(got string) bool {
		rows, want := strings.Split(ansi.Strip(got), "\n"), strings.Split(still, "\n")
		if len(rows) != len(want) {
			return false
		}
		for y, row := range want {
			gr := []rune(rows[y])
			for x, r := range []rune(row) {
				if r != ' ' && (x >= len(gr) || gr[x] != r) {
					return false
				}
			}
		}
		return true
	}
	for _, at := range []float64{0, 0.5, 2.3, 4.4} {
		if got := drawNameAnimation("Luna", 46, 10, seconds(at), false, nameNeon, 1); !sameName(got) {
			t.Fatalf("after %v seconds the neon moved the name:\n%s", at, got)
		}
	}
	// Sparks fly where the defect stutters out, and nowhere else: dots on
	// cells that stay blank, seen at some of the steps of the flicker.
	sparked := 0
	for at := 0.0; at < 14.4; at += neonStep {
		rows, want := strings.Split(ansi.Strip(drawNameAnimation("Luna", 46, 10, seconds(at), false, nameNeon, 1)), "\n"), strings.Split(still, "\n")
		for y, row := range want {
			gr := []rune(rows[y])
			for x, r := range []rune(row) {
				if r == ' ' && x < len(gr) && gr[x] != ' ' {
					sparked++
				}
			}
		}
	}
	if sparked == 0 {
		t.Fatal("no sparks fly in 14 seconds, want them where the defect stutters out")
	}
	// outs is the letter of the name that is out at at in the visit of a
	// tagline, and -1 where none is: at most one, and when one is, all of
	// its cells are, the whole letter flickering together. The cells of the
	// frame are the columns of the artwork moved by the left the name rests
	// at.
	w := len([]rune(art[0]))
	left := (46 - w) / 2
	outs := func(at float64, tagline ...string) int {
		cells := frame(at, tagline...)
		out := -1
		for i, span := range spans {
			low, high := 1<<30, 0
			for x := span[0]; x <= span[1]; x++ {
				for y := top; y < top+h; y++ {
					if b, ok := cells[[2]int{left + x, y}]; ok {
						if b < low {
							low = b
						}
						if b > high {
							high = b
						}
					}
				}
			}
			if low < 500 {
				if out >= 0 || high > 600 {
					t.Fatalf("after %v seconds the letter %d flickers in parts or with the letter %d: %d to %d bright", at, i, out, low, high)
				}
				out = i
			}
		}
		return out
	}
	// In one visit the defect stays with one letter the whole time.
	flickered := map[int]bool{}
	for at := 0.05; at < 14.4; at += neonStep {
		if out := outs(at); out >= 0 {
			flickered[out] = true
		}
	}
	if len(flickered) > 1 {
		t.Fatalf("the defect leaves its letter in one visit: the letters %v flicker, want one of %d", flickered, len(spans))
	}
	// Another visit picks its letter with another tagline, at random.
	n := len(flickered)
	for _, tagline := range emptyTaglines {
		if out := outs(neonStep, tagline); out >= 0 {
			flickered[out] = true
		}
	}
	if len(flickered) == n {
		t.Fatalf("every visit flickers on the same letter of %d, want it taken at random", len(spans))
	}
}

// The defect flickers in steps: all of a step it burns or is out, and it
// burns about four steps for one it is out. Cells outside of its letter,
// and a name without a defect, burn steadily.
func TestNeonFlickerInSteps(t *testing.T) {
	spans := [][2]int{{0, 3}, {5, 8}}
	out := 0
	for step := range 400 {
		early := neonFlicker(6, 0, 1, spans, (float64(step)+0.1)*neonStep)
		if late := neonFlicker(6, 0, 1, spans, (float64(step)+0.9)*neonStep); late != early {
			t.Fatalf("the step %d flickers within itself: %f, then %f", step, early, late)
		}
		switch early {
		case neonOut:
			out++
		case 1:
		default:
			t.Fatalf("the step %d is %f bright", step, early)
		}
	}
	if out < 40 || out > 160 {
		t.Fatalf("the defect is out %d steps of 400, want about 80", out)
	}
	for at := 0.0; at < 28; at += neonStep {
		if neonFlicker(4, 0, 1, spans, at) != 1 || neonFlicker(9, 0, 1, spans, at) != 1 || neonFlicker(6, 0, -1, spans, at) != 1 {
			t.Fatalf("after %v seconds a cell flickers outside of the defect", at)
		}
	}
}

// The letter the defect is on is taken by the whole tagline of the visit, not
// by its length alone; a visit without a tagline has one all the same.
func TestNeonDefectByTheTagline(t *testing.T) {
	art := nameArt("Luna", nameScale)
	spans, w, h := letterSpans(art), len([]rune(art[0])), len(art)
	left, top := (46-w)/2, (10-h)/2
	// inked is a cell of the frame in every letter: the top one of its first
	// column.
	var inked [][2]int
	for _, span := range spans {
		y := 0
		for []rune(art[y])[span[0]] == ' ' {
			y++
		}
		inked = append(inked, [2]int{left + span[0], top + y})
	}
	// defect is the letter that goes out in the visit of tagline.
	defect := func(tagline string) int {
		for step := range 200 {
			cells := colorsOf(drawNameAnimation("Luna", 46, 10, seconds((float64(step)+0.5)*neonStep), false, nameNeon, 1, tagline))
			for i, cell := range inked {
				var r, g, b int
				if n, _ := fmt.Sscanf(cells[cell], "38;2;%d;%d;%d", &r, &g, &b); n == 3 && r+g+b < 300 {
					return i
				}
			}
		}
		t.Fatalf("no letter goes out with the tagline %q", tagline)
		return -1
	}
	seen := map[int]bool{}
	for _, first := range "abcdefgh" {
		seen[defect(string(first)+"tagline")] = true
	}
	if len(seen) < 2 {
		t.Fatalf("taglines of one length flicker on the letters %v alone", seen)
	}
	defect("")
}

// A glitch tears the name for a third of a second, another way with every
// step, and leaves it where it was: in rows as wide as before, and whole
// again before and after. Every visit of an empty chat picks its look by
// chance.
func TestNameAnimationGlitchTearsTheName(t *testing.T) {
	still := ansi.Strip(emptyChat("Luna", 46, 10))
	frame := func(at float64) string {
		return ansi.Strip(drawNameAnimation("Luna", 46, 10, seconds(at), false, nameGlitch, 1))
	}
	for n := range 5 {
		at := glitchAt(n)
		if at < float64(n)*glitchEvery+0.5 || at+glitchFor > float64(n+1)*glitchEvery {
			t.Fatalf("the glitch %d is at %v", n, at)
		}
		first, second := frame(at+0.01), frame(at+glitchStep+0.01)
		if first == still || second == still || first == second || !strings.Contains(first, "▒") {
			t.Fatalf("the glitch %d does not tear the name:\n%s\n%s", n, first, second)
		}
		for _, line := range strings.Split(first, "\n") {
			if ansi.StringWidth(line) != 46 {
				t.Fatalf("a row of %d columns: %q", ansi.StringWidth(line), line)
			}
		}
		if before, after := frame(at-0.01), frame(at+glitchFor+0.01); before != still || after != still {
			t.Fatalf("around the glitch %d the name is\n%s\n%s", n, before, after)
		}
	}

	was := pickNameLook
	defer func() { pickNameLook = was }()
	for _, look := range []nameLook{nameGlitch, nameShine, nameWave, nameFloat, nameFlare, nameNeon, nameBeam, nameLaser, nameRain} {
		pickNameLook = func(n int) int {
			if n != int(nameLooks) {
				t.Fatalf("a look out of %d", n)
			}
			return int(look)
		}
		m, _ := newModel(t)
		m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
		if m.nameAnimation.look != look || m.history.GetContent() != drawNameAnimation(m.agentName(), m.history.Width(), m.history.Height(), 0, false, look, 1, m.nameAnimation.tagline) {
			t.Fatalf("the visit is not in the look %d", look)
		}
	}
}

// The first message of a chat does not put the name out at once: the name
// fades out behind it, on the rows the message leaves blank, and is gone
// after nameFade. Another chat shows nothing of it.
func TestNameFadesOutBehindTheFirstMessage(t *testing.T) {
	nameFade = 450 * time.Millisecond
	defer func() { nameFade = 0 }()
	m, b := newModel(t)
	at := time.Unix(100, 0)
	m.now = func() time.Time { return at }
	b.status.Name = "Luna"
	m.Update(m.refreshStatus())
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	m.chat.entries = []entry{{mine: true, text: "hello"}}
	m.renderHistory(true)
	m.Update(spinner.TickMsg{ID: m.spinner.ID()})
	shown := func() (name, message bool) {
		content := ansi.Strip(m.history.GetContent())
		return strings.Contains(content, nameRow("Luna")), strings.Contains(content, "hello")
	}
	if name, message := shown(); !name || !message || m.nameAnimation.active {
		t.Fatalf("the name %v behind the message %v:\n%s", name, message, ansi.Strip(m.history.GetContent()))
	}
	full := m.history.GetContent()
	at = at.Add(nameFade / 2)
	m.Update(spinner.TickMsg{ID: m.spinner.ID()})
	if name, _ := shown(); !name || m.history.GetContent() == full {
		t.Fatal("the name does not fade")
	}
	was := m.chat
	m.chat = m.conversation("other")
	m.chat.entries = []entry{{mine: true, text: "there"}}
	m.renderHistory(true)
	if name, _ := shown(); name {
		t.Fatal("the name is behind another chat")
	}
	m.chat = was
	at = at.Add(nameFade / 2)
	m.Update(spinner.TickMsg{ID: m.spinner.ID()})
	if name, message := shown(); name || !message || !m.nameAnimation.fading.IsZero() {
		t.Fatalf("after the fade the name is shown %v, the message %v", name, message)
	}
}

func TestNameAnimationVisitLifecycle(t *testing.T) {
	m, b := newModel(t)
	at := time.Unix(100, 0)
	m.now = func() time.Time { return at }
	b.status.Name = "Luna"
	m.Update(m.refreshStatus())
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	if !m.nameAnimation.active {
		t.Fatal("empty chat should start the animation")
	}
	at = at.Add(time.Second)
	m.Update(spinner.TickMsg{ID: m.spinner.ID()})
	if m.nameAnimation.elapsed != time.Second {
		t.Fatal("a tick should move the animation on")
	}
	m.screen = screenHelp
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	if m.nameAnimation.active {
		t.Fatal("hidden chat should stop")
	}
	at = at.Add(time.Second)
	m.screen = screenChat
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	if !m.nameAnimation.active || m.nameAnimation.start != at || m.nameAnimation.elapsed != 0 {
		t.Fatal("reentry should restart")
	}
	m.chat.entries = []entry{{mine: true, text: "hello"}}
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	if m.nameAnimation.active {
		t.Fatal("messages replace animation")
	}
}

// The chat shows the animation as far as it has come with every tick of
// the spinner of the UI, in the colors for the background of the terminal,
// and from its beginning when the visit begins.
func TestNameAnimationIsDrawnWithTheTicks(t *testing.T) {
	m, b := newModel(t)
	at := time.Unix(100, 0)
	m.now = func() time.Time { return at }
	b.status.Name = "Luna"
	m.Update(m.refreshStatus())
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	a := &m.nameAnimation
	width, height := m.transcriptWidth(), m.chatHeight()
	shows := func(elapsed time.Duration, light bool) {
		t.Helper()
		if w, h := m.history.Width(), m.history.Height(); w != width || h != height || m.history.GetContent() != drawNameAnimation("Luna", w, h, elapsed, light, a.look, 1, a.tagline) {
			t.Fatalf("after %v in %dx%d, light %v:\n%s", elapsed, w, h, light, ansi.Strip(m.history.GetContent()))
		}
		if !strings.Contains(ansi.Strip(view(m)), nameRow("Luna")) {
			t.Fatalf("the chat does not show the name:\n%s", view(m))
		}
	}
	shows(0, false)
	at = at.Add(1300 * time.Millisecond)
	m.Update(spinner.TickMsg{ID: m.spinner.ID() + 1}) // of another spinner
	shows(0, false)
	m.Update(spinner.TickMsg{ID: m.spinner.ID()})
	shows(1300*time.Millisecond, false)
	m.light = true
	at = at.Add(700 * time.Millisecond)
	m.Update(spinner.TickMsg{ID: m.spinner.ID()})
	shows(2*time.Second, true)

	// Another name, and another conversation, begin anew.
	b.status.Name = "Terra"
	m.Update(m.refreshStatus())
	if a.name != "Terra" || a.start != at || a.elapsed != 0 || m.history.GetContent() != drawNameAnimation("Terra", width, height, 0, true, a.look, 1, a.tagline) {
		t.Fatalf("after another name: %+v", *a)
	}
	at = at.Add(time.Second)
	m.Update(spinner.TickMsg{ID: m.spinner.ID()})
	was := m.chat
	drive(t, m, run(m, "/new"), func() bool { return true })
	if m.chat == was || a.chat != m.chat || a.start != at || a.elapsed != 0 {
		t.Fatalf("after /new: %+v", *a)
	}
}

// The name moves only in an empty chat of the owner that is on the screen:
// not behind the splash or the setup, not on another screen, not before the
// node is set up, not in a conversation of a peer, which is only to read,
// and not once there is a message.
func TestNameAnimationNeedsAnEmptyChatOnTheScreen(t *testing.T) {
	for what, change := range map[string]func(m *Model) (undo func()){
		"the splash":       func(m *Model) func() { m.splash.show = true; return func() { m.splash.show = false } },
		"the setup":        func(m *Model) func() { m.onboard = &onboarding{}; return func() { m.onboard = nil } },
		"another screen":   func(m *Model) func() { m.screen = screenSessions; return func() { m.screen = screenChat } },
		"no configuration": func(m *Model) func() { m.status.Configured = false; return func() { m.status.Configured = true } },
		"a peer":           func(m *Model) func() { m.chat.peer = "beta"; return func() { m.chat.peer = "" } },
		"a message":        func(m *Model) func() { m.chat.entries = []entry{{text: "hi"}}; return func() { m.chat.entries = nil } },
	} {
		m, _ := newModel(t)
		if !m.nameAnimation.active {
			t.Fatalf("%s: the name does not move in an empty chat", what)
		}
		undo := change(m)
		if m.syncNameAnimation(); m.nameAnimation.active {
			t.Errorf("%s: the name still moves", what)
		}
		undo()
		if m.syncNameAnimation(); !m.nameAnimation.active {
			t.Errorf("after %s the name does not move again", what)
		}
	}
}

// Of the profiles of a node only the one shown moves its name: the one the
// UI opens on, the next one that shift+tab shows, and the one the UI goes
// on with after a new build.
func TestNameMovesOnlyForTheProfileShown(t *testing.T) {
	root, terra, luna, _ := profiles(t)
	// moving are the profiles whose name moves, once each has had a tick of its spinner.
	moving := func() [2]bool {
		terra.Update(spinner.TickMsg{ID: terra.spinner.ID()})
		luna.Update(spinner.TickMsg{ID: luna.spinner.ID()})
		return [2]bool{terra.nameAnimation.active, luna.nameAnimation.active}
	}
	root.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if got := moving(); got != [2]bool{true, false} {
		t.Fatalf("the UI opens on Terra, and the names move: %v", got)
	}
	root.Update(shiftTab)
	luna.Update(luna.refreshStatus())
	if got := moving(); got != [2]bool{false, true} {
		t.Fatalf("Luna is shown, and the names move: %v", got)
	}
	root.SelectProfile("aaa111")
	if got := moving(); got != [2]bool{true, false} {
		t.Fatalf("the UI goes on with Terra, and the names move: %v", got)
	}
	root.SelectProfile("bbb222")
	if got := moving(); got != [2]bool{false, true} {
		t.Fatalf("the UI goes on with Luna, and the names move: %v", got)
	}
}

func TestEmptyTaglineSelectionStaysDuringAnimation(t *testing.T) {
	old := pickEmptyTagline
	t.Cleanup(func() { pickEmptyTagline = old })
	calls := 0
	pickEmptyTagline = func(n int) int { calls++; return n - 1 }
	m, _ := newModel(t)
	m.splash.show = false
	m.status.Configured = true
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	m.syncNameAnimation()
	m.syncNameAnimation()
	if calls != 1 || m.nameAnimation.tagline != emptyTaglines[len(emptyTaglines)-1] {
		t.Fatalf("selection: %d calls, %q", calls, m.nameAnimation.tagline)
	}
	if !strings.Contains(ansi.Strip(m.history.GetContent()), m.nameAnimation.tagline) {
		t.Fatal("selected tagline not shown")
	}
}

func TestResumeWaitsForHistoryBeforeEmptyArtwork(t *testing.T) {
	m, _ := newModel(t)
	m.status.Configured = true
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	m.Resume("resumed")
	m.syncNameAnimation()
	if m.nameAnimation.active || strings.TrimSpace(m.history.GetContent()) != "" {
		t.Fatal("artwork shown before resumed history arrived")
	}
	m.watched(watchedMsg{id: "resumed", err: fmt.Errorf("retry")})
	if m.resuming != m.chat {
		t.Fatal("failed load released artwork")
	}
	m.watched(watchedMsg{id: "resumed"})
	m.syncNameAnimation()
	if m.resuming != nil || !m.nameAnimation.active {
		t.Fatal("a truly empty resumed chat should show artwork after loading")
	}
}
