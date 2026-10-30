package plugin

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"ouroboros/internal/llm"
)

type (
	// Usage is what the API of a provider tells an access may still spend:
	// quota windows, a prepaid balance, or both. Not the tokens of a call,
	// which ChatMessage.Usage tells.
	Usage = llm.Usage
	// UsageWindow is a quota window reported by a provider, not local token counts.
	UsageWindow = llm.UsageWindow
	// UsageBalance is the prepaid credit of an account in one currency.
	UsageBalance = llm.UsageBalance
)

// AccessUsage is the report of one access: what its provider told of it. An
// error belongs to that access alone; other providers can still report.
type AccessUsage struct {
	Access SetupAccess
	Usage
	// Unreported says that the provider of the access tells ouro no usage.
	Unreported bool
	Err        string
	// Uses are the scopes and models the access answers with now, like
	// "sessions: glm-5" and "thread: kimi-k2". Usage leaves it empty.
	Uses []string
}

// LLM is an access and a model the agent answers one scope with.
type LLM struct {
	Scope         string // "owner", "sessions", "thread" or "peers"
	Access, Model string
}

// CurrentLLMs returns the access and the model each scope of the node is
// answered with now: the owner's, the sessions', the thread's and peers'.
// A scope that follows another one is reported with the access and model it
// follows.
func (o *Owner) CurrentLLMs() ([]LLM, error) {
	cfg, err := o.home.Config()
	if err != nil {
		return nil, err
	}
	sa, sm, _ := cfg.SessionLLM()
	ta, tm, _ := cfg.ThreadLLM()
	pa, pm, _ := cfg.PeerLLM()
	return []LLM{
		{Scope: "owner", Access: cfg.Access, Model: cfg.Model},
		{Scope: "sessions", Access: sa, Model: sm},
		{Scope: "thread", Access: ta, Model: tm},
		{Scope: "peers", Access: pa, Model: pm},
	}, nil
}

// Usage reports every access of the node: what the API of its provider tells
// it may still spend, or that it tells nothing. It makes no model request and
// does not estimate quotas for providers without such an API.
func (o *Owner) Usage(ctx context.Context, providers []Provider) ([]AccessUsage, error) {
	s, err := o.Setup(providers)
	if err != nil {
		return nil, err
	}
	accesses, err := s.Accesses()
	if err != nil {
		return nil, err
	}
	return o.report(ctx, accesses, providers)
}

// CurrentUsage reports only the accesses the sessions and the thread are
// answered through now, not every access of the node: what /usage shows. An
// access that both use is reported once, with both uses. Only those accesses
// are asked, so a provider of an access that is not in use is not reached.
func (o *Owner) CurrentUsage(ctx context.Context, providers []Provider) ([]AccessUsage, error) {
	cfg, err := o.home.Config()
	if err != nil {
		return nil, err
	}
	sa, sm, _ := cfg.SessionLLM()
	ta, tm, _ := cfg.ThreadLLM()
	uses := map[string][]string{}
	var order []string
	add := func(scope, access, model string) {
		if access == "" {
			return
		}
		if _, ok := uses[access]; !ok {
			order = append(order, access)
		}
		uses[access] = append(uses[access], fmt.Sprintf("%s: %s", scope, model))
	}
	add("sessions", sa, sm)
	add("thread", ta, tm)

	s, err := o.Setup(providers)
	if err != nil {
		return nil, err
	}
	accesses, err := s.Accesses()
	if err != nil {
		return nil, err
	}
	var current []SetupAccess
	for _, id := range order {
		for _, a := range accesses {
			if a.ID == id {
				current = append(current, a)
				break
			}
		}
	}
	reports, err := o.report(ctx, current, providers)
	if err != nil {
		return nil, err
	}
	for i := range reports {
		reports[i].Uses = uses[reports[i].Access.ID]
	}
	return reports, nil
}

// report asks the provider of every access in accesses, in their order.
func (o *Owner) report(ctx context.Context, accesses []SetupAccess, providers []Provider) ([]AccessUsage, error) {
	book := llm.Book{Store: o.home.Credentials(), Providers: func() []llm.Provider { return providers }}
	var reports []AccessUsage
	for _, a := range accesses {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		usage, err := book.Usage(ctx, a.ID)
		report := AccessUsage{Access: a, Usage: usage}
		switch {
		case errors.Is(err, llm.ErrUsageUnavailable):
			report.Unreported = true
		case err != nil:
			report.Usage, report.Err = Usage{}, err.Error()
		}
		reports = append(reports, report)
	}
	return reports, nil
}

// UsageText lists the accesses of reports with what their provider told of
// it, or why it told nothing.
func UsageText(reports []AccessUsage) string {
	if len(reports) == 0 {
		return "No access is set up."
	}
	var lines []string
	for _, r := range reports {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		label := r.Access.Label
		switch {
		case len(r.Uses) > 0:
			label += " · " + strings.Join(r.Uses, " · ")
		case r.Access.Active:
			label += " · in use"
		}
		lines = append(append(lines, label), reported(r)...)
	}
	return strings.Join(lines, "\n")
}

// reported are the lines under the label of the access of r.
func reported(r AccessUsage) []string {
	switch {
	case r.Err != "":
		return []string{"  Unavailable: " + r.Err}
	case r.Unreported:
		return []string{"  Not reported: ouro reads no quota or balance from this provider."}
	case len(r.Windows) == 0 && len(r.Balances) == 0 && !r.Insufficient:
		return []string{"  No quota or balance reported."}
	}
	var lines []string
	for _, w := range r.Windows {
		remaining := max(0.0, min(100.0, 100-w.UsedPercent))
		name := w.Name
		if name == "rolling" {
			name = "5 hours"
		}
		lines = append(lines, fmt.Sprintf("  %s: ~%.1f%% remaining · resets %s", name, remaining, w.ResetsAt.Local().Format("02 Jan 15:04 MST")))
	}
	for _, b := range r.Balances {
		lines = append(lines, fmt.Sprintf("  balance: %s %s", b.Amount, b.Currency))
	}
	if r.Insufficient {
		lines = append(lines, "  Not enough balance for API calls.")
	}
	return lines
}
