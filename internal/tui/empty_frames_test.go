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
// crosses it, torn by a glitch, another way with every step of it, and
// fading out. A number stands for each frame; a look that is changed on
// purpose changes them, and the test says to what.
func TestNameAnimationIsDrawnAsItWas(t *testing.T) {
	torn := glitchAt(0)
	for _, tt := range []struct {
		w, h  int
		look  nameLook
		at    float64
		shown float64
		want  string
	}{
		{60, 12, nameShine, 0.3, 1, "45295c91"},
		{60, 12, nameShine, 0.9, 1, "20f71f5e"},
		{60, 12, nameShine, 1.4, 1, "a8b9f4f0"},
		{60, 12, nameShine, 4.2, 0.5, "d764951e"},
		{120, 30, nameShine, 0.9, 1, "72c50adc"},
		{120, 30, nameShine, 2.1, 1, "6ed74031"},
		{120, 30, nameShine, 6.3, 1, "a512bcbc"},
		{60, 12, nameGlitch, torn - 0.01, 1, "d187a587"},
		{60, 12, nameGlitch, torn + 0.01, 1, "bf161d9b"},
		{60, 12, nameGlitch, torn + 0.1, 1, "42c4f77e"},
		{60, 12, nameGlitch, torn + 0.2, 1, "82fb2b4d"},
		{60, 12, nameGlitch, torn + 0.3, 1, "b0eaecc8"},
		{120, 30, nameGlitch, glitchAt(1) + 0.1, 1, "39b26e08"},
		{120, 30, nameGlitch, glitchAt(2) + 0.2, 0.5, "044a6de9"},
		{120, 30, nameGlitch, glitchAt(3) + 0.3, 1, "8c0da080"},
	} {
		frame := drawNameAnimation("Luna", tt.w, tt.h, seconds(tt.at), false, tt.look, tt.shown, "stay curious")
		if got := drawn(frame); got != tt.want {
			t.Errorf("%dx%d, look %d at %.2fs, %v shown: drawn as %s, want %s:\n%s", tt.w, tt.h, tt.look, tt.at, tt.shown, got, tt.want, ansi.Strip(frame))
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
