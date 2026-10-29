package llm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"ouroboros/internal/openai"
)

// Usage is what the API of a provider tells an access may still spend: the
// quota windows of a plan, the prepaid balance of an account, or both. It is
// not what a call cost, which chat.Usage tells.
type Usage struct {
	Windows  []UsageWindow
	Balances []UsageBalance
	// Insufficient says that the balance no longer allows calls.
	Insufficient bool
}

// UsageWindow is the quota used in one window of an access. UsedPercent may
// exceed 100; only the display should clamp the percentage remaining.
type UsageWindow struct {
	Name        string
	UsedPercent float64
	ResetsAt    time.Time
}

// UsageBalance is the prepaid credit of an account in one currency. Amount
// is the decimal the API tells, like "12.34": it is shown, not computed with.
type UsageBalance struct {
	Currency string // like "USD"
	Amount   string
}

// ErrUsageUnavailable means the provider does not report usage.
var ErrUsageUnavailable = errors.New("usage is not available from this provider")

// Usage tells what the access id may still spend.
func (b Book) Usage(ctx context.Context, id string) (Usage, error) {
	_, p, a, err := b.of(id)
	if err != nil {
		return Usage{}, err
	}
	if p.Usage == nil {
		return Usage{}, ErrUsageUnavailable
	}
	return p.Usage(ctx, a)
}

// usageLimit is the most bytes FetchUsage takes of an answer.
const usageLimit = 64 << 10

// FetchUsage gets what the API at url tells of the account of key, which goes
// along as a bearer token: the body of an answer with HTTP 200, of at most
// 64 KiB, within 15 seconds. A redirect is not followed. what names what is
// fetched in errors, like "DeepSeek balance". Neither the answer nor an error
// of the transport is told as it is, as either may hold the key; a key that
// is refused fails with ErrKeyRejected.
func FetchUsage(ctx context.Context, what, url, key string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("cannot create %s request", what)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("User-Agent", openai.UserAgent)
	req.Header.Set("Accept", "application/json")
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("cannot fetch %s", what)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, ErrKeyRejected
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cannot fetch %s: HTTP %d", what, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, usageLimit+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("cannot read %s", what)
	}
	if len(body) > usageLimit {
		return nil, fmt.Errorf("%s response is too large", what)
	}
	return body, nil
}
