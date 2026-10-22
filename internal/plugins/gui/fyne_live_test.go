//go:build cgo

package gui

import (
	"context"
	"strconv"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/test"

	"ouroboros/plugin"
)

// atEnd reports whether the transcript shows the end of the history.
func atEnd(ui *fyneUI) bool {
	content := ui.transcript.Content.MinSize().Height
	return ui.transcript.Offset.Y >= content-ui.transcript.Size().Height-1
}

// An answer streams into the thread while its end is in view, and the
// finished answer takes the place of what was written so far.
func TestFyneLiveAnswer(t *testing.T) {
	ui, sessions := e2eWindow(t, fyne.NewSize(900, 640))
	var before int
	fyne.DoAndWait(func() { before = len(ui.content.Objects) })
	for i := range 30 {
		sessions.live <- plugin.SessionEvent{Kind: plugin.SessionAnswering, Text: "piece " + strconv.Itoa(i) + " ", At: time.Now()}
	}
	waitFor(t, func() bool {
		return ui.chat.partial != nil && len(ui.chat.partial.body.Text) > 0 && ui.chat.partial.body.Text[len(ui.chat.partial.body.Text)-3:] == "29 "
	})
	settle(ui.win)
	waitFor(t, func() bool { return atEnd(ui) })
	sessions.live <- plugin.SessionEvent{Kind: plugin.SessionAnswer, Text: "Tabs it is.", At: time.Now()}
	waitFor(t, func() bool { return ui.chat.partial == nil && len(ui.content.Objects) == before+1 })
	settle(ui.win)
	waitFor(t, func() bool { return atEnd(ui) })
}

// Reading older messages, the history stays where the owner put it while
// new ones come; back at the end, it follows them again.
func TestFyneScrollHoldsWhileReading(t *testing.T) {
	ui, sessions := e2eWindow(t, fyne.NewSize(900, 640))
	for i := range 20 {
		sessions.live <- plugin.SessionEvent{Kind: plugin.SessionSay, Text: "Line " + strconv.Itoa(i) + " of a long report.", At: time.Now()}
	}
	waitFor(t, func() bool { return len(ui.visible) >= 26 })
	settle(ui.win)
	waitFor(t, func() bool { return atEnd(ui) })
	var middle fyne.Position
	fyne.DoAndWait(func() {
		p, _ := absolute(ui, ui.transcript)
		middle = p.Add(fyne.NewPos(ui.transcript.Size().Width/2, ui.transcript.Size().Height/2))
		test.Scroll(ui.win.Canvas(), middle, 0, 400)
	})
	var offset float32
	fyne.DoAndWait(func() {
		offset = ui.transcript.Offset.Y
		if ui.following || atEnd(ui) {
			t.Error("scrolling up keeps following")
		}
		if !ui.fade.Visible() {
			t.Error("no fade over older messages")
		}
	})
	sessions.live <- plugin.SessionEvent{Kind: plugin.SessionSay, Text: "One more line.", At: time.Now()}
	waitFor(t, func() bool { return len(ui.visible) >= 27 })
	settle(ui.win)
	fyne.DoAndWait(func() {
		if ui.transcript.Offset.Y != offset {
			t.Errorf("the history moved from %v to %v while reading", offset, ui.transcript.Offset.Y)
		}
		test.Scroll(ui.win.Canvas(), middle, 0, -100000)
	})
	waitFor(t, func() bool { return ui.following && atEnd(ui) })
	sessions.live <- plugin.SessionEvent{Kind: plugin.SessionSay, Text: "And the last.", At: time.Now()}
	waitFor(t, func() bool { return len(ui.visible) >= 28 })
	settle(ui.win)
	waitFor(t, func() bool { return atEnd(ui) })
}

// The composer grows with what is written, up to its limit, and shrinks
// again once it is sent.
func TestFyneComposerGrows(t *testing.T) {
	ui, _ := e2eWindow(t, fyne.NewSize(1180, 780))
	var one float32
	settle(ui.win)
	fyne.DoAndWait(func() { one = ui.draft.Size().Height })
	fyne.DoAndWait(func() {
		ui.win.Canvas().Focus(ui.draft)
		for i := range 3 {
			test.Type(ui.draft, "line "+strconv.Itoa(i))
			ui.draft.KeyDown(&fyne.KeyEvent{Name: desktop.KeyShiftLeft})
			ui.draft.TypedKey(&fyne.KeyEvent{Name: fyne.KeyReturn})
			ui.draft.KeyUp(&fyne.KeyEvent{Name: desktop.KeyShiftLeft})
		}
	})
	settle(ui.win)
	var four float32
	fyne.DoAndWait(func() { four = ui.draft.Size().Height })
	if four <= one*2 {
		t.Fatalf("four lines are %v high, one is %v", four, one)
	}
	fyne.DoAndWait(func() {
		for range 20 {
			ui.draft.KeyDown(&fyne.KeyEvent{Name: desktop.KeyShiftLeft})
			ui.draft.TypedKey(&fyne.KeyEvent{Name: fyne.KeyReturn})
			ui.draft.KeyUp(&fyne.KeyEvent{Name: desktop.KeyShiftLeft})
		}
	})
	settle(ui.win)
	fyne.DoAndWait(func() {
		if ui.draft.rows != composerRows {
			t.Errorf("the composer shows %d rows", ui.draft.rows)
		}
		ui.draft.TypedKey(&fyne.KeyEvent{Name: fyne.KeyReturn})
	})
	waitFor(t, func() bool { return ui.draft.Text == "" })
	settle(ui.win)
	fyne.DoAndWait(func() {
		if ui.draft.rows != 1 {
			t.Errorf("the sent composer keeps %d rows", ui.draft.rows)
		}
	})
}

// The shortcuts of the window work from the composer, which has the focus
// most of the time.
func TestFyneShortcutsFromComposer(t *testing.T) {
	ui, _ := e2eWindow(t, fyne.NewSize(1180, 780))
	fyne.DoAndWait(func() {
		ui.win.Canvas().Focus(ui.draft)
		ui.draft.TypedShortcut(&desktop.CustomShortcut{KeyName: fyne.KeyComma, Modifier: fyne.KeyModifierShortcutDefault})
	})
	waitFor(t, func() bool { return ui.overlay.Visible() })
	fyne.DoAndWait(func() {
		if focused := ui.win.Canvas().Focused(); focused != nil {
			focused.TypedKey(&fyne.KeyEvent{Name: fyne.KeyEscape})
		} else {
			pressEscape()
		}
	})
	waitFor(t, func() bool { return !ui.overlay.Visible() && ui.win.Canvas().Focused() == ui.draft })
	// What the entry knows itself still works in it.
	fyne.DoAndWait(func() {
		test.Type(ui.draft, "abc")
		ui.draft.TypedShortcut(&fyne.ShortcutSelectAll{})
		if ui.draft.SelectedText() != "abc" {
			t.Errorf("select all selected %q", ui.draft.SelectedText())
		}
	})
}

// The notice of a sent message goes again after a while.
func TestFyneSentNoticeFades(t *testing.T) {
	ui, _ := e2eWindow(t, fyne.NewSize(1180, 780))
	fyne.DoAndWait(func() { ui.noticeTime = 50 * time.Millisecond })
	fyne.DoAndWait(func() {
		test.Type(ui.draft, "hello")
		ui.submit(ui.draft.Text, false)
	})
	waitFor(t, func() bool { return ui.sendStatus.Visible() && ui.sendStatus.Text == tr("composer.sent", nil) })
	waitFor(t, func() bool { return !ui.sendStatus.Visible() })
}

// An empty conversation says what to do instead of showing nothing.
func TestFyneEmptyThreadHint(t *testing.T) {
	sessions := newE2ESessions()
	sessions.watch = func(context.Context, string) ([]plugin.SessionEvent, <-chan plugin.SessionEvent, error) {
		return nil, sessions.live, nil
	}
	ui := e2eWindowWith(t, fyne.NewSize(1180, 780), sessions)
	waitFor(t, func() bool { return ui.emptyHint.Visible() })
	sessions.live <- plugin.SessionEvent{Kind: plugin.SessionMessage, Text: "Hi", At: time.Now()}
	waitFor(t, func() bool { return !ui.emptyHint.Visible() })
}

// A session opened while it writes its answer shows that the beginning of
// the answer is not there: its past keeps the writing, not the text.
func TestFyneJoinsAnAnswerBeingWritten(t *testing.T) {
	sessions := newE2ESessions()
	live := make(chan plugin.SessionEvent, 4)
	sessions.watch = func(context.Context, string) ([]plugin.SessionEvent, <-chan plugin.SessionEvent, error) {
		return []plugin.SessionEvent{
			{Kind: plugin.SessionMessage, Text: "Tell a story", At: time.Now()},
			{Kind: plugin.SessionAnswering, At: time.Now()},
		}, live, nil
	}
	ui := e2eWindowWith(t, fyne.NewSize(1180, 780), sessions)
	live <- plugin.SessionEvent{Kind: plugin.SessionAnswering, Text: "under the snow", At: time.Now()}
	waitFor(t, func() bool { return ui.chat.partial != nil && ui.chat.partial.body.Text == "… under the snow" })
	// A live answer from its start has no ellipsis.
	live <- plugin.SessionEvent{Kind: plugin.SessionAnswer, Text: "Done.", At: time.Now()}
	live <- plugin.SessionEvent{Kind: plugin.SessionAnswering, At: time.Now()}
	live <- plugin.SessionEvent{Kind: plugin.SessionAnswering, Text: "Once", At: time.Now()}
	waitFor(t, func() bool { return ui.chat.partial != nil && ui.chat.partial.body.Text == "Once" })
}
