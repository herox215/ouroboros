package assistant

import (
	"context"
	"encoding/json"
	"sync"

	"ouroboros/internal/agent"
	"ouroboros/internal/config"
	"ouroboros/internal/script"
)

// Peers answers the peers of a node, with the agent for the configuration of
// the node as it is now: a change of it holds from the next message on,
// without a restart.
type Peers struct {
	config  func() (config.Config, error)
	builder Builder
	log     func(format string, args ...any)

	mu     sync.Mutex
	agent  *agent.Agent
	built  string        // the configuration agent was built for
	failed string        // why the configuration could not be used the last time
	memory *agent.Memory // of the conversations, for every agent there is
	// scripts are what the scripts of the conversations kept, for every
	// agent there is.
	scripts *script.Sessions
}

// Peers returns what answers the peers of the node whose configuration
// config reads, anew for every message. What goes wrong with a configuration
// that was changed while the node runs is told to log.
func (b Builder) Peers(config func() (config.Config, error), log func(format string, args ...any)) *Peers {
	return &Peers{config: config, builder: b, log: log, memory: agent.NewMemory(), scripts: script.NewSessions()}
}

// Ready reports whether the peers of the node can be answered: the error of
// a configuration no agent can be built from.
func (p *Peers) Ready() error {
	_, err := p.current()
	return err
}

// Respond answers text within the conversation contextID.
func (p *Peers) Respond(ctx context.Context, contextID, text string) (string, error) {
	bot, err := p.current()
	if err != nil {
		return "", err
	}
	return bot.Respond(ctx, contextID, text)
}

// current returns the agent for the configuration as it is now. The agent is
// kept between messages, and built anew when what it is built from changes:
// with the memory of the one before, so that it goes on with its
// conversations.
//
// A configuration that cannot be used does not take the agent away from a
// node that runs: it goes on with the one it has, and the log says why. A
// node that has none yet does not start.
func (p *Peers) current() (*agent.Agent, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	bot, want, err := p.build()
	if err != nil {
		if p.agent == nil {
			return nil, err
		}
		if why := err.Error(); why != p.failed {
			p.failed = why
			p.log("the configuration cannot be used, peers are answered as before: %v", err)
		}
		return p.agent, nil
	}
	if bot != nil {
		if p.agent != nil {
			p.log("the configuration changed: peers are answered by a new agent")
		}
		p.agent, p.built = bot.WithMemory(p.memory), want
	}
	p.failed = ""
	return p.agent, nil
}

// build returns the agent for the configuration as it is now and what it was
// built for, or no agent if the one there is was built for the same.
func (p *Peers) build() (*agent.Agent, string, error) {
	cfg, err := p.config()
	if err != nil {
		return nil, "", err
	}
	if !cfg.Complete() {
		return nil, "", config.ErrIncomplete
	}
	tools, _ := json.Marshal(cfg.PeerTools()) // an entry that cannot be written is reported by ForPeers
	want, _ := json.Marshal([]string{cfg.BaseURL, cfg.APIKey, cfg.Model, cfg.Name, cfg.ID(), string(tools)})
	if p.agent != nil && p.built == string(want) {
		return nil, string(want), nil
	}
	bot, err := p.builder.forPeers(cfg, p.scripts)
	return bot, string(want), err
}
