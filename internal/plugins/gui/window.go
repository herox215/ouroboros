package gui

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"ouroboros/internal/chat"
	"ouroboros/plugin"
)

// State is the read-only profile snapshot handed to the window.
type State struct {
	Profiles []ProfileOption
	Selected int
	Thread   string
	// Resume is the session the window before showed, "" to show the thread:
	// the process that started this one handed it over, see handOver.
	Resume string
}
type ProfileOption struct {
	Name string
	ID   string
}

// Opened is a session the window shows: what happened in it so far, what
// happens from now on, nil for the conversation of a peer, which is only to
// read, and what the window notices of it, like a thread that keeps its
// project.
type Opened struct {
	Events []chat.Event
	Live   <-chan chat.Event
	Notice string
}

type sessionWatch struct {
	ctx    context.Context
	cancel context.CancelFunc
}

// Window exposes the same sessions service as the terminal UI. Profile data
// is immutable; mu protects selection and publication of concurrent opens.
type Window struct {
	state     State
	profiles  []Profile
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	selected  int
	active    *sessionWatch
	lifecycle sync.Mutex
	opened    map[int]bool // profiles opened here; value says whether we started the node
	session   string       // the session the window shows, as the front end named it
	restarts  string       // the build a tool asked ouro to start anew from, see PollSession
	leave     func()       // closes the window for it, see OnRestart
	directory string
	threadMu  sync.Mutex
	threads   map[int]bool // profiles whose thread took this UI's start directory
}

func newWindow(p page) *Window { return newWindowContext(context.Background(), p) }
func newWindowContext(ctx context.Context, p page) *Window {
	state := State{Profiles: make([]ProfileOption, len(p.choices)), Selected: p.selected, Resume: p.session, Thread: "owner/" + plugin.ThreadID}
	for i, choice := range p.choices {
		state.Profiles[i] = ProfileOption{Name: choice.name, ID: choice.id}
	}
	profiles := make([]Profile, len(p.choices))
	for i, profile := range p.profiles {
		profiles[i] = *profile
	}
	ctx, cancel := context.WithCancel(ctx)
	return &Window{state: state, profiles: profiles, ctx: ctx, cancel: cancel, selected: p.selected, opened: make(map[int]bool), directory: p.directory, threads: make(map[int]bool)}
}
func (w *Window) InitialState() State {
	state := w.state
	state.Profiles = append([]ProfileOption(nil), state.Profiles...)
	return state
}

// Close cancels pending opens and polls, including on application shutdown.
func (w *Window) Close() {
	w.mu.Lock()
	w.cancel()
	w.invalidate()
	w.mu.Unlock()
	w.lifecycle.Lock()
	defer w.lifecycle.Unlock()
	for profile, started := range w.opened {
		if started {
			_ = w.profiles[profile].sessions.Release(context.WithoutCancel(w.ctx))
		}
		delete(w.opened, profile)
	}
}

// OnRestart tells the window what closes it once a tool asked ouro to start
// anew from a new build: leave is called once, and the driver starts that
// build in place of this window, see showWindow. Without it the window
// stays as it is.
func (w *Window) OnRestart(leave func()) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.leave = leave
}

// restart takes the build a tool of the watched session asked ouro to start
// anew from, see PollSession, and leaves the window for it.
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

// Restart reports the build ouro starts anew from and what the window of the
// new process goes on with: the profile it shows and the session it watches.
// ok is false if no tool asked for it.
func (w *Window) Restart() (exe string, r resume, ok bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.restarts == "" {
		return "", resume{}, false
	}
	r.Session = w.session
	if w.selected >= 0 && w.selected < len(w.state.Profiles) {
		r.Profile = w.state.Profiles[w.selected].ID
	}
	return w.restarts, r, true
}

// invalidate ends the watch of the session shown. It is called with mu held.
func (w *Window) invalidate() {
	if w.active != nil {
		w.active.cancel()
		w.active = nil
	}
}
func (w *Window) profile(profile int) (Profile, error) {
	if profile < 0 || profile >= len(w.profiles) {
		return Profile{}, fmt.Errorf("invalid GUI profile index %d", profile)
	}
	p := w.profiles[profile]
	if p.sessions == nil {
		return Profile{}, fmt.Errorf("GUI profile %d has no sessions service", profile)
	}
	return p, nil
}

// ready uses the same node lifecycle as the terminal frontend. Only nodes this
// window started are released on close; their running answers continue.
func (w *Window) ready(profile int) (Profile, error) {
	p, err := w.profile(profile)
	if err != nil {
		return Profile{}, err
	}
	w.lifecycle.Lock()
	defer w.lifecycle.Unlock()
	if err := w.ctx.Err(); err != nil {
		return Profile{}, err
	}
	if _, ok := w.opened[profile]; !ok {
		started, err := p.sessions.Open(w.ctx)
		if err != nil {
			return Profile{}, err
		}
		w.opened[profile] = started
	}
	return p, nil
}

// ModelAccess contains only the displayable fields of an access, never credentials.
type ModelAccess struct {
	ID     string
	Label  string
	Models []string
	Error  string
}

type ModelSelection struct {
	Accesses []ModelAccess
	Access   string
	Model    string
}

func (w *Window) modelProfile(profile int) (Profile, error) {
	if profile < 0 || profile >= len(w.profiles) {
		return Profile{}, fmt.Errorf("invalid GUI profile index %d", profile)
	}
	p := w.profiles[profile]
	if p.owner == nil {
		return Profile{}, fmt.Errorf("GUI profile %d has no owner access", profile)
	}
	if err := w.ctx.Err(); err != nil {
		return Profile{}, err
	}
	return p, nil
}

// Models lists each access independently, preserving cached models and provider
// errors just as the terminal model picker does. It need not start the node.
func (w *Window) Models(profile int) (ModelSelection, error) {
	return w.models(profile, func(p Profile, providers []plugin.Provider) ([]plugin.AccessModels, error) {
		return p.owner.Models(w.ctx, providers)
	})
}

// KnownModels is Models as the accesses listed them the last time, without
// asking them: what the model dialog shows at once while Models asks anew.
func (w *Window) KnownModels(profile int) (ModelSelection, error) {
	return w.models(profile, func(p Profile, providers []plugin.Provider) ([]plugin.AccessModels, error) {
		return p.owner.KnownModels(providers)
	})
}

func (w *Window) models(profile int, list func(Profile, []plugin.Provider) ([]plugin.AccessModels, error)) (ModelSelection, error) {
	p, err := w.modelProfile(profile)
	if err != nil {
		return ModelSelection{}, err
	}
	var providers []plugin.Provider
	if p.ctx != nil {
		providers = plugin.All[plugin.Provider](p.ctx)
	}
	listed, err := list(p, providers)
	if err != nil {
		return ModelSelection{}, fmt.Errorf("list models: %w", err)
	}
	status, err := p.owner.Status()
	if err != nil {
		return ModelSelection{}, fmt.Errorf("read selected model: %w", err)
	}
	out := ModelSelection{Accesses: make([]ModelAccess, 0, len(listed)), Model: status.Model}
	for _, entry := range listed {
		models := append([]string{}, entry.Models...)
		out.Accesses = append(out.Accesses, ModelAccess{ID: entry.Access.ID, Label: entry.Access.Label, Models: models, Error: entry.Err})
		if entry.Access.Active {
			out.Access = entry.Access.ID
		}
	}
	return out, nil
}

// SetModel uses the same owner operation as the terminal picker: the saved
// selection applies on the next message without restarting the node.
func (w *Window) SetModel(profile int, access, model string) (bool, error) {
	p, err := w.modelProfile(profile)
	if err != nil {
		return false, err
	}
	switched, err := p.owner.SetModel(access, model)
	if err != nil {
		return false, fmt.Errorf("set model: %w", err)
	}
	return switched, nil
}

// NewSession creates an owner session using the node's existing session API.
func (w *Window) NewSession(profile int) (string, error) {
	p, err := w.ready(profile)
	if err != nil {
		return "", err
	}
	id, err := p.sessions.New(w.ctx)
	if err != nil {
		return "", err
	}
	return "owner/" + id, nil
}

// Deliver gives text to the session id the way the State of the window said,
// see chat.State.Send, as the terminal UI does. Peer transcripts are never
// writable.
func (w *Window) Deliver(profile int, id, text string, way chat.Way) error {
	serviceID, owner, err := sessionID(id)
	if err != nil {
		return err
	}
	if !owner {
		return chat.ErrReadOnly
	}
	p, err := w.ready(profile)
	if err != nil {
		return err
	}
	return chat.Deliver(w.ctx, p.sessions, serviceID, text, way, "gui")
}

// Unsteer takes back the last message queued in the session id, as the
// terminal UI does, and returns it: "" if none is.
func (w *Window) Unsteer(profile int, id string) (string, error) {
	serviceID, owner, err := sessionID(id)
	if err != nil {
		return "", err
	}
	if !owner {
		return "", chat.ErrReadOnly
	}
	p, err := w.ready(profile)
	if err != nil {
		return "", err
	}
	return p.sessions.Unsteer(w.ctx, serviceID)
}

// SessionEntry is the existing session list with its live running state.
type SessionEntry struct {
	ID       string
	Title    string
	Modified time.Time
	Running  bool
}

// Sessions lists owner IDs qualified with owner/ and unchanged peer IDs.
func (w *Window) Sessions(profile int) ([]SessionEntry, error) {
	p, err := w.ready(profile)
	if err != nil {
		return nil, err
	}
	w.mu.Lock()
	if w.selected != profile {
		w.invalidate()
		w.selected = profile
	}
	w.mu.Unlock()
	owners, err := p.sessions.List(w.ctx)
	if err != nil {
		return nil, err
	}
	peers, err := p.sessions.Peers(w.ctx)
	if err != nil {
		return nil, err
	}
	out := make([]SessionEntry, 0, len(owners)+len(peers))
	for _, s := range owners {
		out = append(out, SessionEntry{ID: "owner/" + s.ID, Title: s.Title, Modified: s.Last, Running: s.Running})
	}
	for _, s := range peers {
		out = append(out, SessionEntry{ID: s.ID, Title: s.Title, Modified: s.Last, Running: s.Running})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Modified.After(out[j].Modified) })
	return out, nil
}

// sessionID accepts only qualified owner and peer IDs. The service remains the
// authority on existence; an owner prefix is stripped only after validation.
func sessionID(id string) (string, bool, error) {
	party, name, ok := strings.Cut(id, "/")
	if !ok || name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") || strings.ContainsAny(party, "\\\x00") {
		return "", false, fmt.Errorf("invalid session ID %q", id)
	}
	if party == "owner" {
		return name, true, nil
	}
	if strings.HasPrefix(party, "peer-") && len(party) > len("peer-") {
		return id, false, nil
	}
	return "", false, fmt.Errorf("invalid session ID %q", id)
}

// OpenSession replaces the active watch. Peers are read-only snapshots.
func (w *Window) OpenSession(profile int, id string) (Opened, error) {
	p, err := w.ready(profile)
	if err != nil {
		return Opened{}, err
	}
	serviceID, owner, err := sessionID(id)
	if err != nil {
		return Opened{}, err
	}
	w.mu.Lock()
	w.invalidate()
	w.selected = profile
	ctx, cancel := context.WithCancel(w.ctx)
	watch := &sessionWatch{ctx: ctx, cancel: cancel}
	w.active = watch // publish before I/O, so another selection can cancel it
	w.session = id
	w.mu.Unlock()
	var past []plugin.SessionEvent
	var live <-chan plugin.SessionEvent
	var notice string
	if owner {
		if serviceID == plugin.ThreadID {
			notice = w.placeThread(ctx, p, profile)
		}
		past, live, err = p.sessions.Watch(ctx, serviceID)
	} else {
		past, err = p.sessions.Read(ctx, serviceID)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.active != watch || ctx.Err() != nil {
		cancel()
		return Opened{}, context.Canceled
	}
	if err != nil {
		w.invalidate()
		return Opened{}, err
	}
	opened := Opened{Events: p.events(past), Notice: notice}
	if live != nil {
		opened.Live = w.follow(ctx, p, live)
	}
	return opened, nil
}

// follow shows what happens in the session watched from now on, until the
// watch ends or the node stops: then the channel closes. A restart event
// closes the window, see OnRestart.
func (w *Window) follow(ctx context.Context, p Profile, live <-chan plugin.SessionEvent) <-chan chat.Event {
	out := make(chan chat.Event)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case e, ok := <-live:
				if !ok {
					return
				}
				if e.Kind == plugin.SessionRestart {
					w.restart(e.Text)
				}
				select {
				case out <- chat.Show(p.ctx, e):
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out
}

func (p *Profile) events(events []plugin.SessionEvent) []chat.Event {
	out := make([]chat.Event, len(events))
	for i, e := range events {
		out[i] = chat.Show(p.ctx, e)
	}
	return out
}

// ToolResult is the only bridge call that loads original arguments/output.
func (w *Window) ToolResult(profile int, id, callID string) (plugin.SessionToolResult, error) {
	p, err := w.profile(profile)
	if err != nil {
		return plugin.SessionToolResult{}, err
	}
	serviceID, _, err := sessionID(id)
	if err != nil {
		return plugin.SessionToolResult{}, err
	}
	if callID == "" {
		return plugin.SessionToolResult{}, errors.New("empty tool call ID")
	}
	details, ok := p.sessions.(plugin.SessionToolDetails)
	if !ok {
		return plugin.SessionToolResult{}, errors.New("sessions service has no tool details")
	}
	return details.ToolResult(w.ctx, serviceID, callID)
}

// SessionTrace returns what an owner session did, in the order it came, as the
// thread shows its artefacts beside the part of it that started or addressed
// that session: the messages, the calls of tools with their start and their
// end, the questions and the answers, and as the last event the piece of an
// answer being written, without its text. A front end tells from it where the
// session stands. It watches the session only for its past and lets it go: the
// watch of the selected conversation stays as it is.
func (w *Window) SessionTrace(profile int, id string) ([]chat.Event, error) {
	p, err := w.profile(profile)
	if err != nil {
		return nil, err
	}
	serviceID, owner, err := sessionID(id)
	if err != nil {
		return nil, err
	}
	if !owner {
		return nil, fmt.Errorf("session %q is not the owner's", id)
	}
	ctx, cancel := context.WithCancel(w.ctx)
	defer cancel()
	past, _, err := p.sessions.Watch(ctx, serviceID)
	if err != nil {
		return nil, err
	}
	out := []chat.Event{}
	// The newest piece of an answer being written tells that it is written,
	// when nothing else came after it; what it says is not part of the trace.
	var writing plugin.SessionEvent
	written, kept := -1, -1
	for i, e := range past {
		switch e.Kind {
		case plugin.SessionMessage, plugin.SessionSay, plugin.SessionStep,
			plugin.SessionAsked, plugin.SessionAnswer, plugin.SessionFailed,
			plugin.SessionCanceled, plugin.SessionTask, plugin.SessionJobs:
			out = append(out, chat.Show(p.ctx, e))
			kept = i
		case plugin.SessionAnswering:
			writing, written = e, i
		}
	}
	if written > kept {
		writing.Text = ""
		out = append(out, chat.Show(p.ctx, writing))
	}
	return out, nil
}
