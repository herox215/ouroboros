package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"ouroboros/internal/llm"
	"ouroboros/internal/openai"
)

const usageBodyLimit = 64 << 10

// usage reads the public Go usage route. Neither a server response nor a
// transport error is shown verbatim: either may contain the access key.
func usage(ctx context.Context, endpoint, key string) ([]llm.UsageWindow, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(endpoint, "/")+"/usage", nil)
	if err != nil {
		return nil, errors.New("cannot create OpenCode Go usage request")
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
		return nil, errors.New("cannot fetch OpenCode Go usage")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, llm.ErrKeyRejected
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cannot fetch OpenCode Go usage: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, usageBodyLimit+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("cannot read OpenCode Go usage")
	}
	if len(body) > usageBodyLimit {
		return nil, errors.New("OpenCode Go usage response is too large")
	}
	return parseUsage(body)
}

// Pointer fields distinguish a real zero from missing or null data. All
// three windows must be valid before any quota is offered to the display.
func parseUsage(body []byte) ([]llm.UsageWindow, error) {
	type window struct {
		Status   *string    `json:"status"`
		Percent  *float64   `json:"percent"`
		ResetsAt *time.Time `json:"resetsAt"`
	}
	var response struct {
		Usage *struct {
			Rolling *window `json:"rolling"`
			Weekly  *window `json:"weekly"`
			Monthly *window `json:"monthly"`
		} `json:"usage"`
	}
	invalid := errors.New("invalid OpenCode Go usage response")
	if err := json.Unmarshal(body, &response); err != nil || response.Usage == nil {
		return nil, invalid
	}
	u := response.Usage
	var result []llm.UsageWindow
	for i, w := range []*window{u.Rolling, u.Weekly, u.Monthly} {
		if w == nil || w.Status == nil || (*w.Status != "ok" && *w.Status != "rate-limited") ||
			w.Percent == nil || *w.Percent < 0 || math.IsNaN(*w.Percent) || math.IsInf(*w.Percent, 0) ||
			w.ResetsAt == nil || w.ResetsAt.IsZero() {
			return nil, invalid
		}
		result = append(result, llm.UsageWindow{Name: []string{"rolling", "weekly", "monthly"}[i], UsedPercent: *w.Percent, ResetsAt: *w.ResetsAt})
	}
	return result, nil
}
