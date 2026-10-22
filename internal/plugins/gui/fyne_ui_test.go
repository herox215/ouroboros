//go:build cgo

package gui

import (
	"context"
	"errors"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"

	"ouroboros/plugin"
)

// A part of the thread names each session once.
func TestFyneExchangeHanded(t *testing.T) {
	var part fyneExchange
	if part.handed("") || !part.handed("s1") || part.handed("s1") || !part.handed("s2") {
		t.Fatalf("sessions %v", part.sessions)
	}
}

// The shortcuts of the window, and Escape where nothing has the focus.
func TestFyneShortcutsAndEscape(t *testing.T) {
	ui, _ := e2eWindow(t, fyne.NewSize(500, 700))
	shortcut := func(key fyne.KeyName, modifier fyne.KeyModifier) bool {
		var ran bool
		fyne.DoAndWait(func() {
			ran = ui.shortcut(&desktop.CustomShortcut{KeyName: key, Modifier: modifier})
		})
		return ran
	}
	if shortcut(fyne.KeyComma, fyne.KeyModifierAlt) || shortcut(fyne.KeyX, fyne.KeyModifierShortcutDefault) {
		t.Fatal("a shortcut of nobody ran")
	}
	fyne.DoAndWait(func() {
		if ui.shortcut(&fyne.ShortcutCopy{}) {
			t.Error("copy is no shortcut of the window")
		}
		ui.win.Canvas().Unfocus()
	})
	if !shortcut(fyne.KeyL, fyne.KeyModifierShortcutDefault) {
		t.Fatal("Ctrl+L did not run")
	}
	waitFor(t, func() bool { return ui.win.Canvas().Focused() == ui.draft })

	// Escape closes the drawer, the menu toggles it.
	fyne.DoAndWait(ui.toggleDrawer)
	waitFor(t, func() bool { return ui.drawerOpen })
	fyne.DoAndWait(func() {
		ui.win.Canvas().Unfocus()
		ui.win.Canvas().OnTypedKey()(&fyne.KeyEvent{Name: fyne.KeyEscape})
		ui.win.Canvas().OnTypedKey()(&fyne.KeyEvent{Name: fyne.KeyA})
	})
	waitFor(t, func() bool { return !ui.drawerOpen })
	fyne.DoAndWait(func() {
		ui.toggleDrawer()
		ui.toggleDrawer()
		ui.closeDrawer()
		ui.escape()
	})
	if shortcut(fyne.KeyW, fyne.KeyModifierShortcutDefault) != true {
		t.Fatal("Ctrl+W did not run")
	}
}

// A wide window has no drawer to open.
func TestFyneDrawerOnlyWhenNarrow(t *testing.T) {
	ui, _ := e2eWindow(t, fyne.NewSize(1180, 780))
	fyne.DoAndWait(func() {
		ui.openDrawer()
		if ui.drawerOpen {
			t.Error("a wide window opened the drawer")
		}
	})
}

// The appearance is redrawn wherever the window shows.
func TestFyneRestyle(t *testing.T) {
	ui, _ := e2eWindow(t, fyne.NewSize(1400, 900))
	fyne.DoAndWait(func() {
		ui.restyle()
		ui.read(SessionEntry{ID: "owner/s1", Title: "Review"})
	})
	waitFor(t, func() bool { return ui.ready.Load() })
	fyne.DoAndWait(func() {
		ui.restyle()
		ui.inspecting = &fyneInspect{}
		ui.restyle()
		var empty fyneUI
		empty.restyle()
	})
}

// The session list is read anew in the background: once at a time, and
// shown only where it changed.
func TestFyneRelist(t *testing.T) {
	ui, sessions := e2eWindow(t, fyne.NewSize(1400, 900))
	go ui.refreshList(10 * time.Millisecond)
	list := sessions.list
	added := make(chan struct{})
	sessions.swap(func() {
		sessions.list = func(ctx context.Context) ([]plugin.SessionInfo, error) {
			got, err := list(ctx)
			select {
			case <-added:
				got = append(got, plugin.SessionInfo{ID: "s3", Title: "New", Last: time.Now(), Running: true})
			default:
			}
			return got, err
		}
	})
	close(added)
	waitFor(t, func() bool { return len(ui.entries) == 4 })

	// A failing list keeps what shows.
	sessions.swap(func() {
		sessions.list = func(context.Context) ([]plugin.SessionInfo, error) { return nil, errors.New("down") }
	})
	time.Sleep(50 * time.Millisecond)
	fyne.DoAndWait(func() {
		if len(ui.entries) != 4 {
			t.Errorf("a failed list changed the sidebar: %d", len(ui.entries))
		}
		ui.listing = true
		ui.relist()
		ui.listing = false
		ui.ready.Store(false)
		ui.relist()
		ui.ready.Store(true)
	})
	// A list read for a view the window left is dropped.
	sessions.swap(func() {
		sessions.list = func(context.Context) ([]plugin.SessionInfo, error) {
			fyne.Do(func() { ui.epoch.Add(1) })
			return nil, nil
		}
	})
	fyne.DoAndWait(ui.relist)
	time.Sleep(30 * time.Millisecond)
}

func TestSameEntries(t *testing.T) {
	at := time.Now()
	a := []SessionEntry{{ID: "a", Title: "A", Modified: at}}
	for _, b := range [][]SessionEntry{
		{},
		{{ID: "b", Title: "A", Modified: at}},
		{{ID: "a", Title: "B", Modified: at}},
		{{ID: "a", Title: "A", Modified: at, Running: true}},
		{{ID: "a", Title: "A", Modified: at.Add(time.Second)}},
	} {
		if sameEntries(a, b) {
			t.Fatalf("%v is %v", a, b)
		}
	}
	if !sameEntries(a, []SessionEntry{{ID: "a", Title: "A", Modified: at}}) {
		t.Fatal("the same list differs")
	}
}

// Without a profile the window has no name for it.
func TestFyneProfileName(t *testing.T) {
	ui := &fyneUI{profile: 2}
	if ui.profileName() != "" {
		t.Fatal("a missing profile has a name")
	}
}

// What cannot be read says so above the transcript, and the window goes on
// with the thread where there is one.
func TestFyneLoadFailures(t *testing.T) {
	sessions := newE2ESessions()
	sessions.swap(func() {
		sessions.list = func(context.Context) ([]plugin.SessionInfo, error) { return nil, errors.New("no list") }
	})
	ui := e2eWindowWith(t, fyne.NewSize(1180, 780), sessions)
	waitFor(t, func() bool { return ui.errorBox.Visible() && ui.selected == ui.thread })

	// Without a thread, a failed list is all there is.
	fyne.DoAndWait(func() {
		ui.thread = ""
		ui.load(true)
	})
	waitFor(t, func() bool { return ui.errorBox.Visible() && ui.selected == "" })

	// Without a thread the newest session opens, or the one to go on with.
	sessions.swap(func() {
		sessions.list = func(context.Context) ([]plugin.SessionInfo, error) {
			return []plugin.SessionInfo{{ID: "s1", Title: "Review", Last: time.Now()}, {ID: "s2", Title: "Older", Last: time.Now().Add(-time.Hour)}}, nil
		}
	})
	fyne.DoAndWait(func() { ui.load(true) })
	waitFor(t, func() bool { return ui.selected == "owner/s1" && ui.ready.Load() })
	fyne.DoAndWait(func() {
		ui.selected = ""
		ui.resume = "owner/s2"
		ui.load(false)
	})
	waitFor(t, func() bool { return ui.selected == "owner/s2" && ui.ready.Load() })
	sessions.swap(func() {
		sessions.list = func(context.Context) ([]plugin.SessionInfo, error) { return nil, nil }
	})
	fyne.DoAndWait(func() { ui.load(true) })
	// The peer conversations are all that is left.
	waitFor(t, func() bool { return ui.selected == "peer-alice/p1" && ui.ready.Load() })

	// A transcript that cannot be read.
	sessions.swap(func() {
		sessions.watch = func(context.Context, string) ([]plugin.SessionEvent, <-chan plugin.SessionEvent, error) {
			return nil, nil, errors.New("no transcript")
		}
	})
	fyne.DoAndWait(func() { ui.read(SessionEntry{ID: "owner/s1"}) })
	waitFor(t, func() bool { return ui.errorBox.Visible() })
}

// A stream that ends says the connection is gone and stops sending.
func TestFyneStreamEnds(t *testing.T) {
	sessions := newE2ESessions()
	live := make(chan plugin.SessionEvent)
	watch := sessions.watch
	sessions.swap(func() {
		sessions.watch = func(ctx context.Context, id string) ([]plugin.SessionEvent, <-chan plugin.SessionEvent, error) {
			past, _, err := watch(ctx, id)
			return past, live, err
		}
	})
	ui := e2eWindowWith(t, fyne.NewSize(1180, 780), sessions)
	close(live)
	waitFor(t, func() bool { return !ui.ready.Load() && ui.errorBox.Visible() })
}

// Sending and stopping that fail say why, and keep the draft.
func TestFyneSendAndStopFailures(t *testing.T) {
	ui, sessions := e2eWindow(t, fyne.NewSize(1180, 780))
	sessions.swap(func() {
		sessions.ask = func(context.Context, string, string) error { return errors.New("offline") }
	})
	sessions.swap(func() {
		sessions.cancel = func(context.Context, string) error { return errors.New("not stopped") }
	})
	fyne.DoAndWait(func() {
		ui.draft.SetText("hello")
		ui.submit("hello", false)
	})
	waitFor(t, func() bool { return ui.sendError.Visible() && ui.draft.Text == "hello" })
	waitFor(t, func() bool { return ui.stop.Visible() })
	fyne.DoAndWait(ui.stopAnswer)
	waitFor(t, func() bool { return !ui.activity.stopping && ui.ctx.state.error != "" })
	fyne.DoAndWait(func() {
		ui.activity.busy = false
		ui.stopAnswer()
		if ui.activity.stopping {
			t.Error("stopped what does not work")
		}
		ui.submit("  ", false)
	})
}

// The real driver is what the window opens on outside the tests.
func TestNewFyneApp(t *testing.T) {
	if testing.Short() {
		t.Skip("creates the desktop driver")
	}
	previous := fyne.CurrentApp()
	defer fyne.SetCurrentApp(previous)
	if newFyneApp() == nil {
		t.Fatal("no app")
	}
}

// The shortcuts reach the window from its canvas too.
func TestFyneCanvasShortcut(t *testing.T) {
	ui, _ := e2eWindow(t, fyne.NewSize(1180, 780))
	fyne.DoAndWait(func() {
		ui.typedShortcut(&desktop.CustomShortcut{KeyName: fyne.KeyComma, Modifier: fyne.KeyModifierShortcutDefault})
	})
	waitFor(t, func() bool { return ui.overlay.Visible() })
	// A dialog that answers Escape itself stays.
	fyne.DoAndWait(func() {
		ui.onEscape = func() bool { return true }
		ui.escape()
		if !ui.overlay.Visible() {
			t.Error("the dialog did not keep Escape")
		}
	})
}

// What comes for a session the owner already left is dropped: a transcript
// that failed, a replay, and the end of its stream.
func TestFyneSwitchingAwayDropsLateResults(t *testing.T) {
	ui, sessions := e2eWindow(t, fyne.NewSize(1180, 780))
	release := make(chan struct{})
	entered := make(chan string, 1)
	ended := make(chan plugin.SessionEvent)
	watch := sessions.watch
	sessions.swap(func() {
		sessions.watch = func(ctx context.Context, id string) ([]plugin.SessionEvent, <-chan plugin.SessionEvent, error) {
			switch id {
			case "s7":
				return nil, ended, nil
			case "s8", "s9":
				entered <- id
				<-release
				if id == "s9" {
					return nil, nil, errors.New("gone")
				}
				return bigThread(30), make(chan plugin.SessionEvent), nil
			}
			return watch(ctx, id)
		}
	})
	toThread := func() {
		t.Helper()
		fyne.DoAndWait(func() { ui.read(ui.threadEntry()) })
		waitFor(t, func() bool { return ui.ready.Load() && ui.selected == ui.thread })
	}

	// The thread opened before the failed transcript came.
	fyne.DoAndWait(func() { ui.read(SessionEntry{ID: "owner/s9"}) })
	<-entered
	toThread()
	release <- struct{}{}

	// The owner left while the replay was on its way.
	fyne.DoAndWait(func() { ui.read(SessionEntry{ID: "owner/s8"}) })
	<-entered
	fyne.DoAndWait(func() {
		release <- struct{}{}
		time.Sleep(100 * time.Millisecond) // the replay waits for the window
		ui.epoch.Add(1)
	})
	time.Sleep(50 * time.Millisecond)
	fyne.DoAndWait(func() {
		if len(ui.visible) != 0 {
			t.Errorf("a left session replayed %d messages", len(ui.visible))
		}
	})
	toThread()

	// The stream of a left session ends.
	fyne.DoAndWait(func() { ui.read(SessionEntry{ID: "owner/s7"}) })
	waitFor(t, func() bool { return ui.ready.Load() && ui.selected == "owner/s7" })
	fyne.DoAndWait(func() {
		close(ended)
		time.Sleep(300 * time.Millisecond) // the poll sees the end meanwhile
		ui.epoch.Add(1)
	})
	time.Sleep(50 * time.Millisecond)
	fyne.DoAndWait(func() {
		if ui.errorBox.Visible() {
			t.Error("a left session reported its end")
		}
	})
	toThread()
}

// The status counts the sessions the thread works through, not itself.
func TestFyneStatusCountsSessions(t *testing.T) {
	ui, _ := e2eWindow(t, fyne.NewSize(1180, 780))
	fyne.DoAndWait(func() {
		ui.setQuestion(nil)
		ui.activity = fyneActivity{n: 0, own: true, busy: true}
		ui.updateStatus()
		if ui.statusLabel.Text == "" {
			t.Error("no status")
		}
		ui.activity = fyneActivity{n: 2, own: true, busy: true}
		ui.updateStatus()
	})
}

// The part of the thread in view follows the scrolling: the middle of the
// history picks it.
func TestFynePickFollowsScrolling(t *testing.T) {
	ui, sessions := e2eWindow(t, fyne.NewSize(900, 640))
	for i := range 20 {
		sessions.live <- plugin.SessionEvent{Kind: plugin.SessionSay, Text: "Line " + itoa(i), At: time.Now()}
	}
	waitFor(t, func() bool { return len(ui.visible) >= 26 })
	settle(ui.win)
	var latest *fyneExchange
	fyne.DoAndWait(func() {
		latest = ui.latest
		ui.transcript.ScrollToTop()
		ui.scrolled()
	})
	waitFor(t, func() bool { return ui.section != latest && ui.section != nil })
	fyne.DoAndWait(func() {
		ui.inspecting = &fyneInspect{}
		ui.show(latest)
		if ui.inspecting != nil {
			t.Error("another part of the thread keeps the tool result")
		}
	})
}

// The last uncommon turns of the window: an unchanged list, a load that a
// newer one replaced, a reload on the thread, events for a session left
// while they were on their way, and a notice replaced before it went.
func TestFyneLateAndUnchanged(t *testing.T) {
	ui, sessions := e2eWindow(t, fyne.NewSize(1180, 780))
	fyne.DoAndWait(func() { ui.noticeTime = 100 * time.Millisecond })
	fyne.DoAndWait(ui.relist)
	waitFor(t, func() bool { return !ui.listing })

	fyne.DoAndWait(func() { ui.load(false) })
	waitFor(t, func() bool { return ui.ready.Load() && ui.selected == ui.thread })
	fyne.DoAndWait(func() {
		ui.load(false)
		ui.load(false)
	})
	waitFor(t, func() bool { return ui.ready.Load() && ui.selected == ui.thread })

	fyne.DoAndWait(func() {
		sessions.live <- plugin.SessionEvent{Kind: plugin.SessionSay, Text: "late", At: time.Now()}
		time.Sleep(300 * time.Millisecond) // the poll takes it meanwhile
		ui.read(SessionEntry{ID: "owner/s1"})
	})
	waitFor(t, func() bool { return ui.ready.Load() && ui.selected == "owner/s1" })

	fyne.DoAndWait(func() { ui.submit("hello", false) })
	waitFor(t, func() bool { return ui.ctx.state.notice == tr("composer.sent", nil) })
	fyne.DoAndWait(func() { ui.ctx.state.notice = "something else" })
	time.Sleep(250 * time.Millisecond)
	fyne.DoAndWait(func() {
		if ui.ctx.state.notice != "something else" {
			t.Errorf("a newer notice went with the old one: %q", ui.ctx.state.notice)
		}
		if (&followLayout{ui: ui}).MinSize([]fyne.CanvasObject{ui.transcript}) != ui.transcript.MinSize() {
			t.Error("the history asks for other room than its scroller")
		}
	})
}
