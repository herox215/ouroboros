//go:build cgo

package gui

import (
	"image/color"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// widgetApp is a test app that draws in the appearance pref, or in the
// standard theme of Fyne without one.
func widgetApp(t *testing.T, pref string) fyne.App {
	t.Helper()
	a := test.NewApp()
	t.Cleanup(a.Quit)
	if pref != "" {
		a.Settings().SetTheme(&ouroTheme{pref: pref})
	}
	return a
}

// countEscape installs an escape handler that counts its calls, and restores
// the one before at the end of the test.
func countEscape(t *testing.T) *int {
	t.Helper()
	before := escapeHandler
	t.Cleanup(func() { escapeHandler = before })
	n := 0
	escapeHandler = func() { n++ }
	return &n
}

func box(w, h float32) *canvas.Rectangle {
	r := canvas.NewRectangle(color.Black)
	r.SetMinSize(fyne.NewSize(w, h))
	return r
}

func key(name fyne.KeyName) *fyne.KeyEvent { return &fyne.KeyEvent{Name: name} }

func TestPressEscapeWithoutHandler(t *testing.T) {
	before := escapeHandler
	t.Cleanup(func() { escapeHandler = before })
	escapeHandler = nil
	pressEscape() // nothing to run, and no panic
}

func TestBorderlessEntryEscapeAndShortcuts(t *testing.T) {
	widgetApp(t, "light")
	escapes := countEscape(t)
	beforeShortcut := windowShortcut
	t.Cleanup(func() { windowShortcut = beforeShortcut })

	e := newBorderlessEntry(false)
	w := test.NewWindow(e)
	defer w.Close()
	w.Resize(fyne.NewSize(200, 60))
	e.SetText("hello")

	e.TypedKey(key(fyne.KeyEscape))
	if *escapes != 1 || e.Text != "hello" {
		t.Fatalf("escape ran %d times, text %q", *escapes, e.Text)
	}

	var seen []fyne.Shortcut
	window := true
	windowShortcut = func(s fyne.Shortcut) bool {
		seen = append(seen, s)
		return window
	}
	e.TypedShortcut(&fyne.ShortcutSelectAll{})
	if len(seen) != 1 || e.SelectedText() != "" {
		t.Fatalf("a shortcut of the window reached the entry: seen %d, selected %q", len(seen), e.SelectedText())
	}
	window = false
	e.TypedShortcut(&fyne.ShortcutSelectAll{})
	if len(seen) != 2 || e.SelectedText() != "hello" {
		t.Fatalf("the entry did not edit with the shortcut: seen %d, selected %q", len(seen), e.SelectedText())
	}
	windowShortcut = nil
	e.TypedShortcut(&fyne.ShortcutSelectAll{})
	if e.SelectedText() != "hello" {
		t.Fatal("without window shortcuts the entry does not edit")
	}
}

func TestPickerAndCheckEscape(t *testing.T) {
	widgetApp(t, "light")
	escapes := countEscape(t)

	var changed []string
	p := newPicker([]string{"a", "b"}, func(s string) { changed = append(changed, s) })
	if p.PlaceHolder != "(Select one)" {
		t.Fatalf("placeholder %q", p.PlaceHolder)
	}
	p.TypedKey(key(fyne.KeyEscape))
	if *escapes != 1 || len(changed) != 0 {
		t.Fatalf("escape %d, changed %v", *escapes, changed)
	}
	p.TypedKey(key(fyne.KeyRight))
	if p.Selected != "a" || len(changed) != 1 || *escapes != 1 {
		t.Fatalf("right arrow selected %q, changed %v, escape %d", p.Selected, changed, *escapes)
	}

	c := newCheck("label")
	if c.Text != "label" {
		t.Fatalf("check text %q", c.Text)
	}
	c.TypedKey(key(fyne.KeyEscape))
	if *escapes != 2 {
		t.Fatalf("escape from the check ran %d times", *escapes)
	}
	c.TypedKey(key(fyne.KeyA))
	if *escapes != 2 || c.Checked {
		t.Fatal("another key on the check escaped or checked it")
	}
}

func TestComposerGrowsAndShrinks(t *testing.T) {
	widgetApp(t, "light")
	e := newComposerEntry(func(string) {})
	w := test.NewWindow(e)
	defer w.Close()
	e.SetText(strings.Repeat("line\n", 20))
	e.Resize(fyne.NewSize(300, 100))
	if e.rows != composerRows {
		t.Fatalf("twenty lines show %d rows, want %d", e.rows, composerRows)
	}
	e.SetText("short")
	e.Resize(fyne.NewSize(301, 100))
	if e.rows != 1 {
		t.Fatalf("one short line shows %d rows", e.rows)
	}
	e.Resize(fyne.NewSize(301, 120)) // same width: no new wrapping
	if e.rows != 1 {
		t.Fatalf("rows %d after a height change", e.rows)
	}
}

func TestWrappedRows(t *testing.T) {
	widgetApp(t, "light")
	style := fyne.TextStyle{Monospace: true}
	if got := wrappedRows("anything at all", 0, style); got != 1 {
		t.Fatalf("zero width: %d rows", got)
	}
	if got := wrappedRows("anything at all", -10, style); got != 1 {
		t.Fatalf("negative width: %d rows", got)
	}
	if got := wrappedRows("", 100, style); got != 1 {
		t.Fatalf("empty text: %d rows", got)
	}
	if got := wrappedRows("a\nb\n", 100, style); got != 3 {
		t.Fatalf("three lines: %d rows", got)
	}
	long := strings.Repeat("x", 100)
	width := fyne.MeasureText(long, theme.TextSize(), style).Width
	if got := wrappedRows(long, width/4, style); got != 4 {
		t.Fatalf("a line four widths long: %d rows", got)
	}
}

func TestTextHelpers(t *testing.T) {
	widgetApp(t, "light")
	rich := text("v", sizeBody, theme.ColorNameForeground, fyne.TextStyle{Bold: true})
	seg := rich.Segments[0].(*widget.TextSegment)
	if rich.Wrapping != fyne.TextWrapWord || seg.Style.SizeName != sizeBody || !seg.Style.TextStyle.Bold {
		t.Fatal("rich text style")
	}
	if l := selectable("s", sizeBody); !l.Selectable || l.Wrapping != fyne.TextWrapWord || !l.TextStyle.Monospace {
		t.Fatal("selectable label")
	}
	if l := plain("p", sizeTitle, true); !l.TextStyle.Bold || l.SizeName != sizeTitle {
		t.Fatal("plain label")
	}
	if l := wrapped(muted("m", sizeTiny)); l.Wrapping != fyne.TextWrapWord || l.Importance != widget.LowImportance {
		t.Fatal("wrapped muted label")
	}
	if l := ellipsis("e", sizeSmall); l.Truncation != fyne.TextTruncateEllipsis {
		t.Fatal("ellipsis label")
	}
}

func TestTapTextHover(t *testing.T) {
	widgetApp(t, "light")
	taps := 0
	tt := newTapText("open", sizeBody, fyne.TextStyle{}, func() { taps++ })
	if tt.Cursor() != desktop.PointerCursor {
		t.Fatal("tap text has no pointer cursor")
	}
	tt.MouseIn(&desktop.MouseEvent{})
	if !tt.hovered || tt.Importance != widget.HighImportance {
		t.Fatal("hover does not take the accent")
	}
	tt.MouseMoved(&desktop.MouseEvent{})
	if !tt.hovered {
		t.Fatal("moving loses the hover")
	}
	tt.MouseOut()
	if tt.hovered || tt.Importance != widget.MediumImportance {
		t.Fatal("leaving keeps the accent")
	}
	tt.Tapped(nil)
	if taps != 1 {
		t.Fatalf("taps %d", taps)
	}
	newTapText("none", sizeBody, fyne.TextStyle{}, nil).Tapped(nil) // no tap handler, no panic
}

func TestTapBoxLookAndKeys(t *testing.T) {
	widgetApp(t, "light")
	escapes := countEscape(t)
	taps, looks := 0, 0
	content := box(30, 20)
	b := newTapBox(content, func() { taps++ })
	b.onLook = func() { looks++ }
	if b.Cursor() != desktop.PointerCursor {
		t.Fatal("tap box has no pointer cursor")
	}

	b.MouseIn(nil)
	if !b.hovered || looks != 1 {
		t.Fatal("hover did not change the look")
	}
	b.MouseMoved(nil)
	b.MouseOut()
	if b.hovered || looks != 2 {
		t.Fatal("leaving did not change the look")
	}
	b.FocusGained()
	if !b.focused || looks != 3 {
		t.Fatal("focus did not change the look")
	}
	b.FocusLost()
	if b.focused || looks != 4 {
		t.Fatal("losing the focus did not change the look")
	}

	b.TypedRune('x')
	for _, name := range []fyne.KeyName{fyne.KeyReturn, fyne.KeyEnter, fyne.KeySpace, fyne.KeyA} {
		b.TypedKey(key(name))
	}
	if taps != 3 || *escapes != 0 {
		t.Fatalf("keys tapped %d times, escaped %d", taps, *escapes)
	}
	b.TypedKey(key(fyne.KeyEscape))
	if *escapes != 1 || taps != 3 {
		t.Fatalf("escape ran %d times, taps %d", *escapes, taps)
	}

	r := test.WidgetRenderer(b)
	if r.MinSize() != fyne.NewSize(30, 20) {
		t.Fatalf("tap box min size %v", r.MinSize())
	}
	r.Layout(fyne.NewSize(80, 40))
	if content.Size() != fyne.NewSize(80, 40) {
		t.Fatalf("content size %v", content.Size())
	}
	if len(r.Objects()) != 1 || r.Objects()[0] != content {
		t.Fatal("tap box objects")
	}
	r.Refresh()
	r.Destroy()

	plainBox := newTapBox(box(1, 1), nil)
	plainBox.Tapped(nil) // no tap and no look handler, no panic
	plainBox.MouseIn(nil)
}

func TestPaintFollowsThePalette(t *testing.T) {
	a := widgetApp(t, "dark")
	var got []*palette
	p := newPaint(func(r *canvas.Rectangle, pl *palette) {
		got = append(got, pl)
		r.FillColor = pl.accent
	})
	r := test.WidgetRenderer(p)
	if len(got) == 0 || got[len(got)-1] != &darkPalette || p.rect.FillColor != darkPalette.accent {
		t.Fatal("paint did not draw in the dark palette")
	}
	a.Settings().SetTheme(&ouroTheme{pref: "light"})
	r.Refresh()
	if got[len(got)-1] != &lightPalette || p.rect.FillColor != lightPalette.accent {
		t.Fatal("paint did not follow the change of the appearance")
	}
	r.Layout(fyne.NewSize(10, 12))
	if p.rect.Size() != fyne.NewSize(10, 12) {
		t.Fatalf("rect size %v", p.rect.Size())
	}
	if r.MinSize() != p.rect.MinSize() || len(r.Objects()) != 1 || r.Objects()[0] != p.rect {
		t.Fatal("paint renderer min size or objects")
	}
	r.Destroy()
}

func TestOutlineDrawsAField(t *testing.T) {
	widgetApp(t, "light")
	inner := box(10, 10)
	c := outline(inner)
	edge := c.Objects[0].(*paint)
	test.WidgetRenderer(edge)
	if edge.rect.FillColor != lightPalette.field || edge.rect.StrokeColor != lightPalette.line || edge.rect.CornerRadius != 8 {
		t.Fatal("outline colours")
	}
}

func TestInsetLayout(t *testing.T) {
	inner := box(10, 20)
	c := inset(1, 2, 3, 4, inner)
	if got := c.MinSize(); got != fyne.NewSize(16, 24) {
		t.Fatalf("inset min size %v", got)
	}
	c.Resize(fyne.NewSize(100, 50))
	if inner.Position() != fyne.NewPos(4, 1) || inner.Size() != fyne.NewSize(94, 46) {
		t.Fatalf("inset layout %v %v", inner.Position(), inner.Size())
	}
	c.Layout.Layout(c.Objects, fyne.NewSize(3, 2))
	if inner.Size() != fyne.NewSize(0, 0) {
		t.Fatalf("too small an inset gives %v", inner.Size())
	}
}

func TestCenterMaxLayoutWithGutter(t *testing.T) {
	inner := box(100, 30)
	c := gutterCentered(760, 24, inner)
	if got := c.MinSize(); got != fyne.NewSize(148, 30) {
		t.Fatalf("centered min size %v", got)
	}
	c.Layout.Layout(c.Objects, fyne.NewSize(1000, 20))
	if inner.Position() != fyne.NewPos(120, 0) || inner.Size() != fyne.NewSize(760, 30) {
		t.Fatalf("wide: %v %v", inner.Position(), inner.Size())
	}
	c.Layout.Layout(c.Objects, fyne.NewSize(300, 50))
	if inner.Position() != fyne.NewPos(24, 0) || inner.Size() != fyne.NewSize(252, 50) {
		t.Fatalf("narrow: %v %v", inner.Position(), inner.Size())
	}
	c.Layout.Layout(c.Objects, fyne.NewSize(40, 50))
	if inner.Size().Width != 0 {
		t.Fatalf("narrower than the gutters: %v", inner.Size())
	}
}

func TestColumnsLayoutMinSize(t *testing.T) {
	l := &columnsLayout{}
	if l.MinSize(nil) != fyne.NewSize(360, 420) {
		t.Fatal("columns min size")
	}
	if clamp(220, 100, 400) != 220 || clamp(220, 300, 400) != 300 || clamp(220, 500, 400) != 400 {
		t.Fatal("clamp")
	}
}

func TestTightVBoxSkipsHidden(t *testing.T) {
	a, hidden, b := box(10, 20), box(50, 50), box(30, 10)
	hidden.Hide()
	c := tightVBox(-4, a, hidden, b)
	if got := c.MinSize(); got != fyne.NewSize(30, 26) {
		t.Fatalf("tight min size %v", got)
	}
	c.Resize(fyne.NewSize(100, 100))
	if a.Position() != fyne.NewPos(0, 0) || b.Position() != fyne.NewPos(0, 16) || b.Size() != fyne.NewSize(100, 10) {
		t.Fatalf("tight layout %v %v %v", a.Position(), b.Position(), b.Size())
	}
	if got := tightVBox(-4, box(5, 5)).MinSize(); got != fyne.NewSize(5, 5) {
		t.Fatalf("one object: %v", got)
	}
}

func TestFlowLayoutWrapsAndSkipsHidden(t *testing.T) {
	a, hidden, b, c := box(50, 20), box(500, 500), box(50, 25), box(200, 20)
	hidden.Hide()
	l := &flowLayout{gap: 10}
	objects := []fyne.CanvasObject{a, hidden, b, c}
	if got := l.MinSize(objects); got != fyne.NewSize(200, 25) {
		t.Fatalf("flow min size %v", got)
	}
	positions, height := l.rows(objects, 120)
	if positions[0] != fyne.NewPos(0, 0) || positions[2] != fyne.NewPos(60, 0) || positions[3] != fyne.NewPos(0, 35) || height != 55 {
		t.Fatalf("rows %v height %v", positions, height)
	}
	l.Layout(objects, fyne.NewSize(120, 100))
	if b.Position() != fyne.NewPos(60, 0) || c.Position() != fyne.NewPos(0, 35) || c.Size() != fyne.NewSize(120, 20) {
		t.Fatalf("flow layout %v %v %v", b.Position(), c.Position(), c.Size())
	}
}

func TestFlowBoxHeightFollowsWidth(t *testing.T) {
	widgetApp(t, "light")
	f := newFlowBox(10)
	f.SetObjects(box(50, 20), box(50, 20), box(50, 20))
	r := test.WidgetRenderer(f)
	if got := r.MinSize(); got != fyne.NewSize(50, 20) {
		t.Fatalf("flow box min size before a width %v", got)
	}
	f.Resize(fyne.NewSize(120, 100))
	if got := r.MinSize(); got != fyne.NewSize(50, 50) {
		t.Fatalf("flow box min size in 120 pixels %v", got)
	}
	r.Layout(fyne.NewSize(120, 50))
	if f.box.Size() != fyne.NewSize(120, 50) || len(r.Objects()) != 1 {
		t.Fatal("flow box layout")
	}
	r.Refresh()
	r.Destroy()
}

func TestSidebarLayoutColumnAndDrawer(t *testing.T) {
	heading, footer := box(10, 30), box(10, 20)
	list := container.NewVScroll(box(10, 500))
	objects := []fyne.CanvasObject{heading, list, footer}
	column := &sidebarLayout{}
	if got := column.MinSize(objects); got != fyne.NewSize(160, 90) {
		t.Fatalf("sidebar min size %v", got)
	}
	column.Layout(objects, fyne.NewSize(200, 600))
	if heading.Position().Y != 131 || list.Position().Y != 161 || list.Size().Height != 288 || footer.Position().Y != 580 {
		t.Fatalf("column: heading %v list %v %v footer %v", heading.Position(), list.Position(), list.Size(), footer.Position())
	}

	drawer := &sidebarLayout{ui: &fyneUI{narrow: true}}
	drawer.Layout(objects, fyne.NewSize(200, 600))
	if heading.Position().Y != 0 || list.Position().Y != 30 || list.Size().Height != 500 {
		t.Fatalf("drawer: heading %v list %v %v", heading.Position(), list.Position(), list.Size())
	}

	other := box(10, 10)
	column.Layout([]fyne.CanvasObject{heading, other, footer}, fyne.NewSize(200, 600))
	if other.Size().Height != 40 {
		t.Fatalf("a list that does not scroll takes %v", other.Size())
	}
	column.Layout(objects, fyne.NewSize(200, 60))
	if list.Size().Height != 10 {
		t.Fatalf("a short sidebar gives the list %v", list.Size())
	}
}

func TestRailLayoutOverflow(t *testing.T) {
	eyebrow, title, note := box(10, 20), box(10, 20), box(10, 20)
	details := container.NewVScroll(box(10, 500))
	objects := []fyne.CanvasObject{eyebrow, title, note, details}
	l := &railLayout{}
	if l.MinSize(objects) != fyne.NewSize(160, 100) {
		t.Fatal("rail min size")
	}
	l.Layout(objects, fyne.NewSize(200, 400))
	if eyebrow.Position().Y != 96 || details.Size().Height != 232 || details.Position().Y != 168 {
		t.Fatalf("rail: eyebrow %v details %v %v", eyebrow.Position(), details.Position(), details.Size())
	}
	l.Layout(objects, fyne.NewSize(200, 100))
	if eyebrow.Position().Y != 0 || details.Size().Height != 28 {
		t.Fatalf("overflowing rail: eyebrow %v details %v", eyebrow.Position(), details.Size())
	}
	plainDetails := box(10, 500)
	l.Layout([]fyne.CanvasObject{eyebrow, title, note, plainDetails}, fyne.NewSize(200, 400))
	if plainDetails.Size().Height != 0 {
		t.Fatalf("details that do not scroll take %v", plainDetails.Size())
	}
}

func TestFixedWidthLayout(t *testing.T) {
	inner := box(10, 15)
	c := fixedWidth(46, inner)
	if c.MinSize() != fyne.NewSize(46, 15) {
		t.Fatalf("fixed width min size %v", c.MinSize())
	}
	c.Resize(fyne.NewSize(200, 30))
	if inner.Size() != fyne.NewSize(46, 30) || inner.Position() != fyne.NewPos(0, 0) {
		t.Fatalf("fixed width layout %v", inner.Size())
	}
}

func TestCappedLayout(t *testing.T) {
	scroll := container.NewVScroll(box(40, 300))
	l := &cappedLayout{share: 0.4}
	if got := l.MinSize([]fyne.CanvasObject{scroll}); got != fyne.NewSize(40, 300) {
		t.Fatalf("uncapped scroller %v", got)
	}
	of := box(1, 1)
	l.of = of
	if got := l.MinSize([]fyne.CanvasObject{scroll}); got.Height != 300 {
		t.Fatalf("capped by an object without a size yet %v", got)
	}
	of.Resize(fyne.NewSize(100, 200))
	if got := l.MinSize([]fyne.CanvasObject{scroll}); got != fyne.NewSize(40, 80) {
		t.Fatalf("capped scroller %v", got)
	}
	plainBox := box(20, 50)
	if got := l.MinSize([]fyne.CanvasObject{plainBox}); got != fyne.NewSize(20, 50) {
		t.Fatalf("capped plain object %v", got)
	}
	l.Layout([]fyne.CanvasObject{plainBox}, fyne.NewSize(70, 60))
	if plainBox.Size() != fyne.NewSize(70, 60) {
		t.Fatal("capped layout")
	}
}

func TestDialogLayout(t *testing.T) {
	card := box(100, 50)
	l := &dialogLayout{max: 460, maxHeight: 720}
	if l.MinSize(nil) != fyne.NewSize(0, 0) {
		t.Fatal("dialog min size")
	}
	l.Layout([]fyne.CanvasObject{card}, fyne.NewSize(1000, 800))
	if card.Size() != fyne.NewSize(460, 50) || card.Position() != fyne.NewPos(270, 375) {
		t.Fatalf("dialog without measure %v %v", card.Position(), card.Size())
	}
	l.measure = box(10, 300)
	l.Layout([]fyne.CanvasObject{card}, fyne.NewSize(1000, 800))
	if card.Size() != fyne.NewSize(460, 300) || card.Position() != fyne.NewPos(270, 250) {
		t.Fatalf("dialog with measure %v %v", card.Position(), card.Size())
	}
	l.Layout([]fyne.CanvasObject{card}, fyne.NewSize(200, 100))
	if card.Size() != fyne.NewSize(168, 68) {
		t.Fatalf("dialog in a small window %v", card.Size())
	}
	l.Layout([]fyne.CanvasObject{card}, fyne.NewSize(10, 10))
	if card.Size() != fyne.NewSize(0, 0) {
		t.Fatalf("dialog in a tiny window %v", card.Size())
	}
}

func TestTopFadeLayout(t *testing.T) {
	fade := box(1, 1)
	l := &topFadeLayout{}
	if l.MinSize(nil) != fyne.NewSize(0, 0) {
		t.Fatal("fade min size")
	}
	l.Layout([]fyne.CanvasObject{fade}, fyne.NewSize(300, 100))
	if fade.Size() != fyne.NewSize(300, 40) {
		t.Fatalf("fade in a short transcript %v", fade.Size())
	}
	l.Layout([]fyne.CanvasObject{fade}, fyne.NewSize(300, 1000))
	if fade.Size() != fyne.NewSize(300, 140) {
		t.Fatalf("fade in a tall transcript %v", fade.Size())
	}
}

// buttonLook is the renderer of a flat button, to read its colours.
func buttonLook(t *testing.T, b *flatButton) *flatButtonRenderer {
	t.Helper()
	return test.WidgetRenderer(b).(*flatButtonRenderer)
}

func TestFlatButtonPlain(t *testing.T) {
	widgetApp(t, "light")
	escapes := countEscape(t)
	taps := 0
	b := newFlatButton("ab", func() { taps++ })
	r := buttonLook(t, b)
	if r.fill.FillColor != color.Transparent || r.fill.StrokeColor != lightPalette.line || r.fill.StrokeWidth != 1 || r.fill.CornerRadius != 8 {
		t.Fatal("plain button fill")
	}
	if r.ink.Color != lightPalette.text || r.ink.TextSize != 10 || r.ink.Text != "ab" {
		t.Fatal("plain button ink")
	}
	if len(r.Objects()) != 2 {
		t.Fatal("plain button has an image")
	}
	text := r.ink.MinSize()
	if got := r.MinSize(); got != fyne.NewSize(text.Width+24, fyne.Max(26, text.Height+10)) {
		t.Fatalf("plain button min size %v", got)
	}

	b.MouseIn(nil)
	if r.fill.FillColor != lightPalette.hover || r.fill.StrokeColor != lightPalette.line {
		t.Fatal("hover does not fill the button")
	}
	b.MouseMoved(nil)
	b.MouseOut()
	if r.fill.FillColor != color.Transparent {
		t.Fatal("leaving keeps the hover")
	}
	b.FocusGained()
	if r.fill.StrokeColor != lightPalette.accent || r.fill.StrokeWidth != 2 {
		t.Fatal("focus draws no accent ring")
	}
	b.FocusLost()
	if r.fill.StrokeColor != lightPalette.line || r.fill.StrokeWidth != 1 {
		t.Fatal("losing the focus keeps the ring")
	}

	b.SetLabel("longer label")
	if r.ink.Text != "longer label" {
		t.Fatalf("label %q", r.ink.Text)
	}

	b.TypedRune('x')
	for _, name := range []fyne.KeyName{fyne.KeyReturn, fyne.KeyEnter, fyne.KeySpace, fyne.KeyA} {
		b.TypedKey(key(name))
	}
	if taps != 3 || *escapes != 0 {
		t.Fatalf("keys tapped %d times, escaped %d", taps, *escapes)
	}
	b.TypedKey(key(fyne.KeyEscape))
	if *escapes != 1 {
		t.Fatal("escape on a button")
	}

	r.Layout(fyne.NewSize(200, 40))
	if r.fill.Size() != fyne.NewSize(200, 40) {
		t.Fatal("fill size")
	}
	r.Destroy()
}

func TestFlatButtonDisable(t *testing.T) {
	widgetApp(t, "light")
	taps := 0
	b := newFlatButton("x", func() { taps++ })
	r := buttonLook(t, b)
	if b.Cursor() != desktop.PointerCursor {
		t.Fatal("enabled button has no pointer")
	}
	b.Enable() // already enabled
	if b.Disabled() {
		t.Fatal("enable disabled the button")
	}
	b.Disable()
	b.Disable()
	if !b.Disabled() || b.Cursor() != desktop.DefaultCursor || r.ink.Color != lightPalette.muted {
		t.Fatal("disabled button")
	}
	b.MouseIn(nil)
	if r.fill.FillColor == lightPalette.hover {
		t.Fatal("a disabled button takes the hover")
	}
	b.Tapped(nil)
	b.TypedKey(key(fyne.KeyReturn))
	if taps != 0 {
		t.Fatal("a disabled button took a tap")
	}
	b.Enable()
	b.Tapped(nil)
	if b.Disabled() || taps != 1 || r.fill.FillColor != lightPalette.hover {
		t.Fatal("enabled again, the button does not tap or hover")
	}
	newFlatButton("none", nil).Tapped(nil) // no tap handler, no panic
}

func TestFlatButtonKinds(t *testing.T) {
	widgetApp(t, "dark")
	p := &darkPalette

	back := buttonLook(t, newBackButton("back", nil))
	if back.fill.FillColor != p.field || back.MinSize().Height != 34 {
		t.Fatal("back button")
	}

	option := newOptionButton("opt", true, nil)
	or := buttonLook(t, option)
	if or.fill.StrokeColor != p.accent || or.fill.FillColor != p.field || or.ink.TextSize != 11 {
		t.Fatal("recommended option")
	}
	if got := or.MinSize().Width; got != or.ink.MinSize().Width+28 {
		t.Fatalf("option padding: width %v", got)
	}

	stop := newStopButton("stop", nil)
	sr := buttonLook(t, stop)
	if sr.ink.Color != p.error || sr.fill.StrokeColor != p.errorLine || sr.MinSize().Height != 32 {
		t.Fatal("stop button")
	}
	stop.Disable()
	if sr.ink.Color != p.muted || sr.fill.StrokeColor != p.line {
		t.Fatal("disabled stop button")
	}

	save := buttonLook(t, newDialogAction("save", true, nil))
	if save.fill.FillColor != p.hover || save.fill.StrokeColor != p.lineStrong || save.ink.TextSize != 13 {
		t.Fatal("save action")
	}
	cancel := buttonLook(t, newDialogAction("cancel", false, nil))
	if cancel.fill.FillColor != p.field || cancel.fill.StrokeColor != p.line {
		t.Fatal("other action")
	}
	dialog := buttonLook(t, newDialogButton("model", nil))
	if dialog.MinSize().Height != 40 || dialog.MinSize().Width != dialog.ink.MinSize().Width+36 {
		t.Fatal("dialog button size")
	}
}

func TestFlatButtonRoundAndIcon(t *testing.T) {
	widgetApp(t, "light")
	p := &lightPalette

	send := newSendButton(nil)
	sr := buttonLook(t, send)
	if sr.fill.CornerRadius != 16 || sr.MinSize() != fyne.NewSize(32, 32) || len(sr.Objects()) != 3 {
		t.Fatal("send button")
	}
	if _, ok := sr.image.Resource.(*theme.ThemedResource); !ok {
		t.Fatalf("send icon %T", sr.image.Resource)
	}
	send.MouseIn(nil)
	if sr.fill.StrokeColor != p.accent || sr.fill.FillColor != p.hover {
		t.Fatal("hovered round button")
	}
	if res, ok := sr.image.Resource.(*theme.ThemedResource); !ok || res.ColorName != theme.ColorNamePrimary {
		t.Fatal("hovered icon is not in the accent")
	}
	send.Disable()
	if _, ok := sr.image.Resource.(*theme.DisabledResource); !ok {
		t.Fatalf("disabled icon %T", sr.image.Resource)
	}
	sr.Layout(fyne.NewSize(40, 40))
	if sr.image.Size() != fyne.NewSize(18, 18) || sr.image.Position() != fyne.NewPos(11, 11) {
		t.Fatalf("icon at %v size %v", sr.image.Position(), sr.image.Size())
	}

	icon := newIconButton(theme.SettingsIcon(), nil)
	ir := buttonLook(t, icon)
	if ir.fill.StrokeColor != color.Transparent || ir.MinSize() != fyne.NewSize(36, 36) {
		t.Fatalf("quiet icon button: stroke %v size %v", ir.fill.StrokeColor, ir.MinSize())
	}
	icon.MouseIn(nil)
	if ir.fill.StrokeColor != p.line || ir.fill.FillColor != p.hover {
		t.Fatal("hovered icon button shows no outline")
	}
}

func TestDashedBar(t *testing.T) {
	widgetApp(t, "light")
	for _, running := range []bool{true, false} {
		bar := dashedBar(running).(*canvas.Raster)
		if bar.MinSize() != fyne.NewSize(1, 4) {
			t.Fatalf("bar min size %v", bar.MinSize())
		}
		ink := lightPalette.accent
		if !running {
			ink = withAlpha(lightPalette.muted, 0x73)
		}
		img := bar.Generator(13, 10)
		if got := img.At(1, 4); got != ink {
			t.Fatalf("running %v: dash %v, want %v", running, got, ink)
		}
		if got := img.At(3, 4); got != (color.NRGBA{}) {
			t.Fatalf("gap between dashes %v", got)
		}
		if got := img.At(1, 1); got != (color.NRGBA{}) {
			t.Fatalf("above the dash %v", got)
		}
		if got := img.At(12, 6); got != ink {
			t.Fatalf("last dash %v", got)
		}

		tiny := bar.Generator(5, 3)
		if got := tiny.At(0, 0); got != ink {
			t.Fatalf("a bar three pixels high starts at the top: %v", got)
		}
		if empty := bar.Generator(0, 4); !empty.Bounds().Empty() {
			t.Fatal("a bar without width draws pixels")
		}
		if empty := bar.Generator(5, 0); !empty.Bounds().Empty() {
			t.Fatal("a bar without height draws pixels")
		}
	}
}

func TestDotColours(t *testing.T) {
	widgetApp(t, "light")
	fill := func(live, bad bool) color.Color {
		center := dot(live, bad).(*fyne.Container)
		grid := center.Objects[0].(*fyne.Container)
		return grid.Objects[0].(*canvas.Circle).FillColor
	}
	if fill(false, false) != lightPalette.lineStrong {
		t.Fatal("quiet dot")
	}
	if fill(true, false) != lightPalette.accent {
		t.Fatal("live dot")
	}
	if fill(true, true) != lightPalette.error || fill(false, true) != lightPalette.error {
		t.Fatal("bad dot")
	}
	if accentColor() != lightPalette.accent || mutedColor() != lightPalette.muted {
		t.Fatal("accent or muted colour")
	}
}

func TestFadeGradient(t *testing.T) {
	widgetApp(t, "dark")
	bg := darkPalette.background
	img := fadeGradient(4, 10)
	if img.Bounds().Dx() != 4 || img.Bounds().Dy() != 10 {
		t.Fatalf("gradient bounds %v", img.Bounds())
	}
	top := img.At(0, 0).(color.NRGBA)
	if top.R != bg.R || top.G != bg.G || top.B != bg.B || top.A != 216 {
		t.Fatalf("top of the fade %v", top)
	}
	last := img.At(3, 9).(color.NRGBA)
	if last.A >= top.A || last.A > 3 {
		t.Fatalf("the fade does not thin out: %v", last)
	}
	flat := fadeGradient(4, 0)
	if flat.Bounds().Dy() != 1 || flat.At(0, 0) != (color.NRGBA{}) {
		t.Fatalf("a fade without height %v", flat.Bounds())
	}
}

func TestScrimColor(t *testing.T) {
	if scrimColor() != (color.NRGBA{R: 0x14, G: 0x15, B: 0x10, A: 0x66}) {
		t.Fatal("scrim colour")
	}
}

func TestThemeColors(t *testing.T) {
	for _, variant := range []fyne.ThemeVariant{theme.VariantLight, theme.VariantDark} {
		th := &ouroTheme{pref: "system"}
		p := &lightPalette
		if variant == theme.VariantDark {
			p = &darkPalette
		}
		want := map[fyne.ThemeColorName]color.Color{
			theme.ColorNameBackground:          p.background,
			theme.ColorNameForeground:          p.text,
			theme.ColorNameDisabled:            p.muted,
			theme.ColorNamePrimary:             p.accent,
			theme.ColorNameHyperlink:           p.accent,
			theme.ColorNameError:               p.error,
			theme.ColorNameInputBackground:     p.background,
			theme.ColorNameOverlayBackground:   p.background,
			theme.ColorNameMenuBackground:      p.surface,
			theme.ColorNameFocus:               withAlpha(p.accent, 0x30),
			theme.ColorNamePressed:             withAlpha(p.accent, 0x30),
			theme.ColorNameScrollBar:           withAlpha(p.muted, 0x90),
			theme.ColorNamePlaceHolder:         p.muted,
			theme.ColorNameInputBorder:         p.line,
			theme.ColorNameHeaderBackground:    p.surface,
			theme.ColorNameButton:              p.surface,
			theme.ColorNameDisabledButton:      p.surface,
			theme.ColorNameHover:               p.hover,
			theme.ColorNameSeparator:           p.line,
			theme.ColorNameShadow:              color.NRGBA{},
			theme.ColorNameForegroundOnPrimary: p.background,
			theme.ColorNameForegroundOnError:   p.background,
			theme.ColorNameSelection:           p.selected,
			theme.ColorNameSuccess:             theme.DefaultTheme().Color(theme.ColorNameSuccess, variant),
		}
		for name, c := range want {
			if got := th.Color(name, variant); got != c {
				t.Errorf("variant %v: %s is %v, want %v", variant, name, got, c)
			}
		}
	}
	if !(&ouroTheme{pref: "dark"}).dark(theme.VariantLight) || (&ouroTheme{pref: "light"}).dark(theme.VariantDark) {
		t.Fatal("the chosen appearance does not override the desktop")
	}
	if (&ouroTheme{}).Icon(theme.IconNameSettings) != theme.DefaultTheme().Icon(theme.IconNameSettings) {
		t.Fatal("icon")
	}
}

func TestThemeSizes(t *testing.T) {
	th := &ouroTheme{}
	want := map[fyne.ThemeSizeName]float32{
		sizeTiny: 9, sizeSmall: 10, sizeMeta: 11, sizeBody: 12, sizeTitle: 14,
		sizeSection: 16, sizeHeading: 20, sizeIcon: 23,
		theme.SizeNameText: 13, theme.SizeNameCaptionText: 11,
		theme.SizeNameSubHeadingText: 14, theme.SizeNameHeadingText: 16,
		theme.SizeNamePadding: 4, theme.SizeNameInnerPadding: 6,
		theme.SizeNameLineSpacing: 0, theme.SizeNameSeparatorThickness: 1,
		theme.SizeNameInputBorder: 1, theme.SizeNameInputRadius: 0,
		theme.SizeNameButtonRadius: 8, theme.SizeNameSelectionRadius: 0,
		theme.SizeNameScrollBar: theme.DefaultTheme().Size(theme.SizeNameScrollBar),
	}
	for name, size := range want {
		if got := th.Size(name); got != size {
			t.Errorf("%s is %v, want %v", name, got, size)
		}
	}
}

func TestPalWithoutOurTheme(t *testing.T) {
	widgetApp(t, "") // the standard theme of Fyne
	if pal() != &lightPalette {
		t.Fatal("a foreign theme does not draw in the light palette")
	}
	a := widgetApp(t, "dark")
	if pal() != &darkPalette {
		t.Fatal("the dark choice does not draw in the dark palette")
	}
	fyne.SetCurrentApp(nil)
	t.Cleanup(func() { fyne.SetCurrentApp(a) })
	if pal() != &lightPalette {
		t.Fatal("without an app the window does not draw light")
	}
}

func TestThemePreference(t *testing.T) {
	a := widgetApp(t, "")
	if got := themePreference(a); got != "system" {
		t.Fatalf("unset preference %q", got)
	}
	for value, want := range map[string]string{"light": "light", "dark": "dark", "system": "system", "sepia": "system"} {
		a.Preferences().SetString(themePreferenceKey, value)
		if got := themePreference(a); got != want {
			t.Errorf("preference %q is %q, want %q", value, got, want)
		}
	}
}

func TestMonoFontFaces(t *testing.T) {
	th := &ouroTheme{}
	faces := map[fyne.TextStyle]fyne.Resource{
		{}:                         monoRegular,
		{Monospace: true}:          monoRegular,
		{Bold: true}:               monoBold,
		{Italic: true}:             monoItalic,
		{Bold: true, Italic: true}: monoBoldItalic,
	}
	for style, want := range faces {
		if got := th.Font(style); got != want {
			t.Errorf("style %+v draws in %s, want %s", style, got.Name(), want.Name())
		}
	}
	if monoBoldItalic.Name() != "fonts/IBMPlexMono-BoldItalic.ttf" || len(monoBoldItalic.Content()) == 0 {
		t.Fatal("the bold italic face is not embedded")
	}
	if got := loadMonoFont("fonts/missing.ttf"); got != theme.DefaultTextMonospaceFont() {
		t.Fatalf("a missing face falls back to %s", got.Name())
	}
}

func TestTrFallbacks(t *testing.T) {
	before := fyneLanguage
	t.Cleanup(func() { fyneLanguage = before })

	fyneLanguage = "en"
	if got := tr("no.such.key", nil); got != "no.such.key" {
		t.Fatalf("missing key %q", got)
	}
	if got := tr("chart.more", map[string]any{"n": 3}); got != " and 3 more" {
		t.Fatalf("filled text %q", got)
	}
	if datePattern() != "02 Jan 2006, 15:04" {
		t.Fatal("English date pattern")
	}

	fyneLanguage = "de"
	if got := tr("dialog.save", nil); got != "Speichern" {
		t.Fatalf("German text %q", got)
	}
	if got := tr("no.such.key", nil); got != "no.such.key" {
		t.Fatalf("missing German key %q", got)
	}
	if datePattern() != "02.01.2006, 15:04" {
		t.Fatal("German date pattern")
	}
	at := time.Date(2026, 3, 4, 5, 6, 0, 0, time.Local)
	if got := formatDate(at); got != "04.03.2026, 05:06" {
		t.Fatalf("German date %q", got)
	}

	fyneLanguage = "fr"
	if got := tr("dialog.save", nil); got != "Save" {
		t.Fatalf("a language without a table %q", got)
	}
}

// Every key has a text in every language, so that no fallback shows.
func TestTextTablesMatch(t *testing.T) {
	for key := range fyneTexts["en"] {
		if fyneTexts["de"][key] == "" {
			t.Errorf("no German text for %s", key)
		}
	}
	for key := range fyneTexts["de"] {
		if fyneTexts["en"][key] == "" {
			t.Errorf("no English text for %s", key)
		}
	}
}
