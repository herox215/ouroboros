//go:build cgo

package gui

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/driver/desktop"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// The fyne command opens the same window as gui, drawn with Fyne instead of a
// web view: the same sessions service, the same profile picker, the same
// thread, composer and context rail. It is a command of its own so that both
// windows can be tried beside each other.
func (p *Profile) runFyne(ctx context.Context, args []string) error {
	if len(args) != 0 {
		return errors.New("usage: ouro [--profile PROFILE] fyne")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	window, err := p.windowPage(ctx)
	if err != nil {
		return err
	}
	return showFyne(ctx, window)
}

// showFyne is replaced by tests, so that no window has to open.
var showFyne = showFyneWindow

// newFyneApp and showAndRun are the desktop driver, which tests replace with
// the test driver.
var (
	newFyneApp = func() fyne.App { return app.NewWithID("dev.ouroboros.gui.fyne") }
	showAndRun = fyne.Window.ShowAndRun
)

func showFyneWindow(ctx context.Context, window page) error {
	bridge := newWindowContext(ctx, window)
	defer bridge.Close()
	application := newFyneApp()
	bridge.useTheme(application)
	native := application.NewWindow("Ouroboros")
	native.SetPadded(false)
	application.SetIcon(appIcon)
	native.SetIcon(appIcon)
	native.Resize(windowSize(application))
	native.CenterOnScreen()
	ui := newFyneUI(ctx, application, native, bridge)
	for key, text := range window.drafts {
		ui.drafts[key] = &fyneSessionState{text: text}
	}
	ui.build()
	bridge.OnRestart(func() {
		fyne.Do(func() { native.Close() })
	})
	watchCtx, stopWatching := context.WithCancel(ctx)
	watching := make(chan struct{})
	go func() {
		defer close(watching)
		watchExecutable(watchCtx, bridge.restart)
	}()
	// The watcher ends with the window, before what it read changes.
	defer func() {
		stopWatching()
		<-watching
	}()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			fyne.Do(func() { native.Close() })
		case <-done:
		}
	}()
	go ui.start()
	showAndRun(native)
	keepWindowSize(application, native.Canvas().Size())
	if exe, going, ok := bridge.Restart(); ok {
		if going.Session == "" {
			going.Session = bridge.state.Thread
		}
		going.Drafts = ui.unsentDrafts()
		return handOver(exe, going)
	}
	return ctx.Err()
}

// The size the window had when it closed, which it opens with again, as a
// native window does.
const (
	widthPreferenceKey  = "ouroboros.gui.width"
	heightPreferenceKey = "ouroboros.gui.height"
)

// windowSize is the size the window opens with: the one it closed with,
// 1180×780 the first time, and never below the smallest it allows.
func windowSize(app fyne.App) fyne.Size {
	prefs := app.Preferences()
	return fyne.NewSize(
		fyne.Max(360, float32(prefs.FloatWithFallback(widthPreferenceKey, 1180))),
		fyne.Max(420, float32(prefs.FloatWithFallback(heightPreferenceKey, 780))))
}

// keepWindowSize keeps the size the window closed with; a window that never
// showed has none to keep.
func keepWindowSize(app fyne.App, size fyne.Size) {
	if size.Width <= 0 || size.Height <= 0 {
		return
	}
	app.Preferences().SetFloat(widthPreferenceKey, float64(size.Width))
	app.Preferences().SetFloat(heightPreferenceKey, float64(size.Height))
}

// unsentDrafts carries text only, not pending sends or stale error state.
func (ui *fyneUI) unsentDrafts() map[string]string {
	drafts := make(map[string]string)
	for key, state := range ui.drafts {
		if state.text != "" {
			drafts[key] = state.text
		}
	}
	return drafts
}

// fyneSessionState is what the composer of one session keeps: its draft, the
// error of the last attempt, whether a message is on its way and what a send
// told.
type fyneSessionState struct {
	text         string
	error        string
	pending      bool
	notice       string
	sentQuestion string
}

// fyneContext is the session the window shows.
type fyneContext struct {
	profile int
	id      string
	state   *fyneSessionState
}

// fyneExchange is one part of the thread: an owner message and the sessions
// the thread started or handed a message to on it.
type fyneExchange struct {
	at       time.Time
	sessions map[string]bool
}

func (e *fyneExchange) handed(id string) bool {
	if e.sessions == nil {
		e.sessions = make(map[string]bool)
	}
	if id == "" || e.sessions[id] {
		return false
	}
	e.sessions[id] = true
	return true
}

// fyneActivity is how much works for the shown session.
type fyneActivity struct {
	busy     bool
	n        int
	own      bool
	stopping bool
}

// fyneQuestion is the question panel as it shows.
type fyneQuestion struct {
	key         string
	text        string
	options     []string
	recommended string
	buttons     []*flatButton
}

// fyneTrace is what was read of a session of the part of the thread in view.
type fyneTrace struct {
	list    []ChatEvent
	running bool
}

// fyneInspect is a tool result opened deliberately beside the thread.
type fyneInspect struct {
	profile int
	id      string
}

// fyneUI is the state of the Fyne window: the same state frontend/app.js
// keeps, and the widgets that show it.
type fyneUI struct {
	app    fyne.App
	win    fyne.Window
	bridge *Window
	run    context.Context

	thread   string
	profile  int
	resume   string
	profiles []ProfileOption

	epoch    atomic.Int64 // the view of the window; every read and load bumps it
	ready    atomic.Bool
	entries  []SessionEntry
	selected string

	ctx      *fyneContext
	activity fyneActivity
	asking   *fyneQuestion
	drafts   map[string]*fyneSessionState
	heard    bool
	own      []ChatEvent

	section    *fyneExchange
	latest     *fyneExchange
	visible    []*fyneMessage
	steps      map[string]*fyneTrace
	inspecting *fyneInspect
	following  bool
	listing    bool

	// While a batch of events is applied, the transcript and the composer
	// are laid out once at its end, see applyEvents.
	applying      bool
	contentDirty  bool
	composerDirty bool
	drawing       int

	chat *fyneChat

	// The widgets of the three columns.
	columns        *fyne.Container
	sidebar        *fyne.Container
	sidebarPanel   *fyne.Container
	sidebarPaints  []*paint
	sidebarList    *fyne.Container
	sessionsScroll *container.Scroll
	settingsButton *flatButton
	main           *fyne.Container
	dock           *fyne.Container
	rail           *fyne.Container
	menu           *flatButton
	narrow         bool
	drawerOpen     bool

	header   *fyne.Container
	eyebrow  *widget.RichText
	title    *widget.RichText
	meta     *widget.Label
	back     *flatButton
	errorBox *fyne.Container
	// emptyHint says what to do where a conversation has no messages yet.
	emptyHint *widget.Label
	errorInk  *widget.RichText

	content    *fyne.Container
	transcript *container.Scroll
	fade       *canvas.Raster

	statusLabel  *widget.Label
	questionBox  *fyne.Container
	questionTxt  *widget.RichText
	questionOpt  *flowBox
	questionHint *widget.Label
	draft        *composerEntry
	stop         *flatButton
	send         *flatButton
	sendStatus   *widget.Label
	sendError    *widget.RichText

	contextTitle   *widget.RichText
	contextNote    *widget.Label
	contextDetails *fyne.Container
	contextScroll  *container.Scroll

	overlay       *fyne.Container
	overlayBody   *fyne.Container
	overlayScroll *container.Scroll
	onEscape      func() bool
	shortcuts     map[fyne.KeyName]func()
	shownTitle    string // the title of the session shown

	noticeTime time.Duration // how long a notice under the composer stays

	// The settings pages keep the profile they were opened for, so that a
	// save cannot be redirected by another profile.
	settingsProfile int
	settingsName    string
	dialogVersion   int
}

func newFyneUI(ctx context.Context, application fyne.App, native fyne.Window, bridge *Window) *fyneUI {
	return &fyneUI{
		noticeTime: 4 * time.Second,
		app:        application,
		win:        native,
		bridge:     bridge,
		run:        ctx,
		drafts:     make(map[string]*fyneSessionState),
		steps:      make(map[string]*fyneTrace),
	}
}

// build lays the window out as the grid of the stylesheet: the sidebar, the
// thread and the context rail; below narrowWidth the sidebar is a drawer.
func (ui *fyneUI) build() {
	ui.sidebarList = container.NewVBox()
	ui.sessionsScroll = container.NewVScroll(ui.sidebarList)
	heading := inset(0, 0, 7, 0, text(strings.ToUpper(tr("sessions.title", nil)), sizeSection, theme.ColorNameDisabled,
		fyne.TextStyle{Monospace: true}))
	ui.settingsButton = newIconButton(theme.SettingsIcon(), func() {
		ui.openSettings(ui.profile, ui.profileName())
	})
	ui.settingsButton.iconSize = 22
	ui.settingsButton.minWidth, ui.settingsButton.minHeight = 44, 44
	footer := inset(10, 0, 0, 0, container.NewHBox(ui.settingsButton))
	ui.sidebar = inset(24, 28, 18, 28, container.New(&sidebarLayout{ui: ui}, heading, ui.sessionsScroll, footer))
	sidebarBackground := newPaint(func(r *canvas.Rectangle, p *palette) {
		r.FillColor = color.Transparent
		r.StrokeWidth = 0
		if ui.narrow {
			r.FillColor = p.background
		}
	})
	sidebarEdge := newPaint(func(r *canvas.Rectangle, p *palette) {
		r.FillColor = color.Transparent
		if ui.narrow {
			r.FillColor = p.line
		}
	})
	ui.sidebarPanel = container.NewStack(sidebarBackground,
		container.NewBorder(nil, nil, nil, container.New(&fixedWidthLayout{width: 1}, sidebarEdge), ui.sidebar))
	ui.sidebarPaints = []*paint{sidebarBackground, sidebarEdge}

	ui.eyebrow = text("OUROBOROS", sizeMeta, theme.ColorNameDisabled, fyne.TextStyle{Monospace: true, Bold: true})
	ui.title = text(strings.ToUpper(tr("session.select", nil)), sizeTitle, theme.ColorNameForeground, fyne.TextStyle{Monospace: true})
	ui.meta = muted("", sizeBody)
	ui.meta.Truncation = fyne.TextTruncateEllipsis
	ui.back = newBackButton(tr("nav.backToThread", nil), func() {
		if ui.thread != "" {
			ui.read(ui.threadEntry())
		}
	})
	ui.back.Hide()
	ui.menu = newIconButton(theme.MenuIcon(), ui.toggleDrawer)
	ui.menu.Hide()
	ui.header = inset(22, 0, 10, 0, container.NewBorder(nil, nil, container.NewCenter(ui.menu), container.NewCenter(ui.back),
		tightVBox(-5, ui.eyebrow, ui.title, ui.meta)))

	ui.errorInk = text("", sizeBody, theme.ColorNameError, fyne.TextStyle{Monospace: true})
	errorBackground := newPaint(func(r *canvas.Rectangle, p *palette) {
		r.FillColor = p.errorBackground
		r.StrokeColor = p.errorLine
		r.StrokeWidth = 1
		r.CornerRadius = 8
	})
	ui.errorBox = container.NewStack(errorBackground, inset(10, 14, 10, 14, ui.errorInk))
	ui.errorBox.Hide()
	ui.content = container.NewVBox()
	ui.emptyHint = muted(tr("transcript.empty", nil), sizeBody)
	ui.emptyHint.Wrapping = fyne.TextWrapWord
	ui.emptyHint.Hide()
	frame := container.NewVBox(ui.errorBox, ui.emptyHint, ui.content)
	history := container.New(&anchorLayout{ui: ui, inner: centerMaxLayout{max: 760, gutter: 24}}, inset(18, 0, 22, 0, frame))
	ui.transcript = container.NewVScroll(history)
	ui.transcript.OnScrolled = func(fyne.Position) { ui.scrolled() }
	ui.fade = canvas.NewRaster(fadeGradient)
	ui.fade.Hide()
	fade := container.New(&topFadeLayout{}, ui.fade)
	transcriptFrame := container.NewStack(container.New(&followLayout{ui: ui}, ui.transcript), fade)

	ui.statusLabel = muted("Loading the thread …", sizeTiny)
	ui.statusLabel.Alignment = fyne.TextAlignTrailing
	ui.statusLabel.Truncation = fyne.TextTruncateEllipsis

	ui.questionTxt = text("", sizeBody, theme.ColorNameForeground, fyne.TextStyle{Monospace: true})
	ui.questionOpt = newFlowBox(8)
	ui.questionHint = muted("", sizeSmall)
	ui.questionHint.Wrapping = fyne.TextWrapWord
	questionScroll := container.NewVScroll(container.NewVBox(
		text(tr("question.heading", nil), sizeBody, theme.ColorNamePrimary, fyne.TextStyle{Monospace: true, Bold: true}),
		ui.questionTxt, ui.questionOpt, ui.questionHint,
	))
	ui.questionBox = container.New(&cappedLayout{share: 0.4}, questionScroll)
	ui.questionBox.Hide()

	ui.draft = newComposerEntry(func(value string) { ui.submit(value, false) })
	ui.draft.Wrapping = fyne.TextWrapWord
	ui.draft.PlaceHolder = tr("composer.placeholder", nil)
	ui.draft.TextStyle = fyne.TextStyle{Monospace: true}
	ui.draft.SetMinRowsVisible(1)
	ui.draft.OnChanged = func(value string) {
		if ui.ctx != nil {
			ui.ctx.state.text = value
		}
		ui.draft.grow()
		// Typing changes only the send button, not session status or layout.
		ui.updateSend()
	}
	ui.stop = newStopButton(tr("composer.stop", nil), ui.stopAnswer)
	ui.stop.Hide()
	ui.send = newSendButton(func() { ui.submit(ui.draft.Text, false) })
	ui.send.Disable()
	prompt := inset(2, 0, 0, 0, muted("›", sizeBody))
	actions := container.NewVBox(layout.NewSpacer(), container.NewHBox(ui.stop, ui.send))
	row := container.NewBorder(nil, nil, prompt, actions, ui.draft)
	ui.sendStatus = muted("", sizeTiny)
	ui.sendError = text("", sizeBody, theme.ColorNameError, fyne.TextStyle{Monospace: true})
	ui.sendError.Hide()
	composerTop := container.NewVBox(ui.statusLabel, ui.questionBox)
	ui.dock = gutterCentered(760, 24, inset(0, 0, 14, 0, container.NewVBox(
		composerTop,
		widget.NewSeparator(),
		row,
		ui.sendStatus,
		ui.sendError,
	)))
	ui.main = container.NewBorder(gutterCentered(760, 24, ui.header), ui.dock, nil, nil, transcriptFrame)
	ui.questionBox.Layout.(*cappedLayout).of = ui.main

	ui.contextTitle = text(strings.ToUpper(tr("context.title", nil)), sizeSection, theme.ColorNameForeground, fyne.TextStyle{Monospace: true})
	ui.contextNote = muted(tr("context.hint", nil), sizeMeta)
	ui.contextNote.Wrapping = fyne.TextWrapWord
	ui.contextDetails = container.NewVBox()
	ui.contextScroll = container.NewVScroll(ui.contextDetails)
	ui.rail = inset(0, 28, 24, 28, container.New(&railLayout{},
		text(strings.ToUpper(tr("context.eyebrow", nil)), sizeMeta, theme.ColorNameDisabled,
			fyne.TextStyle{Monospace: true, Bold: true}),
		ui.contextTitle,
		ui.contextNote,
		ui.contextScroll,
	))

	drawerScrim := newTapBox(newPaint(func(r *canvas.Rectangle, _ *palette) { r.FillColor = scrimColor() }), ui.closeDrawer)
	ui.columns = container.New(&columnsLayout{ui: ui}, ui.main, ui.rail, drawerScrim, ui.sidebarPanel)

	ui.overlayBody = container.NewVBox()
	cardBackground := newPaint(func(r *canvas.Rectangle, p *palette) {
		r.FillColor = p.background
		r.StrokeColor = p.line
		r.StrokeWidth = 1
		r.CornerRadius = 16
	})
	cardContent := inset(22, 22, 22, 22, ui.overlayBody)
	ui.overlayScroll = container.NewVScroll(cardContent)
	card := container.NewStack(cardBackground, ui.overlayScroll)
	scrim := newTapBox(newPaint(func(r *canvas.Rectangle, _ *palette) { r.FillColor = scrimColor() }), func() {})
	ui.overlay = container.NewStack(scrim, container.New(&dialogLayout{max: 460, maxHeight: 720, measure: cardContent}, card))
	ui.overlay.Hide()

	ui.win.SetContent(ui.columns)
	escapeHandler = ui.escape
	ui.win.Canvas().SetOnTypedKey(func(key *fyne.KeyEvent) {
		if key.Name == fyne.KeyEscape {
			ui.escape()
		}
	})
	ui.shortcuts = map[fyne.KeyName]func(){
		fyne.KeyComma: func() { ui.openSettings(ui.profile, ui.profileName()) },
		fyne.KeyL:     ui.focusComposer,
		fyne.KeyQ:     ui.win.Close,
		fyne.KeyW:     ui.win.Close,
	}
	for key := range ui.shortcuts {
		shortcut := &desktop.CustomShortcut{KeyName: key, Modifier: fyne.KeyModifierShortcutDefault}
		ui.win.Canvas().AddShortcut(shortcut, ui.typedShortcut)
	}
	windowShortcut = ui.shortcut
	ui.app.Settings().AddListener(func(fyne.Settings) {
		fyne.Do(ui.restyle)
	})
}

// typedShortcut is what the canvas runs for a shortcut of the window.
func (ui *fyneUI) typedShortcut(s fyne.Shortcut) { ui.shortcut(s) }

// shortcut runs a shortcut of the window: Ctrl+, the settings, Ctrl+L the
// composer, Ctrl+Q and Ctrl+W close the window.
func (ui *fyneUI) shortcut(s fyne.Shortcut) bool {
	custom, ok := s.(*desktop.CustomShortcut)
	if !ok || custom.Modifier != fyne.KeyModifierShortcutDefault {
		return false
	}
	run, ok := ui.shortcuts[custom.KeyName]
	if !ok {
		return false
	}
	run()
	return true
}

// restyle draws anew what takes its colours from the palette when it is
// made, once the appearance changed: the sessions and the artefacts.
func (ui *fyneUI) restyle() {
	if ui.sidebarList == nil {
		return
	}
	ui.renderList()
	if ui.inspecting == nil {
		if ui.ctx != nil && ui.ctx.id == ui.thread {
			ui.artefacts()
		} else {
			ui.ownArtefacts()
		}
	}
	ui.fade.Refresh()
}

// setNarrow turns the sidebar into a drawer behind the menu button, or back
// into its column.
func (ui *fyneUI) setNarrow(narrow bool) {
	ui.narrow = narrow
	if !narrow {
		ui.drawerOpen = false
	}
	if narrow {
		ui.menu.Show()
	} else {
		ui.menu.Hide()
	}
	for _, p := range ui.sidebarPaints {
		p.Refresh()
	}
}

// toggleDrawer opens or closes the sessions of a narrow window.
func (ui *fyneUI) toggleDrawer() {
	if ui.drawerOpen {
		ui.closeDrawer()
		return
	}
	ui.openDrawer()
}

func (ui *fyneUI) openDrawer() {
	if !ui.narrow || ui.drawerOpen {
		return
	}
	ui.drawerOpen = true
	ui.columns.Refresh()
	width := ui.sidebarPanel.Size().Width
	ui.sidebarPanel.Move(fyne.NewPos(-width, 0))
	slide := canvas.NewPositionAnimation(fyne.NewPos(-width, 0), fyne.NewPos(0, 0), 160*time.Millisecond, func(p fyne.Position) {
		if ui.drawerOpen {
			ui.sidebarPanel.Move(p)
		}
	})
	slide.Curve = fyne.AnimationEaseOut
	slide.Start()
}

func (ui *fyneUI) closeDrawer() {
	if !ui.drawerOpen {
		return
	}
	ui.drawerOpen = false
	ui.columns.Refresh()
}

// focusComposer puts the keyboard into the composer, as the window does
// when it opens a conversation.
func (ui *fyneUI) focusComposer() {
	if ui.overlay.Visible() || ui.draft.Disabled() {
		return
	}
	ui.win.Canvas().Focus(ui.draft)
}

// start reads the profiles and opens the thread or the session the process
// before handed over, as frontend/app.js does on document readiness.
func (ui *fyneUI) start() {
	state := ui.bridge.InitialState()
	fyne.Do(func() {
		ui.thread = state.Thread
		ui.profile = state.Selected
		ui.resume = state.Resume
		ui.profiles = state.Profiles
		ui.load(false)
	})
	go ui.refreshList(listEvery)
}

// listEvery is how often the sidebar is read anew.
const listEvery = 3 * time.Second

// refreshList keeps the sidebar up to date, as the window refreshes it every
// three seconds without touching the transcript or the watch.
func (ui *fyneUI) refreshList(every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ui.run.Done():
			return
		case <-ticker.C:
		}
		fyne.DoAndWait(ui.relist)
	}
}

// relist reads the session list once more, unless a reading is on its way
// or the window is loading, and shows it where it changed.
func (ui *fyneUI) relist() {
	if !ui.ready.Load() || ui.listing {
		return
	}
	ui.listing = true
	profile := ui.profile
	epoch := ui.epoch.Load()
	go func() {
		result, err := ui.bridge.Sessions(profile)
		fyne.Do(func() {
			ui.listing = false
			if err != nil || ui.epoch.Load() != epoch || profile != ui.profile {
				return
			}
			entries := ui.ownEntries(result)
			if sameEntries(entries, ui.entries) {
				return
			}
			ui.entries = entries
			ui.renderList()
			ui.ownArtefacts()
			if ui.section != nil {
				ui.artefacts()
			}
		})
	}()
}

// ownEntries are the sessions of the sidebar, the newest first: all but the
// thread itself.
func (ui *fyneUI) ownEntries(result []SessionEntry) []SessionEntry {
	entries := make([]SessionEntry, 0, len(result))
	for _, entry := range result {
		if entry.ID != ui.thread {
			entries = append(entries, entry)
		}
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Modified.After(entries[j].Modified) })
	return entries
}

// sameEntries tells whether a new session list is the one already shown.
func sameEntries(a, b []SessionEntry) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID || a[i].Title != b[i].Title || a[i].Running != b[i].Running || !a[i].Modified.Equal(b[i].Modified) {
			return false
		}
	}
	return true
}

func (ui *fyneUI) profileName() string {
	if ui.profile >= 0 && ui.profile < len(ui.profiles) {
		return ui.profiles[ui.profile].Name
	}
	return ""
}

func (ui *fyneUI) owner(id string) bool { return strings.HasPrefix(id, "owner/") }

func (ui *fyneUI) sessionState(profile int, id string) *fyneSessionState {
	key := fmt.Sprintf("%d:%s", profile, id)
	state, ok := ui.drafts[key]
	if !ok {
		state = &fyneSessionState{}
		ui.drafts[key] = state
	}
	return state
}

func (ui *fyneUI) threadEntry() SessionEntry {
	return SessionEntry{ID: ui.thread, Title: "Thread"}
}

// load reads the session list, as the window does when it opens and when the
// profile changes.
func (ui *fyneUI) load(reset bool) {
	epoch := ui.epoch.Add(1)
	ui.ready.Store(false)
	ui.ctx = nil
	ui.setQuestion(nil)
	if ui.draft != nil {
		ui.draft.SetText("")
	}
	ui.updateComposer()
	previous := ui.selected
	if reset {
		previous = ""
	} else if previous == "" {
		previous = ui.resume
	}
	ui.selected = ""
	ui.entries = nil
	if reset || ui.inspecting == nil {
		ui.inspecting = nil
		setContextTitle(ui, tr("context.title", nil))
		ui.contextNote.SetText("")
		setChildren(ui.contextDetails)
	}
	setChildren(ui.sidebarList)
	setChildren(ui.content)
	ui.visible = nil
	ui.errorBox.Hide()
	setTitleText(ui.title, strings.ToUpper(tr("sessions.title", nil)))
	ui.meta.SetText(tr("sessions.subtitle", nil))
	ui.setStatus(tr("sessions.loading", nil))
	profile := ui.profile
	go func() {
		result, err := ui.bridge.Sessions(profile)
		fyne.Do(func() {
			if ui.epoch.Load() != epoch {
				return
			}
			if err != nil && ui.thread == "" {
				ui.setStatus("")
				ui.fail(tr("sessions.loadFailed", nil), err)
				return
			}
			entries := ui.ownEntries(result)
			ui.entries = entries
			ui.renderList()
			if len(entries) > 0 {
				ui.setStatus(tr("sessions.choose", nil))
			} else {
				ui.setStatus(tr("sessions.none", nil))
			}
			var target SessionEntry
			switch {
			case ui.thread != "" && previous == ui.thread:
				target = ui.threadEntry()
			default:
				for _, entry := range entries {
					if entry.ID == previous {
						target = entry
					}
				}
				if target.ID == "" && ui.thread != "" {
					target = ui.threadEntry()
				} else if target.ID == "" && len(entries) > 0 {
					target = entries[0]
				}
			}
			if target.ID != "" {
				ui.read(target)
			}
			if err != nil {
				ui.fail(tr("sessions.loadFailed", nil), err)
			}
		})
	}()
}

// read shows one session: its events are replayed and then followed live.
func (ui *fyneUI) read(entry SessionEntry) {
	epoch := ui.epoch.Add(1)
	profile := ui.profile
	if ui.selected != entry.ID {
		ui.selected = entry.ID
		ui.renderList()
	}
	ui.ready.Store(false)
	ui.activity = fyneActivity{}
	ui.setQuestion(nil)
	ui.ctx = &fyneContext{profile: profile, id: entry.ID, state: ui.sessionState(profile, entry.ID)}
	ui.own = nil
	ui.heard = false
	ui.draft.SetText(ui.ctx.state.text)
	ui.updateComposer()
	setChildren(ui.content)
	ui.visible = nil
	ui.steps = make(map[string]*fyneTrace)
	if entry.ID == ui.thread {
		ui.section = nil
		ui.latest = nil
	}
	if entry.ID != ui.thread || ui.inspecting == nil || ui.inspecting.profile != profile {
		ui.inspecting = nil
		label := "[Session]"
		if entry.ID == ui.thread {
			label = "[Thread]"
		}
		setContextTitle(ui, label)
		ui.contextNote.SetText(tr("context.hint", nil))
		setChildren(ui.contextDetails)
	}
	ui.errorBox.Hide()
	title := entry.Title
	if title == "" {
		title = entry.ID
	}
	setTitleText(ui.title, strings.ToUpper(title))
	ui.win.SetTitle("Ouroboros · " + title)
	ui.shownTitle = title
	// What the session is beside its title: the profile of the thread, or
	// when a session was last touched; its workspace follows as an event.
	meta := formatDate(entry.Modified)
	switch {
	case entry.ID == ui.thread:
		meta = ui.profileName()
	case meta == "":
		meta = entry.ID
	}
	ui.meta.SetText(meta)
	ui.setStatus(tr("session.loading", nil))
	ui.chat = newFyneChat(ui, profile, entry.ID, entry.ID == ui.thread)
	id := entry.ID
	go func() {
		snapshot, err := ui.bridge.OpenSession(profile, id)
		if err != nil {
			fyne.Do(func() {
				if ui.epoch.Load() != epoch {
					return
				}
				ui.setStatus("")
				ui.fail(tr("session.loadFailed", nil), err)
			})
			return
		}
		// Replay in chunks and yield between them, so that switching
		// sessions stays responsive, as the front end does every 40
		// events. A chunk for a session the owner left ends the replay.
		events := snapshot.Events
		for i := 0; ; i += 40 {
			chunk := events[min(i, len(events)):min(i+40, len(events))]
			last := i+40 >= len(events)
			var stale bool
			fyne.DoAndWait(func() {
				if stale = ui.epoch.Load() != epoch; stale {
					return
				}
				if i == 0 {
					ui.ctx.state.notice = snapshot.Notice
				}
				ui.applyEvents(chunk)
				if last {
					ui.ready.Store(true)
					ui.updateComposer()
					ui.stick()
					ui.focusComposer()
					go ui.follow(epoch, profile, id, snapshot.Token)
				}
			})
			if stale || last {
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
}

// follow reads the live stream of the shown session, one poll at a time, as
// the front end does every 150 ms.
func (ui *fyneUI) follow(epoch int64, profile int, id string, token int) {
	for ui.epoch.Load() == epoch && token != 0 && ui.owner(id) && ui.run.Err() == nil {
		asked := time.Now()
		events := ui.bridge.PollSession(token)
		if events == nil {
			fyne.Do(func() {
				if ui.epoch.Load() != epoch {
					return
				}
				ui.ready.Store(false)
				ui.setQuestion(nil)
				ui.setStatus(tr("session.disconnected", nil))
				ui.fail(tr("session.disconnectedHint", nil), errors.New(tr("session.ended", nil)))
			})
			return
		}
		if len(events) == 0 {
			// A poll waits for events; one that came back at once has no
			// stream to wait on, and is not asked again at full speed.
			if time.Since(asked) < 20*time.Millisecond {
				time.Sleep(100 * time.Millisecond)
			}
			continue
		}
		fyne.Do(func() {
			if ui.epoch.Load() != epoch {
				return
			}
			ui.applyEvents(events)
			if ui.following {
				ui.stick()
			}
		})
	}
}

// scrolled follows the history while the owner is at its end, and shows the
// part of the thread that crosses the middle otherwise.
func (ui *fyneUI) scrolled() {
	content := ui.transcript.Content
	offset := ui.transcript.Offset.Y
	height := ui.transcript.Size().Height
	ui.following = offset >= content.Size().Height-height-48
	ui.updateFade()
	ui.pick()
}

// anchorLayout lays the history out and, while the owner follows it, keeps
// its end in view: the desktop driver lays the history out anew whenever
// something in it grows, after the window scrolled to its end.
type anchorLayout struct {
	ui    *fyneUI
	inner fyne.Layout
}

func (l *anchorLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	return l.inner.MinSize(objects)
}

func (l *anchorLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	l.inner.Layout(objects, size)
	ui := l.ui
	if ui.following && ui.transcript != nil {
		// The scroller places its content at this offset right after.
		ui.transcript.Offset.Y = fyne.Max(0, size.Height-ui.transcript.Size().Height)
		ui.updateFade()
	}
}

// followLayout gives the scroller of the history its room and keeps the end
// in view while the owner follows it, as the room changes with the composer.
type followLayout struct{ ui *fyneUI }

func (l *followLayout) MinSize(objects []fyne.CanvasObject) fyne.Size {
	return objects[0].MinSize()
}

func (l *followLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	objects[0].Resize(size)
	ui := l.ui
	if ui.following {
		content := ui.transcript.Content.Size().Height
		ui.transcript.ScrollToOffset(fyne.NewPos(0, fyne.Max(0, content-size.Height)))
		ui.updateFade()
	}
}

// updateFade fades the top of the history only where there is history
// above it to fade.
func (ui *fyneUI) updateFade() {
	if ui.transcript.Offset.Y <= 1 {
		ui.fade.Hide()
	} else if !ui.fade.Visible() {
		ui.fade.Show()
	}
}

// stick keeps the end of the history in view, and with it the part of the
// thread at its end.
func (ui *fyneUI) stick() {
	ui.following = true
	// The scroller clamps to the size its content had at the last frame;
	// give the content the size of what it holds now, or a message that
	// just came would stay below the end.
	content := ui.transcript.Content
	height := fyne.Max(content.MinSize().Height, ui.transcript.Size().Height)
	if content.Size().Height != height {
		content.Resize(fyne.NewSize(ui.transcript.Size().Width, height))
	}
	ui.transcript.ScrollToBottom()
	ui.updateFade()
	ui.pick()
}

// pick shows the part of the thread in view: the newest at the end, else the
// one whose messages cross the middle of the history.
func (ui *fyneUI) pick() {
	if ui.ctx == nil || ui.ctx.id != ui.thread {
		return
	}
	if ui.following {
		ui.show(ui.latest)
		return
	}
	middle := ui.transcript.Offset.Y + ui.transcript.Size().Height/2
	for _, message := range ui.visible {
		top := message.item.Position().Y
		bottom := top + message.item.Size().Height
		if middle >= top && middle <= bottom {
			ui.show(message.exchange)
			return
		}
	}
}

// show puts the sessions of the part of the thread in view in the sidebar and
// their artefacts in the rail.
func (ui *fyneUI) show(part *fyneExchange) {
	if part == nil || part == ui.section {
		return
	}
	if ui.section != nil {
		ui.inspecting = nil
	}
	ui.section = part
	ui.renderList()
	ui.artefacts()
}

// setStatus writes the line above the composer, which the stylesheet sets in
// capitals.
func (ui *fyneUI) setStatus(text string) {
	ui.statusLabel.SetText(strings.ToUpper(text))
}

func (ui *fyneUI) updateStatus() {
	if !ui.ready.Load() {
		return
	}
	central := ui.ctx != nil && ui.ctx.id == ui.thread
	active := ui.activity.n
	if ui.activity.own {
		active--
	}
	if active < 0 {
		active = 0
	}
	if ui.ctx == nil || !ui.owner(ui.ctx.id) {
		ui.setStatus(tr("status.peer", nil))
		return
	}
	word := tr("status.ready", nil)
	if ui.asking != nil {
		word = tr("status.needsAnswer", nil)
	} else if ui.activity.busy {
		word = tr("status.working", nil)
	}
	which := "Session"
	if central {
		which = "Thread"
	}
	line := which + " · " + word
	if central {
		key := "status.active"
		if active == 1 {
			key = "status.oneActive"
		}
		line += tr(key, map[string]any{"n": active})
	}
	ui.setStatus(line)
}

// writable reports whether the composer may send into the shown session.
func (ui *fyneUI) writable() bool {
	return ui.ready.Load() && ui.ctx != nil && ui.owner(ui.ctx.id) && !ui.ctx.state.pending
}

// updateSend is the only composer state that depends on each keystroke.
func (ui *fyneUI) updateSend() {
	if !ui.writable() || strings.TrimSpace(ui.draft.Text) == "" {
		ui.send.Disable()
	} else {
		ui.send.Enable()
	}
}

func (ui *fyneUI) updateComposer() {
	if ui.asking != nil {
		sent := ui.ctx != nil && ui.ctx.state.sentQuestion == ui.asking.key
		for _, button := range ui.asking.buttons {
			if !ui.writable() || sent {
				button.Disable()
			} else {
				button.Enable()
			}
		}
		if sent {
			ui.questionHint.SetText(tr("question.sent", nil))
		} else {
			ui.questionHint.SetText(tr("question.hint", nil))
		}
	}
	owner := ui.ctx != nil && ui.owner(ui.ctx.id)
	// A loading or disconnected stream prevents sending, not drafting.
	if !owner {
		ui.draft.Disable()
	} else {
		ui.draft.Enable()
	}
	ui.updateSend()
	if !ui.ready.Load() || !owner || !ui.activity.busy || ui.activity.stopping {
		ui.stop.Hide()
	} else {
		ui.stop.Show()
	}
	if ui.ctx != nil {
		setRich(ui.sendError, ui.ctx.state.error, sizeBody, theme.ColorNameError)
		if ui.ctx.state.error == "" {
			ui.sendError.Hide()
		} else {
			ui.sendError.Show()
		}
		notice := ui.ctx.state.notice
		if ui.ctx.state.pending {
			notice = tr("composer.sending", nil)
		}
		ui.sendStatus.SetText(notice)
	} else {
		ui.sendError.Hide()
		ui.sendStatus.SetText("")
	}
	if ui.sendStatus.Text == "" {
		ui.sendStatus.Hide()
	} else {
		ui.sendStatus.Show()
	}
	ui.updateStatus()
	if ui.emptyHint != nil {
		if ui.ready.Load() && ui.ctx != nil && ui.owner(ui.ctx.id) && len(ui.visible) == 0 {
			ui.emptyHint.Show()
		} else {
			ui.emptyHint.Hide()
		}
	}
}

func (ui *fyneUI) setQuestion(question *fyneQuestion) {
	ui.asking = question
	if question == nil {
		ui.questionBox.Hide()
		ui.questionOpt.SetObjects()
		return
	}
	ui.questionTxt.Segments = []widget.RichTextSegment{&widget.TextSegment{Text: question.text,
		Style: widget.RichTextStyle{SizeName: sizeBody, ColorName: theme.ColorNameForeground, TextStyle: fyne.TextStyle{Monospace: true}}}}
	ui.questionTxt.Refresh()
	question.buttons = question.buttons[:0]
	options := make([]fyne.CanvasObject, 0, len(question.options))
	current := question
	for _, option := range question.options {
		label := option
		recommended := option == question.recommended
		if recommended {
			label += tr("question.recommended", nil)
		}
		var button *flatButton
		button = newOptionButton(label, recommended, func() {
			if ui.asking == current && !button.Disabled() {
				ui.submit(option, true)
			}
		})
		current.buttons = append(current.buttons, button)
		options = append(options, button)
	}
	ui.questionOpt.SetObjects(options...)
	ui.questionBox.Show()
	ui.composerChanged()
}

func (ui *fyneUI) submit(value string, fromOption bool) {
	if !ui.writable() || strings.TrimSpace(value) == "" {
		return
	}
	ui.stick()
	target := ui.ctx
	question := ui.asking
	state := target.state
	original := state.text
	state.pending = true
	state.error = ""
	state.notice = ""
	ui.updateComposer()
	go func() {
		err := ui.bridge.SendMessage(target.profile, target.id, value)
		fyne.Do(func() {
			if err != nil {
				state.error = tr("composer.sendFailed", map[string]any{"error": err})
			} else {
				if question != nil {
					state.sentQuestion = question.key
				}
				if !fromOption && state.text == original {
					state.text = ""
				}
				state.notice = tr("composer.sent", nil)
				// The notice goes again after a while, as a native toast does.
				notice := state.notice
				time.AfterFunc(ui.noticeTime, func() {
					if ui.run.Err() != nil {
						return // the window is gone
					}
					fyne.Do(func() {
						if state.notice != notice || state.pending {
							return
						}
						state.notice = ""
						if ui.ctx != nil && ui.ctx.state == state {
							ui.updateComposer()
						}
					})
				})
			}
			state.pending = false
			if ui.ctx != nil && ui.ctx.state == state {
				ui.draft.SetText(state.text)
				ui.updateComposer()
			}
		})
	}()
}

func (ui *fyneUI) stopAnswer() {
	if !ui.ready.Load() || ui.ctx == nil || !ui.owner(ui.ctx.id) || !ui.activity.busy || ui.activity.stopping {
		return
	}
	target := ui.ctx
	ui.activity.stopping = true
	target.state.error = ""
	ui.updateComposer()
	go func() {
		err := ui.bridge.StopSession(target.profile, target.id)
		fyne.Do(func() {
			if err != nil {
				target.state.error = tr("composer.stopFailed", map[string]any{"error": err})
			}
			ui.activity.stopping = false
			if ui.ctx == target {
				ui.updateComposer()
			}
		})
	}()
}

// fail shows the error above the transcript, as the window does.
func (ui *fyneUI) fail(message string, cause error) {
	ui.errorInk.Segments = []widget.RichTextSegment{&widget.TextSegment{
		Text:  message + ": " + cause.Error(),
		Style: widget.RichTextStyle{SizeName: sizeBody, ColorName: theme.ColorNameError, TextStyle: fyne.TextStyle{Monospace: true}},
	}}
	ui.errorInk.Refresh()
	ui.errorBox.Show()
}

// escape leaves the dialog, or closes the drawer of the sessions.
func (ui *fyneUI) escape() {
	switch {
	case ui.overlay.Visible():
		if ui.onEscape != nil && ui.onEscape() {
			return
		}
		ui.closeOverlay()
	case ui.drawerOpen:
		ui.closeDrawer()
	}
}

// setRich replaces the text of a rich text line.
func setRich(line *widget.RichText, value string, size fyne.ThemeSizeName, colorName fyne.ThemeColorName) {
	line.Segments = []widget.RichTextSegment{&widget.TextSegment{
		Text: value, Style: widget.RichTextStyle{SizeName: size, ColorName: colorName, TextStyle: fyne.TextStyle{Monospace: true}},
	}}
	line.Refresh()
}

// setTitleText sets the h1 of the window.
func setTitleText(line *widget.RichText, value string) {
	setRich(line, value, sizeTitle, theme.ColorNameForeground)
}

// setChildren replaces what a box holds, as replaceChildren does in the
// window.
func setChildren(box *fyne.Container, objects ...fyne.CanvasObject) {
	box.Objects = objects
	box.Refresh()
}
