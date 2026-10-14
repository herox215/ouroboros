package llm

import (
	"context"
	"errors"
	"time"
)

// UsageWindow is the quota used in one window of an access. UsedPercent may
// exceed 100; only the display should clamp the percentage remaining.
type UsageWindow struct {
	Name        string
	UsedPercent float64
	ResetsAt    time.Time
}

// ErrUsageUnavailable means the provider does not report usage.
var ErrUsageUnavailable = errors.New("usage is not available from this provider")

// Usage lists the quota windows of the access id.
func (b Book) Usage(ctx context.Context, id string) ([]UsageWindow, error) {
	_, p, a, err := b.of(id)
	if err != nil {
		return nil, err
	}
	if p.Usage == nil {
		return nil, ErrUsageUnavailable
	}
	return p.Usage(ctx, a)
}
