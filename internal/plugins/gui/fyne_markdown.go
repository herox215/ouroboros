//go:build cgo

package gui

import (
	"fmt"
	"image/color"
	"sort"
	"strings"
	"time"
	"unicode"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// markdownText is what the agent says and answers, formatted from the
// markdown the models write: headings, emphasis, code, lists, quotes, tables
// and links, without the marks. It lays its text out itself, so that the
// mouse selects it as in a page: a drag marks text, a double click a word,
// and the shortcuts copy what is marked or mark all. A right click copies
// what is marked, or else the source.
type markdownText struct {
	widget.BaseWidget
	source string
	doc    *mdDoc
	// outer is the scroll the text is in: a table, which scrolls only
	// sideways, hands it the turns of the wheel up and down.
	outer fyne.Scrollable

	// anchor and caret are where the selection begins and ends, as indexes
	// into the plain text of the document; they are equal when none is.
	anchor, caret int
	hover         fyne.Position

	layout    *mdLayout
	selection []fyne.CanvasObject
	objects   []fyne.CanvasObject

	// words lays the text out a word a fragment, so that each word can
	// stream in on its own; streaming lets the words that are new to it
	// appear one after the other, each rising from below as it fades in.
	// born is when each word, in the order of the text, appears.
	words, streaming bool
	born             []time.Time
	reveal           *fyne.Animation
}

func newMarkdownText(value string) *markdownText {
	t := &markdownText{}
	t.ExtendBaseWidget(t)
	t.SetMarkdown(value)
	return t
}

// SetMarkdown shows value formatted, in the sizes of the window.
func (t *markdownText) SetMarkdown(value string) {
	t.source = value
	t.doc = parseMarkdownDoc(value)
	t.layout = nil
	t.anchor, t.caret = 0, 0
	t.Refresh()
}

// Text is the markdown the text shows.
func (t *markdownText) Text() string { return t.source }

// SelectedText is the plain text that is marked.
func (t *markdownText) SelectedText() string {
	from, to := t.selected()
	return string(t.doc.text[from:to])
}

func (t *markdownText) selected() (int, int) {
	from, to := min(t.anchor, t.caret), max(t.anchor, t.caret)
	return max(0, from), min(len(t.doc.text), to)
}

func (t *markdownText) selectRange(anchor, caret int) {
	if anchor == t.anchor && caret == t.caret {
		return
	}
	t.anchor, t.caret = anchor, caret
	t.updateSelection()
}

func (t *markdownText) Resize(size fyne.Size) {
	if size == t.Size() {
		return
	}
	height := float32(-1)
	if t.layout != nil {
		height = t.layout.height
	}
	t.BaseWidget.Resize(size)
	if t.layout == nil || t.layout.height != height {
		t.Refresh()
	}
}

func (t *markdownText) CreateRenderer() fyne.WidgetRenderer {
	return &markdownRenderer{text: t}
}

// ensureLayout lays the text out in width, unless it is laid out so.
func (t *markdownText) ensureLayout(width float32) {
	if t.layout != nil && t.layout.width == width {
		return
	}
	t.layout = t.doc.layout(width, t)
	t.stamp()
	t.revealAt(time.Now())
	t.updateSelection()
}

// How the words of a text stream in: each rises revealRise from below while
// it fades in for revealTime, the new ones of a text one after the other,
// all of them within about revealSpan.
const (
	revealRise    = 12
	revealTime    = 380 * time.Millisecond
	revealSpan    = 1200 * time.Millisecond
	revealStepMin = 3 * time.Millisecond
	revealStepMax = 30 * time.Millisecond
)

// stamp gives the words the layout has beyond those the text had the time
// they appear: now and one after the other when it streams, at once when not.
func (t *markdownText) stamp() {
	n := len(t.layout.order)
	if n <= len(t.born) {
		// A final answer inherits the partial answer's times, but needs
		// its own animation to finish the words still on their way in.
		if n > 0 && time.Now().Before(t.born[n-1].Add(revealTime)) {
			t.animate()
		}
		return
	}
	if !t.streaming {
		for len(t.born) < n {
			t.born = append(t.born, time.Time{})
		}
		return
	}
	fresh := n - len(t.born)
	step := min(revealStepMax, max(revealStepMin, revealSpan/time.Duration(fresh)))
	at := time.Now().Add(-step)
	if k := len(t.born); k > 0 && t.born[k-1].After(at) {
		at = t.born[k-1]
	}
	for range fresh {
		at = at.Add(step)
		t.born = append(t.born, at)
	}
	t.animate()
}

// animate runs the words in until all of them stand.
func (t *markdownText) animate() {
	if t.reveal != nil {
		return
	}
	var reveal *fyne.Animation
	reveal = fyne.NewAnimation(time.Second, func(float32) {
		if t.revealAt(time.Now()) {
			reveal.Stop()
			if t.reveal == reveal {
				t.reveal = nil
			}
		}
	})
	reveal.RepeatCount = fyne.AnimationRepeatForever
	t.reveal = reveal
	reveal.Start()
}

// stopRevealAnimation stops the words on their way in, where they are.
func (t *markdownText) stopRevealAnimation() {
	if t.reveal != nil {
		t.reveal.Stop()
		t.reveal = nil
	}
}

// stopReveal lets every word stand where it belongs.
func (t *markdownText) stopReveal() {
	t.stopRevealAnimation()
	for i := range t.born {
		t.born[i] = time.Time{}
	}
	t.revealAt(time.Now())
}

// revealAt shows the words as they are at now on their way in, and tells
// whether all of them stand.
func (t *markdownText) revealAt(now time.Time) bool {
	if t.layout == nil {
		return true
	}
	done := true
	for i, item := range t.layout.order {
		if item.done {
			continue
		}
		f := float32(1)
		if i < len(t.born) && !t.born[i].IsZero() {
			f = max(0, min(1, float32(now.Sub(t.born[i]))/float32(revealTime)))
		}
		e := 1 - (1-f)*(1-f)*(1-f)
		c := color.NRGBAModel.Convert(theme.Color(item.color)).(color.NRGBA)
		c.A = uint8(float32(c.A) * e)
		item.label.Color = c
		item.label.Move(fyne.NewPos(item.base.X, item.base.Y+(1-e)*revealRise))
		item.label.Refresh()
		if f < 1 {
			done = false
		} else {
			item.done = true
		}
	}
	return done
}

// updateSelection draws the shade of the selection anew.
func (t *markdownText) updateSelection() {
	if t.layout == nil {
		return
	}
	from, to := t.selected()
	t.selection = t.layout.shade(from, to)
	t.objects = make([]fyne.CanvasObject, 0, len(t.layout.back)+len(t.selection)+len(t.layout.front))
	t.objects = append(t.objects, t.layout.back...)
	t.objects = append(t.objects, t.selection...)
	t.objects = append(t.objects, t.layout.front...)
	canvas.Refresh(t)
}

func (t *markdownText) focus() {
	if app := fyne.CurrentApp(); app != nil {
		if c := app.Driver().CanvasForObject(t); c != nil && c.Focused() != t {
			c.Focus(t)
		}
	}
}

func (t *markdownText) index(p fyne.Position) int {
	if t.layout == nil {
		return 0
	}
	return t.layout.index(p)
}

func (t *markdownText) MouseDown(event *desktop.MouseEvent) {
	if event.Button != desktop.MouseButtonPrimary {
		return
	}
	t.focus()
	i := t.index(event.Position)
	if event.Modifier&fyne.KeyModifierShift != 0 {
		t.selectRange(t.anchor, i)
		return
	}
	t.selectRange(i, i)
}

func (t *markdownText) MouseUp(*desktop.MouseEvent) {}

func (t *markdownText) Dragged(event *fyne.DragEvent) {
	t.selectRange(t.anchor, t.index(event.Position))
}

func (t *markdownText) DragEnd() {}

// DoubleTapped marks the word under the pointer.
func (t *markdownText) DoubleTapped(event *fyne.PointEvent) {
	text := t.doc.text
	i := t.index(event.Position)
	if i >= len(text) || !isWordRune(text[i]) {
		if i < len(text) && text[i] != '\n' {
			t.selectRange(i, i+1)
		}
		return
	}
	from, to := i, i
	for from > 0 && isWordRune(text[from-1]) {
		from--
	}
	for to < len(text) && isWordRune(text[to]) {
		to++
	}
	t.selectRange(from, to)
}

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}

// Tapped follows the link under the pointer.
func (t *markdownText) Tapped(event *fyne.PointEvent) {
	if t.anchor != t.caret || t.layout == nil {
		return
	}
	link := t.layout.link(event.Position)
	if link == nil {
		return
	}
	if link.OnTapped != nil {
		link.OnTapped()
	} else if link.URL != nil {
		if app := fyne.CurrentApp(); app != nil {
			_ = app.OpenURL(link.URL)
		}
	}
}

func (t *markdownText) TappedSecondary(event *fyne.PointEvent) {
	app := fyne.CurrentApp()
	if app == nil {
		return
	}
	c := app.Driver().CanvasForObject(t)
	if c == nil {
		return
	}
	menu := fyne.NewMenu("", fyne.NewMenuItem(tr("transcript.copy", nil), func() {
		t.copy(app)
	}))
	widget.ShowPopUpMenuAtPosition(menu, c, event.AbsolutePosition)
}

// copy puts what is marked on the clipboard, or the source when nothing is.
func (t *markdownText) copy(app fyne.App) {
	value := t.SelectedText()
	if value == "" {
		value = t.source
	}
	app.Clipboard().SetContent(value)
}

func (t *markdownText) Cursor() desktop.Cursor {
	if t.layout != nil && t.layout.link(t.hover) != nil {
		return desktop.PointerCursor
	}
	return desktop.TextCursor
}

func (t *markdownText) MouseIn(event *desktop.MouseEvent)    { t.hover = event.Position }
func (t *markdownText) MouseMoved(event *desktop.MouseEvent) { t.hover = event.Position }
func (t *markdownText) MouseOut()                            {}

func (t *markdownText) FocusGained() {}

// FocusLost lets go of the selection, so that one text at most shows one.
func (t *markdownText) FocusLost() { t.selectRange(t.caret, t.caret) }

func (t *markdownText) TypedRune(rune)          {}
func (t *markdownText) TypedKey(*fyne.KeyEvent) {}

func (t *markdownText) TypedShortcut(shortcut fyne.Shortcut) {
	switch shortcut.(type) {
	case *fyne.ShortcutCopy:
		if app := fyne.CurrentApp(); app != nil && t.anchor != t.caret {
			app.Clipboard().SetContent(t.SelectedText())
		}
	case *fyne.ShortcutSelectAll:
		t.selectRange(0, len(t.doc.text))
	}
}

type markdownRenderer struct{ text *markdownText }

func (r *markdownRenderer) Layout(size fyne.Size) { r.text.ensureLayout(size.Width) }

func (r *markdownRenderer) MinSize() fyne.Size {
	width := r.text.Size().Width
	if width <= 0 {
		width = 600
	}
	r.text.ensureLayout(width)
	return fyne.NewSize(0, r.text.layout.height)
}

// Refresh lays the text out anew if it changed, and gives it the colours of
// the theme shown.
func (r *markdownRenderer) Refresh() {
	t := r.text
	if t.layout == nil {
		if width := t.Size().Width; width > 0 {
			t.ensureLayout(width)
		}
		return
	}
	t.layout.recolor()
	for _, item := range t.layout.order {
		item.done = false
	}
	t.revealAt(time.Now())
	t.updateSelection()
}

func (r *markdownRenderer) Objects() []fyne.CanvasObject { return r.text.objects }
func (r *markdownRenderer) Destroy()                     { r.text.stopReveal() }

// mdDoc is markdown parsed into the blocks the window draws, and the plain
// text a selection copies from.
type mdDoc struct {
	blocks []*mdBlock
	text   []rune
}

type mdKind int

const (
	mdText mdKind = iota
	mdCode
	mdRule
	mdTable
)

// mdBlock is a paragraph, a heading, an item of a list, a quote, a block of
// code, a rule or a table.
type mdBlock struct {
	kind   mdKind
	runs   []*mdRun
	marker string
	indent int
	quote  int
	list   int
	space  float32
	table  *widget.TableSegment
	start  int
	end    int
}

// mdRun is text of one style.
type mdRun struct {
	text  []rune
	size  fyne.ThemeSizeName
	style fyne.TextStyle
	color fyne.ThemeColorName
	code  bool
	link  *widget.HyperlinkSegment
	quote int
	start int
}

// parseMarkdownDoc reads markdown as Fyne parses it into blocks.
func parseMarkdownDoc(source string) *mdDoc {
	b := &mdBuilder{}
	b.segments(widget.NewRichTextFromMarkdown(source).Segments)
	b.flush()
	doc := &mdDoc{blocks: b.blocks}
	for i, block := range doc.blocks {
		if i > 0 {
			doc.text = append(doc.text, '\n')
		}
		block.start = len(doc.text)
		for _, run := range block.runs {
			run.start = len(doc.text)
			doc.text = append(doc.text, run.text...)
		}
		if block.table != nil {
			doc.text = append(doc.text, []rune(strings.TrimRight(block.table.Textual(), "\n"))...)
		}
		block.end = len(doc.text)
	}
	return doc
}

type mdBuilder struct {
	blocks []*mdBlock
	runs   []*mdRun
	marker string
	indent int
	list   int
	lists  int
}

func (b *mdBuilder) segments(segments []widget.RichTextSegment) {
	for _, segment := range segments {
		switch s := segment.(type) {
		case *widget.TextSegment:
			if s.Text != "" {
				run := &mdRun{text: []rune(s.Text), size: markdownSize(s.Style.SizeName), style: s.Style.TextStyle,
					color: s.Style.ColorName, quote: s.Style.QuotingDepth}
				// Code in a line is the only text Fyne sets in the monospace face.
				run.code = s.Style.TextStyle.Monospace
				if run.color == "" {
					run.color = theme.ColorNameForeground
				}
				if run.quote > 0 {
					run.color = theme.ColorNameDisabled
				}
				b.runs = append(b.runs, run)
			}
			if !s.Inline() {
				b.flush()
			}
		case *widget.HyperlinkSegment:
			b.runs = append(b.runs, &mdRun{text: []rune(s.Text), size: sizeBody, style: s.TextStyle,
				color: theme.ColorNameHyperlink, link: s})
		case *widget.CheckBoxSegment:
			mark := "[ ] "
			if s.Checked {
				mark = "[x] "
			}
			b.runs = append(b.runs, &mdRun{text: []rune(mark + s.Text), size: sizeBody, color: theme.ColorNameForeground})
		case *widget.ListSegment:
			b.flush()
			list, indent := b.list, b.indent
			if list == 0 {
				b.lists++
				b.list = b.lists
			}
			b.listItems(s)
			b.list, b.indent = list, indent
		case *widget.ParagraphSegment:
			b.segments(s.Texts)
			b.flush()
		case *widget.CodeBlockSegment:
			b.flush()
			b.add(&mdBlock{kind: mdCode, runs: []*mdRun{{text: []rune(s.Text), size: sizeBody,
				style: fyne.TextStyle{Monospace: true}, color: theme.ColorNameForeground}}})
		case *widget.TableSegment:
			b.flush()
			b.add(&mdBlock{kind: mdTable, table: s})
		case *widget.SeparatorSegment:
			b.flush()
			b.add(&mdBlock{kind: mdRule})
		default:
			if text := segment.Textual(); text != "" {
				b.runs = append(b.runs, &mdRun{text: []rune(text), size: sizeBody, color: theme.ColorNameForeground})
			}
			if !segment.Inline() {
				b.flush()
			}
		}
	}
}

func (b *mdBuilder) listItems(list *widget.ListSegment) {
	n := list.StartNumber()
	for _, item := range list.Items {
		if sub, ok := item.(*widget.ListSegment); ok {
			b.indent++
			b.listItems(sub)
			b.indent--
			continue
		}
		b.marker = "•"
		if list.Ordered {
			b.marker = fmt.Sprintf("%d.", n)
			n++
		}
		texts := []widget.RichTextSegment{item}
		if p, ok := item.(*widget.ParagraphSegment); ok {
			texts = p.Texts
		}
		b.segments(texts)
		b.flush()
		b.marker = ""
	}
}

// flush ends the block the runs read so far make.
func (b *mdBuilder) flush() {
	if len(b.runs) == 0 {
		return
	}
	block := &mdBlock{kind: mdText, runs: b.runs, marker: b.marker, indent: b.indent}
	for _, run := range b.runs {
		block.quote = max(block.quote, run.quote)
	}
	b.add(block)
	b.runs = nil
	b.marker = ""
}

// add keeps room between blocks, and little between the items of a list.
func (b *mdBuilder) add(block *mdBlock) {
	block.list = b.list
	if len(b.blocks) > 0 {
		block.space = 8
		if last := b.blocks[len(b.blocks)-1]; block.list != 0 && last.list == block.list {
			block.space = 2
		}
	}
	b.blocks = append(b.blocks, block)
}

// markdownSize is the size of the window for a size of formatted text: its
// body size for text, larger ones for headings.
func markdownSize(name fyne.ThemeSizeName) fyne.ThemeSizeName {
	switch name {
	case theme.SizeNameHeadingText:
		return sizeSection
	case theme.SizeNameSubHeadingText:
		return sizeTitle
	}
	return sizeBody
}

// mdLayout is a document laid out in a width: the lines of text, with where
// each character is, and what draws them.
type mdLayout struct {
	width, height float32
	lines         []*mdLine
	back, front   []fyne.CanvasObject
	texts         []*mdFrag
	markers       []*canvas.Text
	// words keeps each word a fragment of its own; order are the words and
	// markers, in the order of the text, as they stream in.
	words bool
	order []*mdReveal
}

// mdReveal is a word or marker of the text where it belongs, as it streams
// in to there.
type mdReveal struct {
	label *canvas.Text
	color fyne.ThemeColorName
	base  fyne.Position
	done  bool
}

// mdLine is a line of text, or a table, which takes no part of a selection
// but whole.
type mdLine struct {
	top, height float32
	start, end  int
	frags       []*mdFrag
	table       bool
}

// mdFrag is the text of one run on one line.
type mdFrag struct {
	run   *mdRun
	text  []rune
	start int
	x     float32
	width float32
	label *canvas.Text
}

const (
	mdListIndent = 20
	mdQuoteStep  = 14
	mdCodePadX   = 10
	mdCodePadY   = 6
)

func (d *mdDoc) layout(width float32, owner *markdownText) *mdLayout {
	l := &mdLayout{width: width, words: owner != nil && owner.words}
	y := float32(0)
	for _, block := range d.blocks {
		y += block.space
		switch block.kind {
		case mdText:
			y = l.textBlock(block, y)
		case mdCode:
			box := newPaint(func(r *canvas.Rectangle, p *palette) {
				r.FillColor = p.toolBackground
				r.StrokeColor = p.line
				r.StrokeWidth = 1
				r.CornerRadius = 8
			})
			l.back = append(l.back, box)
			bottom := l.flow(block.runs, block.start, mdCodePadX, width-mdCodePadX, y+mdCodePadY, true) + mdCodePadY
			box.Move(fyne.NewPos(0, y))
			box.Resize(fyne.NewSize(width, bottom-y))
			y = bottom
		case mdRule:
			rule := newPaint(func(r *canvas.Rectangle, p *palette) { r.FillColor = p.line })
			rule.Move(fyne.NewPos(0, y+4))
			rule.Resize(fyne.NewSize(width, 1))
			l.back = append(l.back, rule)
			y += 9
		case mdTable:
			table := block.table.Visual()
			if scroll, ok := table.(*container.Scroll); ok {
				table = newSideScroll(scroll.Content, owner)
			}
			height := table.MinSize().Height
			table.Move(fyne.NewPos(0, y))
			table.Resize(fyne.NewSize(width, height))
			l.front = append(l.front, table)
			l.lines = append(l.lines, &mdLine{top: y, height: height, start: block.start, end: block.end, table: true})
			y += height
		}
	}
	l.height = y
	for _, item := range l.order {
		item.base = item.label.Position()
	}
	l.recolor()
	return l
}

// textBlock lays out a paragraph, heading, item or quote from top, and
// returns where it ends.
func (l *mdLayout) textBlock(block *mdBlock, top float32) float32 {
	left := float32(block.quote * mdQuoteStep)
	if block.marker != "" {
		left += float32(block.indent * mdListIndent)
	}
	textLeft := left
	if block.marker != "" {
		marker := canvas.NewText(block.marker, nil)
		marker.TextSize = theme.Size(sizeBody)
		marker.TextStyle = fyne.TextStyle{Bold: block.marker != "•"}
		marker.Move(fyne.NewPos(left, top))
		l.markers = append(l.markers, marker)
		l.order = append(l.order, &mdReveal{label: marker, color: theme.ColorNameForeground})
		l.front = append(l.front, marker)
		textLeft += max(mdListIndent+4, fyne.MeasureText(block.marker+" ", marker.TextSize, marker.TextStyle).Width)
	}
	first := len(l.lines)
	bottom := l.flow(block.runs, block.start, textLeft, l.width, top, false)
	if block.marker != "" && first < len(l.lines) {
		line := l.lines[first]
		marker := l.markers[len(l.markers)-1]
		marker.Move(fyne.NewPos(left, line.top+line.height-marker.MinSize().Height))
	}
	for q := 0; q < block.quote; q++ {
		bar := newPaint(func(r *canvas.Rectangle, p *palette) { r.FillColor = p.line })
		bar.Move(fyne.NewPos(float32(q*mdQuoteStep), top))
		bar.Resize(fyne.NewSize(2, bottom-top))
		l.back = append(l.back, bar)
	}
	return bottom
}

// flow wraps runs at words between left and right from top, and returns
// where the last line ends. Code keeps its spaces where a line wraps.
func (l *mdLayout) flow(runs []*mdRun, start int, left, right, top float32, code bool) float32 {
	first := len(l.lines)
	line := &mdLine{top: top, start: start, end: start}
	x := left
	var word []*mdFrag
	end := func() {
		if line.height == 0 {
			line.height = fyne.MeasureText("M", theme.Size(sizeBody), fyne.TextStyle{}).Height
		}
		l.lines = append(l.lines, line)
	}
	next := func(start int) {
		end()
		line = &mdLine{top: line.top + line.height, start: start, end: start}
		x = left
	}
	place := func(piece *mdFrag) {
		if len(line.frags) == 0 {
			line.start = piece.start
		}
		if last := lastFrag(line); !l.words && last != nil && last.run == piece.run && last.start+len(last.text) == piece.start {
			last.text = append(last.text, piece.text...)
			last.width = measureRun(last.run, last.text)
		} else {
			piece.x = x
			line.frags = append(line.frags, piece)
		}
		x += piece.width
		line.end = piece.start + len(piece.text)
		line.height = max(line.height, fyne.MeasureText("M", theme.Size(piece.run.size), piece.run.style).Height)
	}
	flushWord := func() {
		if len(word) == 0 {
			return
		}
		width := float32(0)
		for _, piece := range word {
			width += piece.width
		}
		if x > left && x+width > right {
			next(word[0].start)
		}
		for _, piece := range word {
			// A word longer than the line breaks where the line ends.
			for x+piece.width > right && len(piece.text) > 1 {
				n := fitRunes(piece.run, piece.text, right-x)
				if n == 0 {
					if x > left {
						next(piece.start)
						continue
					}
					n = 1
				}
				head := &mdFrag{run: piece.run, text: piece.text[:n:n], start: piece.start}
				head.width = measureRun(head.run, head.text)
				place(head)
				piece = &mdFrag{run: piece.run, text: piece.text[n:], start: piece.start + n}
				piece.width = measureRun(piece.run, piece.text)
				next(piece.start)
			}
			place(piece)
		}
		word = nil
	}
	for _, run := range runs {
		i := 0
		for i < len(run.text) {
			r := run.text[i]
			j := i + 1
			switch {
			case r == '\n':
				flushWord()
				next(run.start + j)
				i = j
				continue
			case unicode.IsSpace(r):
				for j < len(run.text) && run.text[j] != '\n' && unicode.IsSpace(run.text[j]) {
					j++
				}
			default:
				for j < len(run.text) && !unicode.IsSpace(run.text[j]) {
					j++
				}
			}
			piece := &mdFrag{run: run, text: run.text[i:j:j], start: run.start + i}
			piece.width = measureRun(run, piece.text)
			if unicode.IsSpace(r) {
				flushWord()
				if code || len(line.frags) > 0 || line.top == top {
					place(piece)
				}
			} else {
				word = append(word, piece)
			}
			i = j
		}
	}
	flushWord()
	end()
	l.draw(l.lines[first:])
	return line.top + line.height
}

func lastFrag(line *mdLine) *mdFrag {
	if len(line.frags) == 0 {
		return nil
	}
	return line.frags[len(line.frags)-1]
}

// draw makes the texts of lines just laid out.
func (l *mdLayout) draw(lines []*mdLine) {
	for _, line := range lines {
		for _, frag := range line.frags {
			label := canvas.NewText(string(frag.text), nil)
			label.TextSize = theme.Size(frag.run.size)
			label.TextStyle = frag.run.style
			height := label.MinSize().Height
			label.Move(fyne.NewPos(frag.x, line.top+line.height-height))
			frag.label = label
			l.texts = append(l.texts, frag)
			if strings.TrimSpace(string(frag.text)) != "" {
				l.order = append(l.order, &mdReveal{label: label, color: frag.run.color})
			}
			if frag.run.code {
				shade := newPaint(func(r *canvas.Rectangle, p *palette) {
					r.FillColor = p.surface
					r.CornerRadius = 3
				})
				shade.Move(fyne.NewPos(frag.x-2, line.top+line.height-height))
				shade.Resize(fyne.NewSize(frag.width+4, height))
				l.back = append(l.back, shade)
			}
			l.front = append(l.front, label)
		}
	}
}

// recolor gives the texts the colours of the theme shown.
func (l *mdLayout) recolor() {
	for _, frag := range l.texts {
		frag.label.Color = theme.Color(frag.run.color)
		frag.label.Refresh()
	}
	for _, marker := range l.markers {
		marker.Color = theme.Color(theme.ColorNameForeground)
		marker.Refresh()
	}
}

func measureRun(run *mdRun, text []rune) float32 {
	return fyne.MeasureText(string(text), theme.Size(run.size), run.style).Width
}

// fitRunes is how many runes of text fit in width.
func fitRunes(run *mdRun, text []rune, width float32) int {
	return sort.Search(len(text), func(n int) bool { return measureRun(run, text[:n+1]) > width })
}

// index is the place in the text nearest to p.
func (l *mdLayout) index(p fyne.Position) int {
	if len(l.lines) == 0 {
		return 0
	}
	for _, line := range l.lines {
		if p.Y < line.top {
			return line.start
		}
		if p.Y < line.top+line.height {
			if line.table {
				if p.Y < line.top+line.height/2 {
					return line.start
				}
				return line.end
			}
			return line.at(p.X)
		}
	}
	return l.lines[len(l.lines)-1].end
}

func (line *mdLine) at(x float32) int {
	for _, frag := range line.frags {
		if x < frag.x {
			return frag.start
		}
		if x < frag.x+frag.width {
			return frag.start + frag.offset(x-frag.x)
		}
	}
	return line.end
}

// offset is the place in the fragment nearest to x.
func (f *mdFrag) offset(x float32) int {
	n := fitRunes(f.run, f.text, x)
	if n < len(f.text) {
		before := measureRun(f.run, f.text[:n])
		after := measureRun(f.run, f.text[:n+1])
		if x-before > after-x {
			n++
		}
	}
	return n
}

// link is the link at p, if one is there.
func (l *mdLayout) link(p fyne.Position) *widget.HyperlinkSegment {
	for _, line := range l.lines {
		if p.Y < line.top || p.Y >= line.top+line.height {
			continue
		}
		for _, frag := range line.frags {
			if frag.run.link != nil && p.X >= frag.x && p.X < frag.x+frag.width {
				return frag.run.link
			}
		}
	}
	return nil
}

// shade is what marks the text from from to to.
func (l *mdLayout) shade(from, to int) []fyne.CanvasObject {
	if from >= to {
		return nil
	}
	var out []fyne.CanvasObject
	mark := func(x, y, width, height float32) {
		r := canvas.NewRectangle(theme.Color(theme.ColorNameSelection))
		r.Move(fyne.NewPos(x, y))
		r.Resize(fyne.NewSize(width, height))
		out = append(out, r)
	}
	for _, line := range l.lines {
		if line.end <= from || line.start >= to {
			if !(line.start == line.end && line.start >= from && line.start < to) {
				continue
			}
		}
		if line.table {
			mark(0, line.top, l.width, line.height)
			continue
		}
		for _, frag := range line.frags {
			a, b := max(from, frag.start), min(to, frag.start+len(frag.text))
			if a >= b {
				continue
			}
			x1 := frag.x + measureRun(frag.run, frag.text[:a-frag.start])
			x2 := frag.x + measureRun(frag.run, frag.text[:b-frag.start])
			mark(x1, line.top, x2-x1, line.height)
		}
	}
	return out
}

// sideScroll scrolls its content sideways only. What it does not take of the
// wheel goes to the scroll the formatted text is in.
type sideScroll struct {
	container.Scroll
	text *markdownText
}

func newSideScroll(content fyne.CanvasObject, text *markdownText) *sideScroll {
	s := &sideScroll{text: text}
	s.Direction = container.ScrollHorizontalOnly
	s.Content = content
	s.ExtendBaseWidget(s)
	return s
}

func (s *sideScroll) Scrolled(event *fyne.ScrollEvent) {
	dx, dy := event.Scrolled.DX, event.Scrolled.DY
	if s.text == nil || s.text.outer == nil || dx*dx > dy*dy {
		s.Scroll.Scrolled(event)
		return
	}
	s.text.outer.Scrolled(event)
}
