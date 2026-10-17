package tui

import (
	"fmt"
	"hash/fnv"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// drawn is a frame of the name in a number: the cells and the colors of all
// of it, without a file of frames to keep.
func drawn(frame string) string {
	h := fnv.New32a()
	h.Write([]byte(frame))
	return fmt.Sprintf("%08x", h.Sum32())
}

// The name as it is drawn, in both sizes of the font: while the shine
// crosses it, torn by a glitch, another way with every step of it, with the
// sparks of a neon at windows that cut them off, and fading out. A number
// stands for each frame; a look that is changed on purpose changes them, and
// the test says to what.
func TestNameAnimationIsDrawnAsItWas(t *testing.T) {
	torn := glitchAt(0)
	for _, tt := range []struct {
		w, h  int
		look  nameLook
		at    float64
		shown float64
		want  string
	}{
		{60, 12, nameShine, 0.3, 1, "e2e2cc19"},
		{60, 12, nameShine, 0.9, 1, "5c030a15"},
		{60, 12, nameShine, 1.4, 1, "2fc5a303"},
		{60, 12, nameShine, 4.2, 0.5, "eae56286"},
		{120, 30, nameShine, 0.9, 1, "c3266865"},
		{120, 30, nameShine, 2.1, 1, "c7d8e991"},
		{120, 30, nameShine, 6.3, 1, "c1ed4339"},
		{60, 12, nameGlitch, torn - 0.01, 1, "bd4f8ff7"},
		{60, 12, nameGlitch, torn + 0.01, 1, "92c54031"},
		{60, 12, nameGlitch, torn + 0.1, 1, "70903178"},
		{60, 12, nameGlitch, torn + 0.2, 1, "96a8ed49"},
		{60, 12, nameGlitch, torn + 0.3, 1, "cbd7e49c"},
		{120, 30, nameGlitch, glitchAt(1) + 0.1, 1, "a286fa3a"},
		{120, 30, nameGlitch, glitchAt(2) + 0.2, 0.5, "0893f278"},
		{120, 30, nameGlitch, glitchAt(3) + 0.3, 1, "228c4fe9"},
		{60, 12, nameNeon, 1.5 * neonStep, 1, "0a2612b7"},
		{60, 12, nameNeon, 92.5 * neonStep, 1, "25c16278"},
		{40, 8, nameNeon, 92.5 * neonStep, 1, "eb5cd026"},
		{39, 7, nameNeon, 35.5 * neonStep, 1, "d899d914"},
	} {
		frame := drawNameAnimation("Luna", tt.w, tt.h, seconds(tt.at), false, tt.look, tt.shown, "stay curious")
		if got := drawn(frame); got != tt.want {
			t.Errorf("%dx%d, look %d at %.2fs, %v shown: drawn as %s, want %s:\n%s", tt.w, tt.h, tt.look, tt.at, tt.shown, got, tt.want, ansi.Strip(frame))
		}
	}
}

// The other looks as they are drawn, in windows as small as the name and
// larger ones: the wave, the float, the flare and the beam, a glitch that
// tears the name over the edges, the neon with other defects and their
// sparks, the laser while it cuts, at the end of the cut and after it, the
// rain where it splashes, and the snake as it comes, circles the name and
// leaves, and where there is no room for it.
func TestNameLooksAreDrawnAsTheyWere(t *testing.T) {
	for _, tt := range []struct {
		w, h  int
		look  nameLook
		at    float64
		light bool
		tag   string
		want  string
	}{
		{38, 6, nameWave, 0.77, false, "stay curious", "af470863"},
		{60, 12, nameWave, 2.5, false, "stay curious", "2ad155ad"},
		{38, 6, nameFloat, 1.3, false, "stay curious", "dd90e46f"},
		{60, 12, nameFloat, 2.5, false, "stay curious", "12ae2827"},
		{38, 6, nameFlare, 1.9, false, "stay curious", "d4e7c2ff"},
		{38, 6, nameFlare, 3.7, false, "stay curious", "bfad51ab"},
		{38, 6, nameBeam, 6.1, false, "stay curious", "f1ce3d0f"},
		{60, 12, nameBeam, 1.3, false, "stay curious", "2b278777"},
		{38, 6, nameGlitch, glitchAt(0) + 0.01, false, "stay curious", "c7af7cef"},
		{39, 7, nameNeon, 0.5 * neonStep, false, "stay curious", "2a3cec82"},
		{39, 7, nameNeon, 47.5 * neonStep, false, "stay curious", "169d43b3"},
		{39, 7, nameNeon, 75.5 * neonStep, false, "stay curious", "a62523dc"},
		{38, 6, nameNeon, 74.5 * neonStep, false, "stay curious", "78dedd47"},
		{38, 6, nameNeon, 86.5 * neonStep, false, "dare to begin", "3872337f"},
		{38, 6, nameNeon, 29.5 * neonStep, false, "", "5de5778f"},
		{38, 6, nameLaser, 2.095, false, "stay curious", "2eaf6356"},
		{38, 6, nameLaser, float64(len(laserPath(fitName("Luna", 38, 6)))) * laserStep, false, "stay curious", "f397b629"},
		{39, 7, nameLaser, 0.835, false, "stay curious", "433dd9fd"},
		{39, 7, nameLaser, 2.185, false, "stay curious", "83b15b46"},
		{39, 7, nameLaser, 2.295, false, "stay curious", "73ab1bf5"},
		{60, 12, nameLaser, 1, true, "stay curious", "b419076a"},
		{120, 30, nameLaser, 0, false, "stay curious", "f3af072d"},
		{120, 30, nameLaser, 1.1, false, "stay curious", "d0d69885"},
		{120, 30, nameLaser, 2.5, false, "stay curious", "a1dc0a64"},
		{120, 30, nameLaser, 4, false, "stay curious", "a61357d3"},
		{38, 6, nameRain, 0.4, false, "stay curious", "7cf5a699"},
		{39, 7, nameRain, 3.03, false, "stay curious", "783c1c74"},
		{60, 12, nameRain, 2.15, false, "stay curious", "56822826"},
		{120, 30, nameRain, 0, false, "stay curious", "05757618"},
		{120, 30, nameRain, 1.5, false, "stay curious", "f5d8cc03"},
		{60, 12, nameSnake, 2.9, false, "stay curious", "76503943"},
		{53, 18, nameSnake, 6.15, false, "stay curious", "42fae1eb"},
		{54, 18, nameSnake, 7.65, false, "stay curious", "487fec28"},
		{54, 18, nameSnake, 34.15, false, "stay curious", "a0717af2"},
		{80, 24, nameSnake, 2, false, "stay curious", "d392282d"},
		{80, 24, nameSnake, 8, false, "stay curious", "878192ff"},
		{80, 24, nameSnake, 16, false, "stay curious", "1b8d6b6f"},
		{80, 24, nameSnake, 21.5, false, "stay curious", "a4894e47"},
		{120, 30, nameSnake, 5, false, "stay curious", "e1a223ff"},
	} {
		frame := drawNameAnimation("Luna", tt.w, tt.h, seconds(tt.at), tt.light, tt.look, 1, tt.tag)
		if got := drawn(frame); got != tt.want {
			t.Errorf("%dx%d, look %d at %.4fs, light %v, %q: drawn as %s, want %s:\n%s", tt.w, tt.h, tt.look, tt.at, tt.light, tt.tag, got, tt.want, ansi.Strip(frame))
		}
	}
}

// The splash as it is drawn: the big title as it comes in a window just
// large enough for it, the plain one in a window a row too low for the big
// one, and in one a few columns too narrow for it, as it fades out.
func TestSplashIsDrawnAsItWas(t *testing.T) {
	for _, tt := range []struct {
		w, h int
		at   time.Duration
		want string
	}{
		{92, 10, 480 * time.Millisecond, "da7cf987"},
		{92, 10, 1600 * time.Millisecond, "fb9ccc58"},
		{92, 9, 0, "a20f5756"},
		{84, 30, 2400 * time.Millisecond, "502fcf97"},
	} {
		frame := drawSplash(tt.w, tt.h, tt.at, false)
		if got := drawn(frame); got != tt.want {
			t.Errorf("%dx%d at %v: drawn as %s, want %s:\n%s", tt.w, tt.h, tt.at, got, tt.want, ansi.Strip(frame))
		}
	}
}

// The empty chat as it is drawn where nothing moves: the blocks of the name
// in their color, and the blanks between them without one.
func TestEmptyChatIsDrawnAsItWas(t *testing.T) {
	for _, tt := range []struct {
		w, h int
		want string
	}{
		{38, 6, "9735661a"},
		{60, 12, "8bad2908"},
	} {
		frame := emptyChat("Luna", tt.w, tt.h, "stay curious")
		if got := drawn(frame); got != tt.want {
			t.Errorf("%dx%d: drawn as %s, want %s:\n%s", tt.w, tt.h, got, tt.want, ansi.Strip(frame))
		}
	}
}

// A glitch begins at its moment and lasts glitchFor, a step of it after the
// other, and each glitch tears the name its own ways.
func TestGlitchLastsItsTime(t *testing.T) {
	if got := glitchAt(0); got < 0.5 || got > glitchEvery-glitchFor {
		t.Fatalf("the first glitch begins at %v", got)
	}
	for n := range 40 {
		at := glitchAt(n)
		if at < float64(n)*glitchEvery+0.5 || at+glitchFor > float64(n+1)*glitchEvery {
			t.Fatalf("the glitch %d begins at %v, outside of its span", n, at)
		}
		if first := glitch(at); first != 1+n*8 {
			t.Fatalf("the glitch %d begins as %d", n, first)
		}
		if step := glitch(at + 1.5*glitchStep); step != 2+n*8 {
			t.Fatalf("the second step of the glitch %d is %d", n, step)
		}
		if before := glitch(at - 0.001); before != 0 {
			t.Fatalf("before the glitch %d: %d", n, before)
		}
		if after := glitch(at + glitchFor + 0.001); after != 0 {
			t.Fatalf("after the glitch %d: %d", n, after)
		}
	}
}

// A name that fades in no time is gone at once, and one behind a chat that
// fills the viewport to its last row still shows on the rows left blank.
func TestNameBehindAFullViewport(t *testing.T) {
	a := nameAnimation{fading: time.Unix(100, 0)}
	if got := a.faded(time.Unix(100, 0)); got != 0 {
		t.Fatalf("without a time to fade, %v of the name is left", got)
	}
	nameFade = 450 * time.Millisecond
	defer func() { nameFade = 0 }()
	m, _ := newModel(t)
	at := time.Unix(100, 0)
	m.now = func() time.Time { return at }
	m.nameAnimation = nameAnimation{chat: m.chat, name: "Luna", fading: at}
	rows := m.history.Height()
	for lines, behind := range map[int]bool{rows: true, rows + 1: false} {
		content := "hello" + strings.Repeat("\n", lines-1)
		got := ansi.Strip(m.behind(content))
		if strings.Contains(got, nameRow("Luna")) != behind || !strings.HasPrefix(got, "hello") {
			t.Errorf("behind %d lines in %d rows the name shows: %v", lines, rows, !behind)
		}
	}
}
