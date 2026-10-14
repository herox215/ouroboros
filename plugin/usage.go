package plugin

import (
	"context"

	"ouroboros/internal/llm"
)

// UsageWindow is a quota window reported by a provider, not local token counts.
type UsageWindow = llm.UsageWindow

// AccessUsage is the remaining-quota report of one access. An error belongs
// to that access alone; other providers can still report their quotas.
type AccessUsage struct {
	Access  SetupAccess
	Windows []UsageWindow
	Err     string
}

// Usage asks only providers that expose account quota. It makes no model
// request and does not estimate quotas for providers without such an API.
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
		p, err := book.Provider(a.Provider)
		if err != nil || p.Usage == nil {
			continue
		}
		windows, err := book.Usage(ctx, a.ID)
		report := AccessUsage{Access: a, Windows: windows}
		if err != nil {
			report.Windows, report.Err = nil, err.Error()
		}
		reports = append(reports, report)
	}
	return reports, nil
}
