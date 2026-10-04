// Package assistant builds the agents of a node from one blueprint, the
// Builder: the one its owner chats with (h2a, human to agent) and the one
// that answers peers (a2a, agent to agent). They differ in their prompt and
// in the tools the owner allows them, see config.Tools.
package assistant

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"ouroboros/internal/agent"
	"ouroboros/internal/config"
)

// The prompts; %q is the name of the node. They name tools, so keep them in
// step with tools.go.
const (
	ownerPrompt = `You are %q, the personal agent of the owner of this Ouroboros node. `
	// askHint is added for an owner whose agent may ask peers.
	askHint = `The agents of other people (peers) are reachable on the network: ` + toolListPeers + ` shows who is there, ` +
		toolAskPeer + ` sends one of them a message and returns its answer. ` +
		`Ask a peer when the user wants you to or when a peer is likely to know what you do not. ` +
		`A peer only sees the message you send, so include all the context it needs. ` +
		`The user sees neither your message to a peer nor its answer: tell them briefly and in your own words ` +
		`what the peer said, like "Luna says she is fine", instead of quoting it at length. `
	ownerStyle = `Answer in the language of the user. Be helpful and concise.`

	peerPrompt = `You are %q, an Ouroboros agent reachable via A2A. Be helpful and concise.`
	// toolsHint is added for peers whose questions the agent may use tools for.
	toolsHint = ` You are answering the agent of another person, not your owner. ` +
		`Use your tools when they help to answer. You cannot ask the peer you are answering.`
)

// Step reports that the assistant asks a peer on the way to its answer and,
// with Done set, that the peer has answered. What the two agents say to each
// other stays between them.
type Step struct {
	Peer string
	Done bool
}

// Builder is the blueprint of the agents of a node.
type Builder struct {
	// LLM returns the model that the node configured by cfg talks to.
	LLM func(cfg config.Config) agent.LLM
	// Tools returns everything an agent can do. It is read when an agent is
	// built; the owner's lists in config.Tools decide which of them it gets.
	Tools func() []agent.Tool
}

// Assistant answers the owner of the node.
type Assistant struct {
	config  func() (config.Config, error)
	builder Builder

	mu    sync.Mutex
	agent *agent.Agent
	built setup // what agent was built for
}

// setup is the part of the configuration an agent is built from.
type setup struct{ baseURL, apiKey, model, name, id, tools string }

// New returns the assistant of the node with the given configuration, which
// is read anew for every question.
func New(config func() (config.Config, error), b Builder) *Assistant {
	return &Assistant{config: config, builder: b}
}

// ForPeers returns the agent that answers the peers of the node configured by
// cfg, with the tools its owner allows for them.
func (b Builder) ForPeers(cfg config.Config) (*agent.Agent, error) {
	allowed, err := pick(b.Tools(), cfg.PeerTools())
	if err != nil {
		return nil, fmt.Errorf("tools.a2a: %w", err)
	}
	prompt := fmt.Sprintf(peerPrompt, cfg.Name)
	if len(allowed) > 0 {
		prompt += toolsHint
	}
	return b.build(cfg, prompt, allowed), nil
}

// forOwner returns the agent that answers the owner of the node.
func (b Builder) forOwner(cfg config.Config) (*agent.Agent, error) {
	allowed, err := pick(b.Tools(), cfg.OwnerTools())
	if err != nil {
		return nil, fmt.Errorf("tools.h2a: %w", err)
	}
	prompt := fmt.Sprintf(ownerPrompt, cfg.Name)
	if slices.ContainsFunc(allowed, func(t agent.Tool) bool { return t.Name == toolAskPeer }) {
		prompt += askHint
	}
	return b.build(cfg, prompt+ownerStyle, allowed), nil
}

// build makes an agent on the LLM of cfg.
func (b Builder) build(cfg config.Config, prompt string, tools []agent.Tool) *agent.Agent {
	return agent.New(b.LLM(cfg), cfg.Model, prompt, tools...)
}

// Ready reports whether the assistant can answer: config.ErrIncomplete if the
// node was not set up yet.
func (a *Assistant) Ready() error {
	_, err := a.current()
	return err
}

// Ask answers text within the conversation contextID and reports to observe,
// which may be nil, which peers are asked on the way.
func (a *Assistant) Ask(ctx context.Context, contextID, text string, observe func(Step)) (string, error) {
	bot, err := a.current()
	if err != nil {
		return "", err
	}
	return bot.Run(ctx, contextID, text, func(s agent.Step) {
		if step, ok := peerStep(s); ok && observe != nil {
			observe(step)
		}
	})
}

// current returns the agent for the configuration as it is now. The agent is
// kept between questions so that it remembers its conversations, and rebuilt
// when the node is configured anew.
func (a *Assistant) current() (*agent.Agent, error) {
	cfg, err := a.config()
	if err != nil {
		return nil, err
	}
	if !cfg.Complete() {
		return nil, config.ErrIncomplete
	}
	want := setup{cfg.BaseURL, cfg.APIKey, cfg.Model, cfg.Name, cfg.ID(), strings.Join(cfg.OwnerTools(), ",")}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.agent == nil || a.built != want {
		bot, err := a.builder.forOwner(cfg)
		if err != nil {
			return nil, err
		}
		a.agent, a.built = bot, want
	}
	return a.agent, nil
}
