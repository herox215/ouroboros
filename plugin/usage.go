package plugin

import (
	"context"
	"errors"

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
