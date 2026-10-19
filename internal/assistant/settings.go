package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"ouroboros/internal/a2a"
	"ouroboros/internal/agent"
	"ouroboros/internal/config"
	"ouroboros/internal/guard"
	"ouroboros/internal/proposal"
)

// The tools with which the agent of the owner sees the configuration of the
// node, proposes changes of it and takes a proposal of its own back. They
// are for the owner alone.
const (
	toolGetConfig      = "get_config"
	toolProposeConfig  = "propose_config"
	toolWithdrawConfig = "withdraw_proposal"
)

// node is the node whose configuration the agent of its owner sees and
// proposes changes of.
type node struct {
	config    func() (config.Config, error)
	proposals *proposal.Store
}

// WithConfig returns the blueprint with the owner-only configuration tools.
// Changes are validated, asked about in the conversation and applied only after
// an affirmative answer. The store remains available for older pending proposals.
// The proposals file must be in the node's configuration directory.
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
	return []agent.Tool{b.getConfig(), b.proposeConfig(), b.withdrawProposal()}
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
		"Show the configuration of this node as JSON text, with defaults for what is not set: name, description (what the agent card of the node tells anyone about it), model, the provider that answers, under \"tools\" "+
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
			provider := ""
			if b.Accesses != nil {
				provider = b.Accesses.Label(cfg.Access)
			}
			return string(cfg.Public(provider)), nil
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
		Description: "Propose one change of the configuration to your owner and wait for their answer in this conversation. " +
			"Only the exact answer Apply change authorizes it; any other answer leaves it unchanged. " +
			"Look at it with " + toolGetConfig + " first, and propose the whole new value of one field.",
		Schema: `{"type":"object","properties":{` +
			`"path":{"type":"string","description":"the field: name, description, model, tools.h2a, tools.a2a or tools.protected"},` +
			`"value":{"description":"the new value of the field as JSON: a text for name, description and model, ` +
			`a list for the others. ` +
			`The description is public, one line of at most 300 characters; an empty one is the default"},` +
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
			if err != nil {
				return "", fmt.Errorf("nothing was proposed: %w", err)
			}
			question, _ := json.Marshal(Question{
				Question:    fmt.Sprintf("Change %s from %s to %s?\nReason: %s", p.Path, p.Before, p.Value, p.Reason),
				Options:     []string{"Apply change", "Reject change"},
				Recommended: "Reject change",
			})
			answer, err := agent.AskOwner(ctx, question)
			if err != nil {
				return "", fmt.Errorf("configuration not changed: %w", err)
			}
			if err := ctx.Err(); err != nil {
				return "", fmt.Errorf("configuration not changed: %w", err)
			}
			if answer != "Apply change" {
				return "Configuration not changed: the owner did not approve the change.", nil
			}
			if err := b.node.proposals.Apply(ctx, p, b.Check); err != nil {
				return "", fmt.Errorf("configuration not changed: %w", err)
			}
			return fmt.Sprintf("Configuration changed with the owner's approval: %s is now %s instead of %s.", p.Path, p.Value, p.Before), nil
		},
	}
}

type withdrawArgs struct {
	ID string `json:"id"`
}

// withdrawProposal takes back a proposal of the agent before the owner
// decided on it. Nothing is changed by it: the agent takes back its own
// question, and the owner has one decision less to make.
func (b Builder) withdrawProposal() agent.Tool {
	return agent.Tool{
		Name: toolWithdrawConfig,
		Description: "Take back an older pending configuration proposal: it is dropped, and nothing is changed. " +
			"Without an id this lists those older proposals with their ids; give one of those ids to withdraw it. " +
			"New calls to " + toolProposeConfig + " ask directly and leave no pending proposal. " +
			"What your owner already decided on is out of reach: it is applied, or it is gone.",
		Schema: `{"type":"object","properties":{` +
			`"id":{"type":"string","description":"the id of the proposal to take back; leave it out to see what waits"}}}`,
		OwnerOnly: true,
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			if err := ownerOnly(ctx, toolWithdrawConfig); err != nil {
				return "", err
			}
			var args withdrawArgs
			if len(raw) > 0 && json.Unmarshal(raw, &args) != nil {
				return "", errors.New(toolWithdrawConfig + ` takes an "id", or nothing at all to list what waits`)
			}
			if args.ID == "" {
				return b.waitingProposals()
			}
			p, err := b.node.proposals.Refuse(args.ID)
			switch {
			case errors.Is(err, proposal.ErrGone):
				return "", fmt.Errorf("no proposal waits with the id %q: %s without an id lists what waits", args.ID, toolWithdrawConfig)
			case err != nil:
				return "", fmt.Errorf("nothing was taken back: %w", err)
			}
			return fmt.Sprintf("Taken back, and nothing is changed: the proposal for %s no longer waits for your owner, "+
				"and no one decides on it any more. Tell them that you took it back.", p.Path), nil
		},
	}
}

// waitingProposals lists the proposals that wait for the owner, with their
// ids, so that the agent can take one of them back.
func (b Builder) waitingProposals() (string, error) {
	waiting, err := b.node.proposals.List()
	if err != nil {
		return "", err
	}
	if len(waiting) == 0 {
		return "No proposal waits for your owner: there is nothing of yours to take back.", nil
	}
	lines := make([]string, len(waiting))
	for i, p := range waiting {
		line := fmt.Sprintf("%s: %s is to become %s instead of %s", p.ID, p.Path, p.Value, p.Before)
		if p.Reason != "" {
			line += " (" + p.Reason + ")"
		}
		lines[i] = line
	}
	return fmt.Sprintf("%d of your proposals wait for your owner:\n%s\nTake one back with %s, giving its id.",
		len(waiting), strings.Join(lines, "\n"), toolWithdrawConfig), nil
}

// propose prepares an unrecorded change if the node could run with it:
// the value is checked the way the node checks its configuration when it
// starts. The caller must ask for authorization before applying it.
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
	return proposal.Prepare(cfg, args.Path, value, args.Reason)
}

// Check reports whether the agents of a node can be built from cfg: an
// unknown tool, a tool that is listed twice, a setting a tool does not take,
// a protected path that is none and a description that is no line are errors.
func (b Builder) Check(cfg config.Config) error {
	if _, err := b.ownerTools(cfg); err != nil {
		return err
	}
	if _, err := b.peerTools(cfg, ""); err != nil {
		return err
	}
	if err := b.checkPeers(cfg); err != nil {
		return err
	}
	if err := guard.Valid(cfg.Protected()); err != nil {
		return fmt.Errorf("tools.protected: %w", err)
	}
	return config.ValidDescription(cfg.Description)
}
