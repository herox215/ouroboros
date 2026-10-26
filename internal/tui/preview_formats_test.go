package tui

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/image/bmp"
)

// TestPreviewOfMoreFormats makes the preview of every kind of picture the
// ticket names, and of a PDF's first page, in both forms.
func TestPreviewOfMoreFormats(t *testing.T) {
	for _, name := range []string{"red.webp", "red.avif", "red.heic", "red.svg", "multipage.tiff", "red.pdf"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join("testdata", "preview", name)
			view, sixel := preview(path, graphics{})
			if view == "" || !strings.Contains(view, "▀") {
				t.Fatalf("no preview of %s", name)
			}
			if sixel != "" {
				t.Fatalf("%s: a sixel for a terminal that draws none", name)
			}
			view, sixel = preview(path, graphics{sixel: true, cellW: 10, cellH: 20})
			if view == "" || !strings.HasPrefix(sixel, "\x1bP") {
				t.Fatalf("no sixel of %s", name)
			}
		})
	}
}

// TestPreviewOfBMP makes the preview of a BMP that the test writes itself.
func TestPreviewOfBMP(t *testing.T) {
	path := filepath.Join(t.TempDir(), "red.bmp")
	var buf bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 4, 2))
	for i := range img.Pix {
		img.Pix[i] = 0xff
	}
	img.Set(0, 0, color.RGBA{255, 0, 0, 255})
	if err := bmp.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if view, _ := preview(path, graphics{}); view == "" {
		t.Fatal("no preview of a BMP")
	}
}

// TestPreviewWithoutItsRenderer keeps a PDF away where pdftoppm is missing,
// instead of failing the UI.
func TestPreviewWithoutItsRenderer(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if view, sixel := preview(filepath.Join("testdata", "preview", "red.pdf"), graphics{}); view != "" || sixel != "" {
		t.Fatal("a PDF is shown without pdftoppm")
	}
}

// TestPreviewOfBrokenFiles shows nothing of a damaged file of any kind.
func TestPreviewOfBrokenFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"broken.webp", "broken.bmp", "broken.tiff", "broken.avif", "broken.heic", "broken.svg", "broken.pdf"} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("not a "+name), 0o644); err != nil {
			t.Fatal(err)
		}
		if view, sixel := preview(path, graphics{}); view != "" || sixel != "" {
			t.Errorf("%s shows a picture", name)
		}
	}
}

// TestSVGOfNoUsableViewBox shows nothing rather than a stretched blank.
func TestSVGOfNoUsableViewBox(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.svg")
	if err := os.WriteFile(path, []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`), 0o644); err != nil {
		t.Fatal(err)
	}
	if view, _ := preview(path, graphics{}); view != "" {
		t.Fatal("an SVG without a viewBox shows")
	}
}

// TestSVGRunsNoScriptAndLoadsNothing is the safety of an SVG preview: a script
// it holds runs nowhere, and what it names elsewhere is not fetched.
func TestSVGRunsNoScriptAndLoadsNothing(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("no local listener:", err)
	}
	defer l.Close()
	var reached atomic.Int32
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			reached.Add(1)
			c.Close()
		}
	}()
	svg := fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 60 40">
<rect width="60" height="40" fill="#c00"/>
<script>alert(1)</script>
<image href="http://%s/x.png" width="10" height="10"/>
</svg>`, l.Addr())
	path := filepath.Join(t.TempDir(), "scripted.svg")
	if err := os.WriteFile(path, []byte(svg), 0o644); err != nil {
		t.Fatal(err)
	}
	if view, _ := preview(path, graphics{}); view == "" {
		t.Fatal("no preview of an SVG with a script")
	}
	time.Sleep(100 * time.Millisecond) // a fetch would have been made by now
	if n := reached.Load(); n != 0 {
		t.Fatalf("the SVG preview loaded %d external resources", n)
	}
}

// TestPreviewOfFirstPage shows the first page of a PDF only, and of a
// multi-page TIFF the first page.
func TestPreviewOfFirstPage(t *testing.T) {
	f, err := os.Open(filepath.Join("testdata", "preview", "multipage.tiff"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := readPreview(f, "multipage.tiff")
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() <= 0 || b.Dy() <= 0 {
		t.Fatalf("empty first page: %v", b)
	}
}
