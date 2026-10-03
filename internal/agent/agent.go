// Package agent turns an LLM into a conversational agent with per-context
// memory that can use tools on the way to its answer.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"

	"ouroboros/internal/openai"
)

const (
	maxContexts = 256 // conversations kept in memory
	maxHistory  = 40  // messages kept per conversation
	maxRounds   = 8   // LLM calls per answer; stops an agent that keeps calling tools
)

// LLM generates a reply for a conversation or asks for tools to be run.
type LLM interface {
	Complete(ctx context.Context, model string, msgs []openai.Message, tools []openai.Tool) (openai.Message, error)
}

// Tool is something the agent can do while it answers.
type Tool struct {
	Name        string
	Description string
	Schema      string // JSON schema of the arguments
	// Run executes the tool. Its result, or its error, is handed to the LLM.
	Run func(ctx context.Context, args json.RawMessage) (string, error)
}

// Step reports a tool call: once when it starts and again, with Done set,
// when its result is in.
type Step struct {
	Tool   string
	Args   json.RawMessage
	Done   bool
	Result string
	Err    error
}

type contextKey struct{}

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

	mu            sync.Mutex
	clock         uint64
	conversations map[string]*conversation
}

type conversation struct {
	lastUsed uint64
	msgs     []openai.Message
}

// New returns an agent that answers with model as told by the system prompt
// and may use tools.
func New(llm LLM, model, system string, tools ...Tool) *Agent {
	a := &Agent{
		llm:           llm,
		model:         model,
		system:        openai.Message{Role: "system", Content: system},
		tools:         map[string]Tool{},
		conversations: map[string]*conversation{},
	}
	for _, t := range tools {
		a.tools[t.Name] = t
		a.specs = append(a.specs, openai.NewTool(t.Name, t.Description, t.Schema))
	}
	return a
}

// Respond answers text within the conversation contextID.
func (a *Agent) Respond(ctx context.Context, contextID, text string) (string, error) {
	return a.Run(ctx, contextID, text, nil)
}

// Run is like Respond and reports every tool call to observe, which may be nil.
func (a *Agent) Run(ctx context.Context, contextID, text string, observe func(Step)) (string, error) {
	ctx = openai.WithSession(context.WithValue(ctx, contextKey{}, contextID), contextID)
	history := append(a.history(contextID), openai.Message{Role: "user", Content: text})
	for range maxRounds {
		msg, err := a.llm.Complete(ctx, a.model, append([]openai.Message{a.system}, history...), a.specs)
		if err != nil {
			return "", err
		}
		msg.Role = "assistant"
		history = append(history, msg)
		if len(msg.ToolCalls) == 0 {
			a.remember(contextID, history)
			return msg.Content, nil
		}
		for _, call := range msg.ToolCalls {
			history = append(history, a.call(ctx, call, observe))
		}
	}
	return "", errors.New("agent: gave up after too many tool calls")
}

// call runs one tool call and returns its result as a message for the LLM.
func (a *Agent) call(ctx context.Context, call openai.ToolCall, observe func(Step)) openai.Message {
	step := Step{Tool: call.Function.Name, Args: json.RawMessage(call.Function.Arguments)}
	if observe != nil {
		observe(step)
	}
	if tool, ok := a.tools[step.Tool]; ok {
		step.Result, step.Err = tool.Run(ctx, step.Args)
	} else {
		step.Err = errors.New("unknown tool " + step.Tool)
	}
	step.Done = true
	if observe != nil {
		observe(step)
	}
	content := step.Result
	if step.Err != nil {
		content = "error: " + step.Err.Error()
	}
	return openai.Message{Role: "tool", ToolCallID: call.ID, Content: content}
}

func (a *Agent) history(contextID string) []openai.Message {
	a.mu.Lock()
	defer a.mu.Unlock()
	if c, ok := a.conversations[contextID]; ok {
		return slices.Clone(c.msgs)
	}
	return nil
}

func (a *Agent) remember(contextID string, msgs []openai.Message) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.clock++
	// Start at a user message: a tool result without its call is rejected by the API.
	msgs = msgs[max(0, len(msgs)-maxHistory):]
	for len(msgs) > 0 && msgs[0].Role != "user" {
		msgs = msgs[1:]
	}
	a.conversations[contextID] = &conversation{lastUsed: a.clock, msgs: msgs}
	if len(a.conversations) > maxContexts {
		a.evictOldest()
	}
}

func (a *Agent) evictOldest() {
	var oldest string
	for id, c := range a.conversations {
		if oldest == "" || c.lastUsed < a.conversations[oldest].lastUsed {
			oldest = id
		}
	}
	delete(a.conversations, oldest)
}
