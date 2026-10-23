//go:build cgo

package gui

import (
	"image"
	"image/color"
	"image/draw"
	"math"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// escapeHandler is what Escape does in the window, whichever of its widgets
// has the focus: the desktop driver gives a key without a modifier to the
// focused widget alone, never to a shortcut of the canvas.
var escapeHandler func()

// windowShortcut runs a shortcut of the window and reports whether it was
// one: the driver gives a shortcut to the focused widget alone, so a field
// hands those of the window on, as Ctrl+, opens the settings from the
// composer.
var windowShortcut func(fyne.Shortcut) bool

// pressEscape runs the escape handler of the window, if there is one.
func pressEscape() {
	if escapeHandler != nil {
		escapeHandler()
	}
}

// borderlessEntry suppresses only the renderer border, not the theme size
// that Fyne also uses for the caret.
type borderlessEntry struct {
	widget.Entry
}

func newBorderlessEntry(password bool) *borderlessEntry {
	e := &borderlessEntry{}
	e.Password = password
	e.ExtendBaseWidget(e)
	return e
}

// TypedKey hands Escape to the window, so that it leaves a dialog from a
// field as from anywhere else.
func (e *borderlessEntry) TypedKey(key *fyne.KeyEvent) {
	if key.Name == fyne.KeyEscape {
		pressEscape()
		return
	}
	e.Entry.TypedKey(key)
}

// TypedShortcut runs the shortcuts of the window, and edits with the rest.
func (e *borderlessEntry) TypedShortcut(shortcut fyne.Shortcut) {
	if windowShortcut != nil && windowShortcut(shortcut) {
		return
	}
	e.Entry.TypedShortcut(shortcut)
}

func (e *borderlessEntry) CreateRenderer() fyne.WidgetRenderer {
	r := &borderlessEntryRenderer{WidgetRenderer: e.Entry.CreateRenderer()}
	r.clearBorder()
	return r
}

type borderlessEntryRenderer struct {
	fyne.WidgetRenderer
}

func (r *borderlessEntryRenderer) clearBorder() {
	// Fyne's Entry renderer places the background first and border second.
	if objects := r.Objects(); len(objects) > 1 {
		if background, ok := objects[0].(*canvas.Rectangle); ok {
			background.FillColor = color.Transparent
			background.Refresh()
		}
		if border, ok := objects[1].(*canvas.Rectangle); ok {
			border.StrokeColor = color.Transparent
			border.Refresh()
		}
	}
}

func (r *borderlessEntryRenderer) Layout(size fyne.Size) {
	r.WidgetRenderer.Layout(size)
	r.clearBorder()
}

func (r *borderlessEntryRenderer) Refresh() {
	r.WidgetRenderer.Refresh()
	r.clearBorder()
}

// picker is a select that hands Escape to the window, so that a dialog
// leaves from it as from anywhere else. It draws as a field of the window:
// one rounded box in the colour of the fields, whose line shows the pointer
// and the focus, instead of the box Fyne fills differently in each state.
type picker struct {
	widget.Select
	hovered, focused bool
}

func (c *picker) MouseIn(e *desktop.MouseEvent) {
	c.hovered = true
	c.Select.MouseIn(e)
}

func (c *picker) MouseOut() {
	c.hovered = false
	c.Select.MouseOut()
}

func (c *picker) FocusGained() {
	c.focused = true
	c.Select.FocusGained()
}

func (c *picker) FocusLost() {
	c.focused = false
	c.Select.FocusLost()
}

func (c *picker) CreateRenderer() fyne.WidgetRenderer {
	return &pickerRenderer{WidgetRenderer: c.Select.CreateRenderer(), picker: c}
}

// pickerRenderer restyles the background Fyne's select draws first.
type pickerRenderer struct {
	fyne.WidgetRenderer
	picker *picker
}

// pickerPad is the room above and below the text, as in the fields.
const pickerPad = 4

func (r *pickerRenderer) MinSize() fyne.Size {
	return r.WidgetRenderer.MinSize().AddWidthHeight(0, 2*pickerPad)
}

func (r *pickerRenderer) Layout(size fyne.Size) {
	r.WidgetRenderer.Layout(size)
	r.style()
}

func (r *pickerRenderer) Refresh() {
	r.WidgetRenderer.Refresh()
	r.style()
}

func (r *pickerRenderer) style() {
	box := r.Objects()[0].(*canvas.Rectangle) // Fyne's select draws its background first
	p := pal()
	box.FillColor = p.field
	box.StrokeColor = p.line
	box.StrokeWidth = 1
	box.CornerRadius = 8
	switch {
	case r.picker.focused:
		box.StrokeColor = p.accent
		box.StrokeWidth = 2
	case r.picker.hovered && !r.picker.Disabled():
		box.StrokeColor = p.lineStrong
	}
	box.Refresh()
}

func newPicker(options []string, changed func(string)) *picker {
	c := &picker{}
	c.Options = options
	c.OnChanged = changed
	c.PlaceHolder = "(Select one)"
	c.ExtendBaseWidget(c)
	return c
}

func (c *picker) TypedKey(key *fyne.KeyEvent) {
	if key.Name == fyne.KeyEscape {
		pressEscape()
		return
	}
	c.Select.TypedKey(key)
}

// check is a check box that hands Escape to the window.
type check struct {
	widget.Check
}

func newCheck(label string) *check {
	c := &check{}
	c.Text = label
	c.ExtendBaseWidget(c)
	return c
}

func (c *check) TypedKey(key *fyne.KeyEvent) {
	if key.Name == fyne.KeyEscape {
		pressEscape()
		return
	}
	c.Check.TypedKey(key)
}

// composerRows is how many lines the composer grows to before it scrolls.
const composerRows = 8

// composerEntry keeps multiline editing but sends on Enter instead of inserting
// a newline. Shift+Enter remains available for writing multiline messages.
// It grows with what is written, up to composerRows lines.
type composerEntry struct {
	borderlessEntry
	shiftLeft, shiftRight bool
	onSend                func(string)
	rows                  int
}

func newComposerEntry(onSend func(string)) *composerEntry {
	e := &composerEntry{onSend: onSend, rows: 1}
	e.MultiLine = true
	e.ExtendBaseWidget(e)
	return e
}

func (e *composerEntry) TypedKey(key *fyne.KeyEvent) {
	if key.Name == fyne.KeyReturn || key.Name == fyne.KeyEnter {
		if e.Disabled() {
			return
		}
		if !e.shiftLeft && !e.shiftRight {
			e.onSend(e.Text)
			return
		}
	}
	e.borderlessEntry.TypedKey(key)
}

func (e *composerEntry) KeyDown(key *fyne.KeyEvent) {
	switch key.Name {
	case desktop.KeyShiftLeft:
		e.shiftLeft = true
	case desktop.KeyShiftRight:
		e.shiftRight = true
	}
	e.Entry.KeyDown(key)
}

func (e *composerEntry) KeyUp(key *fyne.KeyEvent) {
	switch key.Name {
	case desktop.KeyShiftLeft:
		e.shiftLeft = false
	case desktop.KeyShiftRight:
		e.shiftRight = false
	}
	e.Entry.KeyUp(key)
}

func (e *composerEntry) FocusLost() {
	e.shiftLeft, e.shiftRight = false, false
	e.Entry.FocusLost()
}

// Resize fits the rows to the new width, as the text wraps anew.
func (e *composerEntry) Resize(size fyne.Size) {
	width := e.Size().Width
	e.Entry.Resize(size)
	if size.Width != width {
		e.grow()
	}
}

// grow shows as many rows as the text takes, from one up to composerRows.
func (e *composerEntry) grow() {
	rows := wrappedRows(e.Text, e.Size().Width-2*theme.InnerPadding()-2*theme.Padding(), e.TextStyle)
	rows = max(1, min(rows, composerRows))
	if rows != e.rows {
		e.rows = rows
		e.SetMinRowsVisible(rows)
	}
}

// wrappedRows is how many lines a text takes in a width, wrapped at words as
// the entry wraps it; an estimate for the height of the composer.
func wrappedRows(text string, width float32, style fyne.TextStyle) int {
	if width <= 0 {
		return 1
	}
	size := theme.TextSize()
	rows := 0
	for _, line := range strings.Split(text, "\n") {
		w := fyne.MeasureText(line, size, style).Width
		rows += max(1, int(math.Ceil(float64(w/width))))
	}
	return rows
}

// text is a paragraph of the window: wrapped, in the monospace face, in the
// size and colour the stylesheet gives it.
func text(value string, size fyne.ThemeSizeName, colorName fyne.ThemeColorName, style fyne.TextStyle) *widget.RichText {
	rich := widget.NewRichText(&widget.TextSegment{Text: value, Style: widget.RichTextStyle{
		SizeName: size, ColorName: colorName, TextStyle: style,
	}})
	rich.Wrapping = fyne.TextWrapWord
	return rich
}

// plain is a line of text that takes no tap, as the title of a session does
// inside the card that selects it.
func plain(value string, size fyne.ThemeSizeName, bold bool) *widget.Label {
	l := widget.NewLabel(value)
	l.TextStyle = fyne.TextStyle{Monospace: true, Bold: bold}
	l.SizeName = size
	l.Wrapping = fyne.TextWrapWord
	return l
}

// selectable is text the owner can select and copy, as what was said in the
// transcript and what a tool returned.
func selectable(value string, size fyne.ThemeSizeName) *widget.Label {
	l := plain(value, size, false)
	l.Selectable = true
	return l
}

// muted is a line of quiet text: the muted colour of the stylesheet. It takes
// no tap, so that a card it is part of opens wherever it is clicked.
func muted(value string, size fyne.ThemeSizeName) *widget.Label {
	l := widget.NewLabel(value)
	l.TextStyle = fyne.TextStyle{Monospace: true}
	l.SizeName = size
	l.Importance = widget.LowImportance
	return l
}

// wrapped is a label that wraps at words instead of running out of its
// column.
func wrapped(l *widget.Label) *widget.Label {
	l.Wrapping = fyne.TextWrapWord
	return l
}

// ellipsis is one line that ends in an ellipsis instead of wrapping, as the
// value of an artefact does.
func ellipsis(value string, size fyne.ThemeSizeName) *widget.Label {
	l := muted(value, size)
	l.Truncation = fyne.TextTruncateEllipsis
	return l
}

// tapText is text that acts as a button: what opens a tool result and expands
// a text, drawn as the text it is rather than as a button. It takes the
// accent under the pointer, as a link does.
type tapText struct {
	widget.Label
	onTap   func()
	hovered bool
}

func newTapText(value string, size fyne.ThemeSizeName, style fyne.TextStyle, onTap func()) *tapText {
	t := &tapText{onTap: onTap}
	t.Text = value
	t.SizeName = size
	t.TextStyle = style
	t.Wrapping = fyne.TextWrapWord
	t.ExtendBaseWidget(t)
	return t
}

func (t *tapText) Tapped(*fyne.PointEvent) {
	if t.onTap != nil {
		t.onTap()
	}
}

func (t *tapText) Cursor() desktop.Cursor { return desktop.PointerCursor }

func (t *tapText) MouseIn(*desktop.MouseEvent) {
	t.hovered = true
	t.Importance = widget.HighImportance
	t.Refresh()
}

func (t *tapText) MouseMoved(*desktop.MouseEvent) {}

func (t *tapText) MouseOut() {
	t.hovered = false
	t.Importance = widget.MediumImportance
	t.Refresh()
}

// tapBox is a box that acts as a button, as one session of the sidebar is. It
// tells its look when the pointer or the focus comes and goes.
type tapBox struct {
	widget.BaseWidget
	content fyne.CanvasObject
	onTap   func()
	hovered bool
	focused bool
	onLook  func()
	key     string // what it opens, as a session card names its session
}

func newTapBox(content fyne.CanvasObject, onTap func()) *tapBox {
	t := &tapBox{content: content, onTap: onTap}
	t.ExtendBaseWidget(t)
	return t
}

func (t *tapBox) Tapped(*fyne.PointEvent) {
	if t.onTap != nil {
		t.onTap()
	}
}

func (t *tapBox) Cursor() desktop.Cursor { return desktop.PointerCursor }

func (t *tapBox) look() {
	if t.onLook != nil {
		t.onLook()
	}
}

func (t *tapBox) MouseIn(*desktop.MouseEvent) {
	t.hovered = true
	t.look()
}

func (t *tapBox) MouseMoved(*desktop.MouseEvent) {}

func (t *tapBox) MouseOut() {
	t.hovered = false
	t.look()
}

func (t *tapBox) FocusGained() {
	t.focused = true
	t.look()
}

func (t *tapBox) FocusLost() {
	t.focused = false
	t.look()
}

func (t *tapBox) TypedRune(rune) {}

// TypedKey opens the card on Enter and Space.
func (t *tapBox) TypedKey(key *fyne.KeyEvent) {
	switch key.Name {
	case fyne.KeyReturn, fyne.KeyEnter, fyne.KeySpace:
		t.Tapped(nil)
	case fyne.KeyEscape:
		pressEscape()
	}
}

func (t *tapBox) CreateRenderer() fyne.WidgetRenderer {
	return &tapBoxRenderer{box: t, objects: []fyne.CanvasObject{t.content}}
}

type tapBoxRenderer struct {
	box     *tapBox
	objects []fyne.CanvasObject
}

func (r *tapBoxRenderer) Layout(size fyne.Size) { r.objects[0].Resize(size) }
func (r *tapBoxRenderer) MinSize() fyne.Size    { return r.objects[0].MinSize() }
func (r *tapBoxRenderer) Refresh()              { r.objects[0].Refresh() }
func (r *tapBoxRenderer) Objects() []fyne.CanvasObject {
	return r.objects
}
func (r *tapBoxRenderer) Destroy() {}

// paint is a rectangle in colours of the palette shown: style sets them each
// time the window draws anew, so that a change of the appearance reaches it.
type paint struct {
	widget.BaseWidget
	rect  *canvas.Rectangle
	style func(*canvas.Rectangle, *palette)
}

func newPaint(style func(*canvas.Rectangle, *palette)) *paint {
	p := &paint{rect: canvas.NewRectangle(color.Transparent), style: style}
	p.ExtendBaseWidget(p)
	return p
}

func (p *paint) CreateRenderer() fyne.WidgetRenderer {
	r := &paintRenderer{paint: p}
	r.Refresh()
	return r
}

type paintRenderer struct{ paint *paint }

func (r *paintRenderer) Layout(size fyne.Size) { r.paint.rect.Resize(size) }
func (r *paintRenderer) MinSize() fyne.Size    { return r.paint.rect.MinSize() }
func (r *paintRenderer) Refresh() {
	r.paint.style(r.paint.rect, pal())
	r.paint.rect.Refresh()
}
func (r *paintRenderer) Objects() []fyne.CanvasObject { return []fyne.CanvasObject{r.paint.rect} }
func (r *paintRenderer) Destroy()                     {}

// insetLayout keeps the padding of a part of the window, as the stylesheet
// writes it: the sidebar 24 28 18, the rail 28, the composer 22 at the foot.
type insetLayout struct {
	top, right, bottom, left float32
}

func inset(top, right, bottom, left float32, object fyne.CanvasObject) *fyne.Container {
	return container.New(insetLayout{top: top, right: right, bottom: bottom, left: left}, object)
}

func (l insetLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	min := objects[0].MinSize()
	return fyne.NewSize(min.Width+l.left+l.right, min.Height+l.top+l.bottom)
}

func (l insetLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	objects[0].Move(fyne.NewPos(l.left, l.top))
	objects[0].Resize(fyne.NewSize(
		fyne.Max(0, size.Width-l.left-l.right),
		fyne.Max(0, size.Height-l.top-l.bottom)))
}

// centerMaxLayout centers a column of at most max pixels in the width it is
// given, as the transcript is 760 pixels wide at most. The column keeps a
// gutter of its own to the edges where the room is narrower.
type centerMaxLayout struct{ max, gutter float32 }

// gutterCentered centers object in a column of at most max pixels, with
// gutter pixels on both sides at least.
func gutterCentered(max, gutter float32, object fyne.CanvasObject) *fyne.Container {
	return container.New(centerMaxLayout{max: max, gutter: gutter}, object)
}

func (l centerMaxLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	min := objects[0].MinSize()
	return fyne.NewSize(min.Width+2*l.gutter, min.Height)
}

func (l centerMaxLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	width := fyne.Max(0, fyne.Min(size.Width-2*l.gutter, l.max))
	objects[0].Move(fyne.NewPos((size.Width-width)/2, 0))
	objects[0].Resize(fyne.NewSize(width, fyne.Max(size.Height, objects[0].MinSize().Height)))
}

// The widths at which the window changes its columns, as the stylesheet
// does: the rail goes below railWidth, the sidebar becomes a drawer below
// narrowWidth.
const (
	railWidth   = 1100
	narrowWidth = 640
)

// columnsLayout is the window as its grid is: the sidebar, the thread and the
// rail, with the rail gone below 1100 pixels and the sidebar a drawer over the
// thread below 640. Its objects are the main column, the rail, the scrim of
// the drawer and the sidebar, in this order, so that the drawer lies on top.
type columnsLayout struct {
	ui *fyneUI
}

func (l *columnsLayout) MinSize([]fyne.CanvasObject) fyne.Size {
	return fyne.NewSize(360, 420)
}

func (l *columnsLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	main, rail, scrim, sidebar := objects[0], objects[1], objects[2], objects[3]
	ui := l.ui
	narrow := size.Width < narrowWidth
	if narrow != ui.narrow {
		ui.setNarrow(narrow)
	}
	if narrow {
		rail.Hide()
		main.Move(fyne.NewPos(0, 0))
		main.Resize(size)
		scrim.Move(fyne.NewPos(0, 0))
		scrim.Resize(size)
		width := fyne.Min(320, size.Width*0.86)
		sidebar.Resize(fyne.NewSize(width, size.Height))
		if ui.drawerOpen {
			scrim.Show()
			sidebar.Move(fyne.NewPos(0, 0))
			sidebar.Show()
		} else {
			scrim.Hide()
			sidebar.Hide()
		}
		return
	}
	scrim.Hide()
	sidebar.Show()
	sideWidth := clamp(220, size.Width*0.23, 400)
	if size.Width <= 900 {
		sideWidth = 220
	}
	sidebar.Move(fyne.NewPos(0, 0))
	sidebar.Resize(fyne.NewSize(sideWidth, size.Height))
	railSize := float32(0)
	if size.Width > railWidth {
		railSize = clamp(200, size.Width*0.23, 400)
		rail.Show()
		rail.Move(fyne.NewPos(size.Width-railSize, 0))
		rail.Resize(fyne.NewSize(railSize, size.Height))
	} else {
		rail.Hide()
	}
	main.Move(fyne.NewPos(sideWidth, 0))
	main.Resize(fyne.NewSize(fyne.Max(0, size.Width-sideWidth-railSize), size.Height))
}

func clamp(minimum, value, maximum float32) float32 {
	return fyne.Max(minimum, fyne.Min(value, maximum))
}

// flatButton is a button of the window: a label within a line, without the
// box of a standard button, as the stylesheet draws the composer, the
// questions and the way back to the thread. It shows the hover colour under
// the pointer and the accent ring with the focus, as a native button does.
type flatButton struct {
	widget.BaseWidget
	label     string
	res       fyne.Resource
	iconSize  float32
	round     bool
	tall      bool
	minHeight float32
	minWidth  float32
	textSize  float32
	pad       float32
	filled    bool
	strong    bool
	accent    bool
	bad       bool
	icon      bool
	disabled  bool
	focused   bool
	hovered   bool
	onTap     func()
}

func newFlatButton(label string, onTap func()) *flatButton {
	return newButtonKind(label, false, false, false, false, onTap)
}

// newBackButton is the way back to the thread.
func newBackButton(label string, onTap func()) *flatButton {
	b := newFlatButton(label, onTap)
	b.minHeight = 34
	b.filled = true
	return b
}

func newOptionButton(label string, recommended bool, onTap func()) *flatButton {
	b := newButtonKind(label, false, false, recommended, false, onTap)
	b.tall = true
	b.minHeight = 34
	b.textSize = 11
	b.pad = 14
	b.filled = true
	return b
}

func newStopButton(label string, onTap func()) *flatButton {
	b := newButtonKind(label, false, false, false, true, onTap)
	b.tall = true
	return b
}

// newSendButton is the round button that sends what is written.
func newSendButton(onTap func()) *flatButton {
	b := newButtonKind("", true, false, false, false, onTap)
	b.res = theme.MoveUpIcon()
	b.iconSize = 18
	b.tall = true
	return b
}

// newIconButton is a quiet button that shows a symbol of the theme: its
// outline shows only under the pointer and with the focus.
func newIconButton(res fyne.Resource, onTap func()) *flatButton {
	b := newButtonKind("", false, false, false, false, onTap)
	b.res = res
	b.icon = true
	b.iconSize = 20
	b.minWidth = 36
	b.minHeight = 36
	return b
}

// newDialogButton is a button of a dialog, with the padding and the height
// the stylesheet gives them.
func newDialogButton(label string, onTap func()) *flatButton {
	b := newButtonKind(label, false, false, false, false, onTap)
	b.minHeight = 40
	b.textSize = 13
	b.pad = 18
	b.filled = true
	return b
}

// newDialogAction is a button of a dialog's actions, as the stylesheet draws
// them: on the surface, and the one that saves a little stronger.
func newDialogAction(label string, save bool, onTap func()) *flatButton {
	b := newButtonKind(label, false, false, false, false, onTap)
	b.minHeight = 40
	b.textSize = 13
	b.pad = 16
	b.filled = true
	b.strong = save
	return b
}

func newButtonKind(label string, round, tall, accent, bad bool, onTap func()) *flatButton {
	b := &flatButton{label: label, round: round, tall: tall, accent: accent, bad: bad, onTap: onTap}
	b.ExtendBaseWidget(b)
	return b
}

func (b *flatButton) Tapped(*fyne.PointEvent) {
	if !b.disabled && b.onTap != nil {
		b.onTap()
	}
}

func (b *flatButton) Cursor() desktop.Cursor {
	if b.disabled {
		return desktop.DefaultCursor
	}
	return desktop.PointerCursor
}

func (b *flatButton) MouseIn(*desktop.MouseEvent) {
	b.hovered = true
	b.Refresh()
}

func (b *flatButton) MouseMoved(*desktop.MouseEvent) {}

func (b *flatButton) MouseOut() {
	b.hovered = false
	b.Refresh()
}

func (b *flatButton) FocusGained() {
	b.focused = true
	b.Refresh()
}

func (b *flatButton) FocusLost() {
	b.focused = false
	b.Refresh()
}

func (b *flatButton) TypedRune(rune) {}

// TypedKey runs the button on Enter and Space, as a button does.
func (b *flatButton) TypedKey(key *fyne.KeyEvent) {
	switch key.Name {
	case fyne.KeyReturn, fyne.KeyEnter, fyne.KeySpace:
		b.Tapped(nil)
	case fyne.KeyEscape:
		pressEscape()
	}
}

// Enable makes the button take taps again.
func (b *flatButton) Enable() {
	if !b.disabled {
		return
	}
	b.disabled = false
	b.Refresh()
}

// Disable greys the button out and keeps it from taking taps.
func (b *flatButton) Disable() {
	if b.disabled {
		return
	}
	b.disabled = true
	b.Refresh()
}

// Disabled reports whether the button takes taps.
func (b *flatButton) Disabled() bool { return b.disabled }

// SetLabel changes what the button says.
func (b *flatButton) SetLabel(label string) {
	b.label = label
	b.Refresh()
}

func (b *flatButton) CreateRenderer() fyne.WidgetRenderer {
	r := &flatButtonRenderer{button: b}
	r.ink = canvas.NewText(b.label, color.Transparent)
	r.ink.TextStyle = fyne.TextStyle{Monospace: true}
	r.ink.Alignment = fyne.TextAlignCenter
	r.fill = canvas.NewRectangle(color.Transparent)
	if b.res != nil {
		r.image = canvas.NewImageFromResource(b.res)
		r.image.FillMode = canvas.ImageFillContain
	}
	r.Refresh()
	return r
}

type flatButtonRenderer struct {
	button *flatButton
	fill   *canvas.Rectangle
	ink    *canvas.Text
	image  *canvas.Image
}

func (r *flatButtonRenderer) Layout(size fyne.Size) {
	r.fill.Move(fyne.NewPos(0, 0))
	r.fill.Resize(size)
	if r.image != nil {
		side := r.button.iconSize
		r.image.Move(fyne.NewPos((size.Width-side)/2, (size.Height-side)/2))
		r.image.Resize(fyne.NewSize(side, side))
	}
	textSize := r.ink.MinSize()
	r.ink.Move(fyne.NewPos((size.Width-textSize.Width)/2, (size.Height-textSize.Height)/2))
	r.ink.Resize(textSize)
}

func (r *flatButtonRenderer) MinSize() fyne.Size {
	if r.button.round {
		return fyne.NewSize(32, 32)
	}
	textSize := r.ink.MinSize()
	if r.button.label == "" {
		textSize = fyne.NewSize(r.button.iconSize, r.button.iconSize)
	}
	height := float32(26)
	if r.button.tall {
		height = 32
	}
	height = fyne.Max(height, r.button.minHeight)
	pad := float32(12)
	if r.button.pad > 0 {
		pad = r.button.pad
	}
	if r.button.icon {
		pad = 8
	}
	width := fyne.Max(textSize.Width+2*pad, r.button.minWidth)
	return fyne.NewSize(width, fyne.Max(height, textSize.Height+10))
}

func (r *flatButtonRenderer) Refresh() {
	b := r.button
	p := pal()
	r.ink.Text = b.label
	r.ink.TextSize = 10
	if b.textSize > 0 {
		r.ink.TextSize = b.textSize
	}
	ink := p.text
	if b.bad {
		ink = p.error
	}
	if b.disabled {
		ink = p.muted
	}
	r.ink.Color = ink
	r.fill.CornerRadius = 8
	if b.round {
		r.fill.CornerRadius = 16
	}
	r.fill.FillColor = color.Transparent
	r.fill.StrokeColor = p.line
	r.fill.StrokeWidth = 1
	if b.accent {
		r.fill.StrokeColor = p.accent
	}
	if b.bad && !b.disabled {
		r.fill.StrokeColor = p.errorLine
	}
	if b.icon {
		r.fill.StrokeColor = color.Transparent
	}
	if b.filled {
		r.fill.FillColor = p.field
	}
	if b.strong {
		r.fill.FillColor = p.hover
		r.fill.StrokeColor = p.lineStrong
	}
	if b.hovered && !b.disabled {
		r.fill.FillColor = p.hover
		if b.icon {
			r.fill.StrokeColor = p.line
		} else if b.round {
			r.fill.StrokeColor = p.accent
		}
	}
	if b.focused {
		r.fill.StrokeColor = p.accent
		r.fill.StrokeWidth = 2
	}
	if r.image != nil {
		switch {
		case b.disabled:
			r.image.Resource = theme.NewDisabledResource(b.res)
		case b.hovered:
			r.image.Resource = theme.NewColoredResource(b.res, theme.ColorNamePrimary)
		default:
			r.image.Resource = theme.NewThemedResource(b.res)
		}
		r.image.Refresh()
	}
	r.fill.Refresh()
	r.ink.Refresh()
}

func (r *flatButtonRenderer) Objects() []fyne.CanvasObject {
	if r.image != nil {
		return []fyne.CanvasObject{r.fill, r.image, r.ink}
	}
	return []fyne.CanvasObject{r.fill, r.ink}
}

func (r *flatButtonRenderer) Destroy() {}

// dashedBar is the bar of a session: dashes while it works, a quiet line when
// it does not, as the sidebar draws it.
func dashedBar(running bool) fyne.CanvasObject {
	bar := canvas.NewRaster(func(width, height int) image.Image {
		img := image.NewNRGBA(image.Rect(0, 0, width, height))
		if width <= 0 || height <= 0 {
			return img
		}
		var ink color.NRGBA
		if running {
			ink = accentColor()
		} else {
			ink = mutedColor()
			ink.A = 0x73
		}
		top := 0
		if height > 4 {
			top = (height - 4) / 2
		}
		for x := 0; x < width; x += 5 {
			draw.Draw(img, image.Rect(x, top, min(x+3, width), top+4), &image.Uniform{C: ink}, image.Point{}, draw.Src)
		}
		return img
	})
	bar.SetMinSize(fyne.NewSize(1, 4))
	return bar
}

// dot is the state mark of an artefact: quiet, live in the accent, or bad in
// the error colour.
func dot(live, bad bool) fyne.CanvasObject {
	p := pal()
	fill := p.lineStrong
	if bad {
		fill = p.error
	} else if live {
		fill = p.accent
	}
	circle := canvas.NewCircle(fill)
	return container.NewCenter(container.NewGridWrap(fyne.NewSize(6, 6), circle))
}

// accentColor is the accent of the appearance shown.
func accentColor() color.NRGBA { return pal().accent }

// mutedColor is the quiet colour of the appearance shown.
func mutedColor() color.NRGBA { return pal().muted }

// topFadeLayout lays the fade of older messages over the top of the
// transcript, as the stylesheet draws it.
type topFadeLayout struct{}

func (l *topFadeLayout) MinSize([]fyne.CanvasObject) fyne.Size { return fyne.NewSize(0, 0) }

func (l *topFadeLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	objects[0].Move(fyne.NewPos(0, 0))
	objects[0].Resize(fyne.NewSize(size.Width, fyne.Min(size.Height*0.4, 140)))
}

// outline draws the line of a field around an object, as the stylesheet
// wraps the entries and selects of a dialog.
func outline(object fyne.CanvasObject) *fyne.Container {
	edge := newPaint(func(r *canvas.Rectangle, p *palette) {
		r.FillColor = p.field
		r.StrokeColor = p.line
		r.StrokeWidth = 1
		r.CornerRadius = 8
	})
	return container.NewStack(edge, inset(2, 4, 2, 4, object))
}

// fadeGradient is the top of the history fading into the background, as the
// window fades older messages.
func fadeGradient(width, height int) image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, width, max(1, height)))
	background := pal().background
	for y := 0; y < height; y++ {
		t := float64(y) / float64(max(1, height))
		alpha := 0.85 * (1 - t) * (1 - t)
		line := color.NRGBA{R: background.R, G: background.G, B: background.B, A: uint8(alpha * 255)}
		draw.Draw(img, image.Rect(0, y, width, y+1), &image.Uniform{C: line}, image.Point{}, draw.Src)
	}
	return img
}

// tightVBox stacks objects with the gap given, which may be negative: the
// line boxes of Fyne are taller than the line height of the stylesheet, so
// the window lets them overlap to keep the rhythm of the page.
func tightVBox(gap float32, objects ...fyne.CanvasObject) *fyne.Container {
	return container.New(&tightVBoxLayout{gap: gap}, objects...)
}

type tightVBoxLayout struct{ gap float32 }

func (l *tightVBoxLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	width, height := float32(0), float32(0)
	shown := 0
	for _, object := range objects {
		if !object.Visible() {
			continue
		}
		min := object.MinSize()
		width = fyne.Max(width, min.Width)
		height += min.Height
		shown++
	}
	if shown > 1 {
		height += l.gap * float32(shown-1)
	}
	return fyne.NewSize(width, height)
}

func (l *tightVBoxLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	y := float32(0)
	first := true
	for _, object := range objects {
		if !object.Visible() {
			continue
		}
		if !first {
			y += l.gap
		}
		min := object.MinSize()
		object.Move(fyne.NewPos(0, y))
		object.Resize(fyne.NewSize(size.Width, min.Height))
		y += min.Height
		first = false
	}
}

// flowLayout lays objects in rows as wide as they are, wrapping to the next
// row where the width ends, as the options of a question do.
type flowLayout struct{ gap float32 }

func (l *flowLayout) rows(objects []fyne.CanvasObject, width float32) (positions []fyne.Position, height float32) {
	x, y, row := float32(0), float32(0), float32(0)
	positions = make([]fyne.Position, len(objects))
	for i, object := range objects {
		if !object.Visible() {
			continue
		}
		min := object.MinSize()
		if x > 0 && x+min.Width > width {
			x = 0
			y += row + l.gap
			row = 0
		}
		positions[i] = fyne.NewPos(x, y)
		x += min.Width + l.gap
		row = fyne.Max(row, min.Height)
	}
	return positions, y + row
}

func (l *flowLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	width, height := float32(0), float32(0)
	for _, object := range objects {
		if object.Visible() {
			min := object.MinSize()
			width = fyne.Max(width, min.Width)
			height = fyne.Max(height, min.Height)
		}
	}
	return fyne.NewSize(width, height)
}

func (l *flowLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	positions, _ := l.rows(objects, size.Width)
	for i, object := range objects {
		if !object.Visible() {
			continue
		}
		object.Move(positions[i])
		width := fyne.Min(object.MinSize().Width, size.Width)
		object.Resize(fyne.NewSize(width, object.MinSize().Height))
	}
}

// flowBox is a widget around a flow of objects that is as tall as its rows
// are in the width it was last given.
type flowBox struct {
	widget.BaseWidget
	box    *fyne.Container
	layout *flowLayout
	width  float32
}

func newFlowBox(gap float32) *flowBox {
	l := &flowLayout{gap: gap}
	f := &flowBox{box: container.New(l), layout: l}
	f.ExtendBaseWidget(f)
	return f
}

// SetObjects replaces what flows.
func (f *flowBox) SetObjects(objects ...fyne.CanvasObject) {
	f.box.Objects = objects
	f.box.Refresh()
	f.Refresh()
}

func (f *flowBox) Resize(size fyne.Size) {
	f.width = size.Width
	f.BaseWidget.Resize(size)
}

func (f *flowBox) CreateRenderer() fyne.WidgetRenderer {
	return &flowRenderer{flow: f}
}

type flowRenderer struct{ flow *flowBox }

func (r *flowRenderer) Layout(size fyne.Size) {
	r.flow.box.Resize(size)
}

func (r *flowRenderer) MinSize() fyne.Size {
	min := r.flow.layout.MinSize(r.flow.box.Objects)
	if r.flow.width > 0 {
		_, height := r.flow.layout.rows(r.flow.box.Objects, r.flow.width)
		min.Height = height
	}
	return min
}

func (r *flowRenderer) Refresh()                     { r.flow.box.Refresh() }
func (r *flowRenderer) Objects() []fyne.CanvasObject { return []fyne.CanvasObject{r.flow.box} }
func (r *flowRenderer) Destroy()                     {}

// sidebarLayout places the sessions of the sidebar as its column does: the
// heading between equal shares of the free room, the list with what it holds
// up to 48 percent of the height, and the settings at the foot. In the drawer
// of a narrow window the list starts at the top and takes the room there is.
type sidebarLayout struct {
	ui *fyneUI
}

func (l *sidebarLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	return fyne.NewSize(160, objects[0].MinSize().Height+objects[2].MinSize().Height+40)
}

func (l *sidebarLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	heading, list, footer := objects[0], objects[1], objects[2]
	headingHeight := heading.MinSize().Height
	footerHeight := footer.MinSize().Height
	share := float32(0.48)
	drawer := l.ui != nil && l.ui.narrow
	if drawer {
		share = 1
	}
	listHeight := float32(40)
	if scroll, ok := list.(*container.Scroll); ok {
		listHeight = fyne.Min(scroll.Content.MinSize().Height, size.Height*share)
	}
	listHeight = fyne.Max(listHeight, 40)
	if !heading.Visible() {
		headingHeight = 0
	}
	if !list.Visible() {
		listHeight = 0
	}
	listHeight = fyne.Min(listHeight, fyne.Max(0, size.Height-headingHeight-footerHeight))
	free := fyne.Max(0, size.Height-headingHeight-listHeight-footerHeight)
	y := free / 2
	if drawer {
		y = 0
	}
	heading.Move(fyne.NewPos(0, y))
	heading.Resize(fyne.NewSize(size.Width, headingHeight))
	y += headingHeight
	list.Move(fyne.NewPos(0, y))
	list.Resize(fyne.NewSize(size.Width, listHeight))
	footer.Move(fyne.NewPos(0, size.Height-footerHeight))
	footer.Resize(fyne.NewSize(size.Width, footerHeight))
}

// railLayout places the context rail as its column does: the whole content at
// the foot, ten pixels apart, with the details no taller than 58 percent of
// the height and scrolling inside themselves.
type railLayout struct{}

func (l *railLayout) MinSize([]fyne.CanvasObject) fyne.Size {
	return fyne.NewSize(160, 100)
}

func (l *railLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	const gap = 4
	eyebrow, title, note, details := objects[0], objects[1], objects[2], objects[3]
	note.Resize(fyne.NewSize(size.Width, note.MinSize().Height))
	heights := []float32{eyebrow.MinSize().Height, title.MinSize().Height, note.MinSize().Height}
	detailsHeight := float32(0)
	if scroll, ok := details.(*container.Scroll); ok {
		detailsHeight = fyne.Min(scroll.Content.MinSize().Height, size.Height*0.58)
	}
	total := heights[0] + heights[1] + heights[2] + detailsHeight + 3*gap
	if total > size.Height {
		detailsHeight = fyne.Max(0, detailsHeight-(total-size.Height))
		total = size.Height
	}
	y := size.Height - total
	for i, object := range []fyne.CanvasObject{eyebrow, title, note} {
		object.Move(fyne.NewPos(0, y))
		object.Resize(fyne.NewSize(size.Width, heights[i]))
		y += heights[i] + gap
	}
	details.Move(fyne.NewPos(0, y))
	details.Resize(fyne.NewSize(size.Width, detailsHeight))
}

// fixedWidth gives a label the width of its column in a row, as the keys of
// the artefacts are 46 pixels wide.
func fixedWidth(width float32, object fyne.CanvasObject) *fyne.Container {
	return container.New(&fixedWidthLayout{width: width}, object)
}

type fixedWidthLayout struct{ width float32 }

func (l *fixedWidthLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	return fyne.NewSize(l.width, objects[0].MinSize().Height)
}

func (l *fixedWidthLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	objects[0].Move(fyne.NewPos(0, 0))
	objects[0].Resize(fyne.NewSize(l.width, size.Height))
}

// cappedLayout keeps a scroller no taller than what it holds and a share of
// the height of another object, as the question panel stays below 40
// percent of the thread and scrolls inside itself beyond.
type cappedLayout struct {
	of    fyne.CanvasObject
	share float32
}

func (l *cappedLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	min := objects[0].MinSize()
	if scroll, ok := objects[0].(*container.Scroll); ok {
		min = scroll.Content.MinSize()
	}
	if l.of != nil && l.of.Size().Height > 0 {
		min.Height = fyne.Min(min.Height, l.of.Size().Height*l.share)
	}
	return min
}

func (l *cappedLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	objects[0].Move(fyne.NewPos(0, 0))
	objects[0].Resize(size)
}

// dialogLayout centers the card of a dialog over the window: 16 pixels from
// its edges at the least, and 460 pixels wide as the stylesheet draws it.
// The card scrolls; measure is what it holds, whose height the card takes
// where the window has the room.
type dialogLayout struct {
	max, maxHeight float32
	measure        fyne.CanvasObject
}

func (l *dialogLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	return fyne.NewSize(0, 0)
}

func (l *dialogLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	width := fyne.Max(0, fyne.Min(l.max, size.Width-32))
	want := objects[0].MinSize().Height
	if l.measure != nil {
		want = l.measure.MinSize().Height
	}
	height := fyne.Max(0, fyne.Min(fyne.Min(l.maxHeight, size.Height-32), want))
	objects[0].Move(fyne.NewPos((size.Width-width)/2, (size.Height-height)/2))
	objects[0].Resize(fyne.NewSize(width, height))
}

// scrim shades the window behind a dialog or the open drawer and takes the
// taps that miss them. It names no pointer, so the arrow shows over it and
// over what of the dialog takes no clicks, as over a native dialog.
type scrim struct {
	widget.BaseWidget
	onTap func()
}

func newScrim(onTap func()) *scrim {
	s := &scrim{onTap: onTap}
	s.ExtendBaseWidget(s)
	return s
}

func (s *scrim) Tapped(*fyne.PointEvent) {
	if s.onTap != nil {
		s.onTap()
	}
}

func (s *scrim) CreateRenderer() fyne.WidgetRenderer {
	return widget.NewSimpleRenderer(newPaint(func(r *canvas.Rectangle, _ *palette) { r.FillColor = scrimColor() }))
}

// scrimColor is the shade over the window behind a dialog or the drawer.
func scrimColor() color.NRGBA { return color.NRGBA{R: 0x14, G: 0x15, B: 0x10, A: 0x66} }
