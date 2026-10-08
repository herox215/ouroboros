// Package session holds the conversations of the owner of a node with its
// agent: the sessions. They belong to the node, not to a front end. The
// Service runs in the process of the node, runs the answers there and keeps
// what happened in each session as Events. The front ends, the terminal UI
// and "ouro chat", are clients of it: they reach it over a socket of the
// file system that only the owner can open (see Handler and Client), and
// show what its Events tell.
//
// An answer runs with a context of the Service, not of the client that asked
// for it: it goes on when the client is gone, and two clients that watch the
// same session see the same Events.
package session

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"ouroboros/internal/a2a"
	"ouroboros/internal/agent"
	"ouroboros/internal/assistant"
	"ouroboros/internal/history"
	"ouroboros/internal/openai"
)

// Kind is what an Event tells.
type Kind string

// The kinds of Events.
const (
	// Message is a message of the owner. An answer to it starts.
	Message Kind = "message"
	// Say is what the agent says along with the tools it asks for, "" if it
	// said nothing: it begins a round of the loop to the answer, before the
	// Steps of those tools.
	Say Kind = "say"
	// Step is a call of a tool that starts, or with Done, that ended.
	Step Kind = "step"
	// Answer is the answer, which ends what the Message started.
	Answer Kind = "answer"
	// Failed is an answer that failed, with why in Text.
	Failed Kind = "failed"
	// Canceled is an answer that was stopped before it was given.
	Canceled Kind = "canceled"
	// Restart follows the end of an answer in which a tool asked for ouro to
	// start anew from the binary in Text: the node takes its own place, and
	// a front end may do the same.
	Restart Kind = "restart"
	// Title names the session for its topic, in Text, once the agent can
	// tell it: on the way to an answer, and anew when the session is loaded.
	Title Kind = "title"
)

// Event is one thing that happened in a session. It is plain data, written
// as JSON on the socket.
type Event struct {
	Kind Kind      `json:"kind"`
	At   time.Time `json:"at"`
	// Text is the message, what the agent said, the answer, why it failed,
	// or the binary of a Restart.
	Text string `json:"text,omitempty"`
	// Tool, Args, Done, Took and Err are of a Step: the call of Tool with
	// Args, and once Done, how long it took and why it failed, if it did.
	Tool string          `json:"tool,omitempty"`
	Args json.RawMessage `json:"args,omitempty"`
	Done bool            `json:"done,omitempty"`
	Took time.Duration   `json:"took,omitempty"`
	Err  string          `json:"err,omitempty"`
	// Context is how long the context of the session is when the Event
	// happens: what the last call of the LLM in it sent, in bytes, 0 if
	// none was made yet.
	Context int `json:"context,omitempty"`
	// Cache is how much of what the LLM was sent in the session the
	// provider had cached when the Event happens, see assistant.Cache.
	Cache assistant.Cache `json:"cache,omitzero"`
}

// AsStep is the call of a tool a Step tells of, as the plugin of the tool
// shows it, see plugin.ToolLine.
func (e Event) AsStep() assistant.Step {
	s := assistant.Step{Tool: e.Tool, Args: e.Args, Done: e.Done, Took: e.Took}
	if e.Err != "" {
		s.Err = errors.New(e.Err)
	}
	return s
}

// Info describes a session for a list of them.
type Info struct {
	ID string `json:"id"`
	// Title names it for its topic, see the Event Title; until there is one
	// it is the first message of the owner in it, at most maxTitle bytes of
	// it.
	Title   string    `json:"title"`
	Running bool      `json:"running"` // an answer is on its way
	Last    time.Time `json:"last"`    // when the last Event happened
	// Peer is the peer a conversation of a peer is with, see API.Peers.
	Peer string `json:"peer,omitempty"`
}

// Line is a message of the context of a session: who it is of, like
// "system", "user" or "tool run", and its text.
type Line struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

// API is what a front end does with the sessions: the Service in the process
// of the node, and the Client of its socket everywhere else.
type API interface {
	// New returns the ID of a new session. It holds nothing until the
	// first message.
	New(ctx context.Context) (string, error)
	// Ask starts the answer to text in the session id and returns at once:
	// what happens on the way is told to those who Watch it. It fails with
	// ErrBusy while an answer is on its way in that session.
	Ask(ctx context.Context, id, text string) error
	// Watch returns what happened in the session id so far, and what
	// happens from now on until ctx ends. The channel is closed when ctx
	// ends, when the node stops, or when the watcher falls too far behind:
	// whoever watches again gets the whole session anew.
	Watch(ctx context.Context, id string) (past []Event, live <-chan Event, err error)
	// Cancel stops the answer that is on its way in the session id, if
	// there is one.
	Cancel(ctx context.Context, id string) error
	// List describes the sessions that have a message, the one with the
	// latest Event first: also those the node keeps from before it started.
	List(ctx context.Context) ([]Info, error)
	// Delete removes the session id, or the conversation of a peer id as
	// Peers names it: from the archive, and what the agent remembers of it.
	// It fails with ErrBusy while an answer is on its way in it, and with
	// ErrNoSession if there is nothing of it.
	Delete(ctx context.Context, id string) error
	// Peers describes the conversations of peers with the agent of the
	// node, the latest first. They are only to read and delete.
	Peers(ctx context.Context) ([]Info, error)
	// Read returns what happened in the conversation of a peer id, as Peers
	// names it.
	Read(ctx context.Context, id string) ([]Event, error)
	// Contents returns what the next call of the LLM in the session id is
	// sent, message by message. With an error it returns what it has of it.
	Contents(ctx context.Context, id string) ([]Line, error)
	// Open makes sure the node that holds the sessions runs, and reports
	// whether it was started for it.
	Open(ctx context.Context) (started bool, err error)
	// Release lets the node stop once no answer is on its way: for a front
	// end that started it. It returns once it stopped, unless an answer
	// keeps it running. A front end that asks or watches before keeps it
	// running.
	Release(ctx context.Context) error
}

var (
	// ErrBusy means that an answer is on its way in the session already: one
	// message at a time keeps it in order.
	ErrBusy = errors.New("wait for the answer first")
	// ErrRestarting means that the node starts anew with a new build once
	// the answers on their way are given, and takes no new message until
	// then.
	ErrRestarting = errors.New("ouro is starting anew with a new build, ask again in a moment")
	// ErrStopped means that the node is stopping.
	ErrStopped = errors.New("the node is stopping")
	// ErrNoSession means that a session was named with an ID that is none,
	// or that there is nothing of it to delete or read.
	ErrNoSession = errors.New("no such session")
	// ErrEmpty means a message without text.
	ErrEmpty = errors.New("the message is empty")
)

// maxTitle is how much of the first message the Title of a session has.
const maxTitle = 200

// titleTries is after how many messages in a session the agent gives up to
// name it, when it cannot tell a topic; titleTime is how long it may take
// for one.
const (
	titleTries = 3
	titleTime  = 20 * time.Second
)

// watchBuffer is how many Events a watcher may fall behind before it is let
// go.
const watchBuffer = 1024

// Assistant is the agent the sessions are with: *assistant.Assistant.
type Assistant interface {
	Ready() error
	Run(ctx context.Context, contextID, text string, observe func(assistant.Step), say func(string)) (string, error)
	Context(contextID string) int
	Cache(contextID string) assistant.Cache
	Contents(contextID string) (assistant.Contents, error)
	Inspection(contextID string) (agent.Inspection, bool)
	Conversation(contextID string) json.RawMessage
	Resume(contextID string, conversation json.RawMessage) error
	Forget(contextID string)
	Title(ctx context.Context, conversation []openai.Message) (string, error)
}

// Service holds the sessions of the owner in the process of the node.
type Service struct {
	agent Assistant
	now   func() time.Time

	ctx     context.Context // of every answer: ends with Close
	stop    context.CancelFunc
	answers sync.WaitGroup // on their way

	mu       sync.Mutex
	sessions map[string]*session
	archive  *archive // where the sessions are kept; nil keeps none, see Archive
	closed   bool     // by Close
	node     *process // s.mu is held to read it
}

// process is what the sessions know of the process of the node they run in:
// whether it is to stop or to be replaced, once no answer is on its way. The
// Services of the profiles of a node share it, see Share: an answer of one
// of them keeps it running for all. Its mu is taken with the mu of a
// Service held, never the other way round.
type process struct {
	mu        sync.Mutex
	answering int  // answers on their way
	release   bool // stop the node once no answer is on its way
	successor string
	// stopNode and replace are what the node does once it is to stop or to
	// be replaced by successor, see Attach.
	stopNode func()
	replace  func(exe string)
}

// session is one conversation of the owner with the agent.
type session struct {
	id       string
	events   []Event
	watchers map[chan Event]struct{}
	cancel   context.CancelFunc // of the answer on its way; nil without one
	restart  string             // the binary a tool of that answer asked to start anew from
	// resumed is when the last answer ended of a session that was loaded
	// from where it is kept, until the agent is told so with the next
	// message; zero otherwise.
	resumed time.Time
	// title names the session for its topic, "" until the agent named it;
	// tries is for how many messages it was asked to.
	title string
	tries int
}

// New returns the Service of the sessions with agent.
func New(agent Assistant) *Service {
	ctx, stop := context.WithCancel(context.Background())
	return &Service{agent: agent, now: time.Now, ctx: ctx, stop: stop, sessions: map[string]*session{}, node: &process{}}
}

// Share lets the Services of the profiles of one node run in one process,
// before any of them is used: it stops, or is replaced by a new build, once
// no answer of any of them is on its way, and an answer of any of them keeps
// it running. Attach one of them, which attaches all.
func Share(services ...*Service) {
	node := &process{}
	for _, s := range services {
		s.mu.Lock()
		s.node = node
		s.mu.Unlock()
	}
}

// Attach tells the Service what the node that serves it does: stop stops
// the node, once Release asked for it and no answer is on its way, and
// replace hands it over to a new build of ouro, once a tool asked for one
// and no answer is on its way. Without replace a tool cannot ask for a new
// build: a Service that is not served by a node has no node to replace.
func (s *Service) Attach(stop func(), replace func(exe string)) {
	p := s.process()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopNode, p.replace = stop, replace
}

// process returns the process s runs in.
func (s *Service) process() *process {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.node
}

// get returns the session id, which it adds if there is none. s.mu is held.
func (s *Service) get(id string) *session {
	se := s.lookup(id)
	if se == nil {
		se = &session{id: id, watchers: map[chan Event]struct{}{}}
		s.sessions[id] = se
	}
	return se
}

// lookup returns the session id, loaded from the archive if it is there;
// nil if there is none. s.mu is held.
func (s *Service) lookup(id string) *session {
	if se, ok := s.sessions[id]; ok {
		return se
	}
	se := s.load(id)
	if se != nil {
		s.sessions[id] = se
	}
	return se
}

// forget drops se once nothing is in it and nobody watches it. s.mu is held.
func (s *Service) forget(se *session) {
	if len(se.events) == 0 && len(se.watchers) == 0 && se.cancel == nil {
		delete(s.sessions, se.id)
	}
}

// named is what the ID of a session may be: as the archive names it.
var named = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// valid checks the ID of a session.
func valid(id string) error {
	if !named.MatchString(id) {
		return ErrNoSession
	}
	return nil
}

// New returns the ID of a new session.
func (s *Service) New(context.Context) (string, error) { return a2a.NewID(), nil }

// Ask starts the answer to text in the session id.
func (s *Service) Ask(_ context.Context, id, text string) error {
	if err := valid(id); err != nil {
		return err
	}
	if strings.TrimSpace(text) == "" {
		return ErrEmpty
	}
	if err := s.agent.Ready(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.node
	p.mu.Lock()
	defer p.mu.Unlock()
	switch {
	case s.closed:
		return ErrStopped
	case p.successor != "":
		return ErrRestarting
	}
	se := s.get(id)
	if se.cancel != nil {
		return ErrBusy
	}
	ctx, cancel := context.WithCancel(s.ctx)
	se.cancel = cancel
	p.answering++
	p.release = false // somebody wants the node
	s.emit(se, Event{Kind: Message, Text: text})
	named := make(chan struct{})
	if se.title == "" && se.tries < titleTries {
		se.tries++
		go s.name(ctx, se, said(se.events), named)
	} else {
		close(named)
	}
	if !se.resumed.IsZero() {
		text = resumed(se.resumed, s.now()) + text
		se.resumed = time.Time{}
	}
	s.answers.Add(1)
	go s.answer(ctx, se, text, p.replace != nil, named)
	return nil
}

// answer runs the answer to text in se and tells what happens on the way.
// With restartable a tool may ask for ouro to start anew. It ends once named
// is closed: the session is named, or not, on the way.
func (s *Service) answer(ctx context.Context, se *session, text string, restartable bool, named <-chan struct{}) {
	defer s.answers.Done()
	if restartable {
		ctx = WithRestart(ctx, func(exe string) {
			s.mu.Lock()
			defer s.mu.Unlock()
			se.restart = exe
		})
	}
	reply, err := s.agent.Run(ctx, se.id, text, func(st assistant.Step) {
		e := Event{Kind: Step, Tool: st.Tool, Args: st.Args, Done: st.Done, Took: st.Took}
		if st.Err != nil {
			e.Err = st.Err.Error()
		}
		s.add(se, e)
	}, func(said string) { s.add(se, Event{Kind: Say, Text: said}) })
	<-named
	end := Event{Kind: Answer, Text: reply}
	switch {
	case err != nil && ctx.Err() != nil:
		end = Event{Kind: Canceled}
	case err != nil:
		end = Event{Kind: Failed, Text: err.Error()}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	se.cancel()
	se.cancel = nil
	p := s.node
	p.mu.Lock()
	defer p.mu.Unlock()
	p.answering--
	// The answer is archived already: who learns of its end finds it there.
	s.emit(se, end)
	if se.restart != "" {
		s.emit(se, Event{Kind: Restart, Text: se.restart})
		if p.successor == "" { // the first build asked for is the one
			p.successor = se.restart
		}
		se.restart = ""
	}
	p.settle()
}

// name asks the agent for the title of se, with what was said in it, while
// the answer to the last message runs, and tells it once it has one: it is
// kept along with the session. named is closed then.
func (s *Service) name(ctx context.Context, se *session, conversation []openai.Message, named chan<- struct{}) {
	defer close(named)
	asking, stop := context.WithTimeout(ctx, titleTime)
	defer stop()
	title, err := s.agent.Title(asking, conversation)
	if len(title) > history.MaxTitle {
		title = strings.ToValidUTF8(title[:history.MaxTitle], "")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.archive != nil && err != nil && ctx.Err() == nil {
		s.archive.log("the session %s cannot be named: %v", se.id, err)
	}
	if title == "" {
		return
	}
	se.title = title
	s.emit(se, Event{Kind: Title, Text: title})
	if s.archive != nil {
		if err := s.archive.SetTitle(history.Owner(se.id), title); err != nil {
			s.archive.log("the title of the session %s cannot be kept: %v", se.id, err)
		}
	}
}

// said is what the owner and the agent said in events: the messages and the
// answers.
func said(events []Event) []openai.Message {
	var msgs []openai.Message
	for _, e := range events {
		switch e.Kind {
		case Message:
			msgs = append(msgs, openai.Message{Role: "user", Content: e.Text})
		case Answer:
			msgs = append(msgs, openai.Message{Role: "assistant", Content: e.Text})
		}
	}
	return msgs
}

// add tells what happened in se.
func (s *Service) add(se *session, e Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.emit(se, e)
}

// emit records e in se and hands it to those who watch it. A watcher that
// fell too far behind is let go: it watches again and gets everything anew.
// s.mu is held.
func (s *Service) emit(se *session, e Event) {
	e.At = s.now().Round(0) // as it is written, without the reading of the clock of this process
	e.Context = s.agent.Context(se.id)
	e.Cache = s.agent.Cache(se.id)
	se.events = append(se.events, e)
	for ch := range se.watchers {
		select {
		case ch <- e:
		default:
			delete(se.watchers, ch)
			close(ch)
		}
	}
}

// settle does what waits for no answer to be on its way: the node is
// replaced by a new build, or stopped. p.mu is held.
func (p *process) settle() {
	if p.answering > 0 {
		return
	}
	switch {
	case p.successor != "":
		p.replace(p.successor)
	case p.release && p.stopNode != nil:
		p.release = false
		p.stopNode()
	}
}

// Watch returns what happened in the session id and what happens from now
// on, until ctx ends.
func (s *Service) Watch(ctx context.Context, id string) ([]Event, <-chan Event, error) {
	if err := valid(id); err != nil {
		return nil, nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, nil, ErrStopped
	}
	se := s.get(id)
	s.node.mu.Lock()
	s.node.release = false // somebody wants the node
	s.node.mu.Unlock()
	live := make(chan Event, watchBuffer)
	se.watchers[live] = struct{}{}
	context.AfterFunc(ctx, func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if _, ok := se.watchers[live]; ok {
			delete(se.watchers, live)
			close(live)
		}
		s.forget(se)
	})
	return slices.Clone(se.events), live, nil
}

// Cancel stops the answer on its way in the session id.
func (s *Service) Cancel(_ context.Context, id string) error {
	if err := valid(id); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if se, ok := s.sessions[id]; ok && se.cancel != nil {
		se.cancel()
	}
	return nil
}

// Delete removes the session id, unless an answer is on its way in it. Those
// who watch it are let go: watched again, it is empty.
func (s *Service) Delete(_ context.Context, id string) error {
	if peerID(id) {
		return s.deletePeer(id)
	}
	if err := valid(id); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	found := false
	if se, ok := s.sessions[id]; ok && len(se.events) > 0 {
		if se.cancel != nil {
			return ErrBusy
		}
		found = true
		for ch := range se.watchers {
			delete(se.watchers, ch)
			close(ch)
		}
		delete(s.sessions, id)
	}
	s.agent.Forget(id)
	if s.archive != nil {
		err := s.archive.Delete(history.Owner(id))
		if err != nil && !errors.Is(err, history.ErrNotArchived) {
			return err
		}
		found = found || err == nil
	}
	if !found {
		return ErrNoSession
	}
	return nil
}

// List describes the sessions that have a message, the latest first: those
// in memory and those kept.
func (s *Service) List(context.Context) ([]Info, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var list []Info
	for _, se := range s.sessions {
		if len(se.events) == 0 {
			continue
		}
		title := cmp.Or(se.title, se.events[0].Text)
		if len(title) > maxTitle {
			title = strings.ToValidUTF8(title[:maxTitle], "")
		}
		list = append(list, Info{ID: se.id, Title: title, Running: se.cancel != nil, Last: se.events[len(se.events)-1].At})
	}
	if s.archive != nil {
		entries, err := s.archive.List()
		if err != nil {
			s.archive.log("the archive cannot be listed: %v", err)
		}
		for _, e := range entries {
			id, ok := strings.CutPrefix(e.ID, "owner/")
			if se, known := s.sessions[id]; ok && (!known || len(se.events) == 0) {
				list = append(list, Info{ID: id, Title: e.Title, Last: e.Modified})
			}
		}
	}
	slices.SortFunc(list, func(a, b Info) int {
		if c := b.Last.Compare(a.Last); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return list, nil
}

// Contents returns what the next call of the LLM in the session id is sent:
// the system prompt, every tool with its schema and the conversation so far,
// and what the plugins that prepared it reported.
func (s *Service) Contents(_ context.Context, id string) ([]Line, error) {
	if err := valid(id); err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.lookup(id) // the agent remembers a session that is kept once it is loaded
	s.mu.Unlock()
	c, err := s.agent.Contents(id)
	var lines []Line
	if c.System != "" {
		lines = append(lines, Line{Role: "system", Text: c.System})
	}
	for _, t := range c.Tools {
		lines = append(lines, Line{Role: "tool " + t.Function.Name,
			Text: t.Function.Description + "\nparameters: " + string(t.Function.Parameters)})
	}
	for _, m := range c.Messages {
		text := m.Content
		for _, call := range m.ToolCalls {
			text += "\n→ " + call.Function.Name + " " + call.Function.Arguments
		}
		lines = append(lines, Line{Role: m.Role, Text: strings.TrimSpace(text)})
	}
	if snapshot, ok := s.agent.Inspection(id); ok {
		for _, d := range snapshot.Diagnostics {
			lines = append(lines, Line{Role: "plugin diagnostic", Text: fmt.Sprintf("%s %s failed: %v", d.Plugin, d.Phase, d.Err)})
		}
	}
	return lines, err
}

// Open reports that the node runs: the Service is in its process.
func (s *Service) Open(context.Context) (bool, error) { return false, nil }

// Release lets the node stop once no answer is on its way.
func (s *Service) Release(context.Context) error {
	s.Releases()
	return nil
}

// Releases is Release, and reports whether the node stops now: no answer
// keeps it running.
func (s *Service) Releases() bool {
	p := s.process()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.release = true
	stops := p.answering == 0 && p.successor == "" && p.stopNode != nil
	p.settle()
	return stops
}

// Close stops the answers on their way and returns once they ended. The
// watchers are let go, and the Service takes nothing more.
func (s *Service) Close() {
	s.mu.Lock()
	s.closed = true
	for _, se := range s.sessions {
		for ch := range se.watchers {
			delete(se.watchers, ch)
			close(ch)
		}
	}
	s.mu.Unlock()
	s.stop()
	s.answers.Wait()
}

// Await asks for the answer to text in the session id of api and waits for
// it: the answer, or the error it failed with. When ctx ends on the way, the
// answer is stopped, and Await returns once it is, with the error of ctx.
func Await(ctx context.Context, api API, id, text string) (string, error) {
	// The answer is the node's: it is asked for, and followed, whether ctx
	// ends or not.
	keep := context.WithoutCancel(ctx)
	watch, stop := context.WithCancel(keep)
	defer stop()
	_, live, err := api.Watch(watch, id)
	if err != nil {
		return "", err
	}
	if err := api.Ask(keep, id, text); err != nil {
		return "", err
	}
	done := ctx.Done()
	for {
		select {
		case <-done:
			done = nil
			if err := api.Cancel(keep, id); err != nil {
				return "", err
			}
		case e, ok := <-live:
			if !ok {
				return "", errLost
			}
			switch e.Kind {
			case Answer:
				return e.Text, nil
			case Failed:
				return "", errors.New(e.Text)
			case Canceled:
				return "", cmp.Or(ctx.Err(), context.Canceled)
			}
		}
	}
}

// errLost means that the node was gone before the answer was given.
var errLost = errors.New("the node stopped before the answer was given")
