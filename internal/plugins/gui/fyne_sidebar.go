//go:build cgo

package gui

import (
	"image/color"

	"sort"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// renderList shows the sessions of the sidebar: the ones of the part of the
// thread in view, grouped as the window groups them, the running first and
// then by the day they were last touched.
func (ui *fyneUI) renderList() {
	if ui.back != nil {
		if ui.thread == "" || ui.selected == ui.thread {
			ui.back.Hide()
		} else {
			ui.back.Show()
		}
	}
	objects := make([]fyne.CanvasObject, 0, len(ui.entries)+4)
	if ui.thread != "" && ui.section != nil && !ui.section.at.IsZero() {
		objects = append(objects, inset(0, 10, 6, 10,
			muted("T – "+clock(ui.section.at, time.Now()), sizeSmall)))
	}
	sorted := append([]SessionEntry(nil), ui.entries...)
	sort.SliceStable(sorted, func(i, j int) bool {
		return runningRank(sorted[j]) < runningRank(sorted[i])
	})
	now := time.Now()
	var order []string
	groups := make(map[string][]SessionEntry)
	for _, entry := range sorted {
		if ui.thread != "" && !ui.inSection(entry) {
			continue
		}
		label := dateGroup(entry.Modified, now)
		if entry.Running {
			label = tr("date.active", nil)
		}
		if _, ok := groups[label]; !ok {
			order = append(order, label)
		}
		groups[label] = append(groups[label], entry)
	}
	for _, label := range order {
		group := container.NewVBox()
		group.Objects = append(group.Objects, inset(0, 10, 7, 10,
			muted(strings.ToUpper(label), sizeMeta)))
		for _, entry := range groups[label] {
			group.Objects = append(group.Objects, ui.sessionItem(entry))
		}
		objects = append(objects, group)
	}
	if len(order) == 0 {
		message := tr("sessions.none", nil)
		if ui.thread != "" {
			message = tr("common.none", nil)
		}
		objects = append(objects, inset(12, 10, 12, 10, muted(message, theme.SizeNameText)))
	}
	setChildren(ui.sidebarList, objects...)
	ui.showSessions(len(order) > 0)
}

// showSessions shows the heading and the list of the sidebar only once there
// is a session to list.
func (ui *fyneUI) showSessions(show bool) {
	for _, object := range []fyne.CanvasObject{ui.sessionsHead, ui.sessionsScroll} {
		if object == nil || object.Visible() == show {
			continue
		}
		if show {
			object.Show()
		} else {
			object.Hide()
		}
		if ui.sidebar != nil {
			ui.sidebar.Refresh()
		}
	}
	if ui.columns != nil {
		ui.columns.Refresh()
	}
}

func runningRank(entry SessionEntry) int {
	if entry.Running {
		return 0
	}
	return 1
}

// inSection reports whether a session belongs to the part of the thread in
// view: a part names a session by its ID or by the handle the thread knows it
// by.
func (ui *fyneUI) inSection(entry SessionEntry) bool {
	if ui.section == nil {
		return false
	}
	handle := entry.ID
	if i := strings.Index(handle, "/"); i >= 0 {
		handle = handle[i+1:]
	}
	for known := range ui.section.sessions {
		if strings.HasPrefix(handle, known) {
			return true
		}
	}
	return false
}

// sessionItem is one session of the sidebar: what it is, where it stands and
// a bar that says whether it works.
func (ui *fyneUI) sessionItem(entry SessionEntry) fyne.CanvasObject {
	title := entry.Title
	if title == "" {
		title = entry.ID
	}
	selected := entry.ID == ui.selected
	heading := plain(strings.ToUpper(title), sizeMeta, entry.Running)
	if selected {
		heading.Importance = widget.HighImportance
	}
	state := tr("phase.ready", nil)
	if entry.Running {
		state = tr("session.working", nil)
	}
	owner := tr("session.peer", nil)
	if ui.owner(entry.ID) {
		owner = tr("session.own", nil)
	}
	subtitle := tr("session.state", map[string]any{"state": state, "owner": owner})
	if date := formatDate(entry.Modified); date != "" {
		subtitle += " · " + date
	}
	body := inset(10, 0, 12, 0, container.NewVBox(
		heading,
		wrapped(muted(subtitle, sizeTiny)),
		dashedBar(entry.Running),
	))
	content := container.NewVBox(inset(0, 10, 0, 10, body), widget.NewSeparator())
	var box *tapBox
	background := newPaint(func(r *canvas.Rectangle, p *palette) {
		r.FillColor = color.Transparent
		r.StrokeColor = color.Transparent
		r.StrokeWidth = 1
		r.CornerRadius = 6
		switch {
		case selected:
			r.FillColor = p.selected
			r.StrokeColor = p.accent
		case box != nil && box.hovered:
			r.FillColor = p.sessionHover
		}
		if box != nil && box.focused {
			r.StrokeColor = p.accent
			r.StrokeWidth = 2
		}
	})
	card := container.NewStack(background, content)
	box = newTapBox(card, func() {
		ui.closeDrawer()
		if ui.selected != entry.ID {
			ui.read(entry)
		}
	})
	box.onLook = background.Refresh
	box.key = entry.ID
	return box
}
