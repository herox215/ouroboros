package gui

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"ouroboros/plugin"
)

// State is the read-only profile snapshot handed to the window.
type State struct {
	Profiles []ProfileOption `json:"profiles"`
	Selected int             `json:"selected"`
	Thread   string          `json:"thread"`
	// Resume is the session the window before showed, "" to show the thread:
	// the process that started this one handed it over, see handOver.
	Resume string `json:"resume,omitempty"`
}
type ProfileOption struct {
	Name string `json:"name"`
	ID   string `json:"id"`
}

// ChatEvent is presentation data, never the raw tool arguments or output.
type ChatEvent struct {
	Kind     plugin.SessionKind `json:"kind"`
	Text     string             `json:"text"`
	Tool     string             `json:"tool"`
	Done     bool               `json:"done"`
	Failed   bool               `json:"failed"`
	At       time.Time          `json:"at"`
	CallID   string             `json:"call_id"`
	N        int                `json:"n"`
	Own      bool               `json:"own"`
	Question *ChatQuestion      `json:"question,omitempty"`
}

// ChatQuestion contains the displayable fields of an owner question.
type ChatQuestion struct {
	Text        string   `json:"text"`
	Options     []string `json:"options"`
	Recommended string   `json:"recommended,omitempty"`
}

type ChatSnapshot struct {
	Token  int         `json:"token"`
	Events []ChatEvent `json:"events"`
	Notice string      `json:"notice,omitempty"`
}

type sessionWatch struct {
	ctx     context.Context
	cancel  context.CancelFunc
	profile Profile
	live    <-chan plugin.SessionEvent
	poll    sync.Mutex
}

// Window exposes the same sessions service as the terminal UI. Profile data
// is immutable; mu protects selection and publication of concurrent opens.
type Window struct {
	state     State
	profiles  []Profile
	archives  []archiveAccess // legacy methods only; Plugin never supplies these
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	token     int
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
	archives := make([]archiveAccess, len(p.choices))
	copy(archives, p.archives)
	ctx, cancel := context.WithCancel(ctx)
	return &Window{state: state, profiles: profiles, archives: archives, ctx: ctx, cancel: cancel, selected: p.selected, opened: make(map[int]bool), directory: p.directory, threads: make(map[int]bool)}
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

// invalidate is called with mu held. Tokens are never reused, even on failure.
func (w *Window) invalidate() {
	w.token++
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
	ID     string   `json:"id"`
	Label  string   `json:"label"`
	Models []string `json:"models"`
	Error  string   `json:"error,omitempty"`
}

type ModelSelection struct {
	Accesses []ModelAccess `json:"accesses"`
	Access   string        `json:"access"`
	Model    string        `json:"model"`
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
	p, err := w.modelProfile(profile)
	if err != nil {
		return ModelSelection{}, err
	}
	var providers []plugin.Provider
	if p.ctx != nil {
		providers = plugin.All[plugin.Provider](p.ctx)
	}
	listed, err := p.owner.Models(w.ctx, providers)
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

// SendMessage starts an answer or, if one is already running, gives it the
// message through Steer, just as the terminal frontend does. This also answers
// owner questions. Peer transcripts are never writable.
func (w *Window) SendMessage(profile int, id, text string) error {
	serviceID, owner, err := sessionID(id)
	if err != nil {
		return err
	}
	if !owner {
		return errors.New("peer sessions are read-only")
	}
	if strings.TrimSpace(text) == "" {
		return errors.New("the message is empty")
	}
	p, err := w.ready(profile)
	if err != nil {
		return err
	}
	ctx := plugin.WithMessageSource(w.ctx, plugin.MessageSource{Interface: "gui"})
	err = p.sessions.Ask(ctx, serviceID, text)
	if errors.Is(err, plugin.ErrBusy) {
		return p.sessions.Steer(ctx, serviceID, text)
	}
	return err
}

// Sessions lists owner IDs qualified with owner/ and unchanged peer IDs.
func (w *Window) Sessions(profile int) ([]plugin.ArchivedSession, error) {
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
	out := make([]plugin.ArchivedSession, 0, len(owners)+len(peers))
	for _, s := range owners {
		out = append(out, plugin.ArchivedSession{ID: "owner/" + s.ID, Title: s.Title, Modified: s.Last})
	}
	for _, s := range peers {
		out = append(out, plugin.ArchivedSession{ID: s.ID, Title: s.Title, Modified: s.Last})
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
func (w *Window) OpenSession(profile int, id string) (ChatSnapshot, error) {
	p, err := w.ready(profile)
	if err != nil {
		return ChatSnapshot{}, err
	}
	serviceID, owner, err := sessionID(id)
	if err != nil {
		return ChatSnapshot{}, err
	}
	w.mu.Lock()
	w.invalidate()
	w.selected = profile
	token := w.token
	ctx, cancel := context.WithCancel(w.ctx)
	watch := &sessionWatch{ctx: ctx, cancel: cancel, profile: p}
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
		return ChatSnapshot{}, context.Canceled
	}
	if err != nil {
		w.invalidate()
		return ChatSnapshot{}, err
	}
	watch.live = live
	if !owner {
		token = 0 // peer snapshots have no live stream
	}
	return ChatSnapshot{Token: token, Events: p.events(past), Notice: notice}, nil
}

// PollSession waits at most 150ms, returning no more than 100 events. It reads
// directly from the service channel without a lossy intermediate buffer. A live
// restart event closes the window, see OnRestart.
func (w *Window) PollSession(token int) []ChatEvent {
	empty := []ChatEvent{}
	w.mu.Lock()
	watch := w.active
	if token != w.token || watch == nil {
		w.mu.Unlock()
		return empty
	}
	w.mu.Unlock()
	watch.poll.Lock()
	defer watch.poll.Unlock()
	w.mu.Lock()
	if token != w.token || watch.ctx.Err() != nil {
		w.mu.Unlock()
		return empty
	}
	live := watch.live
	w.mu.Unlock()
	if live == nil {
		return empty
	}
	timer := time.NewTimer(150 * time.Millisecond)
	defer timer.Stop()
	out := make([]ChatEvent, 0, 100)
	finish := func() []ChatEvent {
		w.mu.Lock()
		defer w.mu.Unlock()
		if token != w.token || watch.ctx.Err() != nil {
			return empty
		}
		return out
	}
	for len(out) < 100 {
		select {
		case <-watch.ctx.Done():
			return empty
		case <-timer.C:
			return finish()
		case e, ok := <-live:
			if !ok {
				if len(out) == 0 {
					return nil // JSON null tells the frontend the stream ended
				}
				return finish()
			}
			if e.Kind == plugin.SessionRestart {
				w.restart(e.Text)
			}
			out = append(out, watch.profile.event(e))
		}
	}
	return finish()
}

func (p *Profile) events(events []plugin.SessionEvent) []ChatEvent {
	out := make([]ChatEvent, len(events))
	for i, e := range events {
		out[i] = p.event(e)
	}
	return out
}
func (p *Profile) event(e plugin.SessionEvent) ChatEvent {
	out := ChatEvent{Kind: e.Kind, Text: e.Text, Tool: e.Tool, Done: e.Done, At: e.At, CallID: e.CallID, N: e.N, Own: e.Own}
	if e.Kind == plugin.SessionAsked && !e.Done {
		q := e.AsQuestion()
		out.Question = &ChatQuestion{Text: q.Question, Options: q.Options, Recommended: q.Recommended}
	}
	if e.Kind == plugin.SessionStep {
		s := e.AsStep()
		out.Failed = s.Done && s.Err != nil
		if p.ctx != nil {
			out.Text = plugin.ToolLine(p.ctx, s)
		} else {
			out.Text = ""
		}
		if strings.TrimSpace(out.Text) == "" {
			out.Text = s.Tool
		}
		// Detail sees timing and failure, not raw errors or arbitrary result remarks.
		s.Remark = ""
		if s.Err != nil {
			s.Err = errors.New("failed")
		}
		if s.Done && p.ctx != nil {
			out.Text = plugin.ToolDetail(p.ctx, s, out.Text)
		}
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

// SessionPage and SessionRecords are retained for legacy archive tests only.
// Production profiles never carry archive callbacks.
func (w *Window) SessionPage(profile int, id string, after int) (plugin.ArchivedPage, error) {
	if profile < 0 || profile >= len(w.archives) {
		return plugin.ArchivedPage{}, fmt.Errorf("invalid GUI profile index %d", profile)
	}
	page := w.archives[profile].page
	if page == nil {
		return plugin.ArchivedPage{}, fmt.Errorf("GUI profile %d has no archive page access", profile)
	}
	return page(id, after)
}
func (w *Window) SessionRecords(profile int, id string) ([]plugin.ArchivedRecord, error) {
	if profile < 0 || profile >= len(w.archives) {
		return nil, fmt.Errorf("invalid GUI profile index %d", profile)
	}
	records := w.archives[profile].records
	if records == nil {
		return nil, fmt.Errorf("GUI profile %d has no archive", profile)
	}
	out, err := records(id)
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []plugin.ArchivedRecord{}
	}
	return out, nil
}
