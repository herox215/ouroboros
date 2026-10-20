// Package agent turns an LLM into a conversational agent with per-context
// memory that can use tools on the way to its answer.
package agent

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ouroboros/internal/a2a"
	"ouroboros/internal/openai"
	"ouroboros/internal/presentation"
)

// emptyNote is what a model that answered with nothing at all is told.
const emptyNote = "[Your last message was empty, which is no answer: go on with the task, or answer with what is done and what is not.]"

const (
	maxContexts = 256               // conversations kept in memory
	maxRounds   = 100               // LLM calls per answer; stops an agent that keeps calling tools
	warnRounds  = maxRounds * 4 / 5 // LLM calls after which the LLM is told how many it has left
	// askRounds are the LLM calls after which the LLM is told to ask the
	// user rather than search on, if it has not found what it looks for:
	// what it is to look for may not be clear, and the user knows.
	askRounds = 15
	// maxRepeats is how often in a row the LLM may ask for the same call of
	// a tool: one more gives the same result and is not run.
	maxRepeats = 2

	// A conversation is cut once it has more than maxHistory messages or
	// maxHistoryBytes, and then down to keptHistory messages and
	// keptHistoryBytes. Cut in steps, its start stays the same from one
	// answer to the next until the next cut, and so does what the provider
	// cached of it. As a cut costs that cache, it is only made when it
	// leaves at most half, or beyond hugeHistoryBytes.
	maxHistory       = 80
	maxHistoryBytes  = 256 << 10
	keptHistory      = 40
	keptHistoryBytes = 128 << 10
	hugeHistoryBytes = 1 << 20
	// maxImages is how many pictures a conversation keeps, the last ones
	// the tools showed. They count for none of the bytes above: a single
	// picture would cut all that came before it.
	maxImages = 8

	// Budget is how many bytes the results of tools of one answer may have
	// before the LLM is told with every call how much it gathered, and the
	// tools are to give back less: see Spent.
	Budget = 128 << 10

	// minRepeated is how many bytes of lines a result of a tool has to repeat
	// of an earlier result of the same answer before they are left out of it.
	minRepeated = 1 << 10

	// abandoned ends an answer that failed or was stopped in the history, so
	// that the next message does not follow tool results.
	abandoned = "[This answer broke off here, before it was given. " +
		"The work above is done: go on from it when you are asked again.]"
)

// ErrTooManyToolCalls is the error of an answer that was given up because the
// LLM kept asking for tools.
var ErrTooManyToolCalls = errors.New("agent: gave up after too many tool calls")

// LLM generates a reply for a conversation or asks for tools to be run.
// openai.Client implements it; another provider has to translate to and from
// the OpenAI message types.
type LLM interface {
	Complete(ctx context.Context, model string, msgs []openai.Message, tools []openai.Tool) (openai.Message, error)
}

// Step reports a tool call: once when it starts and again, with Done set,
// when its result is in, together with how long the call took.
type Step struct {
	// ID identifies the archived tool call.
	ID     string
	Tool   string
	Args   json.RawMessage
	Done   bool
	Result string
	Err    error
	Took   time.Duration
	// Remark is what the tool said of the call for the owner, see Remark.
	Remark string
}

// Call reports a call of the LLM: once when it starts and again, with Done
// set, when it has ended, with how long it took and whether it ended in a
// text, in calls of tools or in an error. Content is the text the LLM sent,
// also along with calls of tools. Sent is how many bytes of messages and
// tools the call sent the LLM, as JSON: what an answer costs grows with it.
// Tokens and Cached are what the API said of the call that has ended: the
// tokens it was sent, and how many of them it had cached. 0 if it did not say.
// Thought is how many bytes the LLM thought before it wrote, which go back
// with the calls after it.
type Call struct {
	Sent    int
	Tokens  int
	Cached  int
	Output  int
	CostUSD *float64
	InPlan  bool
	Thought int
	// Via is what answered instead of the access and the model of the agent,
	// and Instead why those could not: see openai.Message.
	Via, Instead string
	// Retried is why the call was asked again, each time, see
	// openai.Message; "" if it was not.
	Retried string
	// Effort is what the API took of the effort, see openai.Message.
	Effort    openai.Took
	Done      bool
	Took      time.Duration
	ToolCalls int
	Content   string
	Err       error
}

// Observer is told what an agent does on the way to an answer. Either field
// may be nil.
type Observer struct {
	Diagnostic func(Diagnostic)
	Step       func(Step) // every tool call
	Call       func(Call) // every call of the LLM
}

type (
	contextKey         struct{}
	observerKey        struct{}
	spentKey           struct{}
	steeringKey        struct{}
	messageSteeringKey struct{}
	inputMessagesKey   struct{}
	ownerKey           struct{}
	answerKey          struct{}
	imagesKey          struct{}
	remarkKey          struct{}
)

// remark is what a tool says of its call for the owner.
type remark struct {
	mu   sync.Mutex
	text string
}

// Remark gives the owner a short remark on the call of the tool that ctx is
// run for, which is shown with how it ended: the size a picture was shown
// in, say. The LLM is not told it. A later remark replaces an earlier one,
// and outside of a call it goes nowhere: Remark reports false then.
func Remark(ctx context.Context, text string) bool {
	r, ok := ctx.Value(remarkKey{}).(*remark)
	if !ok {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.text = text
	return true
}

// shownImages are the pictures a call of a tool shows the LLM.
type shownImages struct {
	mu     sync.Mutex
	images []openai.Image
}

// Show makes the LLM see images along with the result of the call of a tool
// that ctx is run for: the result says in words what they are. It reports
// false outside of a call, where they would go nowhere.
func Show(ctx context.Context, images ...openai.Image) bool {
	s, ok := ctx.Value(imagesKey{}).(*shownImages)
	if !ok {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.images = append(s.images, images...)
	return true
}

// Answer is what lasts as long as one answer of an agent: what its tools
// keep from one call to the next, like the task its messages to peers
// belong to, and a context that ends with the answer, however it ends.
type Answer struct {
	reports reports
	values  sync.Map
	ctx     context.Context
	ended   atomic.Bool // by End
	// kept is what the tools of the round keep, see Keep.
	keptMu sync.Mutex
	kept   []openai.Message
}

// End makes the answer of ctx end once the tools of its round ran, without a
// further call of the LLM: what they did is all of it, and it answers "".
// What the user said meanwhile is still taken, by a further call. It reports
// whether ctx is of an answer.
func End(ctx context.Context) bool {
	a := AnswerOf(ctx)
	if a == nil {
		return false
	}
	a.ended.Store(true)
	return true
}

// AnswerOf returns the answer a tool is run for under ctx, nil outside of
// one.
func AnswerOf(ctx context.Context) *Answer {
	a, _ := ctx.Value(answerKey{}).(*Answer)
	return a
}

// Value returns what the answer keeps under key: what make returns the
// first time it is asked for.
func (a *Answer) Value(key any, make func() any) any {
	if v, ok := a.values.Load(key); ok {
		return v
	}
	v, _ := a.values.LoadOrStore(key, make())
	return v
}

// Context ends when the answer does: given, failed or stopped. The context
// of a call of a tool may end before.
func (a *Answer) Context() context.Context { return a.ctx }

// WithSteering returns a context under which an answer takes what its user
// says while it is on its way: Run calls steer once the tools of a round of
// its loop ran, and what steer returns follows their results as messages of
// the user, before the next call of the LLM. They go at the end of the
// conversation and are remembered there, so what the provider cached of it
// stays as it is.
func WithSteering(ctx context.Context, steer func() []string) context.Context {
	return context.WithValue(ctx, steeringKey{}, steer)
}

// ErrNoOwner means that nobody is there to be asked: the conversation is
// not one of the owner in a session.
var ErrNoOwner = errors.New("there is no owner to ask in this conversation")

// WithOwner returns a context under which a tool can ask the owner of the
// conversation a question: ask is handed the question, as the tool wrote it,
// and returns what the owner answered once they did. It ends with the error
// of the context it is given when that ends first.
func WithOwner(ctx context.Context, ask func(ctx context.Context, question json.RawMessage) (string, error)) context.Context {
	return WithOwnerReply(ctx, func(ctx context.Context, question json.RawMessage) (OwnerReply, error) {
		text, err := ask(ctx, question)
		return OwnerReply{Text: text}, err
	})
}

// AskOwner asks the owner of the conversation of ctx question and waits for
// the answer: see WithOwner. Without an owner to ask it fails with
// ErrNoOwner.
func AskOwner(ctx context.Context, question json.RawMessage) (string, error) {
	reply, err := AskOwnerReply(ctx, question)
	return reply.Text, err
}

// CanAskOwner reports whether a question to the owner under ctx reaches
// someone, see WithOwner: false where AskOwner fails with ErrNoOwner.
func CanAskOwner(ctx context.Context) bool {
	ask, ok := ctx.Value(ownerKey{}).(func(context.Context, json.RawMessage) (OwnerReply, error))
	return ok && ask != nil
}

// talkKey keeps the conversation an answer started with, see Talk.
type talkKey struct{}

// Talk is the conversation the answer of ctx started with, up to the message
// it answers, nil outside of an answer: a tool can tell from it in what
// language the owner speaks.
func Talk(ctx context.Context) []openai.Message {
	talk, _ := ctx.Value(talkKey{}).([]openai.Message)
	return talk
}

// WithoutOwner returns a context under which nobody is asked: AskOwner fails
// with ErrNoOwner, whatever ctx had. It is for work the owner does not
// follow, like that of a helper.
func WithoutOwner(ctx context.Context) context.Context {
	return context.WithValue(ctx, ownerKey{}, nil)
}

// Steered returns what the user of the answer that ctx belongs to said since
// it was asked the last time: see WithSteering. Who is told does not hear it
// again.
func Steered(ctx context.Context) []string {
	if steer, ok := ctx.Value(messageSteeringKey{}).(func() []openai.Message); ok && steer != nil {
		var texts []string
		for _, m := range steer() {
			texts = append(texts, m.Content)
		}
		return texts
	}
	if steer, ok := ctx.Value(steeringKey{}).(func() []string); ok {
		return steer()
	}
	return nil
}

// Spent returns how many bytes the results of tools of the answer that ctx
// belongs to have so far. A tool that gives back much gives
// back less once it is beyond Budget.
func Spent(ctx context.Context) int {
	if spent, ok := ctx.Value(spentKey{}).(*atomic.Int64); ok {
		return int(spent.Load())
	}
	return 0
}

// ContextID returns the conversation a tool is run for.
func ContextID(ctx context.Context) string {
	id, _ := ctx.Value(contextKey{}).(string)
	return id
}

// Agent answers messages and remembers each conversation by context ID.
type Agent struct {
	llm        LLM
	model      string
	system     openai.Message
	tools      map[string]Tool
	specs      []openai.Tool
	sizeOf     int // of specs, as JSON
	memory     *Memory
	extensions extensions
}

// Memory keeps conversations by context ID. It may outlive the agent that
// has it: an agent built anew with the same memory goes on with them.
type Memory struct {
	mu            sync.Mutex
	clock         uint64
	active        map[string]*answerLock
	conversations map[string]*conversation
}

// NewMemory returns a memory that knows no conversation yet.
func NewMemory() *Memory {
	return &Memory{conversations: map[string]*conversation{}}
}

type conversation struct {
	// notes are what the user told since the last answer without asking,
	// see Agent.Note: the next answer starts with them.
	notes      []openai.Message
	lastUsed   uint64
	inspected  bool
	inspection Inspection
	msgs       []openai.Message
}

// New returns an agent that answers with model as told by the system prompt
// and may use tools.
func New(llm LLM, model, system string, tools ...Tool) *Agent {
	a := &Agent{
		llm:    llm,
		model:  model,
		system: openai.Message{Role: "system", Content: system},
		tools:  map[string]Tool{},
		memory: NewMemory(),
	}
	for _, t := range tools {
		a.tools[t.Name] = t
		a.specs = append(a.specs, openai.NewTool(t.Name, t.Description, t.Schema))
	}
	specs, _ := json.Marshal(a.specs) // texts and JSON: cannot fail
	a.sizeOf = len(specs)
	return a
}

// WithMemory makes the agent remember its conversations in m instead of a
// memory of its own, and returns it. It is for an agent that replaces
// another: given the memory of that one, it knows what it was told.
func (a *Agent) WithMemory(m *Memory) *Agent {
	a.memory = m
	return a
}

// WithSystem returns a copy with a system prompt for one answer. Memory and
// immutable tool definitions are shared; concurrent conversations are unaffected.
func (a *Agent) WithSystem(system string) *Agent {
	copy := *a
	copy.system = openai.Message{Role: "system", Content: system}
	return &copy
}

// WithTools returns a copy that may use tools alone, for one answer. Memory
// and the system prompt are shared; concurrent conversations are unaffected.
func (a *Agent) WithTools(tools ...Tool) *Agent {
	copy := *New(a.llm, a.model, a.system.Content, tools...)
	copy.memory, copy.extensions = a.memory, a.extensions
	return &copy
}

// Prompt returns what the agent sends with every call of the LLM before the
// conversation: its system prompt and its tools.
func (a *Agent) Prompt() (system string, tools []openai.Tool) {
	return a.system.Content, slices.Clone(a.specs)
}

// Respond answers text within the conversation contextID.
func (a *Agent) Respond(ctx context.Context, contextID, text string) (string, error) {
	return a.Run(ctx, contextID, text, Observer{})
}

// Run is like Respond and reports every call of the LLM and every tool call
// to observe.
func (a *Agent) Run(ctx context.Context, contextID, text string, observe Observer) (reply string, answerErr error) {
	ctx = presentation.Ensure(ctx)
	release, err := a.memory.acquire(ctx, contextID)
	if err != nil {
		return "", err
	}
	defer release()
	ctx, end := context.WithCancel(ctx)
	defer end() // after the hooks that are told how the answer ended
	ctx = context.WithValue(ctx, answerKey{}, &Answer{ctx: ctx})
	ctx = openai.WithSession(context.WithValue(ctx, contextKey{}, contextID), contextID)
	ctx = context.WithValue(ctx, observerKey{}, observe)
	spent := &atomic.Int64{}
	ctx = context.WithValue(ctx, spentKey{}, spent)
	// What the user told meanwhile without asking comes before what it asks.
	history := append(a.memory.history(contextID), a.memory.noted(contextID)...)
	asked := len(history) // where this answer starts
	input, ok := ctx.Value(inputMessagesKey{}).([]openai.Message)
	if !ok {
		input = []openai.Message{{Role: "user", Content: text, Source: openai.MessageSourceFrom(ctx)}}
	}
	history = append(history, copyMessages(input)...)
	ctx = context.WithValue(ctx, talkKey{}, copyMessages(history))
	origin, peer := a2a.OriginFrom(ctx)
	processing := Processing{a.llm, a.model, Conversation{contextID, peer, origin}}
	var contributions, events []openai.Message
	var diagnostics []Diagnostic
	emit := func(m openai.Message) error {
		events = append(events, copyMessages([]openai.Message{m})[0])
		if p := a.extensions.provider; p != nil && p.Observe != nil {
			if err := p.Observe(ctx, processing, copyMessages([]openai.Message{m})[0]); err != nil {
				return fmt.Errorf("context provider %s observe: %w", p.Name, err)
			}
		}
		return nil
	}
	defer func() {
		for _, h := range a.extensions.hooks {
			if h.After != nil {
				if err := h.After(ctx, AnswerOutcome{processing, copyMessages(events), reply, answerErr}); err != nil {
					d := Diagnostic{h.Name, "after", err}
					diagnostics = append(diagnostics, d)
					if observe.Diagnostic != nil {
						observe.Diagnostic(d)
					}
				}
			}
		}
		snapshot, _ := a.Inspection(contextID)
		snapshot.Diagnostics = diagnostics
		snapshot.Err = answerErr
		a.inspect(contextID, snapshot)
	}()

	// An answer that fails is remembered as far as it got: asked again, the
	// LLM goes on from there instead of starting over. Why it failed is not
	// part of it: a peer must not learn the cause from a later answer.
	abandon := func(err error) (string, error) {
		a.memory.remember(contextID, Abandon(history))
		return "", err
	}
	for _, m := range history[asked:] {
		if err := emit(m); err != nil {
			return abandon(err)
		}
	}
	for _, h := range a.extensions.hooks {
		if h.Before != nil {
			extra, err := h.Before(ctx, Preparation{processing, copyMessages(append([]openai.Message{a.system}, history...)), copyMessages(contributions)})
			if err != nil {
				return abandon(fmt.Errorf("answer hook %s before: %w", h.Name, err))
			}
			for _, m := range extra {
				if m.Role != "system" && m.Role != "user" {
					return abandon(fmt.Errorf("answer hook %s: context must have system or user role", h.Name))
				}
				if len(m.ToolCalls) > 0 || m.ToolCallID != "" {
					return abandon(fmt.Errorf("answer hook %s: context cannot contain tool calls", h.Name))
				}
			}
			contributions = append(contributions, copyMessages(extra)...)
		}
	}
	// Deliver only at round boundaries, never between a tool call and its
	// result. Completion is channel-driven; there is no timer or LLM polling.
	report := func() (bool, error) {
		texts := AnswerOf(ctx).reported()
		for _, text := range texts {
			said := openai.Message{Role: "user", Content: text}
			if err := emit(said); err != nil {
				return false, err
			}
			spent.Add(int64(len(text)))
			history = append(history, said)
		}
		return len(texts) > 0, nil
	}
	nudged := false // an empty message was answered with emptyNote
	for round := range maxRounds {
		// An answer that was stopped while a tool ran asks the LLM no more.
		if err := ctx.Err(); err != nil {
			return abandon(err)
		}
		msgs := sent(ctx, append([]openai.Message{a.system}, history...))
		if note := notes(round, int(spent.Load())); note != "" && msgs[len(msgs)-1].Role == "tool" {
			msgs = append(msgs, openai.Message{Role: "user", Content: note})
		}
		if p := a.extensions.provider; p != nil {
			prepared, err := p.Prepare(ctx, Preparation{processing, copyMessages(msgs), copyMessages(contributions)})
			if err == nil {
				err = validatePrepared(a.system, prepared)
			}
			if err != nil {
				err = fmt.Errorf("context provider %s prepare: %w", p.Name, err)
				a.inspect(contextID, Inspection{Err: err})
				return abandon(err)
			}
			msgs = copyMessages(prepared)
		} else if len(contributions) > 0 {
			msgs = append(append([]openai.Message{a.system}, copyMessages(contributions)...), msgs[1:]...)
		}
		if err := ctx.Err(); err != nil {
			return abandon(err)
		}
		msgs = openai.MessagesForModel(msgs)
		a.inspect(contextID, Inspection{Messages: msgs, Tools: a.specs})
		sent := 0
		if observe.Call != nil {
			b, _ := json.Marshal(msgs) // texts and JSON: cannot fail
			sent = len(b) + a.sizeOf
			observe.Call(Call{Sent: sent})
		}
		start := time.Now()
		msg, err := a.llm.Complete(ctx, a.model, msgs, a.specs)
		if err == nil {
			msg.PresentationID = presentation.Next(ctx)
		}
		if observe.Call != nil {
			observe.Call(Call{Sent: sent, Tokens: msg.Usage.Prompt, Cached: msg.Usage.Cached, Output: msg.Usage.Output, CostUSD: msg.Usage.CostUSD, InPlan: msg.Usage.InPlan, Thought: len(msg.Reasoning), Via: msg.Via, Instead: msg.Instead, Retried: strings.Join(msg.Retried, "; "), Effort: msg.Effort, Done: true, Took: time.Since(start),
				ToolCalls: len(msg.ToolCalls), Content: msg.Content, Err: err})
		}
		if err != nil {
			return abandon(err)
		}
		msg.Role = "assistant"
		msg.Source = openai.MessageSource{}
		history = append(history, msg)
		if err := emit(msg); err != nil {
			return abandon(err)
		}
		// A model that says nothing at all, as small ones do after a long
		// result, is asked once to go on: nothing is no answer.
		if len(msg.ToolCalls) == 0 && strings.TrimSpace(msg.Content) == "" && !nudged {
			nudged = true
			history = append(history, openai.Message{Role: "user", Content: emptyNote})
			continue
		}
		if len(msg.ToolCalls) == 0 {
			// A job may have finished while the model was answering. Let it
			// account for that result before ending the answer.
			if delivered, err := report(); err != nil {
				return abandon(err)
			} else if delivered {
				continue
			}
			a.memory.remember(contextID, history)
			return msg.Content, nil
		}
		before := history[asked : len(history)-1]
		toolCtx := withInputImages(ctx, history)
		for _, call := range msg.ToolCalls {
			result := repeated(call, before)
			if result.Content == "" {
				result = once(a.call(toolCtx, call), history)
			}
			if err := emit(result); err != nil {
				return abandon(err)
			}
			result.Content = unrepeated(result.Content, history[asked:])
			spent.Add(int64(len(result.Content)))
			history = append(history, result)
		}
		// What the tools kept follows all results: none may stand between
		// a call and its result.
		for _, m := range AnswerOf(ctx).takeKept() {
			if err := emit(m); err != nil {
				return abandon(err)
			}
			history = append(history, m)
		}
		delivered, err := report()
		if err != nil {
			return abandon(err)
		}
		// What the user said meanwhile follows the results.
		steered := steeredMessages(ctx)
		for _, said := range steered {
			if err := emit(said); err != nil {
				return abandon(err)
			}
			history = append(history, said)
		}
		// A tool that ended the answer ends it here, unless something came
		// meanwhile for the LLM to take.
		if AnswerOf(ctx).ended.Swap(false) && !delivered && len(steered) == 0 {
			a.memory.remember(contextID, history)
			return "", nil
		}
	}
	return abandon(ErrTooManyToolCalls)
}

// maxNotes is how many notes a conversation keeps for its next answer: of
// more, the oldest are dropped.
const maxNotes = 32

// Note makes the agent remember text as something the user of the
// conversation contextID said without asking for an answer. No LLM is called
// for it: the next answer in the conversation starts with it. The hooks are
// told of it as of an answer that is nothing but that message, so that it is
// kept where answers are, as presented with the current identity of ctx, if
// it has one: see presentation.As.
func (a *Agent) Note(ctx context.Context, contextID, text string) error {
	note := openai.Message{Role: "user", Content: text, Source: openai.MessageSourceFrom(ctx), PresentationID: presentation.Current(ctx)}
	a.memory.note(contextID, note)
	origin, peer := a2a.OriginFrom(ctx)
	told := AnswerOutcome{Processing{a.llm, a.model, Conversation{contextID, peer, origin}}, []openai.Message{note}, "", nil}
	var failed error
	for _, h := range a.extensions.hooks {
		if h.After != nil {
			if err := h.After(ctx, told); err != nil {
				failed = errors.Join(failed, fmt.Errorf("answer hook %s after: %w", h.Name, err))
			}
		}
	}
	return failed
}

// note keeps msg for the next answer of the conversation contextID.
func (m *Memory) note(contextID string, msg openai.Message) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.clock++
	c := m.conversations[contextID]
	if c == nil {
		c = &conversation{}
		m.conversations[contextID] = c
	}
	c.lastUsed = m.clock
	msg.At = cmp.Or(msg.At, time.Now())
	c.notes = append(c.notes, msg)
	c.notes = c.notes[max(0, len(c.notes)-maxNotes):]
	if len(m.conversations) > maxContexts {
		m.evictOldest()
	}
}

// noted returns the notes of the conversation contextID, which are part of
// its next answer from now on.
func (m *Memory) noted(contextID string) []openai.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	c := m.conversations[contextID]
	if c == nil {
		return nil
	}
	notes := c.notes
	c.notes = nil
	return notes
}

// Abandon returns the conversation msgs as it is remembered when its last
// answer failed or was stopped: as far as it got, its calls of tools that
// did not finish closed, and with a note that it broke off. An answer can
// fail between a call and its result, which the API would reject. Why it
// failed is not part of it, so that no later context leaks the cause. Nor
// are the pictures the tools showed: a model that takes none would fail with
// them again on every answer.
func Abandon(msgs []openai.Message) []openai.Message {
	msgs = slices.Clone(msgs)
	pending := []string{}
	for i, m := range msgs {
		msgs[i].Images = nil
		for _, call := range m.ToolCalls {
			pending = append(pending, call.ID)
		}
		if m.Role == "tool" {
			pending = slices.DeleteFunc(pending, func(id string) bool { return id == m.ToolCallID })
		}
	}
	for _, id := range pending {
		msgs = append(msgs, openai.Message{Role: "tool", ToolCallID: id, Content: "[This call did not finish.]"})
	}
	return append(msgs, openai.Message{Role: "assistant", Content: abandoned})
}

// notes is what the LLM is told after the results of the tools when round,
// counted from 0, is the call of the LLM and the results of the answer have
// spent bytes: "" if nothing. It goes in a message of its own after them, and
// is only sent, not remembered: the next call tells the next numbers. The
// results stay as they are remembered, and with them what the provider
// cached of the conversation.
func notes(round, spent int) string {
	var notes []string
	if spent > Budget {
		notes = append(notes, gathered(spent))
	}
	// Once: an agent that knows what to do and does it, as one that builds
	// what it needs, is not kept from it with every call.
	if round == askRounds {
		notes = append(notes, searching(round))
	}
	if round >= warnRounds {
		notes = append(notes, remaining(round))
	}
	return strings.Join(notes, "\n\n")
}

// remaining is what the LLM is told when round, counted from 0, is one of the
// last calls of the LLM for an answer.
func remaining(round int) string {
	return fmt.Sprintf("[Not from the user: this is call %d of at most %d of the LLM for this answer. "+
		"Without an answer by then, it breaks off. Finish the work that is begun, "+
		"and answer with what is done and what is still open: asked again, you go on from there.]",
		round+1, maxRounds)
}

// searching is what the LLM is told when round, counted from 0, is
// askRounds.
func searching(round int) string {
	return fmt.Sprintf("[Not from the user: this is call %d of the LLM for this answer. "+
		"If you are still looking for what the user means, stop searching: "+
		"tell the user in a few words what you checked and what you found, and ask what exactly they mean. "+
		"If you know what to do, go on until it is done.]", round+1)
}

// repeated returns the result of call when it is the same as each of the
// last maxRepeats calls in msgs, which the LLM asked for in a row: it is not
// run again. Otherwise it returns a message without content.
func repeated(call openai.ToolCall, msgs []openai.Message) openai.Message {
	same := 0
	for _, m := range slices.Backward(msgs) {
		if m.Role == "tool" {
			continue
		}
		if m.Role != "assistant" || len(m.ToolCalls) == 0 {
			break
		}
		last := m.ToolCalls[len(m.ToolCalls)-1]
		if last.Function.Name != call.Function.Name || last.Function.Arguments != call.Function.Arguments {
			break
		}
		if same++; same == maxRepeats {
			return openai.Message{Role: "tool", ToolCallID: call.ID, Content: fmt.Sprintf(
				"error: this is the call of %s you asked for %d times in a row, with the same arguments: "+
					"it was not run again, as it would give the same. Do something else, or answer with what you have.",
				call.Function.Name, maxRepeats+1)}
		}
	}
	return openai.Message{}
}

// once returns result without the pictures history shows already: its
// content names the call they came with instead. What the LLM saw once is
// not sent twice.
func once(result openai.Message, history []openai.Message) openai.Message {
	var kept []openai.Image
	for _, image := range result.Images {
		if call := shownWith(image, history); call != "" {
			result.Content += fmt.Sprintf("\n[The picture is the same as the one shown above with the result of the call %s: it is not sent again.]", call)
			continue
		}
		kept = append(kept, image)
	}
	result.Images = kept
	return result
}

// shownWith returns the call whose result in history shows image, "" if
// none does.
func shownWith(image openai.Image, history []openai.Message) string {
	for _, m := range history {
		for _, seen := range m.Images {
			if bytes.Equal(seen.Data, image.Data) {
				return m.ToolCallID
			}
		}
	}
	return ""
}

// gathered is what the LLM is told once the results of the tools of the
// answer have n bytes, beyond Budget.
func gathered(n int) string {
	return fmt.Sprintf("[Not from the user: the results of tools of this answer have %d KiB, "+
		"and all of it is sent again with every call. Reuse results already returned; "+
		"request smaller, bounded excerpts and fetch more only where needed.]", n>>10)
}

// unrepeated is result without the runs of its lines, of minRepeated bytes
// or more, that earlier results of tools in msgs have too: the LLM still has
// those, and the answer sends them again with every call. In their place a
// line says what was left out. Empty lines go along with a run. Only the
// results of the same answer count: what came before it may be cut from the
// conversation, while the answer is kept whole.
func unrepeated(result string, msgs []openai.Message) string {
	if len(result) < minRepeated {
		return result
	}
	seen := map[string]bool{}
	for _, m := range msgs {
		if m.Role == "tool" {
			for line := range strings.Lines(m.Content) {
				if line = strings.TrimRight(line, "\r\n"); strings.TrimSpace(line) != "" {
					seen[line] = true
				}
			}
		}
	}
	if len(seen) == 0 {
		return result
	}
	lines := strings.SplitAfter(result, "\n")
	var out strings.Builder
	for i := 0; i < len(lines); {
		j, repeated := i, i
		for ; j < len(lines); j++ {
			line := strings.TrimRight(lines[j], "\r\n")
			blank := strings.TrimSpace(line) == ""
			if !blank && !seen[line] {
				break
			}
			if !blank {
				repeated = j + 1 // a run ends with a line that was seen
			}
		}
		if repeated == i {
			out.WriteString(lines[i])
			i++
			continue
		}
		// A shorter run that starts within this one ends with it: none is
		// left out if this one is not.
		run := strings.Join(lines[i:repeated], "")
		if len(run) >= minRepeated {
			fmt.Fprintf(&out, "[%d lines, %d bytes, left out: an earlier result of this answer has them]\n", repeated-i, len(run))
		} else {
			out.WriteString(run)
		}
		i = repeated
	}
	return out.String()
}

// call runs one tool call and returns its result as a message for the LLM.
func (a *Agent) call(ctx context.Context, call openai.ToolCall) openai.Message {
	name, args := call.Function.Name, json.RawMessage(call.Function.Arguments)
	tool, ok := a.tools[name]
	if !ok {
		tool = Tool{Name: name, Run: func(context.Context, json.RawMessage) (string, error) {
			return "", errors.New("unknown tool " + name)
		}}
	}
	shown := &shownImages{}
	content, err := invoke(context.WithValue(ctx, imagesKey{}, shown), tool, args, call.ID)
	if err != nil {
		return openai.Message{Role: "tool", ToolCallID: call.ID, Content: "error: " + err.Error()}
	}
	return openai.Message{Role: "tool", ToolCallID: call.ID, Content: content, Images: shown.images}
}

func invoke(ctx context.Context, tool Tool, args json.RawMessage, id string) (string, error) {
	observe, _ := ctx.Value(observerKey{}).(Observer)
	step := Step{ID: id, Tool: tool.Name, Args: args}
	if observe.Step != nil {
		observe.Step(step)
	}
	start := time.Now()
	said := &remark{}
	step.Result, step.Err = tool.Run(context.WithValue(ctx, remarkKey{}, said), args)
	step.Done, step.Took, step.Remark = true, time.Since(start), said.text
	if observe.Step != nil {
		observe.Step(step)
	}
	return step.Result, step.Err
}

// Conversation returns what m remembers of the conversation contextID, to go
// on with in another memory: see Restore.
func (m *Memory) Conversation(contextID string) []openai.Message { return m.history(contextID) }

// Restore makes m remember msgs as the conversation contextID, as far as it
// keeps of one.
func (m *Memory) Restore(contextID string, msgs []openai.Message) {
	if len(msgs) > 0 {
		m.remember(contextID, slices.Clone(msgs))
	}
}

// Unthink makes m forget what the LLM thought before its answers, in every
// conversation: the messages and the results of the tools stay. It is for a
// memory that goes on with another provider, which is not sent what the one
// before thought, in a form that is its own.
func (m *Memory) Unthink() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.conversations {
		for i := range c.msgs {
			c.msgs[i].Reasoning, c.msgs[i].ReasoningField = "", ""
		}
	}
}

// Forget makes m forget the conversation contextID.
func (m *Memory) Forget(contextID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.conversations, contextID)
}

func (m *Memory) history(contextID string) []openai.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.conversations[contextID]; ok {
		return copyMessages(c.msgs)
	}
	return nil
}

func (m *Memory) remember(contextID string, msgs []openai.Message) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.clock++
	msgs = slices.Clone(trim(msgs))
	now := time.Now() // the end of the answer of the messages that have no time yet
	for i := range msgs {
		msgs[i].At = cmp.Or(msgs[i].At, now)
	}
	c := m.conversations[contextID]
	if c == nil {
		c = &conversation{}
	}
	c.lastUsed = m.clock
	c.msgs = lastImages(msgs)
	m.conversations[contextID] = c
	if len(m.conversations) > maxContexts {
		m.evictOldest()
	}
}

// trim returns the conversation msgs as it is remembered. It starts at a
// user message: a tool result without its call is rejected by the API.
//
// Beyond maxHistory messages or maxHistoryBytes it is cut: it starts at the
// first user message after which there are at most keptHistory messages and
// keptHistoryBytes, and what the LLM thought is left out of every message,
// as the LLM only needs it within an answer. The last user message stays,
// with all that followed it, even beyond those: an answer that took many tool
// calls is not forgotten as a whole. A cut that would keep more than half is
// not made below hugeHistoryBytes: it would cost the cache of all that is
// kept, to save little.
//
// Without a cut, what the LLM thought is left out once it is at least half of
// the conversation from the first message that has it on: the cache of that
// part is lost, but every later call sends less.
func trim(msgs []openai.Message) []openai.Message {
	size := sizes(msgs)
	start := cut(msgs, size, len(msgs), size[0])
	if len(msgs) <= maxHistory && size[0] <= maxHistoryBytes {
		first, thought := -1, 0
		for i, m := range msgs[start:] {
			if m.Reasoning != "" && first < 0 {
				first = start + i
			}
			thought += len(m.Reasoning)
		}
		if first >= 0 && 2*thought >= size[first] {
			return thoughtless(msgs[start:])
		}
		return msgs[start:]
	}
	bare := thoughtless(msgs)
	less := sizes(bare)
	at := cut(bare, less, keptHistory, keptHistoryBytes)
	if less[at] > size[0]/2 && len(msgs)-at > len(msgs)/2 && size[0] <= hugeHistoryBytes {
		return msgs[start:]
	}
	return bare[at:]
}

// Thoughtless returns msgs without what the LLM thought: for another access
// than the one that thought it, which is not sent it.
func Thoughtless(msgs []openai.Message) []openai.Message { return thoughtless(msgs) }

// thoughtless returns msgs without what the LLM thought.
func thoughtless(msgs []openai.Message) []openai.Message {
	msgs = slices.Clone(msgs)
	for i := range msgs {
		msgs[i].Reasoning, msgs[i].ReasoningField = "", ""
	}
	return msgs
}

// sizes returns the sizes of msgs as JSON, which leaves out their pictures:
// sizes[i] is that of msgs[i:].
func sizes(msgs []openai.Message) []int {
	sizes := make([]int, len(msgs)+1)
	for i := len(msgs) - 1; i >= 0; i-- {
		b, _ := json.Marshal(msgs[i]) // texts and JSON: cannot fail
		sizes[i] = sizes[i+1] + len(b)
	}
	return sizes
}

// lastImages returns msgs with no more than the last maxImages pictures:
// those before are left out of their messages, and msgs is not changed.
func lastImages(msgs []openai.Message) []openai.Message {
	kept := 0
	for i := len(msgs) - 1; i >= 0; i-- {
		images := msgs[i].Images
		if kept+len(images) <= maxImages {
			kept += len(images)
			continue
		}
		msgs = slices.Clone(msgs)
		msgs[i].Images = images[len(images)-(maxImages-kept):]
		for j := range i {
			msgs[j].Images = nil
		}
		return msgs
	}
	return msgs
}

// cut returns the first user message of msgs after which there are at most
// keep messages and keepBytes, by sizes; the last one if there is none, and
// len(msgs) if there is no user message at all.
func cut(msgs []openai.Message, sizes []int, keep, keepBytes int) int {
	last := len(msgs)
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != "user" {
			continue
		}
		if last < len(msgs) && (len(msgs)-i > keep || sizes[i] > keepBytes) {
			break
		}
		last = i
	}
	return last
}

func (m *Memory) evictOldest() {
	var oldest string
	for id, c := range m.conversations {
		if oldest == "" || c.lastUsed < m.conversations[oldest].lastUsed {
			oldest = id
		}
	}
	delete(m.conversations, oldest)
}
