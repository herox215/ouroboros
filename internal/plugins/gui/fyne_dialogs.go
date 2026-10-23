//go:build cgo

package gui

import (
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"ouroboros/plugin"
)

// showOverlay puts a page of a dialog over the window, as the <dialog> of the
// window does.
// It lies over the window as an overlay of the canvas, so that the keyboard
// stays inside it as in a modal dialog.
func (ui *fyneUI) showOverlay() {
	ui.closeDrawer()
	if !ui.overlay.Visible() {
		ui.win.Canvas().Unfocus()
		ui.win.Canvas().Overlays().Add(ui.overlay)
		ui.overlay.Show()
	}
	ui.overlayScroll.ScrollToTop()
	ui.overlay.Refresh()
}

// closeOverlay leaves the dialog: its page goes away and what was typed in a
// secret field with it.
func (ui *fyneUI) closeOverlay() {
	ui.dialogVersion++
	ui.onEscape = nil
	if ui.overlay.Visible() {
		ui.win.Canvas().Overlays().Remove(ui.overlay)
		ui.overlay.Hide()
	}
	setChildren(ui.overlayBody)
	ui.focusComposer()
}

// dialogHeader is the head of a dialog: back where there is a page to go back
// to, the title, and the X in the corner.
func dialogHeader(title string, back func(), close func()) fyne.CanvasObject {
	var left fyne.CanvasObject
	if back != nil {
		left = newIconButton(theme.NavigateBackIcon(), back)
	}
	right := newIconButton(theme.CancelIcon(), close)
	var leftBox fyne.CanvasObject
	if left != nil {
		leftBox = container.NewCenter(left)
	}
	return inset(0, 0, 10, 0, container.NewBorder(nil, nil, leftBox, container.NewCenter(right),
		text(title, sizeHeading, theme.ColorNameForeground, fyne.TextStyle{Monospace: true, Bold: true})))
}

// openSettings shows the settings of the profile the window shows.
func (ui *fyneUI) openSettings(index int, name string) {
	if ui.overlay.Visible() {
		return
	}
	ui.settingsProfile = index
	ui.settingsName = name
	ui.dialogVersion++
	ui.settingsHome()
	ui.showOverlay()
}

// settingsHome is the first page of the settings.
func (ui *fyneUI) settingsHome() {
	ui.dialogVersion++
	ui.onEscape = nil
	body := container.NewVBox()
	body.Add(dialogHeader(tr("settings.title", nil)+" · "+ui.settingsName, nil, ui.closeOverlay))
	modelButton := newDialogButton(tr("settings.model", nil), func() {
		ui.closeOverlay()
		ui.openModelDialog(ui.settingsProfile, ui.settingsName, func() {
			ui.settingsHome()
			ui.showOverlay()
		})
	})
	gatewaysButton := newDialogButton(tr("settings.gateways", nil), ui.settingsGateways)
	flow := newFlowBox(8)
	flow.SetObjects(modelButton, gatewaysButton)
	body.Add(flow)
	body.Add(inset(10, 0, 0, 0, text(tr("settings.appearance", nil), sizeBody, theme.ColorNameForeground,
		fyne.TextStyle{Monospace: true, Bold: true})))
	labels := []string{tr("settings.system", nil), tr("settings.light", nil), tr("settings.dark", nil)}
	values := []string{"system", "light", "dark"}
	current := themePreference(ui.app)
	selected := 0
	for i, value := range values {
		if value == current {
			selected = i
		}
	}
	selectTheme := newPicker(labels, nil)
	selectTheme.SetSelectedIndex(selected)
	selectTheme.OnChanged = func(label string) {
		for i, option := range labels {
			if option == label {
				ui.app.Preferences().SetString(themePreferenceKey, values[i])
				ui.app.Settings().SetTheme(&ouroTheme{pref: values[i]})
				return
			}
		}
	}
	body.Add(outline(selectTheme))
	setChildren(ui.overlayBody, body)
}

// settingsGateways lists the gateway plugins of the profile.
func (ui *fyneUI) settingsGateways() {
	ui.dialogVersion++
	version := ui.dialogVersion
	ui.onEscape = nil
	body := container.NewVBox()
	body.Add(dialogHeader(tr("settings.title", nil)+" › "+tr("settings.gateways", nil),
		ui.settingsHome, ui.closeOverlay))
	status := widget.NewLabel(tr("dialog.loading", nil))
	status.Importance = widget.LowImportance
	body.Add(status)
	setChildren(ui.overlayBody, body)
	profile := ui.settingsProfile
	go func() {
		list, err := ui.bridge.Gateways(profile)
		fyne.Do(func() {
			if version != ui.dialogVersion || !ui.overlay.Visible() {
				return
			}
			if err != nil {
				status.SetText("")
				status.Hide()
				failure := widget.NewLabel(tr("settings.gatewayFailed", nil))
				failure.Importance = widget.DangerImportance
				body.Add(failure)
				body.Refresh()
				return
			}
			if len(list) == 0 {
				status.SetText(tr("settings.noGateways", nil))
			} else {
				status.Hide()
			}
			for _, item := range list {
				item := item
				label := item.Title
				if label == "" {
					label = item.Name
				}
				body.Add(container.NewHBox(newDialogButton(label, func() { ui.settingsGateway(item) })))
			}
			body.Refresh()
		})
	}()
}

// fieldInput is one field of a gateway form: a text, a secret, a list or a
// check.
type fieldInput struct {
	kind  string
	entry *borderlessEntry
	check *check
}

// settingsGateway is the form of one gateway.
func (ui *fyneUI) settingsGateway(item plugin.GatewaySettings) {
	ui.dialogVersion++
	version := ui.dialogVersion
	title := item.Title
	if title == "" {
		title = item.Name
	}
	back := func() {
		ui.settingsGateways()
	}
	ui.onEscape = func() bool {
		back()
		return true
	}
	body := container.NewVBox()
	body.Add(dialogHeader(
		tr("settings.title", nil)+" › "+tr("settings.gateways", nil)+" › "+title,
		back, ui.closeOverlay))

	form := container.NewVBox()
	enabled := newCheck(tr("settings.enabled", nil))
	enabled.SetChecked(item.Enabled)
	form.Objects = append(form.Objects, enabled)

	var homeserver, user, password, users *borderlessEntry
	inputs := make(map[string]fieldInput)
	field := func(label, value string) *borderlessEntry {
		entry := newBorderlessEntry(false)
		entry.SetText(value)
		form.Objects = append(form.Objects,
			text(label, sizeBody, theme.ColorNameForeground, fyne.TextStyle{Monospace: true, Bold: true}),
			outline(entry))
		return entry
	}
	secret := func(label string) *borderlessEntry {
		entry := newBorderlessEntry(true)
		form.Objects = append(form.Objects,
			text(label, sizeBody, theme.ColorNameForeground, fyne.TextStyle{Monospace: true, Bold: true}),
			outline(entry))
		return entry
	}
	if len(item.Fields) > 0 {
		for _, fieldDef := range item.Fields {
			label := fieldDef.Label
			if label == "" {
				label = fieldDef.Key
			}
			switch fieldDef.Kind {
			case "bool":
				check := newCheck(label)
				check.SetChecked(fieldDef.On)
				form.Objects = append(form.Objects, check)
				inputs[fieldDef.Key] = fieldInput{kind: "bool", check: check}
			case "secret":
				inputs[fieldDef.Key] = fieldInput{kind: "secret", entry: secret(label)}
			case "list":
				inputs[fieldDef.Key] = fieldInput{kind: "list", entry: field(label, strings.Join(fieldDef.List, ", "))}
			default:
				inputs[fieldDef.Key] = fieldInput{kind: "text", entry: field(label, fieldDef.Value)}
			}
		}
	} else {
		homeserver = field(tr("settings.homeserver", nil), item.Homeserver)
		user = field(tr("settings.user", nil), item.UserID)
		password = secret(tr("settings.password", nil))
		users = field(tr("settings.allowedUsers", nil), strings.Join(item.AllowedUsers, ", "))
	}
	warning := widget.NewLabel(tr("settings.allowlistWarning", nil))
	warning.Wrapping = fyne.TextWrapWord
	warning.Importance = widget.LowImportance
	form.Objects = append(form.Objects, warning)
	login := tr("settings.loggedOut", nil)
	if item.LoggedIn {
		login = tr("settings.loggedIn", nil)
	}
	if item.Status != "" {
		login += " · " + item.Status
	}
	status := widget.NewLabel(login)
	form.Objects = append(form.Objects, status)

	var save, logout, cancel *flatButton
	lock := func(locked bool) {
		for _, button := range []*flatButton{save, logout, cancel} {
			if locked {
				button.Disable()
			} else {
				button.Enable()
			}
		}
	}
	clearPasswords := func() {
		if password != nil {
			password.SetText("")
		}
		for _, input := range inputs {
			if input.kind == "secret" && input.entry != nil {
				input.entry.SetText("")
			}
		}
	}
	// The buttons are locked while a change is on its way, so submit never
	// runs twice at once.
	submit := func(loggingOut bool) {
		change := plugin.GatewayChange{Enabled: enabled.Checked, Logout: loggingOut}
		if len(item.Fields) > 0 {
			change.Values = make(map[string]string)
			change.Lists = make(map[string][]string)
			for _, fieldDef := range item.Fields {
				input := inputs[fieldDef.Key]
				switch fieldDef.Kind {
				case "list":
					change.Lists[fieldDef.Key] = splitList(input.entry.Text)
				case "bool":
					change.Values[fieldDef.Key] = strconv.FormatBool(input.check.Checked)
				case "secret":
					if loggingOut {
						change.Values[fieldDef.Key] = ""
					} else {
						change.Values[fieldDef.Key] = input.entry.Text
					}
				default:
					change.Values[fieldDef.Key] = input.entry.Text
				}
			}
		} else {
			change.Homeserver = homeserver.Text
			change.UserID = user.Text
			if !loggingOut {
				change.Password = password.Text
			}
			change.AllowedUsers = splitList(users.Text)
		}
		clearPasswords()
		lock(true)
		go func() {
			err := ui.bridge.SaveGateway(ui.settingsProfile, item.Name, change)
			fyne.Do(func() {
				if version != ui.dialogVersion || !ui.overlay.Visible() {
					return
				}
				lock(false)
				if err != nil {
					failure := widget.NewLabel(tr("settings.gatewayFailed", nil))
					failure.Importance = widget.DangerImportance
					form.Objects = append(form.Objects, failure)
					form.Refresh()
					return
				}
				ui.settingsGateways()
			})
		}()
	}
	save = newDialogAction(tr("settings.loginSave", nil), true, func() { submit(false) })
	logout = newDialogAction(tr("settings.logout", nil), false, func() { submit(true) })
	cancel = newDialogAction(tr("dialog.cancel", nil), false, func() {
		clearPasswords()
		back()
	})
	actions := newFlowBox(8)
	actions.SetObjects(save, logout, cancel)
	form.Objects = append(form.Objects, actions)
	body.Add(form)
	setChildren(ui.overlayBody, body)
}

func splitList(value string) []string {
	parts := strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == '\n' })
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// fyneModel is the page that sets the model of a profile: what the terminal
// picker does, in the window.
type fyneModel struct {
	ui      *fyneUI
	profile int
	name    string
	back    func()
	version int

	access       *picker
	model        *picker
	status       *widget.Label
	failure      *widget.Label
	confirm      *fyne.Container
	actions      *fyne.Container
	save         *flatButton
	backBtn      *flatButton
	close        *flatButton
	keep         *flatButton
	discard      *flatButton
	saveAndLeave *flatButton

	accessIDs    []string
	accessLabels []string
	accessOf     map[string]string
	data         ModelSelection
	ready        bool
	shown        bool
	shownAccess  string
	shownModel   string
	saving       bool
	leaving      func()
}

// openModelDialog shows the model page, from the settings or on its own.
func (ui *fyneUI) openModelDialog(profile int, name string, back func()) {
	ui.dialogVersion++
	dialog := &fyneModel{ui: ui, profile: profile, name: name, back: back, version: ui.dialogVersion}
	dialog.build()
	ui.onEscape = func() bool { return dialog.escape() }
	setChildren(ui.overlayBody, dialog.page())
	ui.showOverlay()
	// The models the accesses listed the last time show at once; asking
	// the accesses anew takes as long as the slowest of them.
	go func() {
		current := func() bool { return dialog.version == ui.dialogVersion && ui.overlay.Visible() }
		known, knownErr := ui.bridge.KnownModels(profile)
		fyne.Do(func() {
			if current() && knownErr == nil {
				dialog.loaded(known, nil)
				dialog.refreshing()
			}
		})
		listed, listErr := ui.bridge.Models(profile)
		fyne.Do(func() {
			if current() {
				dialog.refreshed(listed, listErr)
			}
		})
	}()
}

// refreshing says that the models shown are asked for anew.
func (d *fyneModel) refreshing() {
	if d.ready && (d.status.Text == "" || d.status.Text == tr("model.none", nil)) && !d.failure.Visible() {
		d.status.SetText(tr("model.refreshing", nil))
	}
}

// refreshed shows the models the accesses listed just now in place of the
// ones they listed the last time, and keeps what the owner already chose
// where it is still there. While a choice is saved, the list stays.
func (d *fyneModel) refreshed(selection ModelSelection, err error) {
	if !d.ready {
		d.loaded(selection, err)
		return
	}
	if d.saving {
		return
	}
	if err != nil {
		d.status.SetText("")
		d.fail(tr("dialog.failed", nil) + " " + err.Error())
		return
	}
	changed := d.changed()
	access, model := d.selectedAccess(), d.model.Selected
	d.loaded(selection, nil)
	if !changed {
		return
	}
	for i, id := range d.accessIDs {
		if id == access {
			d.access.SetSelectedIndex(i)
		}
	}
	for _, name := range d.model.Options {
		if name == model {
			d.model.SetSelected(model)
		}
	}
}

func (d *fyneModel) build() {
	d.access = newPicker(nil, func(string) { d.renderModels() })
	d.access.Disable()
	d.model = newPicker(nil, nil)
	d.model.Disable()
	d.status = widget.NewLabel(tr("dialog.loading", nil))
	d.status.Importance = widget.LowImportance
	d.failure = widget.NewLabel("")
	d.failure.Importance = widget.DangerImportance
	d.failure.Hide()
	d.save = newDialogAction(tr("dialog.save", nil), true, func() { d.store(d.closing()) })
	d.save.Disable()

	d.keep = newDialogAction(tr("dialog.keepEditing", nil), false, func() {
		d.unask()
	})
	d.discard = newDialogAction(tr("dialog.discard", nil), false, func() {
		to := d.leaving
		d.shown = false
		d.unask()
		if to != nil {
			to()
		}
	})
	// The confirmation shows only after ask, so there is always where to go.
	d.saveAndLeave = newDialogAction(tr("dialog.save", nil), true, func() { d.store(d.leaving) })
	confirmButtons := newFlowBox(8)
	confirmButtons.SetObjects(d.keep, d.discard, d.saveAndLeave)
	unsaved := widget.NewLabel(tr("dialog.unsaved", nil))
	unsaved.Wrapping = fyne.TextWrapWord
	d.confirm = container.NewVBox(unsaved, confirmButtons)
	d.confirm.Hide()
	d.actions = container.NewHBox(layout.NewSpacer(), d.save)
}

func (d *fyneModel) page() fyne.CanvasObject {
	d.close = newIconButton(theme.CancelIcon(), func() { d.leave(d.closing()) })
	var left fyne.CanvasObject
	if d.back != nil {
		d.backBtn = newIconButton(theme.NavigateBackIcon(), func() { d.leave(d.backing()) })
		left = container.NewCenter(d.backBtn)
	}
	header := inset(0, 0, 10, 0, container.NewBorder(nil, nil, left, container.NewCenter(d.close),
		text(tr("dialog.model.title", nil), sizeHeading, theme.ColorNameForeground, fyne.TextStyle{Monospace: true, Bold: true})))
	description := muted(tr("model.default", map[string]any{"name": d.displayName()}), theme.SizeNameText)
	description.Wrapping = fyne.TextWrapWord
	form := container.NewVBox(
		text(tr("model.access", nil), sizeBody, theme.ColorNameForeground, fyne.TextStyle{Monospace: true, Bold: true}),
		outline(d.access),
		text(tr("model.model", nil), sizeBody, theme.ColorNameForeground, fyne.TextStyle{Monospace: true, Bold: true}),
		outline(d.model),
		d.status, d.failure, d.confirm, d.actions,
	)
	return container.NewVBox(header, description, form)
}

func (d *fyneModel) displayName() string {
	if d.name != "" {
		return d.name
	}
	return tr("model.thisProfile", nil)
}

func (d *fyneModel) loaded(selection ModelSelection, err error) {
	if err != nil {
		d.status.SetText("")
		d.fail(tr("dialog.failed", nil) + " " + err.Error())
		return
	}
	d.data = selection
	d.ready = true
	d.accessIDs = d.accessIDs[:0]
	d.accessLabels = d.accessLabels[:0]
	d.accessOf = make(map[string]string)
	for _, entry := range selection.Accesses {
		d.accessIDs = append(d.accessIDs, entry.ID)
		d.accessLabels = append(d.accessLabels, entry.Label)
		// The picker names the first of two accesses with the same label.
		if _, ok := d.accessOf[entry.Label]; !ok {
			d.accessOf[entry.Label] = entry.ID
		}
	}
	d.access.SetOptions(d.accessLabels)
	if len(d.accessIDs) == 0 {
		// Without an access there is nothing to pick: the model and the save
		// stay disabled as build left them, and the status says why.
		d.access.Disable()
		d.status.SetText(tr("model.noAccess", nil))
		return
	}
	d.access.Enable()
	chosen := 0
	for i, id := range d.accessIDs {
		if id == selection.Access {
			chosen = i
		}
	}
	d.access.SetSelectedIndex(chosen)
	d.renderModels()
	d.shown = true
	d.shownAccess = d.selectedAccess()
	d.shownModel = d.model.Selected
}

// selectedAccess is the ID of the access the picker names.
func (d *fyneModel) selectedAccess() string {
	return d.accessOf[d.access.Selected]
}

func (d *fyneModel) renderModels() {
	if !d.ready {
		return
	}
	d.clearFailure()
	access := d.selectedAccess()
	var models []string
	for _, entry := range d.data.Accesses {
		if entry.ID != access {
			continue
		}
		models = append(models, entry.Models...)
		if entry.Error != "" {
			d.fail(tr("dialog.failed", nil) + " " + entry.Error)
		}
	}
	if access == d.data.Access && d.data.Model != "" {
		found := false
		for _, name := range models {
			if name == d.data.Model {
				found = true
			}
		}
		if !found {
			models = append([]string{d.data.Model}, models...)
		}
	}
	d.model.SetOptions(models)
	if access == d.data.Access {
		d.model.SetSelected(d.data.Model)
	} else if len(models) > 0 {
		d.model.SetSelectedIndex(0)
	}
	if len(models) == 0 {
		d.model.Disable()
		d.save.Disable()
		d.status.SetText(tr("model.none", nil))
	} else {
		d.model.Enable()
		d.save.Enable()
		d.status.SetText("")
	}
}

func (d *fyneModel) changed() bool {
	return d.shown && (d.selectedAccess() != d.shownAccess || d.model.Selected != d.shownModel)
}

func (d *fyneModel) ask(to func()) {
	d.leaving = to
	d.confirm.Show()
	d.actions.Hide()
}

func (d *fyneModel) unask() {
	d.leaving = nil
	d.confirm.Hide()
	d.actions.Show()
}

func (d *fyneModel) closing() func() {
	return func() { d.ui.closeOverlay() }
}

func (d *fyneModel) backing() func() {
	return func() {
		d.ui.closeOverlay()
		if d.back != nil {
			d.back()
		}
	}
}

// leave closes or goes back, and asks first when there is a change. Its
// buttons are locked while the choice is saved.
func (d *fyneModel) leave(to func()) {
	if d.changed() {
		d.ask(to)
		return
	}
	to()
}

// escape answers the unsaved change before closing, as the window does.
func (d *fyneModel) escape() bool {
	if d.saving {
		return true
	}
	if d.confirm.Visible() {
		d.unask()
		return true
	}
	if d.changed() {
		d.ask(d.closing())
		return true
	}
	return false
}

func (d *fyneModel) lock(locked bool) {
	buttons := []*flatButton{d.close, d.backBtn}
	for _, button := range buttons {
		if button == nil {
			continue
		}
		if locked {
			button.Disable()
		} else {
			button.Enable()
		}
	}
	confirm := []*flatButton{d.keep, d.discard, d.saveAndLeave}
	for _, button := range confirm {
		if locked {
			button.Disable()
		} else {
			button.Enable()
		}
	}
	if locked {
		d.access.Disable()
		d.model.Disable()
		d.save.Disable()
		return
	}
	if len(d.accessIDs) > 0 {
		d.access.Enable()
	}
	if len(d.model.Options) > 0 {
		d.model.Enable()
		d.save.Enable()
	}
}

func (d *fyneModel) fail(text string) {
	d.failure.SetText(text)
	d.failure.Show()
}

func (d *fyneModel) clearFailure() {
	d.failure.SetText("")
	d.failure.Hide()
}

func (d *fyneModel) store(then func()) {
	if d.saving || d.model.Disabled() || !d.ready {
		return
	}
	current := d.version
	d.saving = true
	d.lock(true)
	d.status.SetText(tr("model.saving", nil))
	d.clearFailure()
	go func() {
		_, err := d.ui.bridge.SetModel(d.profile, d.selectedAccess(), d.model.Selected)
		fyne.Do(func() {
			if current != d.ui.dialogVersion {
				return
			}
			if err != nil {
				d.unask()
				d.fail(tr("dialog.failed", nil) + " " + err.Error())
				d.status.SetText("")
			} else {
				d.shown = false
				then()
			}
			d.saving = false
			d.lock(false)
		})
	}()
}
