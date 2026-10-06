package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"ouroboros/internal/a2a"
	"ouroboros/internal/agent"
	"ouroboros/internal/config"
	"ouroboros/internal/guard"
	"ouroboros/internal/proposal"
)

// The tools with which the agent of the owner sees the configuration of the
// node and proposes changes of it. They are for the owner alone.
const (
	toolGetConfig     = "get_config"
	toolProposeConfig = "propose_config"
)

// node is the node whose configuration the agent of its owner sees and
// proposes changes of.
type node struct {
	config    func() (config.Config, error)
	proposals *proposal.Store
}

// WithConfig returns the blueprint with the tools get_config and
// propose_config for the agent of the owner: config reads the configuration
// of the node, and what the agent proposes waits in proposals for the owner
// to decide. The agent changes nothing by itself.
//
// Neither is to be had from the blueprint again: a plugin that gets the
// blueprint gets no way to the key of the node or to the proposals.
func (b Builder) WithConfig(config func() (config.Config, error), proposals *proposal.Store) Builder {
	b.node = &node{config, proposals}
	return b
}

// configTools are the tools of the node of b, if it has one.
func (b Builder) configTools() []agent.Tool {
	if b.node == nil {
		return nil
	}
	return []agent.Tool{b.getConfig(), b.proposeConfig()}
}

// ownerOnly refuses a tool for a conversation of a peer. No list hands these
// tools to a peer, see pick; this holds whatever happens to the lists.
func ownerOnly(ctx context.Context, tool string) error {
	if _, peer := a2a.OriginFrom(ctx); peer {
		return errors.New(tool + " is for the owner of this node only")
	}
	return nil
}

func (b Builder) getConfig() agent.Tool {
	tool := agent.NewTool(toolGetConfig,
		"Show the configuration of this node as JSON, with defaults for what is not set: name, model, and under \"tools\" "+
			"the tools for your owner (h2a), for peers (a2a) and the paths no tool for files touches (protected).",
		nil,
		func(ctx context.Context, _ struct{}) (string, error) {
			if err := ownerOnly(ctx, toolGetConfig); err != nil {
				return "", err
			}
			cfg, err := b.node.config()
			if err != nil {
				return "", err
			}
			return string(cfg.Public()), nil
		})
	tool.OwnerOnly = true
	return tool
}

type proposeArgs struct {
	Path   string          `json:"path"`
	Value  json.RawMessage `json:"value"`
	Reason string          `json:"reason"`
}

func (b Builder) proposeConfig() agent.Tool {
	return agent.Tool{
		Name: toolProposeConfig,
		Description: "Propose one change of the configuration to your owner, who accepts or refuses it in the terminal UI. " +
			"Look at it with " + toolGetConfig + " first, and propose the whole new value of one field.",
		Schema: `{"type":"object","properties":{` +
			`"path":{"type":"string","description":"the field: name, model, tools.h2a, tools.a2a or tools.protected"},` +
			`"value":{"description":"the new value of the field as JSON: a text for name and model, a list for the others"},` +
			`"reason":{"type":"string","description":"one sentence on why, for your owner to read"}},` +
			`"required":["path","value","reason"]}`,
		OwnerOnly: true,
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			if err := ownerOnly(ctx, toolProposeConfig); err != nil {
				return "", err
			}
			var args proposeArgs
			if json.Unmarshal(raw, &args) != nil || args.Path == "" || len(args.Value) == 0 || args.Reason == "" {
				return "", errors.New(toolProposeConfig + ` needs "path", "value" and "reason"`)
			}
			p, err := b.propose(args)
			if errors.Is(err, proposal.ErrFull) {
				return "", fmt.Errorf("%d proposals are waiting for your owner already, and no more are kept: "+
					"they decide on them among the requests in the terminal UI (/requests)", proposal.Max)
			}
			if err != nil {
				return "", fmt.Errorf("nothing was proposed: %w", err)
			}
			return fmt.Sprintf("Proposed, and nothing is changed yet: %s is to become %s instead of %s. "+
				"The proposal waits for your owner among the requests in the terminal UI (the command /requests), where they accept or refuse it. "+
				"Tell them that, and do not say that the change is made.", p.Path, p.Value, p.Before), nil
		},
	}
}

// propose records the change args asks for, if the node could run with it:
// the value is checked the way the node checks its configuration when it
// starts.
func (b Builder) propose(args proposeArgs) (proposal.Proposal, error) {
	cfg, err := b.node.config()
	if err != nil {
		return proposal.Proposal{}, err
	}
	value, changed := args.Value, cfg
	if err := changed.SetField(args.Path, value); err != nil {
		// An LLM likes to hand over a list as a text that holds the list.
		var text string
		if json.Unmarshal(value, &text) != nil || changed.SetField(args.Path, json.RawMessage(text)) != nil {
			return proposal.Proposal{}, err
		}
		value = json.RawMessage(text)
	}
	if err := b.Check(changed); err != nil {
		return proposal.Proposal{}, err
	}
	return b.node.proposals.Add(cfg, args.Path, value, args.Reason)
}

// Check reports whether the agents of a node can be built from cfg: an
// unknown tool, a tool that is listed twice, a setting a tool does not take
// and a protected path that is none are errors.
func (b Builder) Check(cfg config.Config) error {
	if _, err := b.ownerTools(cfg); err != nil {
		return err
	}
	if _, err := b.peerTools(cfg); err != nil {
		return err
	}
	if err := guard.Valid(cfg.Protected()); err != nil {
		return fmt.Errorf("tools.protected: %w", err)
	}
	return nil
}
