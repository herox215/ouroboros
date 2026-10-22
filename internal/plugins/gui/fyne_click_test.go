//go:build cgo

package gui

import (
	"slices"
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"ouroboros/plugin"
)

// The sizes the window is clicked through: wide with the rail, the default,
// without the rail, the drawer and the smallest the window allows.
var clickSizes = []fyne.Size{
	{Width: 1400, Height: 900},
	{Width: 1180, Height: 780},
	{Width: 900, Height: 640},
	{Width: 600, Height: 700},
	{Width: 360, Height: 420},
}

// hitAt is the object a click at pos reaches, found as the desktop driver
// finds it: the deepest and last visible object under the pointer that takes
// taps, the focus or the mouse, inside the scrollers that clip it, and only
// in the top overlay while there is one.
func hitAt(c fyne.Canvas, pos fyne.Position) (fyne.CanvasObject, fyne.Position) {
	var found fyne.CanvasObject
	var at fyne.Position
	var walk func(o fyne.CanvasObject, origin fyne.Position, clipPos fyne.Position, clipSize fyne.Size)
	walk = func(o fyne.CanvasObject, origin, clipPos fyne.Position, clipSize fyne.Size) {
		if o == nil || !o.Visible() {
			return
		}
		p := origin.Add(o.Position())
		size := o.Size()
		if pos.X < clipPos.X || pos.Y < clipPos.Y || pos.X >= clipPos.X+clipSize.Width || pos.Y >= clipPos.Y+clipSize.Height {
			return
		}
		inside := pos.X >= p.X && pos.Y >= p.Y && pos.X < p.X+size.Width && pos.Y < p.Y+size.Height
		if inside {
			switch o.(type) {
			case fyne.Tappable, fyne.SecondaryTappable, fyne.DoubleTappable, fyne.Focusable, desktop.Mouseable:
				found, at = o, pos.Subtract(p)
			}
		}
		if _, ok := o.(*container.Scroll); ok {
			// A scroller clips what it holds to itself.
			if !inside {
				return
			}
			clipPos, clipSize = p, size
		}
		switch o := o.(type) {
		case *fyne.Container:
			for _, child := range o.Objects {
				walk(child, p, clipPos, clipSize)
			}
		case fyne.Widget:
			for _, child := range test.WidgetRenderer(o).Objects() {
				walk(child, p, clipPos, clipSize)
			}
		}
	}
	whole := c.Size()
	if top := c.Overlays().Top(); top != nil {
		walk(top, fyne.Position{}, fyne.Position{}, whole)
	} else {
		walk(c.Content(), fyne.Position{}, fyne.Position{}, whole)
	}
	return found, at
}

// click presses at pos as the desktop driver does: the object hit takes the
// focus if it can and the tap if it can; what else is hit takes nothing.
func click(t *testing.T, ui *fyneUI, pos fyne.Position) fyne.CanvasObject {
	t.Helper()
	settle(ui.win)
	var hit fyne.CanvasObject
	fyne.DoAndWait(func() {
		c := ui.win.Canvas()
		o, at := hitAt(c, pos)
		hit = o
		if o == nil {
			return
		}
		if focusable, ok := o.(fyne.Focusable); ok {
			if _, isTapBox := o.(*tapBox); !isTapBox {
				c.Focus(focusable)
			}
		}
		if tappable, ok := o.(fyne.Tappable); ok {
			tappable.Tapped(&fyne.PointEvent{Position: at, AbsolutePosition: pos})
		}
	})
	return hit
}

// absolute is where an object lies in the window, (0,0) and false if it
// does not show.
func absolute(ui *fyneUI, target fyne.CanvasObject) (fyne.Position, bool) {
	var found fyne.Position
	ok := false
	var walk func(o fyne.CanvasObject, origin fyne.Position)
	walk = func(o fyne.CanvasObject, origin fyne.Position) {
		if ok || o == nil || !o.Visible() {
			return
		}
		p := origin.Add(o.Position())
		if o == target {
			found, ok = p, true
			return
		}
		if s, isScroll := o.(*container.Scroll); isScroll {
			walk(s.Content, p.Subtract(s.Offset).Subtract(s.Content.Position()))
			return
		}
		switch o := o.(type) {
		case *fyne.Container:
			for _, child := range o.Objects {
				walk(child, p)
			}
		case fyne.Widget:
			for _, child := range test.WidgetRenderer(o).Objects() {
				walk(child, p)
			}
		}
	}
	c := ui.win.Canvas()
	for _, overlay := range c.Overlays().List() {
		walk(overlay, fyne.Position{})
	}
	walk(c.Content(), fyne.Position{})
	return found, ok
}

// clickOn clicks the middle of an object, and fails where it does not show
// or where the click would reach another object than it or what it holds.
func clickOn(t *testing.T, ui *fyneUI, target fyne.CanvasObject) {
	t.Helper()
	clickAt(t, ui, target, 0.5, 0.5)
}

// clickAt clicks an object at a share of its width and its height.
func clickAt(t *testing.T, ui *fyneUI, target fyne.CanvasObject, x, y float32) {
	t.Helper()
	clickReaching(t, ui, target, target, x, y)
}

// clickReaching clicks target and fails unless the click reaches receiver,
// which is the target or holds it.
func clickReaching(t *testing.T, ui *fyneUI, target, receiver fyne.CanvasObject, x, y float32) {
	t.Helper()
	settle(ui.win)
	var pos fyne.Position
	var ok bool
	fyne.DoAndWait(func() {
		pos, ok = absolute(ui, target)
		size := target.Size()
		pos = pos.Add(fyne.NewPos(size.Width*x, size.Height*y))
	})
	if !ok {
		t.Fatalf("%T does not show", target)
	}
	window := ui.win.Canvas().Size()
	if pos.X < 0 || pos.Y < 0 || pos.X >= window.Width || pos.Y >= window.Height {
		t.Fatalf("%T lies outside the window at %v (window %v)", target, pos, window)
	}
	hit := click(t, ui, pos)
	if hit != receiver && !holds(receiver, hit) {
		t.Fatalf("a click on %T at %v reached %T, not %T", target, pos, hit, receiver)
	}
}

// holds reports whether object is part of what parent shows.
func holds(parent, object fyne.CanvasObject) bool {
	if parent == nil || object == nil {
		return false
	}
	if parent == object {
		return true
	}
	switch p := parent.(type) {
	case *container.Scroll:
		return holds(p.Content, object)
	case *fyne.Container:
		return slices.ContainsFunc(p.Objects, func(o fyne.CanvasObject) bool { return holds(o, object) })
	case fyne.Widget:
		return slices.ContainsFunc(test.WidgetRenderer(p).Objects(), func(o fyne.CanvasObject) bool { return holds(o, object) })
	}
	return false
}

// sessionCard is the card of the sidebar that opens a session.
func sessionCard(t *testing.T, ui *fyneUI, id string) *tapBox {
	t.Helper()
	var card *tapBox
	fyne.DoAndWait(func() {
		var find func(o fyne.CanvasObject)
		find = func(o fyne.CanvasObject) {
			switch o := o.(type) {
			case *tapBox:
				if strings.Contains(o.key, id) {
					card = o
				}
			case *fyne.Container:
				for _, child := range o.Objects {
					find(child)
				}
			}
		}
		find(ui.sidebarList)
	})
	if card == nil {
		t.Fatalf("no card of %s in the sidebar", id)
	}
	return card
}

// labelsIn is the text of every label an object shows.
func labelsIn(o fyne.CanvasObject) []fyne.CanvasObject {
	var out []fyne.CanvasObject
	var walk func(o fyne.CanvasObject)
	walk = func(o fyne.CanvasObject) {
		switch o := o.(type) {
		case *widget.Label, *widget.RichText:
			out = append(out, o)
		case *fyne.Container:
			for _, child := range o.Objects {
				walk(child)
			}
		case fyne.Widget:
			for _, child := range test.WidgetRenderer(o).Objects() {
				walk(child)
			}
		}
	}
	walk(o)
	return out
}

// openSidebar shows the sessions: the drawer where the window is narrow.
func openSidebar(t *testing.T, ui *fyneUI) {
	t.Helper()
	settle(ui.win)
	var narrow bool
	fyne.DoAndWait(func() { narrow = ui.narrow })
	if narrow {
		clickOn(t, ui, ui.menu)
		waitFor(t, func() bool { return ui.drawerOpen })
	}
}

// Every part of a session card opens its session, in every size, and the
// way back leads to the thread again.
func TestFyneClickSessionCard(t *testing.T) {
	for _, size := range clickSizes {
		t.Run(sizeName(size), func(t *testing.T) {
			ui, _ := e2eWindow(t, size)
			openSidebar(t, ui)
			card := sessionCard(t, ui, "owner/s2")
			parts := labelsIn(card)
			if len(parts) < 2 {
				t.Fatalf("card shows %d texts", len(parts))
			}
			for i := range parts {
				if i > 0 {
					openSidebar(t, ui)
					card = sessionCard(t, ui, "owner/s2")
					parts = labelsIn(card)
				}
				clickReaching(t, ui, parts[i], card, 0.2, 0.5)
				waitFor(t, func() bool { return ui.selected == "owner/s2" })
				clickOn(t, ui, ui.back)
				waitFor(t, func() bool { return ui.selected == ui.thread && ui.ready.Load() })
			}
			var drawer bool
			fyne.DoAndWait(func() { drawer = ui.drawerOpen })
			if drawer {
				t.Fatal("opening a session left the drawer open")
			}
		})
	}
}

// A click on the tool line opens its result below it and in the rail.
func TestFyneClickToolResult(t *testing.T) {
	ui, _ := e2eWindow(t, fyne.NewSize(1400, 900))
	var call *fyneToolCall
	fyne.DoAndWait(func() {
		for _, c := range ui.chat.calls {
			call = c
		}
	})
	if call == nil {
		t.Fatal("no tool call in the thread")
	}
	clickOn(t, ui, call.summary)
	waitFor(t, func() bool { return call.loaded && call.details.Visible() })
	waitFor(t, func() bool { return ui.inspecting != nil })
	settle(ui.win)
	if _, ok := absolute(ui, ui.contextDetails); !ok {
		t.Fatal("the rail does not show the result")
	}
	clickOn(t, ui, call.summary)
	waitFor(t, func() bool { return !call.details.Visible() })
}

// A click on an option answers the question with it; the options then wait.
func TestFyneClickQuestionOption(t *testing.T) {
	for _, size := range clickSizes {
		t.Run(sizeName(size), func(t *testing.T) {
			ui, sessions := e2eWindow(t, size)
			var option *flatButton
			fyne.DoAndWait(func() {
				if ui.asking != nil && len(ui.asking.buttons) > 1 {
					option = ui.asking.buttons[1]
				}
			})
			if option == nil {
				t.Fatal("the question shows no options")
			}
			clickOn(t, ui, option)
			waitFor(t, func() bool { return len(sessions.sent()) == 1 })
			if got := sessions.sent()[0]; got != plugin.ThreadID+": One page" {
				t.Fatalf("sent %q", got)
			}
			waitFor(t, func() bool { return option.Disabled() })
		})
	}
}

// Writing and clicking the send button sends; the stop button stops.
func TestFyneClickSendAndStop(t *testing.T) {
	for _, size := range clickSizes {
		t.Run(sizeName(size), func(t *testing.T) {
			ui, sessions := e2eWindow(t, size)
			clickOn(t, ui, ui.draft)
			fyne.DoAndWait(func() {
				if ui.win.Canvas().Focused() != ui.draft {
					t.Error("a click into the composer does not focus it")
				}
			})
			fyne.DoAndWait(func() { test.Type(ui.draft, "Make it native") })
			clickOn(t, ui, ui.send)
			waitFor(t, func() bool { return len(sessions.sent()) == 1 && ui.draft.Text == "" })
			if got := sessions.sent()[0]; got != plugin.ThreadID+": Make it native" {
				t.Fatalf("sent %q", got)
			}
			waitFor(t, func() bool { return ui.stop.Visible() })
			clickOn(t, ui, ui.stop)
			waitFor(t, func() bool { return len(sessions.stopped()) == 1 })
		})
	}
}

// The settings open from the gear, and close with the X and with Escape; a
// click beside the dialog reaches nothing behind it.
func TestFyneClickSettings(t *testing.T) {
	for _, size := range clickSizes {
		t.Run(sizeName(size), func(t *testing.T) {
			ui, _ := e2eWindow(t, size)
			openSidebar(t, ui)
			clickOn(t, ui, ui.settingsButton)
			waitFor(t, func() bool { return ui.overlay.Visible() })
			hit := click(t, ui, fyne.NewPos(2, 2))
			if hit != nil && !holds(ui.overlay, hit) {
				t.Fatalf("a click beside the dialog reached %T", hit)
			}
			var closeButton *flatButton
			fyne.DoAndWait(func() { closeButton = findButton(ui.overlayBody, true) })
			clickOn(t, ui, closeButton)
			waitFor(t, func() bool { return !ui.overlay.Visible() })

			fyne.DoAndWait(func() { ui.openSettings(ui.profile, ui.profileName()) })
			fyne.DoAndWait(func() { ui.win.Canvas().Focus(ui.draft) })
			// Escape reaches the window whatever has the focus.
			fyne.DoAndWait(func() {
				if focused := ui.win.Canvas().Focused(); focused != nil {
					focused.TypedKey(&fyne.KeyEvent{Name: fyne.KeyEscape})
				} else {
					pressEscape()
				}
			})
			waitFor(t, func() bool { return !ui.overlay.Visible() })
		})
	}
}

// findButton is the first icon button in what a dialog shows: its close
// button, which is the last of its header.
func findButton(o fyne.CanvasObject, icon bool) *flatButton {
	var found *flatButton
	var walk func(o fyne.CanvasObject)
	walk = func(o fyne.CanvasObject) {
		switch o := o.(type) {
		case *flatButton:
			if o.icon == icon && found == nil {
				found = o
			}
		case *fyne.Container:
			for _, child := range o.Objects {
				walk(child)
			}
		}
	}
	walk(o)
	return found
}

// The drawer of a narrow window opens with the menu and closes with a click
// beside it, without opening anything behind.
func TestFyneClickDrawer(t *testing.T) {
	ui, sessions := e2eWindow(t, fyne.NewSize(500, 700))
	openSidebar(t, ui)
	settle(ui.win)
	var size fyne.Size
	fyne.DoAndWait(func() { size = ui.win.Canvas().Size() })
	click(t, ui, fyne.NewPos(size.Width-10, size.Height-30))
	waitFor(t, func() bool { return !ui.drawerOpen })
	if len(sessions.sent()) != 0 || len(sessions.stopped()) != 0 {
		t.Fatal("a click beside the drawer reached the composer")
	}
	fyne.DoAndWait(func() { ui.win.Resize(fyne.NewSize(1000, 700)) })
	settle(ui.win)
	fyne.DoAndWait(func() {
		if ui.narrow || ui.menu.Visible() || !ui.sidebarPanel.Visible() {
			t.Error("a wide window keeps the drawer")
		}
	})
}

// The peer session reads only: its composer takes no text.
func TestFyneClickPeerSessionIsReadOnly(t *testing.T) {
	ui, _ := e2eWindow(t, fyne.NewSize(1180, 780))
	fyne.DoAndWait(func() {
		ui.read(SessionEntry{ID: "peer-alice/p1", Title: "Alice asks"})
	})
	waitFor(t, func() bool { return ui.ready.Load() && ui.selected == "peer-alice/p1" })
	fyne.DoAndWait(func() {
		if !ui.draft.Disabled() || !ui.send.Disabled() {
			t.Error("a peer session can be written to")
		}
		if !strings.Contains(ui.statusLabel.Text, strings.ToUpper(tr("status.peer", nil))) {
			t.Errorf("status %q", ui.statusLabel.Text)
		}
	})
}

// The pointer over a session card, a button and a tool line shows that
// they take a click.
func TestFyneHover(t *testing.T) {
	ui, _ := e2eWindow(t, fyne.NewSize(1400, 900))
	card := sessionCard(t, ui, "owner/s2")
	settle(ui.win)
	var pos fyne.Position
	fyne.DoAndWait(func() {
		pos, _ = absolute(ui, card)
		test.MoveMouse(ui.win.Canvas(), pos.Add(fyne.NewPos(20, 10)))
	})
	waitFor(t, func() bool { return card.hovered })
	fyne.DoAndWait(func() {
		send, _ := absolute(ui, ui.settingsButton)
		test.MoveMouse(ui.win.Canvas(), send.Add(fyne.NewPos(5, 5)))
	})
	waitFor(t, func() bool { return !card.hovered && ui.settingsButton.hovered })
}

func sizeName(size fyne.Size) string {
	return itoa(int(size.Width)) + "x" + itoa(int(size.Height))
}
