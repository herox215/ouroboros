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
	"ouroboros/internal/config"
	"ouroboros/internal/history"
	"ouroboros/internal/openai"
)

// Kind is what an Event tells.
type Kind string

// The kinds of Events.
const (
	// Message is a message of the owner. An answer to it starts, or the one
	// on its way goes on with it: then it was Queued before.
	Message Kind = "message"
	// Queued is a message of the owner that waits for the answer on its way
	// to take it, see API.Steer: it follows as a Message once it does.
	Queued Kind = "queued"
	// Unqueued is the last message that was Queued, taken back: see
	// API.Unsteer.
	Unqueued Kind = "unqueued"
	// Say is what the agent says along with the tools it asks for, "" if it
	// said nothing: it begins a round of the loop to the answer, before the
	// Steps of those tools.
	Say Kind = "say"
	// Step is a call of a tool that starts, or with Done, that ended.
	Step Kind = "step"
	// Asked is a question the agent asks the owner on its way to the answer,
	// in Args as an assistant.Question, and waits for: what the owner says
	// next in the session answers it, see API.Steer. With Done the wait is
	// over, and Text is what the owner answered, "" if they did not.
	Asked Kind = "asked"
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
	// Effort is how much the model thinks in the session, in Text, as the
	// owner set it with SetEffort; "" leaves it to the model. Who watches a
	// session that has one gets it with what happened in it so far, last.
	// Without one the session has the effort of the node.
	Effort Kind = "effort"
	// Jobs are the commands that run in the background for the answer on
	// its way, one per line in Text, "" once none does. They end with the
	// answer at the latest: then Jobs is "" before it ends, if it was not.
	Jobs Kind = "jobs"
)

// Event is one thing that happened in a session. It is plain data, written
// as JSON on the socket.
type Event struct {
	Kind Kind      `json:"kind"`
	At   time.Time `json:"at"`
	// Text is the message, what the agent said, the answer, why it failed,
	// or the binary of a Restart.
	Text string `json:"text,omitempty"`
	// Tool, Args, Done, Took, Err and Remark are of a Step: the call of
	// Tool with Args, and once Done, how long it took, why it failed, if it
	// did, and what the tool said of it for the owner. Args and Done are
	// also of a question, see Asked.
	Tool   string          `json:"tool,omitempty"`
	Args   json.RawMessage `json:"args,omitempty"`
	Done   bool            `json:"done,omitempty"`
	Took   time.Duration   `json:"took,omitempty"`
	Err    string          `json:"err,omitempty"`
	Remark string          `json:"remark,omitempty"`
	// Context is how long the context of the session is when the Event
	// happens: what the last call of the LLM in it sent, in bytes, 0 if
	// none was made yet.
	Context int `json:"context,omitempty"`
	// Cache is how much of what the LLM was sent in the session the
	// provider had cached when the Event happens, see assistant.Cache.
	Cache assistant.Cache `json:"cache,omitzero"`
}

// AsQuestion is the question an Asked that is not Done tells of: what the
// agent asks the owner, and the answers to choose from.
func (e Event) AsQuestion() assistant.Question {
	q, _ := assistant.ParseQuestion(e.Args) // the tool asks nothing it could not read
	return q
}

// AsStep is the call of a tool a Step tells of, as the plugin of the tool
// shows it, see plugin.ToolLine.
func (e Event) AsStep() assistant.Step {
	s := assistant.Step{Tool: e.Tool, Args: e.Args, Done: e.Done, Took: e.Took, Remark: e.Remark}
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
	// Steer gives text to the answer that is on its way in the session id,
	// which takes it as soon as it can without losing what the provider
	// cached of the conversation: after the tools it waits for, else as the
	// next message, once the answer is given. Until then it is queued.
	// While the agent waits for the owner to answer a question, see Asked,
	// text is that answer, and nothing is queued. Without an answer on its
	// way it is Ask.
	Steer(ctx context.Context, id, text string) error
	// Unsteer takes back the last message that is queued in the session id
	// and returns it: "" if none is, as when the answer took it meanwhile.
	Unsteer(ctx context.Context, id string) (string, error)
	// Watch returns what happened in the session id so far, and what
	// happens from now on until ctx ends. The channel is closed when ctx
	// ends, when the node stops, or when the watcher falls too far behind:
	// whoever watches again gets the whole session anew.
	Watch(ctx context.Context, id string) (past []Event, live <-chan Event, err error)
	// Cancel stops the answer that is on its way in the session id, if
	// there is one.
	Cancel(ctx context.Context, id string) error
	// SetEffort makes effort, one of config.AllEfforts, how much the model
	// thinks in the session id from its next call on, whatever the node
	// says: the Event Effort tells it. It fails with ErrNoEffort for one
	// that is none.
	SetEffort(ctx context.Context, id, effort string) error
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
	// Jobs are the commands that run in the background for the answers on
	// their way, the oldest first: in the sessions and in the
	// conversations of peers.
	Jobs(ctx context.Context) ([]Job, error)
	// StopJob stops the job n of the answer on its way in the session or
	// conversation of a peer id, as Jobs names them, with everything it
	// started. It fails with ErrNoJob if that job does not run.
	StopJob(ctx context.Context, id string, n int) error
	// Services are the services that run past the answers, like servers,
	// the oldest first: those of the owner and those of peers.
	Services(ctx context.Context) ([]ServiceInfo, error)
	// StopService stops the service id, as Services names it, with
	// everything it started. It fails with ErrNoService if it does not run.
	StopService(ctx context.Context, id string) error
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
	// ErrNoEffort means an effort that is none of config.AllEfforts.
	ErrNoEffort = errors.New("no such effort")
	// ErrEmpty means a message without text.
	ErrEmpty = errors.New("the message is empty")
	// errAsked means that the agent asked the owner while a question of it
	// still waits for its answer.
	errAsked = errors.New("a question to the user waits for its answer already: one at a time")
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
	Effort() string
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
	jobs     map[string]running // of the answers on their way, by session or conversation of a peer, see Jobs
	services func() []Services  // that run past the answers, see ServicesFrom
	archive  *archive           // where the sessions are kept; nil keeps none, see Archive
	closed   bool               // by Close
	node     *process           // s.mu is held to read it
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
	kept      bool // the owner wants the node to run: no front end stops it
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
	queue    []string           // the messages that wait for that answer to take them
	asked    chan string        // takes what the owner answers the question that answer waits for; nil without one
	restart  string             // the binary a tool of that answer asked to start anew from
	goOn     bool               // the session goes on by itself in that build, see GoOn
	// resumed is when the last answer ended of a session that was loaded
	// from where it is kept, until the agent is told so with the next
	// message; zero otherwise.
	resumed time.Time
	// title names the session for its topic, "" until the agent named it;
	// tries is for how many messages it was asked to.
	title string
	tries int
	// effort is how much the model thinks in the session, once efforted
	// says that the owner set it: else the agent says.
	effort   string
	efforted bool
}

// New returns the Service of the sessions with agent.
func New(agent Assistant) *Service {
	ctx, stop := context.WithCancel(context.Background())
	return &Service{agent: agent, now: time.Now, ctx: ctx, stop: stop, sessions: map[string]*session{}, jobs: map[string]running{}, node: &process{}}
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
		s.effortOf(se) // set before anything was said in it
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
	if len(se.events) == 0 && len(se.watchers) == 0 && se.cancel == nil && !se.efforted {
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
	s.begin(se, p, text)
	return nil
}

// begin starts the answer to texts, the messages of the owner, in se. s.mu
// and the mu of p, the process of s, are held.
func (s *Service) begin(se *session, p *process, texts ...string) {
	ctx, cancel := context.WithCancel(s.ctx)
	se.cancel = cancel
	p.answering++
	p.release = false // somebody wants the node
	for _, text := range texts {
		s.emit(se, Event{Kind: Message, Text: text})
	}
	text := strings.Join(texts, "\n\n")
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
}

// Steer queues text for the answer on its way in the session id, and asks
// for an answer to it if none is.
func (s *Service) Steer(ctx context.Context, id, text string) error {
	if err := valid(id); err != nil {
		return err
	}
	if strings.TrimSpace(text) == "" {
		return ErrEmpty
	}
	s.mu.Lock()
	if se, ok := s.sessions[id]; ok && se.cancel != nil {
		defer s.mu.Unlock()
		if se.asked != nil {
			// What the owner says now answers the question: ask tells it.
			se.asked <- text
			se.asked = nil
			return nil
		}
		se.queue = append(se.queue, text)
		s.emit(se, Event{Kind: Queued, Text: text})
		return nil
	}
	s.mu.Unlock()
	return s.Ask(ctx, id, text)
}

// Unsteer takes back the last message that is queued in the session id.
func (s *Service) Unsteer(_ context.Context, id string) (string, error) {
	if err := valid(id); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	se, ok := s.sessions[id]
	if !ok || len(se.queue) == 0 {
		return "", nil
	}
	last := len(se.queue) - 1
	text := se.queue[last]
	se.queue = se.queue[:last]
	s.emit(se, Event{Kind: Unqueued, Text: text})
	return text, nil
}

// ask asks the owner question for the answer on its way in se, and waits
// for what they say next in the session, see Steer: that is returned. The
// question is told to those who watch the session, and so is how the wait
// ended. It ends without an answer, with the error of ctx, when ctx does: the
// answer was stopped, or the time of the tool that asks is over.
func (s *Service) ask(ctx context.Context, se *session, question json.RawMessage) (string, error) {
	s.mu.Lock()
	if se.asked != nil {
		s.mu.Unlock()
		return "", errAsked
	}
	asked := make(chan string, 1)
	se.asked = asked
	s.emit(se, Event{Kind: Asked, Args: question})
	s.mu.Unlock()
	var answer string
	select {
	case answer = <-asked:
	case <-ctx.Done():
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if se.asked == asked {
		se.asked = nil // nobody answers it any more
	}
	close(asked)
	if answer == "" {
		answer = <-asked // what the owner said just when ctx ended, if anything
	}
	s.emit(se, Event{Kind: Asked, Done: true, Text: answer})
	if answer == "" {
		return "", ctx.Err()
	}
	return answer, nil
}

// steered hands the answer on its way in se the messages that are queued
// for it: each is a Message now.
func (s *Service) steered(se *session) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	texts := se.queue
	se.queue = nil
	for _, text := range texts {
		s.emit(se, Event{Kind: Message, Text: text})
	}
	return texts
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
		ctx = WithGoOn(ctx, func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			se.goOn = true
		})
	}
	// What a tool tells of the jobs of the answer once it is over is not
	// told: they end with it.
	var jobs string
	over := false
	ctx = WithJobs(ctx, func(running []Job, stop func(int) bool) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if over {
			return
		}
		s.setJobs(se.id, running, stop)
		if text := jobLines(running); text != jobs {
			jobs = text
			s.emit(se, Event{Kind: Jobs, Text: text})
		}
	})
	ctx = agent.WithSteering(ctx, func() []string { return s.steered(se) })
	ctx = agent.WithOwner(ctx, func(ctx context.Context, question json.RawMessage) (string, error) {
		return s.ask(ctx, se, question)
	})
	ctx = openai.WithEffortOf(ctx, func() string { return s.effort(se) })
	reply, err := s.agent.Run(ctx, se.id, text, func(st assistant.Step) {
		e := Event{Kind: Step, Tool: st.Tool, Args: st.Args, Done: st.Done, Took: st.Took, Remark: st.Remark}
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
	over = true
	delete(s.jobs, se.id)
	if jobs != "" {
		s.emit(se, Event{Kind: Jobs})
	}
	// The answer is archived already: who learns of its end finds it there.
	s.emit(se, end)
	if se.restart != "" {
		s.emit(se, Event{Kind: Restart, Text: se.restart})
		if p.successor == "" { // the first build asked for is the one
			p.successor = se.restart
		}
		if se.goOn && s.archive != nil {
			if err := s.archive.GoOn(history.Owner(se.id)); err != nil {
				s.archive.log("the session %s does not go on by itself in the new build: %v", se.id, err)
			}
		}
		se.restart = ""
	}
	se.goOn = false
	// What was queued too late for this answer is the next message, unless
	// the node takes none: then it is lost with the process.
	if len(se.queue) > 0 && !s.closed && p.successor == "" {
		texts := se.queue
		se.queue = nil
		s.begin(se, p, texts...)
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
	s.tell(se, e)
}

// tell hands e to those who watch se. A watcher that fell too far behind is
// let go: it watches again and gets everything anew. s.mu is held.
func (s *Service) tell(se *session, e Event) {
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
	past := slices.Clone(se.events)
	if se.efforted {
		past = append(past, s.effortEvent(se))
	}
	return past, live, nil
}

// SetEffort makes effort, one of config.AllEfforts, how much the model thinks
// in the session id, from its next call on: also in the answer on its way.
// It is kept along with the session, and told to who watches it.
func (s *Service) SetEffort(_ context.Context, id, effort string) error {
	if err := valid(id); err != nil {
		return err
	}
	if !slices.Contains(config.AllEfforts, effort) {
		return fmt.Errorf("%w: %q, there are %s", ErrNoEffort, effort, strings.Join(config.AllEfforts[1:], ", "))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrStopped
	}
	se := s.get(id)
	se.effort, se.efforted = effort, true
	s.tell(se, s.effortEvent(se))
	if s.archive != nil {
		if err := s.archive.SetEffort(history.Owner(id), effort); err != nil {
			s.archive.log("the effort of the session %s cannot be kept: %v", id, err)
		}
	}
	return nil
}

// effortEvent tells the effort of se, with the size of its context and
// what was cached as the last Event told them. s.mu is held.
func (s *Service) effortEvent(se *session) Event {
	e := Event{Kind: Effort, At: s.now().Round(0), Text: se.effort}
	if n := len(se.events); n > 0 {
		e.Context, e.Cache = se.events[n-1].Context, se.events[n-1].Cache
	}
	return e
}

// effort is how much the model thinks in se now: as the owner set it, or as
// the agent says.
func (s *Service) effort(se *session) string {
	s.mu.Lock()
	effort, own := se.effort, se.efforted
	s.mu.Unlock()
	if own {
		return effort
	}
	return s.agent.Effort()
}

// effortOf gives se the effort it is kept with, if it is. s.mu is held.
func (s *Service) effortOf(se *session) {
	if s.archive == nil {
		return
	}
	effort, ok, err := s.archive.Effort(history.Owner(se.id))
	if err != nil {
		s.archive.log("the effort of the session %s cannot be read: %v", se.id, err)
	}
	se.effort, se.efforted = effort, ok
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
	if p.kept {
		return false
	}
	p.release = true
	stops := p.answering == 0 && p.successor == "" && p.stopNode != nil
	p.settle()
	return stops
}

// Keep makes the node run on: the owner started it, as with ouro start, and
// a front end that started it before and lets go of it does not stop it.
func (s *Service) Keep() {
	p := s.process()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.kept, p.release = true, false
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
// answer is stopped, and Await returns once it is, with the error of ctx. A
// question the agent asks the owner on the way is answered with Unattended.
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
			case Asked:
				// Nobody sees the question here: the agent is told so, and
				// does not wait for an answer that cannot come. Where it
				// cannot be told, the question ends with the tool that asked.
				if !e.Done {
					api.Steer(keep, id, Unattended)
				}
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

// Unattended is what a question of the agent to the owner is answered with
// where nobody is shown it, as by Await.
const Unattended = "Nobody can be asked here: this conversation shows no questions. " +
	"Go on as you think best, and say in your answer what you assumed."

// errLost means that the node was gone before the answer was given.
var errLost = errors.New("the node stopped before the answer was given")
