package tui

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/gif" // the pictures a preview can show
	_ "image/jpeg"
	"image/png"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
	"github.com/gen2brain/avif"
	"github.com/gen2brain/heic"
	"github.com/srwiley/oksvg"
	"github.com/srwiley/rasterx"
	_ "golang.org/x/image/bmp" // the kinds Go does not read by itself
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"

	"ouroboros/plugin"
)

// A picture that was dropped or pasted shows as its label, [Image_n]. The
// mouse that rests on such a label for a moment shows the picture above it,
// small. A terminal that draws sixels, as foot does, gets the picture as it
// is: the UI keeps a box of blank cells free for it, and once the box is on
// the screen the picture is drawn into it. iTerm2 draws no sixel, but pictures
// of its own, which go into such a box just the same. Any other terminal gets
// it in cells of two pixels each: the upper half of ▀ in the color of its
// foreground, the lower in that of its background. Once the mouse moves, or a
// key or the wheel is used, it is gone; a picture of a protocol is not part
// of what the UI draws, so the screen is drawn anew. A picture that cannot be
// read shows nothing; a PDF page is drawn with pdftoppm, where the system has
// it.

// hoverDelay is how long the mouse rests on a label before its picture shows.
// Tests shorten it.
var hoverDelay = time.Second

// pictureDelay is how long the box for a picture of a protocol is on the
// screen before the picture is drawn into it: the frame with the box must be
// written first, or it would write its blanks over the picture.
const pictureDelay = 50 * time.Millisecond

// The preview of cells is at most so many cells wide and high, and that of a
// picture of a protocol at most so many pixels wide and high.
const previewCols, previewRows, sixelPixels = 64, 40, 240

// Bound the source, not just the thumbnail: decoding allocates the original
// pixels. Only one preview may decode/render at a time across all profiles;
// a busy preview is skipped rather than queued behind an obsolete hover.
const previewMaxBytes = 32 << 20
const previewMaxPixels = 16 << 20
const previewMaxDimension = 16384

var previewWork = make(chan struct{}, 1)

// terminalQuery asks the terminal whether it draws sixels, how large its cells
// are in pixels, and whether it draws pictures of its own, as iTerm2 does: see
// graphics.
// 16 reports the cell size in pixels; ansi deprecated its name for it.
var terminalQuery = ansi.RequestPrimaryDeviceAttributes + ansi.WindowOp(16) +
	ansi.ITerm2("ReportCellSize")

// graphics is what the terminal told of its pictures, and the room a preview
// has in the window.
type graphics struct {
	sixel  bool
	inline bool // it draws pictures of its own, as iTerm2 does
	cellW  int  // the size of a cell in pixels, 0 while not known
	cellH  int
	roomW  int // the cells the window is wide and high, 0 while not known
	roomH  int
}

// cell is the size of a cell in pixels, as the terminal told it or as most are.
func (g graphics) cell() (int, int) {
	if g.cellW <= 0 || g.cellH <= 0 {
		return 10, 20
	}
	return g.cellW, g.cellH
}

// hover is where the mouse rests and the preview it shows there, if any.
type hover struct {
	x, y    int
	seq     int    // counts the moves, so that a late tick of an old one is dropped
	view    string // the preview shown, boxed
	picture string // the sequence that draws the picture into the box of view, if one
	drawn   bool   // the picture is on the screen
}

// hoverMsg is the tick of a mouse that rested since the move seq.
type hoverMsg struct{ seq int }

// previewMsg is the picture of the label the mouse rests on since the move
// seq: the box to show, "" if it could not be read, and the sequence that
// draws into it, if any.
type previewMsg struct {
	seq           int
	view, picture string
}

// pictureMsg is the tick of the box of a picture of a protocol of the move seq
// on the screen.
type pictureMsg struct{ seq int }

// told takes what the terminal told of its pictures, for every profile.
func (m *Model) told(msg tea.Msg) {
	for _, o := range m.siblings() {
		switch msg := msg.(type) {
		case uv.PrimaryDeviceAttributesEvent:
			o.graphics.sixel = slices.Contains(msg, 4)
		case uv.CellSizeEvent:
			o.graphics.cellW, o.graphics.cellH = msg.Width, msg.Height
		case uv.UnknownOscEvent:
			if w, h, ok := reportCellSize(string(msg)); ok {
				o.graphics.inline = true
				o.graphics.cellW, o.graphics.cellH = w, h
			}
		}
	}
}

// reportCellSize reads iTerm2's answer to the ReportCellSize probe: the height
// and width of a cell in points and the pixels a point has, as the pixels of a
// cell. A terminal that does not answer, as one behind tmux does not, gives
// false, and its preview stays what it was.
func reportCellSize(osc string) (w, h int, ok bool) {
	body := strings.TrimPrefix(strings.TrimPrefix(osc, "\x1b]"), "\x9d")
	body, found := strings.CutPrefix(body, "1337;ReportCellSize=")
	if !found {
		return 0, 0, false
	}
	if i := strings.IndexAny(body, "\a\x1b"); i >= 0 {
		body = body[:i]
	}
	fields := strings.Split(body, ";")
	if len(fields) < 2 {
		return 0, 0, false
	}
	height, errH := strconv.ParseFloat(fields[0], 64)
	width, errW := strconv.ParseFloat(fields[1], 64)
	if errH != nil || errW != nil || height <= 0 || width <= 0 {
		return 0, 0, false
	}
	scale := 1.0
	if len(fields) > 2 {
		if s, err := strconv.ParseFloat(fields[2], 64); err == nil && s > 0 {
			scale = s
		}
	}
	w = max(1, int(math.Round(width*scale)))
	h = max(1, int(math.Round(height*scale)))
	return w, h, true
}

// rest notes the mouse at x and y, without a button down: what it showed is
// gone, and if it stays, its tick comes.
func (m *Model) rest(x, y int) tea.Cmd {
	if m.hover.x == x && m.hover.y == y && m.hover.view != "" {
		return nil
	}
	cmd := m.unhover()
	m.hover.x, m.hover.y = x, y
	seq := m.hover.seq
	return tea.Batch(cmd, tea.Tick(hoverDelay, func(time.Time) tea.Msg { return hoverMsg{seq} }))
}

// unhover takes the preview away, and the tick of where the mouse was. A
// picture of a protocol that was drawn goes with the screen drawn anew.
func (m *Model) unhover() tea.Cmd {
	drawn := m.hover.drawn
	m.hover = hover{x: m.hover.x, y: m.hover.y, seq: m.hover.seq + 1}
	if drawn {
		return tea.ClearScreen
	}
	return nil
}

// hovered reads the picture of the label under the mouse that rested.
func (m *Model) hovered(msg hoverMsg) tea.Cmd {
	if msg.seq != m.hover.seq || !m.shown() || m.screen != screenChat || m.pressed != nil {
		return nil
	}
	path, ok := m.pictureAt(m.hover.x, m.hover.y)
	if !ok {
		return nil
	}
	seq, g := m.hover.seq, m.graphics
	g.roomW, g.roomH = m.width, m.height // a preview larger than that would be cut
	return func() tea.Msg {
		view, picture := preview(path, g)
		return previewMsg{seq, view, picture}
	}
}

// previewed shows the picture read for where the mouse still rests: one of a
// protocol once its box is on the screen.
func (m *Model) previewed(msg previewMsg) tea.Cmd {
	if msg.seq != m.hover.seq || !m.shown() || m.screen != screenChat {
		return nil
	}
	m.hover.view, m.hover.picture = msg.view, msg.picture
	if msg.picture == "" {
		return nil
	}
	return tea.Tick(pictureDelay, func(time.Time) tea.Msg { return pictureMsg{msg.seq} })
}

// drawPicture draws the picture into its box, where the box still is: at the
// first cell inside its border, the cursor kept where the UI has it.
func (m *Model) drawPicture(msg pictureMsg) tea.Cmd {
	if msg.seq != m.hover.seq || !m.shown() || m.hover.picture == "" || m.screen != screenChat {
		return nil
	}
	x, y := m.previewAt()
	m.hover.drawn = true
	return tea.Raw(ansi.SaveCursor + ansi.CursorPosition(x+2, y+2) + m.hover.picture + ansi.RestoreCursor)
}

// pictureAt is the file of the picture whose label or path is at x and y on
// the chat, as it is drawn: in the history or in the composer.
func (m *Model) pictureAt(x, y int) (string, bool) {
	lines := strings.Split(m.chatScreen(), "\n")
	if y < 0 || y >= len(lines) {
		return "", false
	}
	line := ansi.Strip(lines[y])
	for _, at := range labels.FindAllStringIndex(line, -1) {
		from := ansi.StringWidth(line[:at[0]])
		if x < from || x >= from+at[1]-at[0] {
			continue
		}
		path, ok := m.chat.attachment(line[at[0]:at[1]])
		return path, ok && plugin.IsPicture(path)
	}
	return picturePathAt(line, x, m.chat.directory)
}

// pictureNames keeps quoted paths with spaces together. Markdown punctuation
// separates bare paths from the surrounding prose; ANSI has already been removed.
var pictureNames = regexp.MustCompile("\"(?:[^\"\\\\]|\\\\.)+\"|'[^']+'|`[^`]+`|(?:\\\\.|[^\\s\"'`<>\\[\\]()])+")

// previewable reports whether a preview is made of the file at path: of a
// picture, and of a PDF, whose first page it shows. A PDF stays a file
// attachment in the chat; it is no picture that is handed to a model.
func previewable(path string) bool {
	return plugin.IsPicture(path) || strings.EqualFold(filepath.Ext(path), ".pdf")
}

// picturePathAt resolves only the name under the mouse, never a remote URL.
// Relative names belong to the conversation's project, not the UI process cwd.
func picturePathAt(line string, x int, directory string) (string, bool) {
	for _, at := range pictureNames.FindAllStringIndex(line, -1) {
		name := line[at[0]:at[1]]
		from := ansi.StringWidth(line[:at[0]])
		if x < from || x >= from+ansi.StringWidth(name) {
			continue
		}
		if name[0] == '`' {
			name = strings.Trim(name, "`")
		} else if parsed, ok := words(name); ok && len(parsed) == 1 {
			name = parsed[0]
		}
		name = strings.TrimRight(name, ".,;:!?")
		path := plugin.LocalPath(name)
		if !filepath.IsAbs(path) {
			if directory == "" || strings.Contains(name, "://") {
				return "", false
			}
			path = filepath.Join(directory, path)
		}
		if !previewable(path) {
			return "", false
		}
		if file, ok := attachmentPath(path); ok {
			return file, true
		}
		return "", false
	}
	return "", false
}

// previewAt is where the preview lies: above the mouse, or below it where
// there is no room above, and inside the window.
func (m *Model) previewAt() (x, y int) {
	w, h := lipgloss.Width(m.hover.view), lipgloss.Height(m.hover.view)
	x = min(max(0, m.hover.x-w/2), max(0, m.width-w))
	y = m.hover.y - h
	if y < 0 {
		y = min(m.hover.y+1, max(0, m.height-h))
	}
	return x, y
}

// withPreview lays the preview over the chat.
func (m *Model) withPreview(screen string) string {
	if m.hover.view == "" {
		return screen
	}
	x, y := m.previewAt()
	canvas := lipgloss.NewCanvas(m.width, m.height)
	canvas.Compose(lipgloss.NewCompositor(
		lipgloss.NewLayer(screen),
		lipgloss.NewLayer(m.hover.view).X(x).Y(y).Z(1),
	))
	return lipgloss.NewStyle().Width(m.width).Height(m.height).MaxHeight(m.height).Render(canvas.Render())
}

// preview reads the picture at path and draws it boxed for a terminal of g,
// and if it draws pictures of its own, the box blank and the sequence that
// draws the picture into it. "" if the picture cannot be read.
func preview(path string, g graphics) (view, picture string) {
	select {
	case previewWork <- struct{}{}:
		defer func() { <-previewWork }()
	default:
		return "", ""
	}
	f, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > previewMaxBytes {
		return "", ""
	}
	img, err := readPreview(f, path)
	if err != nil || img.Bounds().Empty() {
		return "", ""
	}
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(ui.line)
	if !g.sixel && !g.inline {
		cols, rows := previewCols, previewRows
		if g.roomW > 0 && g.roomH > 0 { // a larger box would be cut by the window
			cols, rows = min(cols, g.roomW-2), min(rows, g.roomH-2)
		}
		return box.Render(halfBlocks(img, max(1, cols), 2*max(1, rows))), ""
	}
	w, h := fit(img.Bounds(), sixelPixels, sixelPixels, false)
	cw, ch := g.cell()
	if g.sixel {
		// The cells that the picture covers, also the last band of six
		// pixels of it, whole.
		cols, rows := (w+cw-1)/cw, ((h+5)/6*6+ch-1)/ch
		return blankBox(box, cols, rows), encodeSixel(scaled(img, w, h))
	}
	// iTerm2 draws a PNG of its own, of as many pixels as the cells of the
	// box have: on a screen of two pixels a point, that is what sharp means.
	cols, rows := (w+cw-1)/cw, (h+ch-1)/ch
	pw, ph := fit(img.Bounds(), cols*cw, rows*ch, false)
	return blankBox(box, cols, rows), inlineImage(encodePNG(scaled(img, pw, ph)), cols, rows)
}

// blankBox is a box of cols×rows blank cells, for a picture of a protocol to
// be drawn into.
func blankBox(box lipgloss.Style, cols, rows int) string {
	blank := strings.Repeat(strings.Repeat(" ", cols)+"\n", rows)
	return box.Render(strings.TrimSuffix(blank, "\n"))
}

// inlineImage is iTerm2's sequence for a PNG drawn into a box of cols×rows
// cells, as sharp as the box is.
func inlineImage(data []byte, cols, rows int) string {
	return fmt.Sprintf("\x1b]1337;File=inline=1;width=%d;height=%d;preserveAspectRatio=1:%s\a",
		cols, rows, base64.StdEncoding.EncodeToString(data))
}

// encodePNG is img as a PNG, which iTerm2 draws where a sixel terminal takes a
// sixel.
func encodePNG(img image.Image) []byte {
	var buf bytes.Buffer
	_ = png.Encode(&buf, img) // a picture of pixels, into memory: cannot fail
	return buf.Bytes()
}

// readPreview reads the picture at path from f. The kinds Go reads are bounded
// by their header before they are decoded; a vector or a page is drawn in a
// size that is bounded as well. An unreadable file, a missing decoder and a
// missing renderer only keep the preview away, never the UI.
func readPreview(f *os.File, path string) (image.Image, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".svg":
		return svgPreview(f)
	case ".pdf":
		return pdfPreview(path)
	case ".avif":
		return boundedPreview(f, avif.DecodeConfig, func() (image.Image, error) {
			return avif.Decode(f, avif.Options{AutoRotate: true})
		})
	case ".heic":
		return boundedPreview(f, heic.DecodeConfig, func() (image.Image, error) { return heic.Decode(f) })
	}
	return boundedPreview(f, imageConfig, func() (image.Image, error) {
		img, _, err := image.Decode(f)
		return img, err
	})
}

// imageConfig is image.DecodeConfig without the name of the kind.
func imageConfig(r io.Reader) (image.Config, error) {
	c, _, err := image.DecodeConfig(r)
	return c, err
}

// boundedPreview checks the header of f against the limits of a preview and
// decodes the picture from the start of f.
func boundedPreview(f io.ReadSeeker, config func(io.Reader) (image.Config, error), decode func() (image.Image, error)) (image.Image, error) {
	c, err := config(f)
	if err != nil {
		return nil, err
	}
	if c.Width <= 0 || c.Height <= 0 || c.Width > previewMaxDimension ||
		c.Height > previewMaxDimension || c.Width > previewMaxPixels/c.Height {
		return nil, errors.New("a picture of that size is not shown")
	}
	if _, err := f.Seek(0, 0); err != nil {
		return nil, err
	}
	return decode()
}

// svgRaster is the longer side in pixels an SVG is drawn at: both forms of
// the preview are smaller than that.
const svgRaster = 512

// svgMaxBytes bounds an SVG tighter than a picture: rasterizing many paths
// takes time, and one preview slot serves every hover.
const svgMaxBytes = 2 << 20

// svgPreview draws the shapes of the SVG in f. It is a vector image, so it is
// rasterized here, with oksvg and rasterx alone: no script it holds runs and
// nothing it names elsewhere is loaded.
func svgPreview(f io.Reader) (image.Image, error) {
	data, err := io.ReadAll(io.LimitReader(f, svgMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > svgMaxBytes {
		return nil, errors.New("an SVG of that size is not shown")
	}
	icon, err := oksvg.ReadIconStream(bytes.NewReader(data), oksvg.IgnoreErrorMode)
	if err != nil {
		return nil, err
	}
	w, h := icon.ViewBox.W, icon.ViewBox.H
	if math.IsNaN(w) || math.IsNaN(h) || w <= 0 || h <= 0 ||
		w > previewMaxDimension || h > previewMaxDimension {
		return nil, errors.New("an SVG needs a viewBox of a size that can be shown")
	}
	tw, th := fit(image.Rect(0, 0, int(w+0.5), int(h+0.5)), svgRaster, svgRaster, false)
	img := image.NewRGBA(image.Rect(0, 0, tw, th))
	icon.SetTarget(0, 0, float64(tw), float64(th))
	icon.Draw(rasterx.NewDasher(tw, th, rasterx.NewScannerGV(tw, th, img, img.Bounds())), 1)
	return img, nil
}

// pdfRaster is the longer side in pixels a PDF page is drawn at: both forms
// of the preview are smaller than that, and the renderer is bounded before it
// writes a picture that would be too large.
const pdfRaster = 512

// pdfTimeout bounds how long the first page of a PDF may take to be drawn. It
// is a variable so that a test can shorten it.
var pdfTimeout = 10 * time.Second

// pdfRenderer is the program that draws a PDF page. It is looked up once, so a
// poppler installed while ouro runs is found after a restart, not on the next
// hover. A test puts its own renderer in its place.
var pdfRenderer = sync.OnceValue(lookPDFRenderer)

// lookPDFRenderer is the path of pdftoppm, "" where it is not installed.
func lookPDFRenderer() string {
	path, err := exec.LookPath("pdftoppm")
	if err != nil {
		return ""
	}
	return path
}

// pdfPreview draws the first page of the PDF at path with pdftoppm, which only
// some systems have: without it the preview stays away, and the PDF is still
// handed on as the file it is.
func pdfPreview(path string) (image.Image, error) {
	bin := pdfRenderer()
	if bin == "" {
		return nil, exec.ErrNotFound
	}
	dir, err := os.MkdirTemp("", "ouro-pdf-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	out := filepath.Join(dir, "page")
	ctx, cancel := context.WithTimeout(context.Background(), pdfTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "-png", "-f", "1", "-l", "1", "-singlefile",
		"-scale-to", strconv.Itoa(pdfRaster), path, out)
	// A timeout takes the renderer's children along with it.
	detached(cmd)
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	f, err := os.Open(out + ".png")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || info.Size() > previewMaxBytes {
		return nil, errors.New("the first page is too large to show")
	}
	return boundedPreview(f, imageConfig, func() (image.Image, error) {
		img, _, err := image.Decode(f)
		return img, err
	})
}

// fit is the size of a picture of bounds b in at most w×h, as it is, and
// larger than it is only if grow.
func fit(b image.Rectangle, w, h int, grow bool) (int, int) {
	scale := min(float64(w)/float64(b.Dx()), float64(h)/float64(b.Dy()))
	if !grow {
		scale = min(scale, 1)
	}
	return max(1, int(float64(b.Dx())*scale+0.5)), max(1, int(float64(b.Dy())*scale+0.5))
}

// scaled is img in w×h pixels, each the average of those it covers, on the
// background of the UI where they are transparent.
func scaled(img image.Image, w, h int) *image.RGBA {
	b := img.Bounds()
	back := opaque(ui.background)
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			out.SetRGBA(x, y, average(img, b, x*b.Dx()/w, (x+1)*b.Dx()/w, y*b.Dy()/h, (y+1)*b.Dy()/h, back))
		}
	}
	return out
}

// halfBlocks draws img in at most cols cells wide and pixels high, two
// pixels a cell.
func halfBlocks(img image.Image, cols, pixels int) string {
	w, h := fit(img.Bounds(), cols, pixels, true)
	h += h % 2 // a cell has two pixels
	px := scaled(img, w, h)
	var s strings.Builder
	for y := 0; y < h; y += 2 {
		if y > 0 {
			s.WriteByte('\n')
		}
		for x := range w {
			top, bottom := px.RGBAAt(x, y), px.RGBAAt(x, y+1)
			fmt.Fprintf(&s, "\x1b[38;2;%d;%d;%dm\x1b[48;2;%d;%d;%dm▀", top.R, top.G, top.B, bottom.R, bottom.G, bottom.B)
		}
		s.WriteString("\x1b[m")
	}
	return s.String()
}

// average is the color of the pixels of img from x0 to x1 and y0 to y1 of
// its bounds b, on back where they are transparent. A large picture is
// sampled, at most 8×8 pixels each.
func average(img image.Image, b image.Rectangle, x0, x1, y0, y1 int, back color.RGBA) color.RGBA {
	x1, y1 = max(x1, x0+1), max(y1, y0+1)
	sx, sy := max(1, (x1-x0)/8), max(1, (y1-y0)/8)
	var r, g, bl, n uint32
	for y := y0; y < y1; y += sy {
		for x := x0; x < x1; x += sx {
			c := color.NRGBAModel.Convert(img.At(b.Min.X+x, b.Min.Y+y)).(color.NRGBA)
			a := uint32(c.A)
			r += (uint32(c.R)*a + uint32(back.R)*(255-a)) / 255
			g += (uint32(c.G)*a + uint32(back.G)*(255-a)) / 255
			bl += (uint32(c.B)*a + uint32(back.B)*(255-a)) / 255
			n++
		}
	}
	return color.RGBA{uint8(r / n), uint8(g / n), uint8(bl / n), 255}
}

// opaque is c as 8 bits a channel.
func opaque(c color.Color) color.RGBA {
	r, g, b, _ := c.RGBA()
	return color.RGBA{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8), 255}
}

// encodeSixel writes img as a sixel: in at most 256 colors, those it has
// most of, the rest dithered with them.
func encodeSixel(img *image.RGBA) string {
	b := img.Bounds()
	p := dither(img, paletteOf(img, 256))
	w, h := b.Dx(), b.Dy()
	var s strings.Builder
	// The pixels of no color are left as they are; the size is w×h.
	fmt.Fprintf(&s, "\x1bP0;1;0q\"1;1;%d;%d", w, h)
	for i, c := range p.Palette {
		r, g, bl, _ := c.RGBA()
		fmt.Fprintf(&s, "#%d;2;%d;%d;%d", i, r*100/0xffff, g*100/0xffff, bl*100/0xffff)
	}
	used := make([]bool, len(p.Palette))
	for y0 := 0; y0 < h; y0 += 6 {
		clear(used)
		for y := y0; y < min(y0+6, h); y++ {
			for _, i := range p.Pix[y*p.Stride : y*p.Stride+w] {
				used[i] = true
			}
		}
		first := true
		for i, u := range used {
			if !u {
				continue
			}
			if !first {
				s.WriteByte('$') // back to the start of the band, for the next color
			}
			first = false
			fmt.Fprintf(&s, "#%d", i)
			run, last := 0, byte(0)
			flush := func() {
				switch {
				case run > 3:
					fmt.Fprintf(&s, "!%d%c", run, last)
				default:
					s.WriteString(strings.Repeat(string(last), run))
				}
			}
			for x := range w {
				bits := byte(0)
				for k := range 6 {
					if y := y0 + k; y < h && int(p.Pix[y*p.Stride+x]) == i {
						bits |= 1 << k
					}
				}
				if ch := 63 + bits; ch == last {
					run++
				} else {
					flush()
					run, last = 1, ch
				}
			}
			flush()
		}
		s.WriteByte('-') // the next band
	}
	s.WriteString("\x1b\\")
	return s.String()
}

// dither is img in the colors of pal, the error of each pixel spread to
// those right of and below it, as Floyd and Steinberg do.
func dither(img *image.RGBA, pal color.Palette) *image.Paletted {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	out := image.NewPaletted(image.Rect(0, 0, w, h), pal)
	cols := make([][3]int, len(pal))
	for i, c := range pal {
		r, g, bl, _ := c.RGBA()
		cols[i] = [3]int{int(r >> 8), int(g >> 8), int(bl >> 8)}
	}
	// The errors carried to this line and the next, in 1/16, one pixel
	// more on each side.
	line, next := make([][3]int, w+2), make([][3]int, w+2)
	for y := range h {
		for x := range w {
			o := img.PixOffset(b.Min.X+x, b.Min.Y+y)
			var want [3]int
			for c := range 3 {
				want[c] = min(255, max(0, int(img.Pix[o+c])+line[x+1][c]/16))
			}
			best, dist := 0, 1<<30
			for i, c := range cols {
				dr, dg, db := want[0]-c[0], want[1]-c[1], want[2]-c[2]
				if d := dr*dr + dg*dg + db*db; d < dist {
					best, dist = i, d
				}
			}
			out.Pix[y*out.Stride+x] = uint8(best)
			for c := range 3 {
				e := want[c] - cols[best][c]
				line[x+2][c] += e * 7
				next[x][c] += e * 3
				next[x+1][c] += e * 5
				next[x+2][c] += e
			}
		}
		line, next = next, line
		clear(next)
	}
	return out
}

// paletteOf is a palette of at most n colors for img, by median cut: the
// pixels are split in two at the middle of the channel they differ most in,
// the box with the widest range first, until there are n boxes, and each
// box gives the average of its pixels.
func paletteOf(img *image.RGBA, n int) color.Palette {
	px := make([][3]uint8, 0, len(img.Pix)/4)
	for i := 0; i < len(img.Pix); i += 4 {
		px = append(px, [3]uint8{img.Pix[i], img.Pix[i+1], img.Pix[i+2]})
	}
	// widest is the channel the pixels of box differ most in, and by how much.
	widest := func(box [][3]uint8) (int, int) {
		lo, hi := [3]uint8{255, 255, 255}, [3]uint8{}
		for _, p := range box {
			for c := range 3 {
				lo[c], hi[c] = min(lo[c], p[c]), max(hi[c], p[c])
			}
		}
		at, wide := 0, -1
		for c := range 3 {
			if d := int(hi[c]) - int(lo[c]); d > wide {
				at, wide = c, d
			}
		}
		return at, wide
	}
	boxes := [][][3]uint8{px}
	for len(boxes) < n {
		at, channel, wide := -1, 0, 0
		for i, box := range boxes {
			if c, w := widest(box); len(box) > 1 && w > wide {
				at, channel, wide = i, c, w
			}
		}
		if at < 0 {
			break // every box has one color
		}
		box := boxes[at]
		slices.SortFunc(box, func(a, b [3]uint8) int { return int(a[channel]) - int(b[channel]) })
		boxes[at], boxes = box[:len(box)/2], append(boxes, box[len(box)/2:])
	}
	p := make(color.Palette, 0, len(boxes))
	for _, box := range boxes {
		var sum [3]int
		for _, q := range box {
			sum[0], sum[1], sum[2] = sum[0]+int(q[0]), sum[1]+int(q[1]), sum[2]+int(q[2])
		}
		p = append(p, color.RGBA{uint8(sum[0] / len(box)), uint8(sum[1] / len(box)), uint8(sum[2] / len(box)), 255})
	}
	return p
}
