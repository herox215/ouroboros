package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"ouroboros/internal/a2a"
	"ouroboros/internal/agent"
	"ouroboros/internal/config"
	"ouroboros/internal/identity"
	"ouroboros/internal/openai"
)

// Peers answers the peers of a node, with the agent for the configuration of
// the node as it is now: a change of it holds from the next message on,
// without a restart. A peer with a list of tools of its own in tools.peers
// has an agent of its own; the others share one.
type Peers struct {
	config  func() (config.Config, error)
	builder Builder
	log     func(format string, args ...any)

	mu     sync.Mutex
	agents map[string]*built // by the ID of the peer, "" for the peers without a list of their own
	access string            // the access the agents were built for
	failed string            // why the configuration could not be used the last time
	memory *agent.Memory     // of the conversations, for every agent there is
	// recall returns what the agent said in a conversation it does not
	// remember, as the archive kept it; nil without an archive.
	recall func(ctx context.Context, contextID string) []openai.Message
}

// built is an agent and the configuration it was built for.
type built struct {
	agent *agent.Agent
	of    string // the configuration
}

// Peers returns what answers the peers of the node whose configuration
// config reads, anew for every message. What goes wrong with a configuration
// that was changed while the node runs is told to log.
func (b Builder) Peers(config func() (config.Config, error), log func(format string, args ...any)) *Peers {
	return &Peers{config: config, builder: b, log: log, agents: map[string]*built{}, memory: agent.NewMemory()}
}

// Recall makes the agent go on with a conversation it does not remember,
// as after a new build of ouro, with what recall returns of it: what was
// said in it, as the archive kept it.
func (p *Peers) Recall(recall func(ctx context.Context, contextID string) []openai.Message) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.recall = recall
}

// Ready reports whether the peers of the node can be answered: the error of
// a configuration no agent can be built from.
func (p *Peers) Ready() error {
	_, err := p.current("")
	return err
}

// Respond answers text within the conversation contextID.
func (p *Peers) Respond(ctx context.Context, contextID, text string) (string, error) {
	bot, err := p.current(peerOf(ctx))
	if err != nil {
		return "", err
	}
	p.remember(ctx, contextID)
	return bot.Run(ctx, contextID, text, agent.Observer{Diagnostic: func(d agent.Diagnostic) {
		if p.log != nil {
			p.log("conversation %s plugin %s %s failed: %v", contextID, d.Plugin, d.Phase, d.Err)
		}
	}})
}

// peerOf is the ID of the node of the peer that is answered in ctx.
func peerOf(ctx context.Context) string {
	origin, _ := a2a.OriginFrom(ctx)
	return identity.IDOf(origin.PublicKey)
}

// remember makes the agent remember the conversation contextID as the
// archive kept it, unless it does already.
func (p *Peers) remember(ctx context.Context, contextID string) {
	p.mu.Lock()
	recall := p.recall
	p.mu.Unlock()
	if recall != nil && len(p.memory.Conversation(contextID)) == 0 {
		p.memory.Restore(contextID, recall(ctx, contextID))
	}
}

// noteHead is what the agent reads before a note of a peer.
const noteHead = "[A note from the peer, which asks for no answer:]\n"

// Note keeps text as what the peer of the conversation contextID told
// without asking for an answer: no agent starts for it. The agent reads it
// when the peer asks the next time, and it is archived like an answer.
func (p *Peers) Note(ctx context.Context, contextID, text string) error {
	bot, err := p.current(peerOf(ctx))
	if err != nil {
		return err
	}
	p.remember(ctx, contextID)
	return bot.Note(ctx, contextID, noteHead+text)
}

// Forget makes the agent forget the conversation contextID, as if it was
// never had.
func (p *Peers) Forget(contextID string) { p.memory.Forget(contextID) }

// current returns the agent for the peer whose node has the ID peer, for
// the configuration as it is now. The agent is kept between messages, and
// built anew when what it is built from changes: with the memory of the one
// before, so that it goes on with its conversations.
//
// A configuration that cannot be used does not take the agent away from a
// node that runs: it goes on with the one it has, and the log says why. A
// node that has none yet does not start.
func (p *Peers) current(peer string) (*agent.Agent, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	cfg, err := p.config()
	if err == nil && !cfg.Complete() {
		err = config.ErrIncomplete
	}
	if _, own := cfg.ToolsOf(peer); !own {
		peer = ""
	}
	had := p.agents[peer]
	var bot *agent.Agent
	var want, access string
	if err == nil {
		bot, want, access, err = p.build(cfg, peer, had)
	}
	if err != nil {
		if had == nil || errors.Is(err, agent.ErrContextProvider) {
			return nil, err
		}
		if why := err.Error(); why != p.failed {
			p.failed = why
			p.log("the configuration cannot be used, peers are answered as before: %v", err)
		}
		return had.agent, nil
	}
	if bot != nil {
		if had != nil {
			p.log("the configuration changed: peers are answered by a new agent")
		}
		// What the LLM thought so far is not sent to another access.
		if p.access != "" && p.access != access {
			p.memory.Unthink()
		}
		had = &built{agent: bot.WithMemory(p.memory), of: want}
		p.agents[peer], p.access = had, access
	}
	p.failed = ""
	return had.agent, nil
}

// build returns the agent for the peer whose node has the ID peer, "" for
// the peers without a list of their own, for cfg, what it was built for and
// the access it answers through, or no agent if had was built for the same.
func (p *Peers) build(cfg config.Config, peer string, had *built) (*agent.Agent, string, string, error) {
	cfg.Access, cfg.Model, _ = cfg.PeerLLM()
	entries, _ := cfg.ToolsOf(peer)
	tools, _ := json.Marshal(entries) // an entry that cannot be written is reported by ForPeers
	var lists []byte
	if peer == "" && cfg.Tools != nil && len(cfg.Tools.Peers) > 0 {
		lists, _ = json.Marshal(cfg.Tools.Peers) // which this agent checks
	}
	want, _ := json.Marshal([]string{cfg.Access, p.builder.revision(cfg.Access), cfg.Model, cfg.Name, cfg.ID(), string(tools), string(lists), cfg.ContextProvider})
	if had != nil && had.of == string(want) {
		return nil, string(want), cfg.Access, nil
	}
	bot, err := p.builder.forPeers(cfg, peer)
	return bot, string(want), cfg.Access, err
}
