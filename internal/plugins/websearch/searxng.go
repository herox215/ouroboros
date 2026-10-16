package websearch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// searxng asks only the configured instance. Its operator must enable the JSON
// format in SearXNG's search.formats. An error or empty result never invokes a
// public fallback, nor are the returned source URLs fetched here.
func (s *search) searxng(ctx context.Context, a args) (string, error) {
	u, err := searchURL(s.endpoint)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("q", a.Query)
	q.Set("format", "json")
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("the search of SearXNG: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("the search of SearXNG returned HTTP %d (JSON search must be enabled)", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse+1))
	if err != nil {
		return "", fmt.Errorf("reading SearXNG search: %w", err)
	}
	if len(data) > maxResponse {
		return "", errors.New("the search response of SearXNG exceeds 1 MiB")
	}
	var r struct {
		Results *[]struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return "", fmt.Errorf("invalid SearXNG search response: %w", err)
	}
	if r.Results == nil {
		return "", errors.New("the search of SearXNG returned no results array")
	}
	var hits []hit
	for _, item := range *r.Results {
		// Result links are untrusted too; do not emit executable or relative URLs.
		link, err := url.Parse(item.URL)
		if err != nil || (link.Scheme != "http" && link.Scheme != "https") || link.Hostname() == "" || link.User != nil {
			continue
		}
		hits = append(hits, hit{Title: strings.TrimSpace(item.Title), URL: item.URL, Excerpt: strings.TrimSpace(item.Content)})
		if len(hits) == a.Limit {
			break
		}
	}
	if len(hits) == 0 {
		return "no search results found", nil
	}
	return hitsText(hits), nil
}
