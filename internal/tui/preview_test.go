package tui

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
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
	// 64×32 pixels fit 64 cells wide and 32 pixels, 16 cells, high.
	if w, h := ansi.StringWidth(strings.Split(m.hover.view, "\n")[1]), len(strings.Split(m.hover.view, "\n")); w != 66 || h != 18 {
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

// An iTerm2 answers the probe for the size of its cells and gets the picture
// as a PNG of its own, drawn into a blank box once that is on the screen.
func TestInlinePreview(t *testing.T) {
	for _, tc := range []struct {
		name, answer string
		inline       bool
		w, h         int
	}{
		{"retina", "\x1b]1337;ReportCellSize=20.0;10.0;2.0\x1b\\", true, 20, 40},
		{"without a scale", "\x1b]1337;ReportCellSize=20;10\a", true, 10, 20},
		{"another answer", "\x1b]11;rgb:0000/0000/0000\x1b\\", false, 0, 0},
		{"half an answer", "\x1b]1337;ReportCellSize=20\x1b\\", false, 0, 0},
		{"no numbers", "\x1b]1337;ReportCellSize=tall;wide\a", false, 0, 0},
		{"no size", "\x1b]1337;ReportCellSize=0;10\a", false, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := newModel(t)
			m.Update(uv.UnknownOscEvent(tc.answer))
			if m.graphics.inline != tc.inline || m.graphics.cellW != tc.w || m.graphics.cellH != tc.h {
				t.Fatalf("graphics %+v", m.graphics)
			}
		})
	}
	t.Run("picture", func(t *testing.T) {
		m, _ := newModel(t)
		if !strings.Contains(terminalQuery, "\x1b]1337;ReportCellSize") {
			t.Fatal("the size of a cell is not asked of iTerm2")
		}
		m.Update(uv.UnknownOscEvent("\x1b]1337;ReportCellSize=20.0;10.0;2.0\x1b\\"))
		path := filepath.Join(t.TempDir(), "red.png")
		writePNG(t, path, 400, 200, color.NRGBA{255, 0, 0, 255})
		m.Update(tea.PasteMsg{Content: path})
		x, y := labelCell(t, m, "[Image_1]")
		m.Update(tea.MouseMotionMsg{X: x, Y: y})
		_, cmd := m.Update(hoverMsg{m.hover.seq})
		_, cmd = m.Update(cmd())
		if cmd == nil || strings.Contains(m.View().Content, "▀") {
			t.Fatal("the box must be blank, and the picture come")
		}
		// 400×200 pixels fit 240×120: cells of 20×40 pixels, 12 wide and 3 high.
		lines := strings.Split(m.hover.view, "\n")
		if w := ansi.StringWidth(lines[1]); w != 14 || len(lines) != 5 {
			t.Fatalf("box of %d×%d cells", w, len(lines))
		}
		_, cmd = m.Update(cmd())
		raw, ok := cmd().(tea.RawMsg)
		file := fmt.Sprint(raw.Msg)
		head := "\x1b]1337;File=inline=1;width=12;height=3;preserveAspectRatio=1:"
		at := strings.Index(file, head)
		if !ok || at < 0 {
			t.Fatalf("iTerm2 picture %q", file)
		}
		at += len(head)
		end := strings.Index(file[at:], "\a")
		if end < 0 {
			t.Fatalf("no end of the iTerm2 picture in %q", file)
		}
		data, err := base64.StdEncoding.DecodeString(file[at : at+end])
		if err != nil {
			t.Fatal(err)
		}
		config, err := png.DecodeConfig(bytes.NewReader(data))
		if err != nil || config.Width != 240 || config.Height != 120 {
			t.Fatalf("the picture is %d×%d, %v; want 240×120", config.Width, config.Height, err)
		}
		_, cmd = m.Update(tea.MouseMotionMsg{X: x + 1, Y: y})
		if msgs, ok := cmd().(tea.BatchMsg); !ok || len(msgs) == 0 || fmt.Sprintf("%T", msgs[0]()) != fmt.Sprintf("%T", tea.ClearScreen()) {
			t.Fatal("the picture must go with the screen drawn anew")
		}
	})
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

func TestRestingOnAPathPreviewsItsPicture(t *testing.T) {
	m, _ := newModel(t)
	path := filepath.Join(t.TempDir(), "red.png")
	writePNG(t, path, 64, 32, color.NRGBA{255, 0, 0, 255})
	// Insert literal text rather than paste, which would create an attachment.
	m.chat.directory = filepath.Dir(path)
	m.input.SetValue("./red.png")
	x, y := labelCell(t, m, "./red.png")
	m.Update(tea.MouseMotionMsg{X: x + 3, Y: y})
	_, cmd := m.Update(hoverMsg{m.hover.seq})
	if cmd == nil {
		t.Fatal("the path under the mouse must be read")
	}
	m.Update(cmd())
	if !strings.Contains(m.View().Content, "38;2;255;0;0") {
		t.Fatal("the path must show its red picture")
	}
	m.Update(tea.MouseMotionMsg{X: 0, Y: 0})
	if m.hover.view != "" {
		t.Fatal("moving must dismiss the preview")
	}
}

func TestPicturePathAt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "red.png")
	spaced := filepath.Join(dir, "my picture.png")
	writePNG(t, path, 2, 2, color.White)
	writePNG(t, spaced, 2, 2, color.White)
	for _, tc := range []struct {
		name, want string
	}{
		{path, path},
		{"red.png", path},
		{"./red.png", path},
		{"`red.png`", path},
		{"(" + path + ").", path},
		{quote(spaced), spaced},
		{"'" + spaced + "'", spaced},
		{strings.ReplaceAll(spaced, " ", `\ `), spaced},
		{"file://" + strings.ReplaceAll(spaced, " ", "%20"), spaced},
		{"missing.png", ""},
		{"https://example.org/red.png", ""},
		{"notes.txt", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prefix := "界 look "
			line := prefix + tc.name + " end"
			got, ok := picturePathAt(line, ansi.StringWidth(prefix)+2, dir)
			if got != tc.want || ok != (tc.want != "") {
				t.Fatalf("got %q, %v; want %q", got, ok, tc.want)
			}
			if _, ok := picturePathAt(line, 0, dir); ok {
				t.Fatal("text beside a path must not preview it")
			}
		})
	}
	if _, ok := picturePathAt("red.png", 2, ""); ok {
		t.Fatal("a relative path without a known project must not use the process cwd")
	}
}

func TestProfileSwitchInvalidatesPreview(t *testing.T) {
	for _, stage := range []string{"decoding", "waiting for sixel", "drawn"} {
		t.Run(stage, func(t *testing.T) {
			root, m, _, _ := profiles(t)
			root.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
			m.graphics.sixel = true
			path := filepath.Join(t.TempDir(), "red.png")
			writePNG(t, path, 8, 8, color.White)
			m.Update(tea.PasteMsg{Content: path})
			x, y := labelCell(t, m, "[Image_1]")
			m.Update(tea.MouseMotionMsg{X: x, Y: y})
			seq := m.hover.seq
			_, decode := m.Update(hoverMsg{seq})
			if decode == nil {
				t.Fatal("no preview read")
			}
			result := decode()
			if stage != "decoding" {
				m.Update(result)
			}
			if stage == "drawn" {
				_, draw := m.Update(pictureMsg{seq})
				if draw == nil {
					t.Fatal("no sixel draw")
				}
			}
			_, switched := root.Update(shiftTab)
			if m.hover.seq == seq || m.hover.view != "" || m.hover.picture != "" || m.hover.drawn {
				t.Fatal("leaving a profile must invalidate its preview")
			}
			if stage == "drawn" {
				batch := switched().(tea.BatchMsg)
				if len(batch) == 0 || fmt.Sprintf("%T", batch[0]()) != fmt.Sprintf("%T", tea.ClearScreen()) {
					t.Fatal("leaving a drawn sixel must clear the screen")
				}
			}
			for _, msg := range []tea.Msg{hoverMsg{seq}, result, pictureMsg{seq}} {
				if _, cmd := root.Update(profileMsg{0, msg}); cmd != nil {
					t.Fatalf("hidden callback %T returned a command", msg)
				}
			}
			root.Update(shiftTab)
			for _, msg := range []tea.Msg{result, pictureMsg{seq}} {
				if _, cmd := root.Update(profileMsg{0, msg}); cmd != nil {
					t.Fatalf("old callback %T survived switching back", msg)
				}
			}
			if m.hover.view != "" || m.hover.drawn {
				t.Fatal("switching back must not restore an old preview")
			}
		})
	}
}

func TestHiddenProfileGuardsPreviewCallbacks(t *testing.T) {
	root, m, _, _ := profiles(t)
	path := filepath.Join(t.TempDir(), "red.png")
	writePNG(t, path, 1, 1, color.White)
	m.Update(tea.PasteMsg{Content: path})
	x, y := labelCell(t, m, "[Image_1]")
	root.profiles.shown = 1 // exercise visibility independently of invalidation
	m.hover = hover{x: x, y: y, seq: 7, view: "box", picture: "picture"}
	for _, msg := range []tea.Msg{hoverMsg{7}, previewMsg{7, "new box", "new picture"}, pictureMsg{7}} {
		if _, cmd := root.Update(profileMsg{0, msg}); cmd != nil {
			t.Fatalf("hidden callback %T returned a command", msg)
		}
	}
	if m.hover.view != "box" || m.hover.picture != "picture" || m.hover.drawn {
		t.Fatal("hidden callbacks changed the preview")
	}
}

// A header advertising too many original pixels must never reach Decode.
// A tiny registered fixture proves rejection before allocation, without
// allocating a huge source image just to test the limit.
func TestPreviewRejectsLargeSources(t *testing.T) {
	for _, config := range []image.Config{
		{Width: previewMaxDimension + 1, Height: 1},
		{Width: 1, Height: previewMaxDimension + 1},
		{Width: 8192, Height: 8192},
		{Width: int(^uint(0) >> 1), Height: int(^uint(0) >> 1)},
		{Width: 0, Height: 1},
		{Width: 1, Height: -1},
	} {
		t.Run(fmt.Sprintf("%dx%d", config.Width, config.Height), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "source")
			magic := "preview-limit:" + path
			decoded := false
			image.RegisterFormat(magic, magic, func(io.Reader) (image.Image, error) {
				decoded = true
				return image.NewRGBA(image.Rect(0, 0, 1, 1)), nil
			}, func(io.Reader) (image.Config, error) { return config, nil })
			if err := os.WriteFile(path, []byte(magic), 0600); err != nil {
				t.Fatal(err)
			}
			if view, sixel := preview(path, graphics{}); view != "" || sixel != "" || decoded {
				t.Fatal("an unsafe source reached Decode or produced a preview")
			}
		})
	}
	t.Run("file size", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "large.png")
		writePNG(t, path, 1, 1, color.White)
		if err := os.Truncate(path, previewMaxBytes+1); err != nil {
			t.Fatal(err)
		}
		if view, sixel := preview(path, graphics{}); view != "" || sixel != "" {
			t.Fatal("an oversized file produced a preview")
		}
	})
}

func TestPreviewBoundsConcurrentWork(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source")
	magic := "preview-concurrency:" + path
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan string, 1)
	image.RegisterFormat(magic, magic, func(io.Reader) (image.Image, error) {
		close(entered)
		<-release
		return image.NewRGBA(image.Rect(0, 0, 1, 1)), nil
	}, func(io.Reader) (image.Config, error) { return image.Config{Width: 1, Height: 1}, nil })
	if err := os.WriteFile(path, []byte(magic), 0600); err != nil {
		t.Fatal(err)
	}
	go func() {
		view, _ := preview(path, graphics{})
		done <- view
	}()
	<-entered
	// This call must return immediately, not wait for the active decoder.
	view, sixel := preview(path, graphics{})
	close(release)
	if view != "" || sixel != "" {
		t.Error("a busy decoder must skip another preview")
	}
	if <-done == "" {
		t.Fatal("the active preview did not finish")
	}
	valid := filepath.Join(t.TempDir(), "small.png")
	writePNG(t, valid, 1, 1, color.White)
	if view, _ := preview(valid, graphics{}); view == "" {
		t.Fatal("finished work did not release the decode slot")
	}
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
