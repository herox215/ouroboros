package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"

	"ouroboros/internal/llm"
)

// usage reads the public Go usage route. Neither a server response nor a
// transport error is shown verbatim: either may contain the access key.
func usage(ctx context.Context, endpoint, key string) (llm.Usage, error) {
	body, err := llm.FetchUsage(ctx, "OpenCode Go usage", strings.TrimRight(endpoint, "/")+"/usage", key)
	if err != nil {
		return llm.Usage{}, err
	}
	windows, err := parseUsage(body)
	return llm.Usage{Windows: windows}, err
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
