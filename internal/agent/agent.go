// Package agent turns an LLM into a conversational agent with per-context memory.
package agent

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"ouroboros/internal/openai"
)

const (
	maxContexts = 256 // conversations kept in memory
	maxHistory  = 40  // messages kept per conversation
)

// LLM generates a reply for a conversation.
type LLM interface {
	Chat(ctx context.Context, model string, msgs []openai.Message) (string, error)
}

// Agent answers messages and remembers each conversation by context ID.
type Agent struct {
	llm    LLM
	model  string
	system openai.Message

	mu            sync.Mutex
	clock         uint64
	conversations map[string]*conversation
}

type conversation struct {
	lastUsed uint64
	msgs     []openai.Message
}

// New returns an agent named name that answers with model.
func New(llm LLM, model, name string) *Agent {
	return &Agent{
		llm:   llm,
		model: model,
		system: openai.Message{Role: "system", Content: fmt.Sprintf(
			"You are %q, an Ouroboros agent reachable via A2A. Be helpful and concise.", name)},
		conversations: map[string]*conversation{},
	}
}

// Respond answers text within the conversation contextID.
func (a *Agent) Respond(ctx context.Context, contextID, text string) (string, error) {
	history := append(a.history(contextID), openai.Message{Role: "user", Content: text})
	answer, err := a.llm.Chat(openai.WithSession(ctx, contextID), a.model, append([]openai.Message{a.system}, history...))
	if err != nil {
		return "", err
	}
	a.remember(contextID, append(history, openai.Message{Role: "assistant", Content: answer}))
	return answer, nil
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
	a.conversations[contextID] = &conversation{
		lastUsed: a.clock,
		msgs:     msgs[max(0, len(msgs)-maxHistory):],
	}
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
