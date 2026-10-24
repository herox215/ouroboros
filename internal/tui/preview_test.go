package tui

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// The picture of a label shows above the mouse once it rested on the label,
// and is gone once the mouse moves on or a key is pressed. A tick of a move
// before shows nothing.
func TestRestingOnALabelPreviewsItsPicture(t *testing.T) {
	m, _ := newModel(t)
	path := filepath.Join(t.TempDir(), "red.png")
	writePNG(t, path, 64, 32, color.NRGBA{255, 0, 0, 255})
	m.Update(tea.PasteMsg{Content: path})
	x, y := labelCell(t, m, "[Image_1]")

	_, cmd := m.Update(tea.MouseMotionMsg{X: x + 3, Y: y})
	if cmd == nil {
		t.Fatal("a mouse that rests must tick")
	}
	stale := hoverMsg{m.hover.seq}
	m.Update(tea.MouseMotionMsg{X: x + 4, Y: y})
	if _, cmd := m.Update(stale); cmd != nil {
		t.Fatal("the tick of a move before must show nothing")
	}
	_, cmd = m.Update(hoverMsg{m.hover.seq})
	if cmd == nil {
		t.Fatal("the label under the mouse must be read")
	}
	m.Update(cmd())
	screen := m.View().Content
	if !strings.Contains(screen, "▀") || !strings.Contains(screen, "38;2;255;0;0") {
		t.Fatal("the picture must show")
	}
	// 64×32 pixels fit 32 cells wide and 16 pixels, 8 cells, high.
	if w, h := ansi.StringWidth(strings.Split(m.hover.view, "\n")[1]), len(strings.Split(m.hover.view, "\n")); w != 34 || h != 10 {
		t.Fatalf("preview of %d×%d cells", w, h)
	}
	if above := strings.Split(screen, "\n")[y-2]; !strings.Contains(above, "▀") { // over the border below it
		t.Fatal("the picture must show above the mouse")
	}
	press(m, "a")
	if strings.Contains(m.View().Content, "▀") {
		t.Fatal("a key must take the picture away")
	}

	m.Update(tea.MouseMotionMsg{X: 0, Y: 0})
	if _, cmd := m.Update(hoverMsg{m.hover.seq}); cmd != nil {
		t.Fatal("beside a label nothing is read")
	}
}

// A picture that cannot be read shows nothing.
func TestPreviewOfNoPicture(t *testing.T) {
	path := filepath.Join(files(t, "broken.png"), "broken.png")
	if v, _ := preview(path, graphics{}); v != "" {
		t.Fatal("no picture, no preview")
	}
	if v, _ := preview(path+".missing", graphics{sixel: true}); v != "" {
		t.Fatal("no picture, no preview")
	}
}

// A terminal that draws sixels gets the picture as a sixel, drawn into a
// blank box once that is on the screen, and the screen drawn anew once it
// goes.
func TestSixelPreview(t *testing.T) {
	m, _ := newModel(t)
	m.Update(uv.PrimaryDeviceAttributesEvent{62, 4, 22})
	m.Update(uv.CellSizeEvent{Width: 8, Height: 16})
	path := filepath.Join(t.TempDir(), "red.png")
	writePNG(t, path, 400, 200, color.NRGBA{255, 0, 0, 255})
	m.Update(tea.PasteMsg{Content: path})
	x, y := labelCell(t, m, "[Image_1]")
	m.Update(tea.MouseMotionMsg{X: x, Y: y})
	_, cmd := m.Update(hoverMsg{m.hover.seq})
	_, cmd = m.Update(cmd())
	if cmd == nil || strings.Contains(m.View().Content, "▀") {
		t.Fatal("the box must be blank, and the sixel come")
	}
	// 400×200 pixels fit 240×120: 30 cells wide, and 120 pixels, 8 cells, high.
	lines := strings.Split(m.hover.view, "\n")
	if w := ansi.StringWidth(lines[1]); w != 32 || len(lines) != 10 {
		t.Fatalf("box of %d×%d cells", w, len(lines))
	}
	_, cmd = m.Update(cmd())
	raw, ok := cmd().(tea.RawMsg)
	if !ok || !strings.Contains(fmt.Sprint(raw.Msg), "\x1bP0;1;0q\"1;1;240;120#0;2;100;0;0") {
		t.Fatalf("sixel %q", fmt.Sprint(raw.Msg))
	}
	_, cmd = m.Update(tea.MouseMotionMsg{X: x + 1, Y: y})
	if msgs, ok := cmd().(tea.BatchMsg); !ok || len(msgs) == 0 || fmt.Sprintf("%T", msgs[0]()) != fmt.Sprintf("%T", tea.ClearScreen()) {
		t.Fatal("the sixel must go with the screen drawn anew")
	}
}

// labelCell is the cell of the chat where label begins.
func labelCell(t *testing.T, m *Model, label string) (int, int) {
	t.Helper()
	for y, line := range strings.Split(m.chatScreen(), "\n") {
		line = ansi.Strip(line)
		if i := strings.Index(line, label); i >= 0 {
			return ansi.StringWidth(line[:i]), y
		}
	}
	t.Fatalf("%s is not drawn", label)
	return 0, 0
}

func writePNG(t *testing.T, path string, w, h int, c color.Color) {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, c)
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}
