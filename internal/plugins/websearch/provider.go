package websearch

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Settings belong to the tool, separately for owner and peer conversations.
// Endpoint is the full search URL (including /search for SearXNG). Credentials
// in URLs are refused so that errors and tool settings cannot expose them.
type settings struct {
	Provider string `json:"provider" about:"exa or searxng" unset:"exa"`
	Endpoint string `json:"endpoint" about:"full HTTP(S) search URL; required for searxng; disables public fallback" unset:"Exa's hosted MCP endpoint"`
}

func (s *search) configure(cfg settings) (*search, error) {
	configured := *s
	configured.provider = cfg.Provider
	if configured.provider == "" {
		configured.provider = "exa"
	}
	if configured.provider != "exa" && configured.provider != "searxng" {
		return nil, errors.New("provider must be exa or searxng")
	}
	if configured.provider == "searxng" && cfg.Endpoint == "" {
		return nil, errors.New("searxng requires an endpoint (the full /search URL)")
	}
	if cfg.Endpoint != "" {
		configured.endpoint = cfg.Endpoint
		configured.private = true
	}
	if configured.endpoint == "" {
		configured.endpoint = endpoint
	}
	if _, err := searchURL(configured.endpoint); err != nil {
		return nil, err
	}
	// Never follow redirects: a private instance must not send the query to
	// another host. Copy rather than mutate a client shared by configurations.
	client := http.Client{Timeout: 30 * time.Second}
	if s.client != nil {
		client = *s.client
		if client.Timeout == 0 {
			client.Timeout = 30 * time.Second
		}
	}
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	configured.client = &client
	return &configured, nil
}

func searchURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Opaque != "" || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.RawFragment != "" {
		return nil, errors.New("endpoint must be an absolute HTTP(S) URL without credentials or fragment")
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return nil, errors.New("endpoint port must be between 1 and 65535")
		}
	}
	return u, nil
}
