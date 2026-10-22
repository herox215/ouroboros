//go:build cgo

package gui

import (
	"context"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"

	"ouroboros/internal/kernel"
	"ouroboros/plugin"
)

// dialogWindow is the window on a bridge whose profiles have owners, models
// and settings, without a thread to read: enough for the dialogs.
func dialogWindow(t *testing.T, w *Window) *fyneUI {
	t.Helper()
	application := newLoopApp(t)
	var ui *fyneUI
	fyne.DoAndWait(func() {
		w.useTheme(application)
		native := test.NewWindow(nil)
		native.SetPadded(false)
		ui = newFyneUI(context.Background(), application, native, w)
		ui.build()
		native.Resize(fyne.NewSize(1180, 780))
		state := w.InitialState()
		ui.profiles = state.Profiles
		ui.profile = 0
	})
	return ui
}

// chooseInSelect opens a select with a click and clicks the option named.
func chooseInSelect(t *testing.T, ui *fyneUI, sel *picker, option string) {
	t.Helper()
	clickOn(t, ui, sel)
	settle(ui.win)
	var item *canvas.Text
	fyne.DoAndWait(func() {
		top := ui.win.Canvas().Overlays().Top()
		var walk func(o fyne.CanvasObject)
		walk = func(o fyne.CanvasObject) {
			switch o := o.(type) {
			case *canvas.Text:
				if o.Text == option && o.Visible() {
					item = o
				}
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
		walk(top)
	})
	if item == nil {
		t.Fatalf("the select offers no %q", option)
	}
	var pos fyne.Position
	fyne.DoAndWait(func() {
		pos, _ = absolute(ui, item)
		pos = pos.Add(fyne.NewPos(item.Size().Width/2, item.Size().Height/2))
	})
	click(t, ui, pos)
	waitFor(t, func() bool { return sel.Selected == option })
}

// The model is chosen from the settings with clicks alone, saved, and an
// unsaved choice asks before the dialog closes.
func TestFyneModelDialog(t *testing.T) {
	w, _ := modelWindow(t)
	ui := dialogWindow(t, w)
	fyne.DoAndWait(func() { ui.openSettings(0, "Test") })
	var model *flatButton
	fyne.DoAndWait(func() { model = buttonLabeled(ui.overlayBody, tr("settings.model", nil)) })
	clickOn(t, ui, model)
	var selects []*picker
	waitFor(t, func() bool {
		selects = selectsIn(ui.overlayBody)
		return len(selects) == 2 && len(selects[1].Options) > 1 && !selects[1].Disabled()
	})
	before, err := w.Models(0)
	if err != nil {
		t.Fatal(err)
	}
	var other string
	fyne.DoAndWait(func() {
		for _, name := range selects[1].Options {
			if name != before.Model {
				other = name
			}
		}
	})
	chooseInSelect(t, ui, selects[1], other)

	// Leaving with the X asks first; keep editing stays.
	var closeButton *flatButton
	fyne.DoAndWait(func() { closeButton = findButton(ui.overlayBody, true) })
	clickOn(t, ui, closeButton)
	var keep *flatButton
	waitFor(t, func() bool {
		keep = buttonLabeled(ui.overlayBody, tr("dialog.keepEditing", nil))
		return keep != nil && shows(ui, keep)
	})
	clickOn(t, ui, keep)
	waitFor(t, func() bool { return !shows(ui, keep) })

	var save *flatButton
	fyne.DoAndWait(func() {
		walkObjects(ui.overlayBody, func(o fyne.CanvasObject) {
			if b, ok := o.(*flatButton); ok && b.label == tr("dialog.save", nil) && shows(ui, b) {
				save = b
			}
		})
	})
	clickOn(t, ui, save)
	waitFor(t, func() bool { return !ui.overlay.Visible() })
	after, err := w.Models(0)
	if err != nil || after.Model != other {
		t.Fatalf("saved %q, %v; want %q", after.Model, err, other)
	}
}

// A gateway is set up from the settings: the form takes what is typed, and
// the login reaches the gateway of the profile.
func TestFyneGatewayDialog(t *testing.T) {
	w, _ := modelWindow(t)
	received := make(chan plugin.GatewayChange, 1)
	k := kernel.New()
	if err := k.Start(plugin.New("gateway", func(ctx *plugin.Context, _ struct{}) error {
		plugin.Add(ctx, plugin.Gateway{Name: "fixture", Title: "Fixture", Settings: func(context.Context) (plugin.GatewaySettings, error) {
			return plugin.GatewaySettings{Homeserver: "https://example.org"}, nil
		}, Save: func(_ context.Context, c plugin.GatewayChange) error { received <- c; return nil }})
		return nil
	})); err != nil {
		t.Fatal(err)
	}
	w.profiles[0].ctx = k.Context()
	ui := dialogWindow(t, w)
	fyne.DoAndWait(func() { ui.openSettings(0, "Test") })
	var gateways *flatButton
	fyne.DoAndWait(func() { gateways = buttonLabeled(ui.overlayBody, tr("settings.gateways", nil)) })
	clickOn(t, ui, gateways)
	var fixture *flatButton
	waitFor(t, func() bool {
		fixture = buttonLabeled(ui.overlayBody, "Fixture")
		return fixture != nil
	})
	clickOn(t, ui, fixture)
	var entries []*borderlessEntry
	waitFor(t, func() bool {
		entries = entriesIn(ui.overlayBody)
		return len(entries) == 4
	})
	clickOn(t, ui, entries[1])
	fyne.DoAndWait(func() {
		if ui.win.Canvas().Focused() != entries[1] {
			t.Error("a click into the field does not focus it")
		}
		test.Type(entries[1], "@owner:example.org")
	})
	var login *flatButton
	fyne.DoAndWait(func() { login = buttonLabeled(ui.overlayBody, tr("settings.loginSave", nil)) })
	clickOn(t, ui, login)
	change := <-received
	if change.UserID != "@owner:example.org" || change.Homeserver != "https://example.org" {
		t.Fatalf("change %+v", change)
	}
	waitFor(t, func() bool { return buttonLabeled(ui.overlayBody, "Fixture") != nil })
}

// The appearance follows the choice in the settings at once, and the
// desktop with the system choice.
func TestFyneAppearance(t *testing.T) {
	w, _ := modelWindow(t)
	ui := dialogWindow(t, w)
	fyne.DoAndWait(func() { ui.openSettings(0, "Test") })
	var appearance *picker
	waitFor(t, func() bool {
		s := selectsIn(ui.overlayBody)
		if len(s) == 1 {
			appearance = s[0]
		}
		return appearance != nil
	})
	chooseInSelect(t, ui, appearance, tr("settings.dark", nil))
	waitFor(t, func() bool { return pal() == &darkPalette })
	if got := themePreference(ui.app); got != "dark" {
		t.Fatalf("kept %q", got)
	}
	chooseInSelect(t, ui, appearance, tr("settings.light", nil))
	waitFor(t, func() bool { return pal() == &lightPalette })
	// Escape leaves the dialog from the select that has the focus.
	fyne.DoAndWait(func() {
		ui.win.Canvas().Focus(appearance)
		ui.win.Canvas().Focused().TypedKey(&fyne.KeyEvent{Name: fyne.KeyEscape})
	})
	waitFor(t, func() bool { return !ui.overlay.Visible() })
	system := &ouroTheme{pref: "system"}
	if !system.dark(theme.VariantDark) || system.dark(theme.VariantLight) {
		t.Fatal("the system choice does not follow the desktop")
	}
}

// shows reports whether an object and all that holds it are visible.
func shows(ui *fyneUI, o fyne.CanvasObject) bool {
	_, ok := absolute(ui, o)
	return ok
}

func buttonLabeled(o fyne.CanvasObject, label string) *flatButton {
	var found *flatButton
	walkObjects(o, func(o fyne.CanvasObject) {
		if b, ok := o.(*flatButton); ok && b.label == label && found == nil {
			found = b
		}
	})
	return found
}

func selectsIn(o fyne.CanvasObject) []*picker {
	var out []*picker
	walkObjects(o, func(o fyne.CanvasObject) {
		if s, ok := o.(*picker); ok {
			out = append(out, s)
		}
	})
	return out
}

func entriesIn(o fyne.CanvasObject) []*borderlessEntry {
	var out []*borderlessEntry
	walkObjects(o, func(o fyne.CanvasObject) {
		if e, ok := o.(*borderlessEntry); ok {
			out = append(out, e)
		}
	})
	return out
}

// walkObjects visits what an object holds, containers and flows within.
func walkObjects(o fyne.CanvasObject, visit func(fyne.CanvasObject)) {
	visit(o)
	switch o := o.(type) {
	case *fyne.Container:
		for _, child := range o.Objects {
			walkObjects(child, visit)
		}
	case *flowBox:
		walkObjects(o.box, visit)
	}
}
