// Package agent turns an LLM into a conversational agent with per-context
// memory that can use tools on the way to its answer.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"ouroboros/internal/openai"
)

const (
	maxContexts = 256               // conversations kept in memory
	maxHistory  = 40                // messages kept per conversation
	maxRounds   = 100               // LLM calls per answer; stops an agent that keeps calling tools
	warnRounds  = maxRounds * 4 / 5 // LLM calls after which the LLM is told how many it has left

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

// Endpoint is where the LLM of a node is reached: all that whoever provides
// the LLM gets to know of the configuration of the node.
type Endpoint struct {
	BaseURL string
	APIKey  string
}

// Step reports a tool call: once when it starts and again, with Done set,
// when its result is in, together with how long the call took.
type Step struct {
	Tool   string
	Args   json.RawMessage
	Done   bool
	Result string
	Err    error
	Took   time.Duration
}

// Call reports a call of the LLM: once when it starts and again, with Done
// set, when it has ended, with how long it took and whether it ended in a
// text, in calls of tools or in an error. Content is the text the LLM sent,
// also along with calls of tools.
type Call struct {
	Done      bool
	Took      time.Duration
	ToolCalls int
	Content   string
	Err       error
}

// Observer is told what an agent does on the way to an answer. Either field
// may be nil.
type Observer struct {
	Step func(Step) // every tool call, also those of a script
	Call func(Call) // every call of the LLM
}

type (
	contextKey  struct{}
	observerKey struct{}
)

// ContextID returns the conversation a tool is run for.
func ContextID(ctx context.Context) string {
	id, _ := ctx.Value(contextKey{}).(string)
	return id
}

// Agent answers messages and remembers each conversation by context ID.
type Agent struct {
	llm    LLM
	model  string
	system openai.Message
	tools  map[string]Tool
	specs  []openai.Tool
	memory *Memory
}

// Memory keeps conversations by context ID. It may outlive the agent that
// has it: an agent built anew with the same memory goes on with them.
type Memory struct {
	mu            sync.Mutex
	clock         uint64
	conversations map[string]*conversation
}

// NewMemory returns a memory that knows no conversation yet.
func NewMemory() *Memory {
	return &Memory{conversations: map[string]*conversation{}}
}

type conversation struct {
	lastUsed uint64
	msgs     []openai.Message
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
	return a
}

// WithMemory makes the agent remember its conversations in m instead of a
// memory of its own, and returns it. It is for an agent that replaces
// another: given the memory of that one, it knows what it was told.
func (a *Agent) WithMemory(m *Memory) *Agent {
	a.memory = m
	return a
}

// Respond answers text within the conversation contextID.
func (a *Agent) Respond(ctx context.Context, contextID, text string) (string, error) {
	return a.Run(ctx, contextID, text, Observer{})
}

// Run is like Respond and reports every call of the LLM and every tool call
// to observe.
func (a *Agent) Run(ctx context.Context, contextID, text string, observe Observer) (string, error) {
	ctx = openai.WithSession(context.WithValue(ctx, contextKey{}, contextID), contextID)
	ctx = context.WithValue(ctx, observerKey{}, observe)
	history := append(a.memory.history(contextID), openai.Message{Role: "user", Content: text})
	// An answer that fails is remembered as far as it got: asked again, the
	// LLM goes on from there instead of starting over. Why it failed is not
	// part of it: a peer must not learn the cause from a later answer.
	abandon := func(err error) (string, error) {
		a.memory.remember(contextID, append(history, openai.Message{Role: "assistant", Content: abandoned}))
		return "", err
	}
	for round := range maxRounds {
		// An answer that was stopped while a tool ran asks the LLM no more.
		if err := ctx.Err(); err != nil {
			return abandon(err)
		}
		if observe.Call != nil {
			observe.Call(Call{})
		}
		start := time.Now()
		msgs := append([]openai.Message{a.system}, history...)
		if round >= warnRounds {
			msgs[len(msgs)-1].Content += remaining(round)
		}
		msg, err := a.llm.Complete(ctx, a.model, msgs, a.specs)
		if observe.Call != nil {
			observe.Call(Call{Done: true, Took: time.Since(start), ToolCalls: len(msg.ToolCalls), Content: msg.Content, Err: err})
		}
		if err != nil {
			return abandon(err)
		}
		msg.Role = "assistant"
		history = append(history, msg)
		if len(msg.ToolCalls) == 0 {
			a.memory.remember(contextID, history)
			return msg.Content, nil
		}
		for _, call := range msg.ToolCalls {
			history = append(history, a.call(ctx, call))
		}
	}
	return abandon(ErrTooManyToolCalls)
}

// remaining is what the LLM is told after the last result of a tool when
// round, counted from 0, is one of the last calls of the LLM for an answer.
// It is only sent, not remembered: the next call tells the next number.
func remaining(round int) string {
	return fmt.Sprintf("\n\n[Not from the tool: this is call %d of at most %d of the LLM for this answer. "+
		"Without an answer by then, it breaks off. Finish the work that is begun, "+
		"and answer with what is done and what is still open: asked again, you go on from there.]",
		round+1, maxRounds)
}

// call runs one tool call and returns its result as a message for the LLM.
func (a *Agent) call(ctx context.Context, call openai.ToolCall) openai.Message {
	tool, ok := a.tools[call.Function.Name]
	if !ok {
		tool = Tool{Name: call.Function.Name, Run: func(context.Context, json.RawMessage) (string, error) {
			return "", errors.New("unknown tool " + call.Function.Name)
		}}
	}
	content, err := Invoke(ctx, tool, json.RawMessage(call.Function.Arguments))
	if err != nil {
		content = "error: " + err.Error()
	}
	return openai.Message{Role: "tool", ToolCallID: call.ID, Content: content}
}

// Invoke runs tool with args within the conversation of ctx and reports the
// call to whoever observes that conversation, like a call the LLM asked for.
// It is for a tool that runs other tools of its agent, such as a script.
func Invoke(ctx context.Context, tool Tool, args json.RawMessage) (string, error) {
	observe, _ := ctx.Value(observerKey{}).(Observer)
	step := Step{Tool: tool.Name, Args: args}
	if observe.Step != nil {
		observe.Step(step)
	}
	start := time.Now()
	step.Result, step.Err = tool.Run(ctx, args)
	step.Done, step.Took = true, time.Since(start)
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

func (m *Memory) history(contextID string) []openai.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.conversations[contextID]; ok {
		return slices.Clone(c.msgs)
	}
	return nil
}

func (m *Memory) remember(contextID string, msgs []openai.Message) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.clock++
	// Start at a user message: a tool result without its call is rejected by the API.
	// The last one stays, with all that followed it, even beyond maxHistory:
	// an answer that took many tool calls is not forgotten as a whole.
	cut := max(0, len(msgs)-maxHistory)
	for cut < len(msgs) && msgs[cut].Role != "user" {
		cut++
	}
	for i := len(msgs) - 1; cut == len(msgs) && i >= 0; i-- {
		if msgs[i].Role == "user" {
			cut = i
		}
	}
	msgs = msgs[cut:]
	m.conversations[contextID] = &conversation{lastUsed: m.clock, msgs: msgs}
	if len(m.conversations) > maxContexts {
		m.evictOldest()
	}
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
