package tui

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"image/color"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestPreviewOfMoreFormats makes the preview of every kind of picture the
// ticket names, and of a PDF's first page, in both forms. Each fixture is red,
// so a preview that only draws blanks fails it.
func TestPreviewOfMoreFormats(t *testing.T) {
	type picture struct {
		name, path string
	}
	pictures := []picture{
		{"red.webp", filepath.Join("testdata", "preview", "red.webp")},
		{"red.avif", filepath.Join("testdata", "preview", "red.avif")},
		{"red.heic", filepath.Join("testdata", "preview", "red.heic")},
		{"red.svg", filepath.Join("testdata", "preview", "red.svg")},
		{"multipage.tiff", filepath.Join("testdata", "preview", "multipage.tiff")},
	}
	bmp := filepath.Join(t.TempDir(), "red.bmp")
	writeBMP(t, bmp, false)
	pictures = append(pictures, picture{"red.bmp", bmp})
	pdftoppm, pdfErr := exec.LookPath("pdftoppm")
	if pdfErr != nil {
		t.Logf("no pdftoppm: the PDF case is skipped: %v", pdfErr)
	} else {
		pictures = append(pictures, picture{"red.pdf", filepath.Join("testdata", "preview", "red.pdf")})
	}
	for _, p := range pictures {
		t.Run(p.name, func(t *testing.T) {
			if p.name == "red.pdf" {
				withPDFRenderer(t, pdftoppm)
			}
			view, sixel := preview(p.path, graphics{})
			if view == "" || !strings.Contains(view, "▀") {
				t.Fatalf("no preview of %s", p.name)
			}
			if !showsRed(view) {
				t.Fatalf("%s: no red pixel in the preview", p.name)
			}
			if sixel != "" {
				t.Fatalf("%s: a sixel for a terminal that draws none", p.name)
			}
			view, sixel = preview(p.path, graphics{sixel: true, cellW: 10, cellH: 20})
			if view == "" || !strings.HasPrefix(sixel, "\x1bP") || len(sixel) < 20 {
				t.Fatalf("no sixel of %s", p.name)
			}
		})
	}
}

// TestPreviewOfRotatedAVIF applies the irot/imir orientation of an AVIF: the
// stored page is 64x32, the previewed one 32x64.
func TestPreviewOfRotatedAVIF(t *testing.T) {
	path := filepath.Join("testdata", "preview", "rotated.avif")
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := readPreview(f, path)
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 32 || b.Dy() != 64 {
		t.Fatalf("AVIF orientation is %v, want 32x64", b)
	}
}

// TestPreviewOfBMP makes the preview of a BMP that the test writes itself,
// bottom-up as encoders write it and top-down with a negative height.
func TestPreviewOfBMP(t *testing.T) {
	for _, topDown := range []bool{false, true} {
		name := "bottom-up"
		if topDown {
			name = "top-down"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "red.bmp")
			writeBMP(t, path, topDown)
			f, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			img, err := readPreview(f, path)
			f.Close()
			if err != nil {
				t.Fatal(err)
			}
			if !isRed(img.At(0, 0)) || !isBlue(img.At(0, 1)) {
				t.Fatalf("rows are %v and %v, want red above blue", img.At(0, 0), img.At(0, 1))
			}
			view, _ := preview(path, graphics{})
			if view == "" || !showsRed(view) {
				t.Fatal("no red preview of a BMP")
			}
			if _, sixel := preview(path, graphics{sixel: true, cellW: 10, cellH: 20}); !strings.HasPrefix(sixel, "\x1bP") {
				t.Fatal("no sixel of a BMP")
			}
		})
	}
}

// TestPreviewWithoutItsRenderer keeps a PDF away where pdftoppm is missing,
// instead of failing the UI.
func TestPreviewWithoutItsRenderer(t *testing.T) {
	withPDFRenderer(t, "")
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

// TestSVGSizeLimit keeps a huge SVG, whose many paths would hold the one
// preview slot for long, away before it is parsed.
func TestSVGSizeLimit(t *testing.T) {
	svg := `<svg xmlns="http://www.w3.org/2000/svg" width="4" height="4" viewBox="0 0 4 4">` +
		`<rect width="4" height="4" fill="#f00"/><!--` + strings.Repeat("x", svgMaxBytes) + `--></svg>`
	path := filepath.Join(t.TempDir(), "huge.svg")
	if err := os.WriteFile(path, []byte(svg), 0o644); err != nil {
		t.Fatal(err)
	}
	if view, sixel := preview(path, graphics{}); view != "" || sixel != "" {
		t.Fatal("an SVG over the size limit shows")
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
	view, _ := preview(path, graphics{})
	if view == "" {
		t.Fatal("no preview of an SVG with a script")
	}
	if !showsRed(view) {
		t.Fatal("no red pixel in the preview of the scripted SVG")
	}
	time.Sleep(100 * time.Millisecond) // a fetch would have been made by now
	if n := reached.Load(); n != 0 {
		t.Fatalf("the SVG preview loaded %d external resources", n)
	}
}

// TestPDFRendererBoundsThePage checks what the renderer is asked for: the
// first page only, and its longer side at pdfRaster before it writes a PNG
// that would be too large to decode.
func TestPDFRendererBoundsThePage(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake renderer is a shell script")
	}
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args")
	page := filepath.Join(dir, "red.png")
	writePNG(t, page, 4, 2, color.RGBA{255, 0, 0, 255})
	t.Setenv("FAKE_ARGS", argsFile)
	t.Setenv("FAKE_PNG", page)
	renderer := fakePDFRenderer(t, dir, `printf '%s\n' "$@" > "$FAKE_ARGS"
for last do :; done
cp "$FAKE_PNG" "$last.png"`)
	withPDFRenderer(t, renderer)
	pdf := filepath.Join(dir, "one.pdf")
	if err := os.WriteFile(pdf, []byte("%PDF-1.4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	view, _ := preview(pdf, graphics{})
	if view == "" || !showsRed(view) {
		t.Fatal("no preview from the fake renderer")
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	joined := " " + strings.Join(strings.Fields(string(args)), " ") + " "
	for _, want := range []string{" -f 1 ", " -l 1 ", " -singlefile ", " -scale-to 512 "} {
		if !strings.Contains(joined, want) {
			t.Errorf("the renderer got %q, without %q", joined, want)
		}
	}
	if strings.Contains(joined, " -r ") {
		t.Errorf("the renderer got %q, with a fixed resolution", joined)
	}
}

// TestPDFOversizedPage shows nothing of a page whose header advertises more
// pixels than a preview may decode, without decoding it.
func TestPDFOversizedPage(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake renderer is a shell script")
	}
	dir := t.TempDir()
	page := filepath.Join(dir, "huge.png")
	writeOversizedPNG(t, page)
	t.Setenv("FAKE_PNG", page)
	renderer := fakePDFRenderer(t, dir, `for last do :; done
cp "$FAKE_PNG" "$last.png"`)
	withPDFRenderer(t, renderer)
	pdf := filepath.Join(dir, "one.pdf")
	if err := os.WriteFile(pdf, []byte("%PDF-1.4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if view, sixel := preview(pdf, graphics{}); view != "" || sixel != "" {
		t.Fatal("an oversized page previews")
	}
}

// TestPDFTimeout ends a renderer that does not finish in time.
func TestPDFTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake renderer is a shell script")
	}
	dir := t.TempDir()
	renderer := fakePDFRenderer(t, dir, "sleep 30")
	withPDFRenderer(t, renderer)
	withPDFTimeout(t, 100*time.Millisecond)
	start := time.Now()
	if _, err := pdfPreview(filepath.Join(dir, "slow.pdf")); err == nil {
		t.Fatal("a renderer that does not finish was not ended")
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Fatalf("the timeout took %v", took)
	}
}

// TestPreviewOfFirstPage shows the first page of a PDF only, and of a
// multi-page TIFF the first page: the first pages differ from the second ones
// in size and color.
func TestPreviewOfFirstPage(t *testing.T) {
	t.Run("tiff", func(t *testing.T) {
		path := filepath.Join("testdata", "preview", "multipage.tiff")
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		img, err := readPreview(f, path)
		if err != nil {
			t.Fatal(err)
		}
		if b := img.Bounds(); b.Dx() != 4 || b.Dy() != 2 {
			t.Fatalf("first page is %v, want 4x2", b)
		}
		if !isRed(img.At(0, 0)) {
			t.Fatalf("first page is %v, want the red one", img.At(0, 0))
		}
	})
	t.Run("pdf", func(t *testing.T) {
		pdftoppm, err := exec.LookPath("pdftoppm")
		if err != nil {
			t.Skipf("no pdftoppm: %v", err)
		}
		withPDFRenderer(t, pdftoppm)
		path := filepath.Join("testdata", "preview", "multipage.pdf")
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		img, err := readPreview(f, path)
		if err != nil {
			t.Fatal(err)
		}
		b := img.Bounds()
		if b.Dx() <= b.Dy() {
			t.Fatalf("first page is %v, want the landscape red page", b)
		}
		if longer := max(b.Dx(), b.Dy()); longer != pdfRaster {
			t.Fatalf("first page is %v, want its longer side at %d", b, pdfRaster)
		}
		if c := img.At(b.Min.X+b.Dx()/2, b.Min.Y+b.Dy()/2); !isRed(c) {
			t.Fatalf("first page is %v, want the red one", c)
		}
	})
}

// withPDFRenderer puts bin in the place of the looked-up pdftoppm for one test.
func withPDFRenderer(t *testing.T, bin string) {
	t.Helper()
	old := pdfRenderer
	pdfRenderer = func() string { return bin }
	t.Cleanup(func() { pdfRenderer = old })
}

// withPDFTimeout shortens the time a test lets a renderer take.
func withPDFTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	old := pdfTimeout
	pdfTimeout = d
	t.Cleanup(func() { pdfTimeout = old })
}

// fakePDFRenderer writes a shell script at the place of pdftoppm.
func fakePDFRenderer(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "pdftoppm")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeBMP writes a 24-bit BMP of 4x2 pixels, red above blue, bottom-up or
// top-down: the encoder of x/image/bmp writes only bottom-up.
func writeBMP(t *testing.T, path string, topDown bool) {
	t.Helper()
	const width, height = 4, 2
	red := bytes.Repeat([]byte{0x00, 0x00, 0xff}, width)  // BGR
	blue := bytes.Repeat([]byte{0xff, 0x00, 0x00}, width) // BGR
	pixels := append(append([]byte{}, red...), blue...)
	if !topDown {
		pixels = append(append([]byte{}, blue...), red...)
	}
	h := make([]byte, 54)
	copy(h, "BM")
	binary.LittleEndian.PutUint32(h[2:], uint32(len(h)+len(pixels)))
	binary.LittleEndian.PutUint32(h[10:], uint32(len(h)))
	binary.LittleEndian.PutUint32(h[14:], 40)
	binary.LittleEndian.PutUint32(h[18:], width)
	size := int32(height)
	if topDown {
		size = -size
	}
	binary.LittleEndian.PutUint32(h[22:], uint32(size))
	binary.LittleEndian.PutUint16(h[26:], 1)
	binary.LittleEndian.PutUint16(h[28:], 24)
	binary.LittleEndian.PutUint32(h[34:], uint32(len(pixels)))
	if err := os.WriteFile(path, append(h, pixels...), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeOversizedPNG writes the header of a PNG whose side is one over the
// limit: DecodeConfig rejects it before any pixel is decoded.
func writeOversizedPNG(t *testing.T, path string) {
	t.Helper()
	var buf bytes.Buffer
	buf.WriteString("\x89PNG\r\n\x1a\n")
	chunk := func(typ string, data []byte) {
		var n [4]byte
		binary.BigEndian.PutUint32(n[:], uint32(len(data)))
		buf.Write(n[:])
		buf.WriteString(typ)
		buf.Write(data)
		crc := crc32.NewIEEE()
		crc.Write([]byte(typ))
		crc.Write(data)
		binary.BigEndian.PutUint32(n[:], crc.Sum32())
		buf.Write(n[:])
	}
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], previewMaxDimension+1)
	binary.BigEndian.PutUint32(ihdr[4:], previewMaxDimension+1)
	ihdr[8] = 8 // bits per sample
	ihdr[9] = 2 // truecolor
	chunk("IHDR", ihdr)
	chunk("IEND", nil)
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// showsRed reports whether the half-block preview has a red pixel, not just
// the ▀ of an empty or background-colored picture.
func showsRed(view string) bool {
	for _, m := range previewForeground.FindAllStringSubmatch(view, -1) {
		r, _ := strconv.Atoi(m[1])
		g, _ := strconv.Atoi(m[2])
		b, _ := strconv.Atoi(m[3])
		if r > 200 && g < 80 && b < 80 {
			return true
		}
	}
	return false
}

var previewForeground = regexp.MustCompile(`\x1b\[38;2;(\d+);(\d+);(\d+)m`)

// isRed reports a red pixel in the sense of the fixtures.
func isRed(c color.Color) bool {
	n := color.NRGBAModel.Convert(c).(color.NRGBA)
	return n.R > 200 && n.G < 80 && n.B < 80
}

// isBlue reports a blue pixel in the sense of the second pages.
func isBlue(c color.Color) bool {
	n := color.NRGBAModel.Convert(c).(color.NRGBA)
	return n.B > 200 && n.R < 80 && n.G < 80
}
