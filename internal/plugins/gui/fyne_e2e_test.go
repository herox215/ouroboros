//go:build cgo

package gui

import (
	"context"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"

	"ouroboros/internal/chat"
	"ouroboros/plugin"
)

// e2eSessions is a session service with a thread and two sessions it handed
// work to, enough to draw every part of the window.
type e2eSessions struct {
	testSessions
	mu       sync.Mutex
	asked    []string
	canceled []string
	live     chan plugin.SessionEvent
	fn       sync.RWMutex // guards the functions of testSessions, see swap
}

// swap changes the functions of the service while the window calls them.
func (s *e2eSessions) swap(change func()) {
	s.fn.Lock()
	defer s.fn.Unlock()
	change()
}

func (s *e2eSessions) List(ctx context.Context) ([]plugin.SessionInfo, error) {
	s.fn.RLock()
	list := s.list
	s.fn.RUnlock()
	return list(ctx)
}

func (s *e2eSessions) Watch(ctx context.Context, id string) ([]plugin.SessionEvent, <-chan plugin.SessionEvent, error) {
	s.fn.RLock()
	watch := s.watch
	s.fn.RUnlock()
	return watch(ctx, id)
}

func (s *e2eSessions) Unsteer(ctx context.Context, id string) (string, error) {
	s.fn.RLock()
	unsteer := s.unsteer
	s.fn.RUnlock()
	return unsteer(ctx, id)
}

func (s *e2eSessions) Ask(ctx context.Context, id, text string) error {
	s.fn.RLock()
	ask := s.ask
	s.fn.RUnlock()
	return ask(ctx, id, text)
}

func (s *e2eSessions) Cancel(ctx context.Context, id string) error {
	s.fn.RLock()
	cancel := s.cancel
	s.fn.RUnlock()
	return cancel(ctx, id)
}

// sent is what the composer sent so far.
func (s *e2eSessions) sent() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.asked...)
}

func (s *e2eSessions) stopped() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.canceled...)
}

func newE2ESessions() *e2eSessions {
	s := &e2eSessions{live: make(chan plugin.SessionEvent, 64)}
	now := time.Now()
	step := func(tool, call string, done bool, at time.Time) plugin.SessionEvent {
		args, _ := json.Marshal(map[string]string{"path": "internal/plugins/gui/fyne.go"})
		return plugin.SessionEvent{Kind: plugin.SessionStep, Tool: tool, CallID: call, Args: args, Done: done, At: at}
	}
	question, _ := json.Marshal(map[string]any{
		"question":    "Which layout should the settings use?",
		"options":     []string{"Tabs", "One page"},
		"recommended": "Tabs",
	})
	thread := []plugin.SessionEvent{
		{Kind: plugin.SessionMessage, Text: "Polish the window and tell me when it is done.", At: now.Add(-20 * time.Minute)},
		{Kind: plugin.SessionHanded, Text: "s1", At: now.Add(-19 * time.Minute)},
		{Kind: plugin.SessionTask, Text: "Review the layout of the sidebar", At: now.Add(-19 * time.Minute)},
		step("read", "c1", false, now.Add(-18*time.Minute)),
		step("read", "c1", true, now.Add(-18*time.Minute)),
		{Kind: plugin.SessionAnswer, Text: "The sidebar is reviewed. It lists the sessions of the part of the thread in view, and the context rail shows where each stands.", At: now.Add(-17 * time.Minute)},
		{Kind: plugin.SessionMessage, Text: "Now the settings.", At: now.Add(-5 * time.Minute)},
		{Kind: plugin.SessionHanded, Text: "s2", At: now.Add(-5 * time.Minute)},
		{Kind: plugin.SessionAsked, Args: question, At: now.Add(-4 * time.Minute)},
		{Kind: plugin.SessionWorking, N: 1, At: now.Add(-4 * time.Minute)},
	}
	session := []plugin.SessionEvent{
		{Kind: plugin.SessionMessage, Text: "Review the layout of the sidebar", At: now.Add(-19 * time.Minute)},
		step("read", "c1", false, now.Add(-18*time.Minute)),
		step("read", "c1", true, now.Add(-18*time.Minute)),
		step("bash", "c2", false, now.Add(-17*time.Minute)),
		step("bash", "c2", true, now.Add(-17*time.Minute)),
		{Kind: plugin.SessionAnswer, Text: "Reviewed.", At: now.Add(-16 * time.Minute)},
	}
	s.list = func(context.Context) ([]plugin.SessionInfo, error) {
		return []plugin.SessionInfo{
			{ID: plugin.ThreadID, Title: "Thread", Last: now},
			{ID: "s1", Title: "Review the sidebar", Last: now.Add(-16 * time.Minute)},
			{ID: "s2", Title: "Settings layout", Last: now.Add(-4 * time.Minute), Running: true},
		}, nil
	}
	s.watch = func(_ context.Context, id string) ([]plugin.SessionEvent, <-chan plugin.SessionEvent, error) {
		if id == plugin.ThreadID {
			return thread, s.live, nil
		}
		return session, make(chan plugin.SessionEvent), nil
	}
	s.details = func(context.Context, string, string) (plugin.SessionToolResult, error) {
		return plugin.SessionToolResult{Found: true, Arguments: `{"path":"fyne.go"}`, Content: "package gui\n\nfunc main() {}"}, nil
	}
	s.peers = []plugin.SessionInfo{{ID: "peer-alice/p1", Title: "Alice asks", Last: now.Add(-3 * time.Hour)}}
	s.read = func(context.Context, string) ([]plugin.SessionEvent, error) {
		return []plugin.SessionEvent{{Kind: plugin.SessionMessage, Text: "Hello from Alice", At: now.Add(-3 * time.Hour)}}, nil
	}
	s.cancel = func(_ context.Context, id string) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.canceled = append(s.canceled, id)
		return nil
	}
	s.ask = func(_ context.Context, id, text string) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.asked = append(s.asked, id+": "+text)
		return nil
	}
	// An answer to a question shows as such among what was sent.
	s.steer = func(_ context.Context, id, text string) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.asked = append(s.asked, "answer "+id+": "+text)
		return nil
	}
	return s
}

// e2eWindow opens the whole window on the test driver, as showWindow
// opens it on the desktop, and waits until the thread is read.
func e2eWindow(t *testing.T, size fyne.Size) (*fyneUI, *e2eSessions) {
	t.Helper()
	sessions := newE2ESessions()
	return e2eWindowWith(t, size, sessions), sessions
}

// e2eWindowWith opens the window on the sessions given.
func e2eWindowWith(t *testing.T, size fyne.Size, sessions *e2eSessions) *fyneUI {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return e2eWindowUntil(t, ctx, size, sessions)
}

// e2eWindowUntil opens the window on the sessions given until ctx ends.
func e2eWindowUntil(t *testing.T, ctx context.Context, size fyne.Size, sessions *e2eSessions) *fyneUI {
	t.Helper()
	application := newLoopApp(t)
	bridge := newWindowContext(ctx, page{
		choices:  []choice{{name: "herox", id: "herox"}},
		profiles: []*Profile{{sessions: sessions}},
	})
	t.Cleanup(bridge.Close)
	bridge.useTheme(application)
	var ui *fyneUI
	fyne.DoAndWait(func() {
		native := test.NewWindow(nil)
		native.SetPadded(false)
		ui = newFyneUI(ctx, application, native, bridge)
		ui.build()
		native.Resize(size)
	})
	ui.start()
	waitFor(t, func() bool { return ui.ready.Load() })
	return ui
}

// loopApp is the test app with a driver that runs what is handed to the UI
// on one goroutine of its own, as the desktop driver runs it on the main
// thread: the test driver runs it on whichever goroutine hands it over.
type loopApp struct {
	fyne.App
	driver *loopDriver
}

func (a *loopApp) Driver() fyne.Driver { return a.driver }

type loopDriver struct {
	fyne.Driver
	*mainLoop
}

// mainLoop is the one goroutine all test windows run on, as a process has
// one main thread: windows of tests that ended may still hand it work, and
// it must never run beside the next test's.
type mainLoop struct {
	jobs chan func()
	id   uint64
}

var (
	theLoop     *mainLoop
	theLoopOnce sync.Once
)

func sharedLoop() *mainLoop {
	theLoopOnce.Do(func() {
		theLoop = &mainLoop{jobs: make(chan func(), 1024)}
		ready := make(chan struct{})
		go func() {
			theLoop.id = goroutineID()
			close(ready)
			for job := range theLoop.jobs {
				job()
			}
		}()
		<-ready
	})
	return theLoop
}

func newLoopApp(t *testing.T) *loopApp {
	t.Helper()
	inner := test.NewApp()
	d := &loopDriver{Driver: inner.Driver(), mainLoop: sharedLoop()}
	a := &loopApp{App: inner, driver: d}
	fyne.SetCurrentApp(a)
	t.Cleanup(func() { fyne.DoAndWait(func() {}) })
	return a
}

func (d *loopDriver) DoFromGoroutine(f func(), wait bool) {
	if goroutineID() == d.id {
		if wait {
			f()
		} else {
			go func() { d.jobs <- f }()
		}
		return
	}
	if !wait {
		d.jobs <- f
		return
	}
	done := make(chan struct{})
	d.jobs <- func() {
		defer close(done)
		f()
	}
	<-done
}

// goroutineID is the number the runtime gives the calling goroutine.
func goroutineID() uint64 {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	field := strings.Fields(strings.TrimPrefix(string(buf[:n]), "goroutine "))[0]
	id, _ := strconv.ParseUint(field, 10, 64)
	return id
}

// waitFor polls cond on the UI goroutine until it holds.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		var ok bool
		fyne.DoAndWait(func() { ok = cond() })
		if ok {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not reached")
}

// TestFyneSnapshots writes pictures of the window in several sizes to the
// directory OURO_FYNE_SNAPSHOTS names, for looking at the layout headless.
func TestFyneSnapshots(t *testing.T) {
	dir := os.Getenv("OURO_FYNE_SNAPSHOTS")
	if dir == "" {
		t.Skip("set OURO_FYNE_SNAPSHOTS to a directory")
	}
	for _, size := range []fyne.Size{{Width: 1400, Height: 900}, {Width: 1180, Height: 780}, {Width: 900, Height: 640}, {Width: 600, Height: 700}, {Width: 380, Height: 520}} {
		for _, pref := range []string{"light", "dark"} {
			name := pref + "-" + itoa(int(size.Width)) + "x" + itoa(int(size.Height))
			t.Run(name, func(t *testing.T) {
				ui, _ := e2eWindow(t, size)
				if pref == "dark" {
					fyne.DoAndWait(func() {
						ui.app.Preferences().SetString(themePreferenceKey, "dark")
						ui.bridge.useTheme(ui.app)
					})
				}
				time.Sleep(100 * time.Millisecond)
				writeSnapshot(t, ui.win, filepath.Join(dir, name+".png"))
				if size.Width == 1180 && pref == "light" {
					fyne.DoAndWait(func() { ui.openSettings(ui.profile, ui.profileName()) })
					writeSnapshot(t, ui.win, filepath.Join(dir, name+"-settings.png"))
				}
			})
		}
	}
}

func writeSnapshot(t *testing.T, win fyne.Window, path string) {
	t.Helper()
	settle(win)
	var img image.Image
	fyne.DoAndWait(func() { img = win.Canvas().Capture() })
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

// settle lays the window out again from the top, as the desktop driver does
// before every frame when a minimum size changed; the test canvas does not.
func settle(win fyne.Window) {
	fyne.DoAndWait(func() {
		for range 3 {
			relayout(win.Canvas().Content())
			for _, overlay := range win.Canvas().Overlays().List() {
				relayout(overlay)
			}
		}
	})
}

func relayout(object fyne.CanvasObject) {
	if object == nil || !object.Visible() {
		return
	}
	switch o := object.(type) {
	case *fyne.Container:
		if o.Layout != nil {
			o.Layout.Layout(o.Objects, o.Size())
		}
		for _, child := range o.Objects {
			relayout(child)
		}
	case fyne.Widget:
		r := test.WidgetRenderer(o)
		r.Layout(o.Size())
		for _, child := range r.Objects() {
			relayout(child)
		}
	}
}

// bigThread is a thread of n exchanges: a message, a call of a tool and an
// answer each, all within the six hours the thread shows.
func bigThread(n int) []plugin.SessionEvent {
	start := time.Now().Add(-5 * time.Hour)
	out := make([]plugin.SessionEvent, 0, n*4)
	for i := range n {
		at := start.Add(time.Duration(i) * 5 * time.Hour / time.Duration(n))
		id := "c" + strconv.Itoa(i)
		out = append(out,
			plugin.SessionEvent{Kind: plugin.SessionMessage, Text: "Question number " + strconv.Itoa(i) + " about the window", At: at},
			plugin.SessionEvent{Kind: plugin.SessionStep, Tool: "read", CallID: id, At: at},
			plugin.SessionEvent{Kind: plugin.SessionStep, Tool: "read", CallID: id, Done: true, At: at},
			plugin.SessionEvent{Kind: plugin.SessionAnswer, Text: strings.Repeat("An answer that wraps over a few lines of the transcript. ", 4), At: at},
		)
	}
	return out
}

// TestFynePerformance replays a long thread and streams an answer into it,
// and holds the time either takes on the UI goroutine to what keeps the
// window fluid.
func TestFynePerformance(t *testing.T) {
	if testing.Short() || raceEnabled {
		t.Skip("times a long thread, which -short and the race detector skip")
	}
	sessions := newE2ESessions()
	thread := bigThread(400)
	live := make(chan plugin.SessionEvent, 4096)
	watch := sessions.watch
	sessions.watch = func(ctx context.Context, id string) ([]plugin.SessionEvent, <-chan plugin.SessionEvent, error) {
		if id == plugin.ThreadID {
			return thread, live, nil
		}
		return watch(ctx, id)
	}
	application := newLoopApp(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	bridge := newWindowContext(ctx, page{choices: []choice{{name: "herox"}}, profiles: []*Profile{{sessions: sessions}}})
	t.Cleanup(bridge.Close)
	var ui *fyneUI
	fyne.DoAndWait(func() {
		bridge.useTheme(application)
		native := test.NewWindow(nil)
		ui = newFyneUI(ctx, application, native, bridge)
		ui.build()
		native.Resize(fyne.NewSize(1180, 780))
	})
	began := time.Now()
	ui.start()
	waitFor(t, func() bool { return ui.ready.Load() })
	settle(ui.win)
	replay := time.Since(began)
	t.Logf("replay of %d events: %v", len(thread), replay)

	// One frame of a streamed answer: a piece, then what the layout costs.
	var worst time.Duration
	for i := range 200 {
		event := plugin.SessionEvent{Kind: plugin.SessionAnswering, Text: "piece " + strconv.Itoa(i) + " ", At: time.Now()}
		took := time.Now()
		fyne.DoAndWait(func() {
			ui.applyEvents([]chat.Event{chat.Show(bridge.profiles[0].ctx, event)})
			ui.stick()
			relayout(ui.win.Canvas().Content())
		})
		worst = max(worst, time.Since(took))
	}
	t.Logf("worst frame while streaming: %v", worst)
	if worst > 50*time.Millisecond {
		t.Errorf("a streamed piece takes %v of the UI goroutine", worst)
	}
	took := time.Now()
	fyne.DoAndWait(func() {
		ui.read(SessionEntry{ID: "owner/s1", Title: "Review"})
	})
	waitFor(t, func() bool { return ui.ready.Load() && ui.selected == "owner/s1" })
	t.Logf("switch away from the long thread: %v", time.Since(took))
}
