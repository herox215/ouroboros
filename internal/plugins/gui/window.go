package gui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"ouroboros/plugin"
)

// State is what the window knows before it opens the thread: the profile it
// belongs to, and the conversation it shows. Note that the window has no
// picker: which profile it belongs to was chosen when ouro was started.
type State struct {
	Profile string `json:"profile"`
	Thread  string `json:"thread"`
}

// Event is one thing the thread told, as the window shows it. It is what the
// terminal UI shows of a plugin.SessionEvent (see internal/plugins/tui), in
// the shape the page reads: the kinds are the SessionKinds, the text of a
// call is the line the plugin of the tool wants shown, a question carries the
// answers to choose from.
type Event struct {
	Kind        plugin.SessionKind `json:"kind"`
	Text        string             `json:"text,omitempty"`
	Tool        string             `json:"tool,omitempty"`
	CallID      string             `json:"call_id,omitempty"`
	Done        bool               `json:"done,omitempty"`
	Failed      bool               `json:"failed,omitempty"`
	At          time.Time          `json:"at"`
	N           int                `json:"n,omitempty"`
	Own         bool               `json:"own,omitempty"`
	Options     []string           `json:"options,omitempty"`
	Recommended string             `json:"recommended,omitempty"`
}

// Snapshot is what the thread told so far, when the window opens it.
type Snapshot struct {
	Events []Event `json:"events"`
}

// Window is the chat of the thread of one profile. It watches the very
// conversation the terminal UI watches first, with the same Sessions.Watch,
// and passes on what it tells. It adds no routing and no other view of its
// own: what the thread starts, hands on or asks is on the page as the thread
// tells it.
type Window struct {
	profile *profile
	name    string
	ctx     context.Context
	cancel  context.CancelFunc

	// lifecycle serializes opening the node with closing the window.
	lifecycle sync.Mutex

	mu       sync.Mutex
	opened   bool
	started  bool
	watch    context.CancelFunc
	gen      int
	flush    time.Duration
	emit     func([]Event)
	ended    func()
	leave    func()
	restarts string
	instance string
}

func newWindow(p *profile, name string) *Window {
	return newWindowContext(context.Background(), p, name)
}

func newWindowContext(ctx context.Context, p *profile, name string) *Window {
	ctx, cancel := context.WithCancel(ctx)
	return &Window{profile: p, name: name, ctx: ctx, cancel: cancel, flush: defaultFlush}
}

// InitialState names the profile of the window and the conversation it shows.
func (w *Window) InitialState() State {
	return State{Profile: w.name, Thread: "owner/" + plugin.ThreadID}
}

// Close cancels pending watches and releases the node this window started.
// Answers still on their way go on in the node.
func (w *Window) Close() {
	w.mu.Lock()
	w.cancel()
	if w.watch != nil {
		w.watch()
		w.watch = nil
	}
	w.gen++
	w.mu.Unlock()
	w.lifecycle.Lock()
	defer w.lifecycle.Unlock()
	if w.started {
		_ = w.profile.sessions.Release(context.WithoutCancel(w.ctx))
		w.started = false
	}
}

// onEmit tells the window where to pass on what the thread tells, and
// onEnded that the live stream of the thread closed.
func (w *Window) onEmit(emit func([]Event)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.emit = emit
}

func (w *Window) onEnded(ended func()) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ended = ended
}

// onRestart tells the window what closes it once ouro is to start anew from
// a new build: leave is called once.
func (w *Window) onRestart(leave func()) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.leave = leave
}

// restart takes the build a tool of the watched session asked ouro to start
// anew from, or the one a node that started anew runs, and leaves the window
// for it.
func (w *Window) restart(exe string) {
	w.mu.Lock()
	take := w.restarts == "" && w.leave != nil
	if take {
		w.restarts = exe
	}
	leave := w.leave
	w.mu.Unlock()
	if take {
		leave()
	}
}

// pendingRestart reports the build ouro starts anew from, ok false if no tool
// asked for it.
func (w *Window) pendingRestart() (exe string, ok bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.restarts, w.restarts != ""
}

// instances is what a sessions service tells of the image of ouro its node
// runs, see session.Instance.
type instances interface {
	Instance(context.Context) (plugin.NodeInstance, error)
}

// noticeRestart compares the image the node of the profile runs with the one
// the window saw before, and leaves the window for the build that runs once it
// changed: the node started anew, as a rebuild in another session, ouro update
// or a peer make it do, and no restart event of the thread told the window.
// A service that cannot tell, or a node that does not answer, changes nothing.
func (w *Window) noticeRestart(ctx context.Context) {
	tells, ok := w.profile.sessions.(instances)
	if !ok {
		return
	}
	now, err := tells.Instance(ctx)
	if err != nil || now.ID == "" {
		return
	}
	w.mu.Lock()
	before := w.instance
	w.instance = now.ID
	w.mu.Unlock()
	if before != "" && before != now.ID && now.Executable != "" {
		w.restart(now.Executable)
	}
}

// ready opens the node through the same session service as the terminal front
// end, once, and keeps it for the window: only a node this window started is
// released when it closes.
func (w *Window) ready() error {
	w.lifecycle.Lock()
	defer w.lifecycle.Unlock()
	if err := w.ctx.Err(); err != nil {
		return err
	}
	if w.opened {
		return nil
	}
	started, err := w.profile.sessions.Open(w.ctx)
	if err != nil {
		return err
	}
	w.started = started
	w.opened = true
	return nil
}

// Open shows the thread of the window: what it told so far, and from now on
// what it tells, until the stream ends. Opening again replaces the watch.
func (w *Window) Open() (Snapshot, error) {
	if err := w.ready(); err != nil {
		return Snapshot{}, err
	}
	w.noticeRestart(w.ctx)
	past, err := w.watchThread()
	if err != nil {
		return Snapshot{}, err
	}
	events := make([]Event, 0, len(past))
	for _, e := range past {
		if e.Kind == plugin.SessionThought {
			continue
		}
		events = append(events, w.event(e))
	}
	return Snapshot{Events: events}, nil
}

// watchThread replaces the live watch with one on the thread, and starts
// passing on what it tells. It reports what the thread told so far.
func (w *Window) watchThread() ([]plugin.SessionEvent, error) {
	ctx, cancel := context.WithCancel(w.ctx)
	w.mu.Lock()
	if w.watch != nil {
		w.watch()
	}
	w.gen++
	gen := w.gen
	w.watch = cancel
	w.mu.Unlock()
	past, live, err := w.profile.sessions.Watch(ctx, plugin.ThreadID)
	if err != nil {
		cancel()
		return nil, err
	}
	go w.stream(gen, ctx, live)
	return past, nil
}

// defaultFlush is how long the window gathers what the thread tells before it
// passes it on: an answer streaming in would otherwise come as a call per
// piece. A window keeps its own, as tests pass what they tell on at once.
const defaultFlush = 40 * time.Millisecond

// maxBatch keeps one pass on to the page bounded while an answer streams in
// faster than it is shown.
const maxBatch = 64

func (w *Window) stream(gen int, ctx context.Context, live <-chan plugin.SessionEvent) {
	ticker := time.NewTicker(w.flush)
	defer ticker.Stop()
	batch := make([]Event, 0, maxBatch)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		if !w.current(gen) {
			return
		}
		out := batch
		batch = make([]Event, 0, maxBatch)
		w.pass(out)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			flush()
		case e, ok := <-live:
			if !ok {
				flush()
				w.end(gen)
				return
			}
			switch e.Kind {
			case plugin.SessionThought:
				continue
			case plugin.SessionRestart:
				w.restart(e.Text)
				continue
			}
			batch = append(batch, w.event(e))
			if len(batch) >= maxBatch {
				flush()
			}
		}
	}
}

// current says whether gen is still the watch the window shows.
func (w *Window) current(gen int) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return gen == w.gen
}

// pass hands what the thread told to the page, if it still shows this watch.
func (w *Window) pass(events []Event) {
	w.mu.Lock()
	emit := w.emit
	w.mu.Unlock()
	if emit != nil {
		emit(events)
	}
}

// end tells the page that the live stream of the thread closed, once for the
// watch it belongs to.
func (w *Window) end(gen int) {
	w.mu.Lock()
	if gen != w.gen {
		w.mu.Unlock()
		return
	}
	w.gen++
	ended := w.ended
	w.mu.Unlock()
	if ended != nil {
		ended()
	}
}

// Send starts the answer to text in the thread or, while one is on its way,
// gives it the text through Steer, just as the terminal front end does. This
// is also how the owner answers a question the thread asks.
func (w *Window) Send(text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return errors.New("the message is empty")
	}
	if err := w.ready(); err != nil {
		return err
	}
	ctx := plugin.WithMessageSource(w.ctx, plugin.MessageSource{Interface: "gui"})
	err := w.profile.sessions.Ask(ctx, plugin.ThreadID, text)
	if errors.Is(err, plugin.ErrBusy) {
		return w.profile.sessions.Steer(ctx, plugin.ThreadID, text)
	}
	return err
}

// Stop cancels the answer on its way in the thread. The node also stops the
// answers the thread handed to other sessions.
func (w *Window) Stop() error {
	return w.profile.sessions.Cancel(w.ctx, plugin.ThreadID)
}

// event is e as the window shows it, through the same plugin.ToolView the
// terminal UI uses.
func (w *Window) event(e plugin.SessionEvent) Event {
	out := Event{
		Kind:   e.Kind,
		Text:   e.Text,
		Tool:   e.Tool,
		CallID: e.CallID,
		Done:   e.Done,
		At:     e.At,
		N:      e.N,
		Own:    e.Own,
	}
	switch e.Kind {
	case plugin.SessionStep:
		s := e.AsStep()
		out.Failed = s.Done && s.Err != nil
		if w.profile.ctx != nil {
			out.Text = plugin.ToolLine(w.profile.ctx, s)
			if s.Done && out.Text != "" {
				out.Text = plugin.ToolDetail(w.profile.ctx, s, out.Text)
			}
		}
		if strings.TrimSpace(out.Text) == "" {
			out.Text = s.Tool
		}
	case plugin.SessionAsked:
		if !e.Done {
			q := e.AsQuestion()
			out.Text, out.Options, out.Recommended = q.Question, q.Options, q.Recommended
		}
	}
	return out
}
