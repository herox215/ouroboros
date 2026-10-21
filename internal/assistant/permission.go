package assistant

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"ouroboros/internal/agent"
	"ouroboros/internal/config"
)

// The answers to the question whether a call may run, see permitted.
const (
	AllowOnce   = "Run it this time"
	AllowAlways = "Always allow it"
	AllowNot    = "Don't run it"
)

// WithAllowing returns the blueprint whose agent of the owner keeps a call
// the owner allowed for good with allow, see config.Tools.Allowed.
func (b Builder) WithAllowing(allow func(config.Call) error) Builder {
	b.allow = allow
	return b
}

// permitted returns tools as they run for the owner: a call that a rule of
// config.Tools.Ask matches, and that the owner did not allow for good, runs
// once the owner allowed it in a question, see agent.AskOwner. Where nobody
// can be asked, as for a peer or a helper, it is refused. The rules and the
// calls allowed are read at each call, from the configuration of the node or,
// without one, from cfg.
func (b Builder) permitted(cfg config.Config, tools []agent.Tool) []agent.Tool {
	current := func() (config.Config, error) { return cfg, nil }
	if b.node != nil {
		current = b.node.config
	}
	guarded := slices.Clone(tools)
	for i, t := range guarded {
		if t.Name == ToolUserQuestion {
			continue // asks the owner itself
		}
		run := t.Run
		guarded[i].Run = func(ctx context.Context, args json.RawMessage) (string, error) {
			talk, err := b.permit(ctx, current, t.Name, args)
			if err != nil {
				return "", err // says what the owner answered last
			}
			// What the owner was asked and said stays in the conversation
			// as a message of its own: the agent knows it later on too.
			// Outside of an answer it follows the result.
			if talk == "" || agent.Keep(ctx, talk) {
				return run(ctx, args)
			}
			result, err := run(ctx, args)
			if err != nil {
				return result, fmt.Errorf("%w\n\n%s", err, talk)
			}
			return result + "\n\n" + talk, nil
		}
	}
	return guarded
}

// permit returns nil if the call of tool with args may run, and why not
// otherwise: it asks the owner if a rule wants it. With it comes what was
// said then, see exchange: "" where nobody was asked; a refusal says what
// the owner answered last, and with a question of theirs before, all of it.
func (b Builder) permit(ctx context.Context, current func() (config.Config, error), tool string, args json.RawMessage) (string, error) {
	talk := exchange{tool: tool}
	err := b.permitting(ctx, current, tool, args, &talk)
	return talk.String(), err
}

// exchange is what was said between the agent and the owner on whether a
// call may run: questions and answers, in turn, and whether the owner
// answered in words of their own rather than with an option.
type exchange struct {
	tool     string
	said     []string
	ownWords bool
}

// ExchangeHead begins what the agent keeps of what the owner was asked and
// said before a call ran, see exchange.
const ExchangeHead = "[Not from the owner: before the call of "

// String is the exchange as the agent keeps it: "" where the owner only
// chose an option, which the result tells well enough.
func (e exchange) String() string {
	if !e.ownWords {
		return ""
	}
	return ExchangeHead + e.tool + " ran, the owner was asked whether it may:\n" + strings.Join(e.said, "\n") + "\n]"
}

// permitting is permit, keeping what is said in talk.
func (b Builder) permitting(ctx context.Context, current func() (config.Config, error), tool string, args json.RawMessage, talk *exchange) error {
	cfg, err := current()
	if err != nil {
		return fmt.Errorf("%s did not run: the rules for what to ask the owner cannot be read: %w", tool, err)
	}
	call := config.Call{Tool: tool, Call: callText(tool, args)}
	rule, err := matching(cfg.AskRules(), call)
	if err != nil {
		return fmt.Errorf("%s did not run: %w", tool, err)
	}
	if rule == nil || slices.Contains(cfg.AllowedCalls(), call) {
		return nil
	}
	if !agent.CanAskOwner(ctx) {
		return fmt.Errorf("%s did not run: it needs the leave of the owner, who cannot be asked here (it matches the rule %q of tools.ask)", tool, rule.Match)
	}
	// The owner is asked in a sentence of the conversation, not with the
	// call: they see the call itself when they ask for it.
	ask := b.askingOf(cfg)
	p := ask.phrase(ctx, tool, call.Call)
	question := p.Question
	askedBack := false // after an unclear answer, once
	for range maxDetails + 1 {
		asked, _ := json.Marshal(Question{Question: question, Options: []string{p.Once, p.Always, p.Not}}) // strings only: cannot fail
		// Permission stays open until the owner decides or the caller cancels.
		ownerReply, err := agent.AskOwnerReply(ctx, asked)
		answer := ownerReply.Text
		// Cancellation wins even if the callback returns a late approval.
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		if !errors.Is(err, agent.ErrNoOwner) {
			talk.said = append(talk.said, "asked: "+flat(question)+" ("+strings.Join([]string{p.Once, p.Always, p.Not}, " / ")+")")
		}
		if err == nil {
			talk.said = append(talk.said, "owner: "+answer)
			options := []string{p.Once, p.Always, p.Not, AllowOnce, AllowAlways, AllowNot}
			talk.ownWords = talk.ownWords || !slices.ContainsFunc(options, func(o string) bool { return strings.EqualFold(flat(answer), o) })
		}
		switch {
		case errors.Is(err, agent.ErrNoOwner):
			return fmt.Errorf("%s did not run: it needs the leave of the owner, who cannot be asked here (it matches the rule %q of tools.ask)", tool, rule.Match)
		case err != nil:
			return fmt.Errorf("%s did not run: the owner was asked whether it may, and did not answer: %w", tool, err)
		}
		// Pictures are conversational input, never permission evidence, even
		// with an approval-looking caption. Return control to the agent.
		if ownerReply.HasAttachments {
			return fmt.Errorf("%s did not run: the owner sent attachments instead of explicit permission; owner said: %s", tool, answer)
		}
		if len(ownerReply.Images) != 0 {
			return fmt.Errorf("%s did not run: the owner sent images instead of explicit permission; owner said: %s", tool, answer)
		}
		decided, reply := ask.decide(ctx, p, tool, call.Call, answer)
		// The owner hears at once that their answer arrived, in words of
		// the conversation, before the call runs or is left.
		switch decided {
		case decidedOnce, decidedAlways:
			agent.Say(ctx, cmp.Or(reply, p.Yes))
		case decidedNot:
			agent.Say(ctx, cmp.Or(reply, p.No))
		}
		switch decided {
		case decidedOnce:
			return nil
		case decidedAlways:
			if b.allow == nil {
				return nil // nowhere to keep it: allowed this time
			}
			if err := b.allow(call); err != nil {
				agent.Remark(ctx, "allowed this time; keeping it for good failed: "+err.Error())
			}
			return nil
		case decidedNot:
			return fmt.Errorf("%s did not run: the owner did not allow it", tool)
		case decidedDetails:
			question = p.Question + "\n\n" + tool + ": " + call.Call
			continue
		case decidedReply:
			question = reply + "\n\n" + p.Question
			continue
		case decidedMessage:
			return fmt.Errorf("%s did not run: permission remains undecided; continue with the owner's new message: %s", tool, answer)
		case decidedUnclear:
			// The owner is asked back once rather than left without word.
			if reply != "" && !askedBack {
				askedBack = true
				question = reply
				continue
			}
		}
		if len(talk.said) > 2 {
			return fmt.Errorf("%s did not run: the owner did not decide on it:\n\n%s", tool, talk)
		}
		return fmt.Errorf("%s did not run: asked whether it may (%q), the owner said: %s", tool, p.Question, answer)
	}
	return fmt.Errorf("%s did not run: the owner asked about the call and did not decide:\n\n%s", tool, talk)
}

// maxDetails is how often the owner is answered a question about the call,
// or shown it in full on their asking, before the call is left undecided.
const maxDetails = 2

// callText is the text of a call of tool with args that a rule matches: the
// command of bash, the arguments of any other tool as JSON on one line.
func callText(tool string, args json.RawMessage) string {
	if tool == ToolBash {
		var a struct {
			Command string `json:"command"`
		}
		if json.Unmarshal(args, &a) == nil && a.Command != "" {
			return a.Command
		}
	}
	var line bytes.Buffer
	if json.Compact(&line, args) != nil {
		return strings.Join(strings.Fields(string(args)), " ")
	}
	return line.String()
}

// matching returns the first of rules that matches call, nil if none does.
// A rule whose expression is none is an error, so that a typo of the owner
// lets nothing through that it was meant to stop.
func matching(rules []config.Rule, call config.Call) (*config.Rule, error) {
	for i, r := range rules {
		if r.Tool != "" && r.Tool != call.Tool {
			continue
		}
		re, err := regexp.Compile(r.Match)
		if err != nil {
			return nil, fmt.Errorf("tools.ask: %q is no regular expression: %w", r.Match, err)
		}
		if re.MatchString(call.Call) {
			return &rules[i], nil
		}
	}
	return nil, nil
}
