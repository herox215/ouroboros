//go:build cgo

package gui

import (
	"image/color"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"
)

// markdownIn shows source formatted in a window of the width given.
func markdownIn(t *testing.T, source string, width float32) *markdownText {
	t.Helper()
	widgetApp(t, "light")
	md := newMarkdownText(source)
	window := test.NewWindow(md)
	t.Cleanup(window.Close)
	window.SetPadded(false)
	window.Resize(fyne.NewSize(width, 600))
	return md
}

func TestMarkdownText(t *testing.T) {
	source := "# Title\n\nSome **bold** text\n\n- one\n- two"
	md := markdownIn(t, source, 400)
	if md.Text() != source {
		t.Fatal("source lost")
	}
	if got := string(md.doc.text); got != "Title\nSome bold text\none\ntwo" {
		t.Fatalf("not formatted: %q", got)
	}
	heading := md.doc.blocks[0].runs[0]
	if heading.size != sizeSection {
		t.Fatalf("heading size %q", heading.size)
	}
	if md.doc.blocks[2].marker != "•" || md.doc.blocks[3].space >= md.doc.blocks[2].space {
		t.Fatal("the items of a list are not marked, or not kept together")
	}
	if md.MinSize().Height <= 0 {
		t.Fatal("no height")
	}
}

// A drag marks text, a double click a word, and the shortcuts copy what is
// marked or mark all.
func TestMarkdownTextSelects(t *testing.T) {
	md := markdownIn(t, "Hello **bold** world\n\nSecond paragraph", 400)
	first := md.layout.lines[0]
	last := md.layout.lines[len(md.layout.lines)-1]
	md.MouseDown(&desktop.MouseEvent{PointEvent: fyne.PointEvent{Position: fyne.NewPos(0, first.top+1)}, Button: desktop.MouseButtonPrimary})
	md.Dragged(&fyne.DragEvent{PointEvent: fyne.PointEvent{Position: fyne.NewPos(1000, last.top+1)}})
	if got := md.SelectedText(); got != "Hello bold world\nSecond paragraph" {
		t.Fatalf("dragged over %q", got)
	}
	if len(md.selection) == 0 {
		t.Fatal("the selection is not shaded")
	}
	md.TypedShortcut(&fyne.ShortcutCopy{})
	if got := fyne.CurrentApp().Clipboard().Content(); got != "Hello bold world\nSecond paragraph" {
		t.Fatalf("copied %q", got)
	}

	frag := first.frags[1]
	md.DoubleTapped(&fyne.PointEvent{Position: fyne.NewPos(frag.x+frag.width/2, first.top+1)})
	if got := md.SelectedText(); got != "bold" {
		t.Fatalf("double click marked %q", got)
	}
	md.TypedShortcut(&fyne.ShortcutSelectAll{})
	if md.SelectedText() != string(md.doc.text) {
		t.Fatal("not all marked")
	}
	md.FocusLost()
	if md.SelectedText() != "" || len(md.selection) != 0 {
		t.Fatal("the selection stays when the text loses the focus")
	}
	md.copy(fyne.CurrentApp())
	if fyne.CurrentApp().Clipboard().Content() != md.Text() {
		t.Fatal("without a selection the source is not copied")
	}
}

// Text wraps at words within the width, and a mark after code stays with it.
func TestMarkdownTextWraps(t *testing.T) {
	size := theme.Size(sizeBody)
	style := fyne.TextStyle{}
	width := fyne.MeasureText("aaaa bbbb", size, style).Width + 1
	md := markdownIn(t, "aaaa `bbbb`. "+strings.Repeat("word ", 40), width)
	if len(md.layout.lines) < 10 {
		t.Fatalf("%d lines", len(md.layout.lines))
	}
	for _, line := range md.layout.lines {
		if len(line.frags) == 0 {
			continue
		}
		if string(line.frags[0].text) == "." {
			t.Fatal("a mark after code wraps alone")
		}
		last := line.frags[len(line.frags)-1]
		trimmed := []rune(strings.TrimRight(string(last.text), " "))
		if last.x+measureRun(last.run, trimmed) > width+0.5 {
			t.Fatalf("line runs over: %q", string(last.text))
		}
	}
	long := markdownIn(t, strings.Repeat("x", 200), 100)
	if len(long.layout.lines) < 2 {
		t.Fatal("a long word does not break")
	}
}

// The wheel up and down over a table scrolls the transcript, not the table;
// code takes no scroll of its own.
func TestMarkdownTextHandsOnTheWheel(t *testing.T) {
	md := markdownIn(t, "Text\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\n```\ncode\n```", 300)
	outer := container.NewVScroll(container.NewVBox())
	outer.Content = newMarkdownText(strings.Repeat("line\n\n", 40))
	outer.Resize(fyne.NewSize(200, 20))
	md.outer = outer
	var table *sideScroll
	for _, o := range md.objects {
		switch o := o.(type) {
		case *sideScroll:
			table = o
		case fyne.Scrollable:
			t.Fatalf("%T scrolls of its own", o)
		}
	}
	if table == nil {
		t.Fatal("no table")
	}
	table.Scrolled(&fyne.ScrollEvent{Scrolled: fyne.Delta{DY: -10}})
	if outer.Offset.Y == 0 {
		t.Fatal("the table keeps the wheel from the transcript")
	}
}

// The words of a text that streams appear one after the other, each rising
// from below as it fades in, until all of them stand where they belong.
func TestMarkdownTextStreamsWords(t *testing.T) {
	widgetApp(t, "light")
	md := newMarkdownText("")
	md.words, md.streaming = true, true
	window := test.NewWindow(md)
	t.Cleanup(window.Close)
	window.Resize(fyne.NewSize(400, 600))
	md.SetMarkdown("one two three")
	md.ensureLayout(md.Size().Width)
	order := md.layout.order
	if len(order) != 3 || len(md.born) != 3 || !md.born[0].Before(md.born[2]) {
		t.Fatalf("words not streamed one after the other: %d words, %d times", len(order), len(md.born))
	}
	start := md.born[0].Add(-time.Millisecond)
	md.revealAt(start)
	if last := order[2].label; last.Color.(color.NRGBA).A != 0 || last.Position().Y <= order[2].base.Y {
		t.Fatal("a word to come shows already, or not below its place")
	}
	if !md.revealAt(start.Add(time.Minute)) {
		t.Fatal("the words do not come to stand")
	}
	for _, item := range order {
		if item.label.Position() != item.base {
			t.Fatal("a word does not stand where it belongs")
		}
	}
}

// Replacing a partial answer must continue, not freeze, its unfinished words.
func TestMarkdownTextContinuesReveal(t *testing.T) {
	widgetApp(t, "light")
	md := newMarkdownText("")
	md.words, md.streaming = true, true
	md.born = []time.Time{time.Now().Add(-revealTime / 2), time.Now().Add(time.Second)}
	md.SetMarkdown("one two")
	md.ensureLayout(400)
	t.Cleanup(md.stopReveal)
	if md.reveal == nil {
		t.Fatal("the final answer does not continue the partial answer's animation")
	}
	md.stopRevealAnimation()
	md.revealAt(md.born[0].Add(revealTime / 2))
	first, second := md.layout.order[0], md.layout.order[1]
	if a := first.label.Color.(color.NRGBA).A; a == 0 || a == 255 {
		t.Fatal("the first word does not fade in between its start and finish")
	}
	if first.label.Position().Y <= first.base.Y || second.label.Color.(color.NRGBA).A != 0 {
		t.Fatal("the words do not rise in sequence")
	}
}
