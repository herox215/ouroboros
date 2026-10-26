//go:build cgo

package gui

import (
	"context"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/test"

	"ouroboros/internal/chat"
	"ouroboros/plugin"
)

// A long history keeps only its newest messages as widgets, in slices that
// leave the window responsive while it opens; the older ones come back as
// the owner scrolls up to them, without moving what is in view.
func TestFyneLongHistory(t *testing.T) {
	sessions := newE2ESessions()
	thread := bigThread(150) // 450 messages
	sessions.watch = func(context.Context, string) ([]plugin.SessionEvent, <-chan plugin.SessionEvent, error) {
		return thread, make(chan plugin.SessionEvent), nil
	}
	ui := e2eWindowWith(t, fyne.NewSize(1180, 780), sessions)
	// Opened again in slices of almost no time, it is all there as well.
	fyne.DoAndWait(func() {
		ui.replayTime = time.Nanosecond
		ui.read(ui.threadEntry())
	})
	waitFor(t, func() bool { return ui.ready.Load() && len(ui.visible) == 450 })
	settle(ui.win)
	fyne.DoAndWait(func() {
		if shown := len(ui.content.Objects); shown < transcriptWindow || shown > transcriptWindow+transcriptStep || ui.first != 450-shown {
			t.Errorf("%d messages are widgets, from %d", shown, ui.first)
		}
		if ui.content.Objects[shown(ui)-1] != ui.visible[449].item {
			t.Error("the newest message is not at the end")
		}
	})

	// Scrolled up to the first shown, older ones come above it, and the
	// message in view stays where it was.
	var anchor *fyneMessage
	var before fyne.Position
	var from int
	fyne.DoAndWait(func() {
		from = ui.first
		ui.transcript.ScrollToTop()
		anchor = ui.visible[ui.first]
		before, _ = absolute(ui, anchor.item)
		ui.scrolled()
	})
	settle(ui.win)
	fyne.DoAndWait(func() {
		if ui.first != from-transcriptStep {
			t.Errorf("first shown %d", ui.first)
		}
		after, ok := absolute(ui, anchor.item)
		if !ok || after.Y < before.Y-2 || after.Y > before.Y+2 {
			t.Errorf("the message in view moved from %v to %v", before, after)
		}
		// Taking out a message before the shown ones keeps them as they are.
		first := ui.first
		ui.removeItem(ui.visible[0])
		if ui.first != first-1 {
			t.Errorf("first shown %d after a removal", ui.first)
		}
		// Older ones are not added twice at once, and not before the first.
		ui.addingEarlier = true
		ui.earlier()
		ui.addingEarlier = false
		if ui.first != first-1 {
			t.Error("older messages were added while they were being added")
		}
		ui.first = 0
		ui.earlier()
	})
	// Back at the end, the window keeps to the newest again.
	fyne.DoAndWait(ui.stick)
	sessions.live <- plugin.SessionEvent{Kind: plugin.SessionSay, Text: "new", At: time.Now()}
}

func shown(ui *fyneUI) int { return len(ui.content.Objects) }

// A batch with a budget stops after it, twenty events at a time.
func TestFyneApplyForBudget(t *testing.T) {
	ui, _ := e2eWindow(t, fyne.NewSize(1180, 780))
	events := make([]chat.Event, 60)
	for i := range events {
		events[i] = chat.Event{Kind: plugin.SessionSay, Text: "x", At: time.Now()}
	}
	fyne.DoAndWait(func() {
		if got := ui.applyFor(events, 0, time.Nanosecond); got != 20 {
			t.Errorf("stopped at %d", got)
		}
		if got := ui.applyFor(events, 20, 0); got != 60 {
			t.Errorf("without a budget stopped at %d", got)
		}
	})
}

// A select draws as one field: its line shows the pointer and the focus.
func TestFynePickerLooksLikeAField(t *testing.T) {
	application := newLoopApp(t)
	var p *picker
	var box *canvas.Rectangle
	fyne.DoAndWait(func() {
		application.Settings().SetTheme(&ouroTheme{pref: "light"})
		p = newPicker([]string{"a", "b"}, nil)
		test.NewWindow(p).Resize(fyne.NewSize(200, 60))
		box = test.WidgetRenderer(p).Objects()[0].(*canvas.Rectangle)
	})
	check := func(stroke any, width float32) {
		t.Helper()
		fyne.DoAndWait(func() {
			if box.FillColor != lightPalette.field || box.StrokeColor != stroke || box.StrokeWidth != width || box.CornerRadius != 8 {
				t.Errorf("fill %v stroke %v width %v radius %v", box.FillColor, box.StrokeColor, box.StrokeWidth, box.CornerRadius)
			}
		})
	}
	check(lightPalette.line, 1)
	fyne.DoAndWait(func() { p.MouseIn(nil) })
	check(lightPalette.lineStrong, 1)
	fyne.DoAndWait(func() { p.FocusGained() })
	check(lightPalette.accent, 2)
	fyne.DoAndWait(func() {
		p.FocusLost()
		p.MouseOut()
	})
	check(lightPalette.line, 1)
	if min := test.WidgetRenderer(p).MinSize(); min.Height < 2*pickerPad {
		t.Fatalf("min size %v", min)
	}
}
