package agent

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"

	"ouroboros/internal/a2a"
	"ouroboros/internal/openai"
)

// Conversation identifies the trusted caller. ID is local to an agent's memory;
// plugins must include Peer and Origin.PublicKey in persistent storage keys.
type Conversation struct {
	ID     string
	Peer   bool
	Origin a2a.Origin
}

// Processing gives plugins direct, configured model access. Complete does not
// run the agent, providers, hooks or tools. The caller owns any returned calls.
type Processing struct {
	Model        LLM
	ModelName    string
	Conversation Conversation
}

// Preparation contains a bounded source snapshot and ephemeral hook context.
// Providers own persistence; Observe receives new messages before trimming.
type Preparation struct {
	Processing
	Messages []openai.Message
	Context  []openai.Message
}

// ContextProvider is inactive until its Name is selected in configuration.
// Observe is optional; Prepare is required. Callbacks must respect cancellation.
type ContextProvider struct {
	Name    string
	Observe func(context.Context, Processing, openai.Message) error
	Prepare func(context.Context, Preparation) ([]openai.Message, error)
}

// AnswerOutcome is delivered once, even after failure or cancellation. Messages
// contains this answer's raw source events, including unreduced tool results.
type AnswerOutcome struct {
	Processing
	Messages []openai.Message
	Reply    string
	Err      error
}

// AnswerHook runs in registration order. Before contributions are ephemeral;
// later hooks see earlier contributions. After errors never replace the answer.
type AnswerHook struct {
	Name   string
	Before func(context.Context, Preparation) ([]openai.Message, error)
	After  func(context.Context, AnswerOutcome) error
}

// Diagnostic identifies an extension failure without changing the answer outcome.
type Diagnostic struct {
	Plugin, Phase string
	Err           error
}

// Inspection is the last attempted preparation, not a side-effecting preview.
type Inspection struct {
	Tools       []openai.Tool
	Messages    []openai.Message
	Err         error
	Diagnostics []Diagnostic
}

// ErrContextProvider marks invalid explicit provider configuration.
var ErrContextProvider = errors.New("invalid context provider configuration")

type extensions struct {
	provider *ContextProvider
	hooks    []AnswerHook
}

// WithExtensions selects exactly one named provider. Duplicate matches are errors.
func (a *Agent) WithExtensions(name string, providers []ContextProvider, hooks []AnswerHook) (*Agent, error) {
	e := extensions{hooks: slices.Clone(hooks)}
	if name != "" {
		for _, p := range providers {
			if p.Name == name {
				if e.provider != nil {
					return nil, fmt.Errorf("%w: context provider %q: conflicting registrations", ErrContextProvider, name)
				}
				q := p
				e.provider = &q
			}
		}
		if e.provider == nil {
			return nil, fmt.Errorf("%w: context provider %q: not registered", ErrContextProvider, name)
		}
		if e.provider.Prepare == nil {
			return nil, fmt.Errorf("%w: context provider %q: missing Prepare", ErrContextProvider, name)
		}
	}
	a.extensions = e
	return a, nil
}
func copyMessages(ms []openai.Message) []openai.Message {
	out := slices.Clone(ms)
	for i := range out {
		out[i].ToolCalls = slices.Clone(out[i].ToolCalls)
	}
	return out
}
func validatePrepared(system openai.Message, ms []openai.Message) error {
	if len(ms) == 0 || !reflect.DeepEqual(ms[0], system) {
		return fmt.Errorf("system instruction must be preserved as first message")
	}
	pending := map[string]bool{}
	for _, m := range ms {
		switch m.Role {
		case "system", "developer", "user", "assistant", "tool":
		default:
			return fmt.Errorf("invalid message role %q", m.Role)
		}
		if m.Role != "tool" && m.ToolCallID != "" {
			return fmt.Errorf("tool result ID requires tool role")
		}

		if len(pending) > 0 && m.Role != "tool" {
			return fmt.Errorf("tool calls require adjacent results")
		}
		if m.Role == "tool" {
			if !pending[m.ToolCallID] {
				return fmt.Errorf("orphan tool result %q", m.ToolCallID)
			}
			delete(pending, m.ToolCallID)
		}
		if len(m.ToolCalls) > 0 && m.Role != "assistant" {
			return fmt.Errorf("tool calls require assistant role")
		}
		for _, t := range m.ToolCalls {
			if t.ID == "" || pending[t.ID] {
				return fmt.Errorf("invalid tool call ID %q", t.ID)
			}
			pending[t.ID] = true
		}
	}
	if len(pending) > 0 {
		return fmt.Errorf("missing tool results")
	}
	return nil
}

// Inspection returns owned copies of the last prepared request and diagnostics.
func (a *Agent) Inspection(id string) (Inspection, bool) {
	a.memory.mu.Lock()
	defer a.memory.mu.Unlock()
	c, ok := a.memory.conversations[id]
	if !ok || !c.inspected {
		return Inspection{}, false
	}
	s := c.inspection
	s.Messages = copyMessages(s.Messages)
	s.Tools = copyTools(s.Tools)
	s.Diagnostics = slices.Clone(s.Diagnostics)
	return s, true
}
func (a *Agent) inspect(id string, s Inspection) {
	a.memory.mu.Lock()
	defer a.memory.mu.Unlock()
	c := a.memory.conversations[id]
	if c == nil {
		c = &conversation{}
		a.memory.conversations[id] = c
	}
	c.inspected = true
	c.inspection = s
	c.inspection.Messages = copyMessages(s.Messages)
	c.inspection.Tools = copyTools(s.Tools)
}

// Active locks are released and removed after each answer, not retained history.
type answerLock struct {
	token chan struct{}
	users int
}

func (m *Memory) acquire(ctx context.Context, id string) (func(), error) {
	m.mu.Lock()
	if m.active == nil {
		m.active = map[string]*answerLock{}
	}
	l := m.active[id]
	if l == nil {
		l = &answerLock{token: make(chan struct{}, 1)}
		m.active[id] = l
	}
	l.users++
	m.mu.Unlock()
	releaseRef := func() {
		m.mu.Lock()
		l.users--
		if l.users == 0 {
			delete(m.active, id)
		}
		m.mu.Unlock()
	}
	select {
	case l.token <- struct{}{}:
		return func() { <-l.token; releaseRef() }, nil
	case <-ctx.Done():
		releaseRef()
		return nil, ctx.Err()
	}
}

// HasExtensions reports whether context inspection should show actual
// requests: whether a provider or a Before hook changes them. A hook that
// only observes the outcome of an answer leaves the next context as it is.
func (a *Agent) HasExtensions() bool {
	return a.extensions.provider != nil || slices.ContainsFunc(a.extensions.hooks, func(h AnswerHook) bool { return h.Before != nil })
}

func copyTools(ts []openai.Tool) []openai.Tool {
	out := slices.Clone(ts)
	for i := range out {
		out[i].Function.Parameters = slices.Clone(out[i].Function.Parameters)
	}
	return out
}
