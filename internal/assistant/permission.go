package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"ouroboros/internal/agent"
	"ouroboros/internal/config"
)

// The answers to the question whether a call may run, see permitted.
const (
	AllowOnce   = "Run it this time"
	AllowAlways = "Always allow it"
	AllowNot    = "Don't run it"
)

// permissionTimeout bounds only the wait for permission, not the tool it
// guards or an ordinary user_question. Rebuild waits for the owner until the
// enclosing context ends instead. No answer means no permission.
const permissionTimeout = 30 * time.Second

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
			if err := b.permit(ctx, current, t.Name, args); err != nil {
				return "", err
			}
			return run(ctx, args)
		}
	}
	return guarded
}

// permit returns nil if the call of tool with args may run, and why not
// otherwise: it asks the owner if a rule wants it.
func (b Builder) permit(ctx context.Context, current func() (config.Config, error), tool string, args json.RawMessage) error {
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
	q := Question{
		// one line, as the question shows: the call first, where it is cut
		Question: fmt.Sprintf("Allow %s: %s", tool, flat(call.Call)),
		Options:  []string{AllowOnce, AllowAlways, AllowNot},
	}
	asked, _ := json.Marshal(q) // strings only: cannot fail
	asking := ctx
	if tool != ToolRebuild {
		var cancel context.CancelFunc
		asking, cancel = context.WithTimeout(ctx, permissionTimeout)
		defer cancel()
	}
	answer, err := agent.AskOwner(asking, asked)
	// Even an answer arriving just after the deadline grants no permission.
	if asking.Err() != nil {
		err = asking.Err()
	}
	switch {
	case errors.Is(err, agent.ErrNoOwner):
		return fmt.Errorf("%s did not run: it needs the leave of the owner, who cannot be asked here (it matches the rule %q of tools.ask)", tool, rule.Match)
	case tool != ToolRebuild && errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("%s did not run: not allowed because the owner did not answer the permission question in time (at most %s): %w", tool, permissionTimeout, err)
	case err != nil:
		return fmt.Errorf("%s did not run: the owner was asked whether it may, and did not answer: %w", tool, err)
	}
	switch answer {
	case AllowOnce:
		return nil
	case AllowAlways:
		if b.allow == nil {
			return nil // nowhere to keep it: allowed this time
		}
		if err := b.allow(call); err != nil {
			agent.Remark(ctx, "allowed this time; keeping it for good failed: "+err.Error())
		}
		return nil
	case AllowNot:
		return fmt.Errorf("%s did not run: the owner did not allow it", tool)
	}
	return fmt.Errorf("%s did not run: asked whether it may, the owner said: %s", tool, answer)
}

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
