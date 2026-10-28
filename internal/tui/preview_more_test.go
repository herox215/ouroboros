package tui

import (
	"errors"
	"image"
	"image/color"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// failingReader fails every read and seek, as a file that went away would.
type failingReader struct{}

func (failingReader) Read([]byte) (int, error)          { return 0, errors.New("gone") }
func (failingReader) Seek(int64, int) (int64, error)    { return 0, errors.New("gone") }
func (failingReader) ReadAt([]byte, int64) (int, error) { return 0, errors.New("gone") }

// The mouse that rests asks for its preview once the delay is over, and a
// mouse that rests where its preview shows already asks nothing again.
func TestRestingTicksOnce(t *testing.T) {
	was := hoverDelay
	hoverDelay = 0
	t.Cleanup(func() { hoverDelay = was })
	m, _ := newModel(t)
	// Nothing showed before: the tick is all there is to it.
	if msg, ok := m.rest(3, 4)().(hoverMsg); !ok || msg.seq != m.hover.seq {
		t.Fatalf("the rest brought %#v", msg)
	}
	m.hover.view = "a picture"
	if cmd := m.rest(3, 4); cmd != nil {
		t.Fatal("resting where the preview shows asks again")
	}
}

// The preview lies below the mouse where there is no room above it, and no
// picture is under the mouse off the chat or beside a label.
func TestPreviewBelowAndBeside(t *testing.T) {
	m, _ := newModel(t)
	m.hover = hover{x: 10, y: 1, view: "a\nb\nc"}
	if x, y := m.previewAt(); y != 2 || x != 10 {
		t.Fatalf("the preview is at %d,%d", x, y)
	}
	if _, ok := m.pictureAt(0, -1); ok {
		t.Fatal("a picture above the chat")
	}
	if _, ok := m.pictureAt(0, 10000); ok {
		t.Fatal("a picture below the chat")
	}
	path := filepath.Join(t.TempDir(), "red.png")
	writePNG(t, path, 4, 4, color.NRGBA{255, 0, 0, 255})
	m.input.SetValue("look ")
	m.Update(tea.PasteMsg{Content: path})
	x, y := labelCell(t, m, "[Image_1]")
	if _, ok := m.pictureAt(x-2, y); ok {
		t.Fatal("a picture beside its label")
	}
	if got, ok := m.pictureAt(x, y); !ok || got != path {
		t.Fatalf("the label holds %q, %v", got, ok)
	}
}

// A file that fails while it is read shows no picture: neither one whose
// start cannot be found again, nor an SVG that cannot be read or parsed.
func TestPreviewOfFilesThatFail(t *testing.T) {
	config := func(io.Reader) (image.Config, error) { return image.Config{Width: 1, Height: 1}, nil }
	if _, err := boundedPreview(failingReader{}, config, nil); err == nil {
		t.Fatal("a file that cannot seek previews")
	}
	if _, err := svgPreview(failingReader{}); err == nil {
		t.Fatal("an SVG that cannot be read previews")
	}
	if _, err := svgPreview(strings.NewReader("<svg><g></svg")); err == nil {
		t.Fatal("an SVG that is no XML previews")
	}
}

// pdftoppm is found where it is installed, and missing elsewhere.
func TestPDFRendererIsLookedUp(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake renderer is a shell script")
	}
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	if got := lookPDFRenderer(); got != "" {
		t.Fatalf("a renderer was found in an empty path: %q", got)
	}
	want := fakePDFRenderer(t, dir, "exit 0")
	if got := lookPDFRenderer(); got != want {
		t.Fatalf("the renderer is %q, want %q", got, want)
	}
}

// A renderer that writes no page, or one too large, shows nothing, and
// neither does one without a place for its page.
func TestPDFRendererWithoutAPage(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake renderer is a shell script")
	}
	dir := t.TempDir()
	pdf := filepath.Join(dir, "one.pdf")
	if err := os.WriteFile(pdf, []byte("%PDF-1.4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	withPDFRenderer(t, fakePDFRenderer(t, dir, "exit 0"))
	if _, err := pdfPreview(pdf); err == nil {
		t.Fatal("a page that was not written previews")
	}

	huge := t.TempDir()
	withPDFRenderer(t, fakePDFRenderer(t, huge, `for last do :; done
dd if=/dev/zero of="$last.png" bs=1 count=0 seek=40000000 2>/dev/null`))
	if _, err := pdfPreview(pdf); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("a page of 40 MB: %v", err)
	}

	t.Setenv("TMPDIR", filepath.Join(dir, "missing"))
	if _, err := pdfPreview(pdf); err == nil {
		t.Fatal("a page without a place previews")
	}
}
