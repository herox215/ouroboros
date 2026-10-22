//go:build cgo

package gui

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"ouroboros/internal/credentials"
	"ouroboros/internal/home"
	"ouroboros/internal/kernel"
	"ouroboros/internal/llm/llmtest"
	"ouroboros/plugin"
)

// dcTap taps a button on the UI goroutine, as a click does: a disabled
// button does nothing.
func dcTap(b *flatButton) {
	fyne.DoAndWait(func() { b.Tapped(&fyne.PointEvent{}) })
}

// dcIcon is the icon button of what a dialog shows that draws the icon named.
func dcIcon(o fyne.CanvasObject, icon fyne.Resource) *flatButton {
	var found *flatButton
	walkObjects(o, func(o fyne.CanvasObject) {
		if b, ok := o.(*flatButton); ok && b.icon && b.res != nil && b.res.Name() == icon.Name() && found == nil {
			found = b
		}
	})
	return found
}

// dcShownLabel is the visible label of a dialog whose text holds part.
func dcShownLabel(ui *fyneUI, part string) *widget.Label {
	var found *widget.Label
	walkObjects(ui.overlayBody, func(o fyne.CanvasObject) {
		if l, ok := o.(*widget.Label); ok && part != "" && strings.Contains(l.Text, part) && shows(ui, l) && found == nil {
			found = l
		}
	})
	return found
}

// dcShownButton is the visible button of a dialog with the label given.
func dcShownButton(ui *fyneUI, label string) *flatButton {
	var found *flatButton
	walkObjects(ui.overlayBody, func(o fyne.CanvasObject) {
		if b, ok := o.(*flatButton); ok && b.label == label && shows(ui, b) && found == nil {
			found = b
		}
	})
	return found
}

// dcSettle lets a call the dialog made in the background hand its answer to
// the UI goroutine, also when the answer is dropped and leaves no trace.
func dcSettle() {
	time.Sleep(100 * time.Millisecond)
	fyne.DoAndWait(func() {})
	fyne.DoAndWait(func() {})
}

// dcGateways gives profile the gateways named, in place of its plugins.
func dcGateways(t *testing.T, w *Window, profile int, gateways ...plugin.Gateway) {
	t.Helper()
	k := kernel.New()
	if err := k.Start(plugin.New("gateway", func(ctx *plugin.Context, _ struct{}) error {
		for _, g := range gateways {
			plugin.Add(ctx, g)
		}
		return nil
	})); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k.Stop)
	w.profiles[profile].ctx = k.Context()
}

// The settings open once, and lead to the gateways and the model and back.
func TestFyneSettingsNavigation(t *testing.T) {
	w, _ := modelWindow(t)
	ui := dialogWindow(t, w)
	fyne.DoAndWait(func() {
		ui.openSettings(0, "Test")
		ui.openSettings(1, "Other")
		if ui.settingsProfile != 0 || ui.settingsName != "Test" {
			t.Errorf("a second open replaced the settings: %d %q", ui.settingsProfile, ui.settingsName)
		}
	})
	var gateways *flatButton
	fyne.DoAndWait(func() { gateways = buttonLabeled(ui.overlayBody, tr("settings.gateways", nil)) })
	dcTap(gateways)
	// The page of the gateways goes back to the first page of the settings.
	var back *flatButton
	fyne.DoAndWait(func() { back = dcIcon(ui.overlayBody, theme.NavigateBackIcon()) })
	if back == nil {
		t.Fatal("the gateways have no way back")
	}
	dcTap(back)
	var model *flatButton
	fyne.DoAndWait(func() { model = buttonLabeled(ui.overlayBody, tr("settings.model", nil)) })
	if model == nil {
		t.Fatal("back did not lead to the settings")
	}
	dcTap(model)
	waitFor(t, func() bool {
		s := selectsIn(ui.overlayBody)
		return len(s) == 2 && !s[0].Disabled()
	})
	// Unchanged, the model goes back to the settings without asking.
	fyne.DoAndWait(func() { back = dcIcon(ui.overlayBody, theme.NavigateBackIcon()) })
	dcTap(back)
	fyne.DoAndWait(func() {
		if !ui.overlay.Visible() || buttonLabeled(ui.overlayBody, tr("settings.gateways", nil)) == nil {
			t.Error("the model did not go back to the settings")
		}
	})
	// Unchanged, the X of the model closes it without asking.
	fyne.DoAndWait(func() { model = buttonLabeled(ui.overlayBody, tr("settings.model", nil)) })
	dcTap(model)
	waitFor(t, func() bool {
		s := selectsIn(ui.overlayBody)
		return len(s) == 2 && !s[0].Disabled()
	})
	var closeButton *flatButton
	fyne.DoAndWait(func() { closeButton = dcIcon(ui.overlayBody, theme.CancelIcon()) })
	dcTap(closeButton)
	fyne.DoAndWait(func() {
		if ui.overlay.Visible() {
			t.Error("the unchanged model did not close")
		}
	})
}

// The gateway list says when there is none and when it cannot be read, and
// drops an answer that comes after the page is gone.
func TestFyneGatewayList(t *testing.T) {
	w, _ := modelWindow(t)
	w.profiles[0].ctx = nil
	dcGateways(t, w, 1, plugin.Gateway{Name: "broken", Settings: func(context.Context) (plugin.GatewaySettings, error) {
		return plugin.GatewaySettings{}, errors.New("unreadable")
	}})
	ui := dialogWindow(t, w)

	fyne.DoAndWait(func() {
		ui.openSettings(0, "Test")
		ui.settingsGateways()
	})
	waitFor(t, func() bool { return dcShownLabel(ui, tr("settings.noGateways", nil)) != nil })

	fyne.DoAndWait(func() {
		ui.closeOverlay()
		ui.openSettings(1, "Other")
		ui.settingsGateways()
	})
	waitFor(t, func() bool { return dcShownLabel(ui, tr("settings.gatewayFailed", nil)) != nil })
	fyne.DoAndWait(func() {
		if dcShownLabel(ui, tr("dialog.loading", nil)) != nil {
			t.Error("the failed list still loads")
		}
	})

	// The list comes after the settings closed: nothing is added.
	var body *fyne.Container
	fyne.DoAndWait(func() {
		ui.closeOverlay()
		ui.openSettings(1, "Other")
		ui.settingsGateways()
		body = ui.overlayBody.Objects[0].(*fyne.Container)
		ui.closeOverlay()
	})
	dcSettle()
	fyne.DoAndWait(func() {
		if ui.overlay.Visible() || len(body.Objects) != 2 {
			t.Errorf("the closed list changed: %d objects", len(body.Objects))
		}
	})
}

// A gateway that describes its own fields shows them, sends what is typed,
// forgets secrets, logs out, reports a failed save and cancels.
func TestFyneGatewayFields(t *testing.T) {
	w, _ := modelWindow(t)
	received := make(chan plugin.GatewayChange, 4)
	var fail bool
	dcGateways(t, w, 0, plugin.Gateway{Name: "fields", Settings: func(context.Context) (plugin.GatewaySettings, error) {
		return plugin.GatewaySettings{LoggedIn: true, Status: "syncing", Fields: []plugin.GatewayField{
			{Key: "notify", Label: "Notify", Kind: "bool", On: true},
			{Key: "token", Kind: "secret"},
			{Key: "rooms", Label: "Rooms", Kind: "list", List: []string{"a", "b"}},
			{Key: "server", Label: "Server", Value: "https://example.org"},
		}}, nil
	}, Save: func(_ context.Context, c plugin.GatewayChange) error {
		received <- c
		if fail {
			return errors.New("refused")
		}
		return nil
	}})
	ui := dialogWindow(t, w)
	fyne.DoAndWait(func() {
		ui.openSettings(0, "Test")
		ui.settingsGateways()
	})
	open := func() []*borderlessEntry {
		t.Helper()
		var item *flatButton
		waitFor(t, func() bool {
			item = buttonLabeled(ui.overlayBody, "fields")
			return item != nil
		})
		dcTap(item)
		var entries []*borderlessEntry
		fyne.DoAndWait(func() { entries = entriesIn(ui.overlayBody) })
		if len(entries) != 3 {
			t.Fatalf("%d entries", len(entries))
		}
		return entries
	}
	entries := open()
	fyne.DoAndWait(func() {
		if dcShownLabel(ui, tr("settings.loggedIn", nil)+" · syncing") == nil {
			t.Error("the login state is not shown")
		}
		if entries[1].Text != "a, b" || entries[2].Text != "https://example.org" {
			t.Errorf("fields %q %q", entries[1].Text, entries[2].Text)
		}
		var notify *check
		walkObjects(ui.overlayBody, func(o fyne.CanvasObject) {
			if c, ok := o.(*check); ok && c.Text == "Notify" {
				notify = c
			}
		})
		if notify == nil || !notify.Checked {
			t.Fatal("the check is not shown as on")
		}
		notify.SetChecked(false)
		test.Type(entries[0], "s3cret")
		entries[1].SetText("a, b\n c,, ")
	})
	var save *flatButton
	fyne.DoAndWait(func() { save = buttonLabeled(ui.overlayBody, tr("settings.loginSave", nil)) })
	dcTap(save)
	change := <-received
	wantValues := map[string]string{"notify": "false", "token": "s3cret", "server": "https://example.org"}
	if !reflect.DeepEqual(change.Values, wantValues) || !reflect.DeepEqual(change.Lists, map[string][]string{"rooms": {"a", "b", "c"}}) || change.Logout {
		t.Fatalf("change %+v", change)
	}
	fyne.DoAndWait(func() {
		if entries[0].Text != "" {
			t.Error("the secret stays in its field")
		}
	})

	// A failed logout sends no secret and stays on the form.
	entries = open()
	fail = true
	fyne.DoAndWait(func() { test.Type(entries[0], "kept") })
	var logout, cancel *flatButton
	fyne.DoAndWait(func() {
		logout = buttonLabeled(ui.overlayBody, tr("settings.logout", nil))
		cancel = buttonLabeled(ui.overlayBody, tr("dialog.cancel", nil))
	})
	dcTap(logout)
	change = <-received
	if !change.Logout || change.Values["token"] != "" {
		t.Fatalf("logout %+v", change)
	}
	waitFor(t, func() bool { return dcShownLabel(ui, tr("settings.gatewayFailed", nil)) != nil })
	fyne.DoAndWait(func() {
		if save.disabled || logout.disabled || cancel.disabled {
			t.Error("the form stays locked after the failure")
		}
	})

	// Cancel forgets what was typed and goes back to the list.
	fyne.DoAndWait(func() { test.Type(entries[0], "typed") })
	dcTap(cancel)
	fyne.DoAndWait(func() {
		if entries[0].Text != "" {
			t.Error("cancel keeps the secret")
		}
	})
	open()
}

// The form of a Matrix gateway logs out without the password, goes back on
// Escape, and drops an answer that comes after the dialog closed.
func TestFyneGatewayMatrix(t *testing.T) {
	w, _ := modelWindow(t)
	received := make(chan plugin.GatewayChange, 4)
	dcGateways(t, w, 0, plugin.Gateway{Name: "matrix", Title: "Matrix", Settings: func(context.Context) (plugin.GatewaySettings, error) {
		return plugin.GatewaySettings{Homeserver: "https://example.org", UserID: "@me:example.org", AllowedUsers: []string{"@a:x"}}, nil
	}, Save: func(_ context.Context, c plugin.GatewayChange) error { received <- c; return nil }})
	ui := dialogWindow(t, w)
	fyne.DoAndWait(func() {
		ui.openSettings(0, "Test")
		ui.settingsGateways()
	})
	open := func() []*borderlessEntry {
		t.Helper()
		var item *flatButton
		waitFor(t, func() bool {
			item = buttonLabeled(ui.overlayBody, "Matrix")
			return item != nil
		})
		dcTap(item)
		var entries []*borderlessEntry
		fyne.DoAndWait(func() { entries = entriesIn(ui.overlayBody) })
		if len(entries) != 4 {
			t.Fatalf("%d entries", len(entries))
		}
		return entries
	}
	entries := open()
	fyne.DoAndWait(func() { test.Type(entries[2], "password") })
	var logout *flatButton
	fyne.DoAndWait(func() { logout = buttonLabeled(ui.overlayBody, tr("settings.logout", nil)) })
	dcTap(logout)
	change := <-received
	if !change.Logout || change.Password != "" || change.UserID != "@me:example.org" || !reflect.DeepEqual(change.AllowedUsers, []string{"@a:x"}) {
		t.Fatalf("logout %+v", change)
	}

	open()
	fyne.DoAndWait(func() { ui.escape() })
	fyne.DoAndWait(func() {
		if !ui.overlay.Visible() || len(entriesIn(ui.overlayBody)) != 0 {
			t.Error("escape did not go back to the list")
		}
	})

	open()
	var save *flatButton
	fyne.DoAndWait(func() { save = buttonLabeled(ui.overlayBody, tr("settings.loginSave", nil)) })
	fyne.DoAndWait(func() {
		save.Tapped(&fyne.PointEvent{})
		ui.closeOverlay()
	})
	<-received
	dcSettle()
	fyne.DoAndWait(func() {
		if ui.overlay.Visible() {
			t.Error("a late answer opened the closed dialog")
		}
	})
}

func TestSplitList(t *testing.T) {
	if got := splitList(" a, b\n\n,c ,, "); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Fatalf("split %q", got)
	}
	if got := splitList(" , \n"); got == nil || len(got) != 0 {
		t.Fatalf("empty %#v", got)
	}
}

// dcAccesses gives the first profile two more accesses: one more with models
// and one whose models cannot be listed, and a model the list no longer has.
func dcAccesses(t *testing.T, dir home.Dir) (second string) {
	t.Helper()
	access, err := dir.Credentials().Put(credentials.Access{Provider: "two", Name: "Second", Secret: json.RawMessage(`{"token":"` + llmtest.Token + `"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dir.Credentials().Put(credentials.Access{Provider: "two", Name: "Broken", Secret: json.RawMessage(`{"token":"` + llmtest.Down + `"}`)}); err != nil {
		t.Fatal(err)
	}
	cfg, err := dir.Config()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Model = "retired"
	if err := dir.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	return access.ID
}

// dcModelPickers waits until the model page has read its accesses.
func dcModelPickers(t *testing.T, ui *fyneUI) (access, model *picker) {
	t.Helper()
	waitFor(t, func() bool {
		s := selectsIn(ui.overlayBody)
		if len(s) == 2 && !s[0].Disabled() {
			access, model = s[0], s[1]
		}
		return access != nil
	})
	return access, model
}

// The model page names the model the list lost, says what an access lacks,
// and asks before an unsaved change is left: Escape answers the question,
// keep stays, discard goes back, and an access without models saves nothing.
func TestFyneModelAccesses(t *testing.T) {
	w, dirs := modelWindow(t)
	dcAccesses(t, dirs[0])
	ui := dialogWindow(t, w)
	backs := 0
	fyne.DoAndWait(func() { ui.openModelDialog(0, "", func() { backs++ }) })
	access, model := dcModelPickers(t, ui)
	fyne.DoAndWait(func() {
		if len(access.Options) != 3 || access.Selected != "Account" {
			t.Errorf("accesses %q, %q", access.Options, access.Selected)
		}
		if len(model.Options) != 3 || model.Options[0] != "retired" || model.Selected != "retired" {
			t.Errorf("models %q, %q", model.Options, model.Selected)
		}
		if dcShownLabel(ui, tr("model.default", map[string]any{"name": tr("model.thisProfile", nil)})) == nil {
			t.Error("the page does not name the profile")
		}
		access.SetSelected("Broken")
	})
	fyne.DoAndWait(func() {
		if dcShownLabel(ui, "is down") == nil || dcShownLabel(ui, tr("model.none", nil)) == nil {
			t.Error("the broken access is not reported")
		}
		if !model.Disabled() || len(model.Options) != 0 {
			t.Errorf("the broken access offers %q", model.Options)
		}
	})

	var closeButton, back *flatButton
	fyne.DoAndWait(func() {
		closeButton = dcIcon(ui.overlayBody, theme.CancelIcon())
		back = dcIcon(ui.overlayBody, theme.NavigateBackIcon())
	})
	dcTap(closeButton)
	asked := func() bool {
		var ok bool
		fyne.DoAndWait(func() { ok = dcShownButton(ui, tr("dialog.keepEditing", nil)) != nil })
		return ok
	}
	if !asked() {
		t.Fatal("the change was left without asking")
	}
	fyne.DoAndWait(func() { ui.escape() })
	if asked() {
		t.Fatal("escape did not answer the question")
	}
	fyne.DoAndWait(func() { ui.escape() })
	if !asked() || !ui.overlay.Visible() {
		t.Fatal("escape left the change without asking")
	}
	// Saving an access without models does nothing.
	var saveAndLeave *flatButton
	fyne.DoAndWait(func() { saveAndLeave = dcShownButton(ui, tr("dialog.save", nil)) })
	dcTap(saveAndLeave)
	fyne.DoAndWait(func() {
		if !ui.overlay.Visible() || dcShownLabel(ui, tr("model.saving", nil)) != nil {
			t.Error("an access without models was saved")
		}
	})
	var keep *flatButton
	fyne.DoAndWait(func() { keep = dcShownButton(ui, tr("dialog.keepEditing", nil)) })
	dcTap(keep)
	if asked() {
		t.Fatal("keep editing still asks")
	}

	fyne.DoAndWait(func() { access.SetSelected("Second") })
	fyne.DoAndWait(func() {
		if model.Disabled() || model.Selected != "two-small" || dcShownLabel(ui, "is down") != nil {
			t.Errorf("second access: %q of %q", model.Selected, model.Options)
		}
	})
	dcTap(back)
	var discard *flatButton
	fyne.DoAndWait(func() { discard = dcShownButton(ui, tr("dialog.discard", nil)) })
	if discard == nil {
		t.Fatal("back left the change without asking")
	}
	dcTap(discard)
	fyne.DoAndWait(func() {
		if ui.overlay.Visible() || backs != 1 {
			t.Errorf("discard: visible %v, backs %d", ui.overlay.Visible(), backs)
		}
	})
}

// A save that fails says why and unlocks the page, Escape waits for a save
// on its way, and an answer after the page closed is dropped.
func TestFyneModelStore(t *testing.T) {
	w, dirs := modelWindow(t)
	second := dcAccesses(t, dirs[0])
	ui := dialogWindow(t, w)
	fyne.DoAndWait(func() { ui.openModelDialog(0, "Test", nil) })
	access, model := dcModelPickers(t, ui)
	var save, closeButton *flatButton
	fyne.DoAndWait(func() {
		if dcIcon(ui.overlayBody, theme.NavigateBackIcon()) != nil {
			t.Error("a page on its own has a way back")
		}
		closeButton = dcIcon(ui.overlayBody, theme.CancelIcon())
		save = dcShownButton(ui, tr("dialog.save", nil))
		access.SetSelected("Second")
	})
	if err := dirs[0].Credentials().Remove(second); err != nil {
		t.Fatal(err)
	}
	dcTap(save)
	waitFor(t, func() bool { return dcShownLabel(ui, tr("dialog.failed", nil)) != nil })
	fyne.DoAndWait(func() {
		if save.disabled || closeButton.disabled || access.Disabled() || model.Disabled() || !ui.overlay.Visible() {
			t.Error("the failed save leaves the page locked")
		}
		if dcShownLabel(ui, tr("model.saving", nil)) != nil {
			t.Error("the failed save still saves")
		}
		access.SetSelected("Account")
		model.SetSelected("two-large")
	})
	fyne.DoAndWait(func() {
		save.Tapped(&fyne.PointEvent{})
		ui.escape()
		if !ui.overlay.Visible() || !closeButton.disabled {
			t.Error("escape left a save on its way")
		}
	})
	waitFor(t, func() bool { return !ui.overlay.Visible() })
	if cfg, err := dirs[0].Config(); err != nil || cfg.Model != "two-large" {
		t.Fatalf("saved %q, %v", cfg.Model, err)
	}

	// The page closes while the choice is saved: the choice is kept, the
	// page stays closed.
	fyne.DoAndWait(func() { ui.openModelDialog(0, "Test", nil) })
	_, model = dcModelPickers(t, ui)
	fyne.DoAndWait(func() {
		model.SetSelected("two-small")
		dcShownButton(ui, tr("dialog.save", nil)).Tapped(&fyne.PointEvent{})
		ui.closeOverlay()
	})
	waitFor(t, func() bool {
		cfg, err := dirs[0].Config()
		return err == nil && cfg.Model == "two-small"
	})
	dcSettle()
	fyne.DoAndWait(func() {
		if ui.overlay.Visible() {
			t.Error("a late save opened the closed page")
		}
	})
}

// A page whose models cannot be read says why, one closed before its models
// came stays closed, and the models are not drawn before they are read.
func TestFyneModelLoad(t *testing.T) {
	ui := dialogWindow(t, sessionWindow(t, &testSessions{}))
	fyne.DoAndWait(func() { ui.openModelDialog(0, "Test", nil) })
	waitFor(t, func() bool { return dcShownLabel(ui, "has no owner access") != nil })
	fyne.DoAndWait(func() {
		if dcShownLabel(ui, tr("dialog.loading", nil)) != nil {
			t.Error("the failed page still loads")
		}
		// Escape closes a page without a change at once.
		ui.escape()
		if ui.overlay.Visible() {
			t.Error("escape did not close the page")
		}
		ui.openModelDialog(0, "Test", nil)
		ui.closeOverlay()
	})
	dcSettle()
	fyne.DoAndWait(func() {
		if ui.overlay.Visible() {
			t.Error("late models opened the closed page")
		}
	})

}

// A profile without accesses says so and offers nothing to pick.
func TestFyneModelNoAccess(t *testing.T) {
	w, dirs := modelWindow(t)
	list, err := dirs[0].Credentials().List()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range list {
		if err := dirs[0].Credentials().Remove(a.ID); err != nil {
			t.Fatal(err)
		}
	}
	ui := dialogWindow(t, w)
	fyne.DoAndWait(func() { ui.openModelDialog(0, "Test", nil) })
	waitFor(t, func() bool { return dcShownLabel(ui, tr("model.noAccess", nil)) != nil })
	fyne.DoAndWait(func() {
		s := selectsIn(ui.overlayBody)
		if !s[0].Disabled() || !s[1].Disabled() || len(s[1].Options) != 0 || !dcShownButton(ui, tr("dialog.save", nil)).disabled {
			t.Error("a profile without accesses can pick")
		}
	})

	fyne.DoAndWait(func() {
		d := &fyneModel{ui: ui}
		d.build()
		d.renderModels()
		if d.status.Text != tr("dialog.loading", nil) {
			t.Errorf("drawn before read: %q", d.status.Text)
		}
	})
}
