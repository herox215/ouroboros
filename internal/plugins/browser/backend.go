package browser

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const requestTimeout = 45 * time.Second
const maxResponse = 16 << 20

func (s settings) normalized() (settings, error) {
	s.Provider = strings.ToLower(strings.TrimSpace(s.Provider))
	if s.Provider == "" {
		s.Provider = "local"
	}
	s.Endpoint = strings.TrimSpace(s.Endpoint)
	switch s.Provider {
	case "local":
		if s.Endpoint != "" || s.TokenEnv != "" || s.Token != "" {
			return s, errors.New("local browser does not use endpoint or token; choose cdp or camofox")
		}
	case "cdp", "camofox":
		u, err := url.Parse(s.Endpoint)
		if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" {
			return s, errors.New("remote browser needs an absolute endpoint without user info or fragment")
		}
		if u.Scheme != "http" && u.Scheme != "https" && !(s.Provider == "cdp" && (u.Scheme == "ws" || u.Scheme == "wss")) {
			return s, errors.New("endpoint must be http/https (or ws/wss for CDP)")
		}
		if s.Visible {
			return s, errors.New("visible is only supported by local Chromium; the hosted server controls its display")
		}
		if strings.ContainsAny(s.Token, "\r\n") {
			return s, errors.New("browser token must not contain newlines")
		}
	default:
		return s, fmt.Errorf("unknown browser provider %q: use local, cdp or camofox", s.Provider)
	}
	if err := validTokenEnv(s.TokenEnv); err != nil {
		return s, err
	}
	return s, nil
}

// selectBackend returns with a read lock, held through the entire action.
// A settings change drains existing calls before cleaning up owned resources.
// Never carry URLs, element references or tab IDs to a different endpoint.
func (b *browser) selectBackend(ctx context.Context, s settings) error {
	// Visible is a local launch preference, not a different endpoint. Existing
	// calls can override it without losing their conversation tabs.
	s.Visible = false
	for {
		if err := waitLock(ctx, b.backendMu.TryRLock); err != nil {
			return err
		}
		active := b.active
		if active.Provider == "" {
			active.Provider = "local"
		}
		active.Visible = false
		if active == s {
			return nil
		}
		b.backendMu.RUnlock()
		if err := waitLock(ctx, b.backendMu.TryLock); err != nil {
			return err
		}
		active = b.active
		active.Visible = false
		if active.Provider == "" {
			active.Provider = "local"
		}
		if active != s {
			b.closeBackend()
			b.convs = map[string]*conversation{}
			b.active = s
			if s.Provider == "camofox" {
				b.camo = newCamo(s)
			}
		}
		b.backendMu.Unlock()
	}
}

// A remote endpoint is an explicit trust decision. Do not follow redirects
// (including redirects to a public service), and never try another backend.
func remoteHTTP() *http.Client {
	return &http.Client{Timeout: requestTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (p *process) ownContext(id string) {
	if !p.remote {
		return
	}
	p.ownedMu.Lock()
	defer p.ownedMu.Unlock()
	if p.owned == nil {
		p.owned = map[string]bool{}
	}
	p.owned[id] = true
}

func (p *process) downloads(ctx context.Context, id string) error {
	if p.remote && id == "" {
		return nil
	} // never change a hosted browser's default context
	params := map[string]any{"behavior": "deny"}
	if id != "" {
		params["browserContextId"] = id
	}
	return p.call(ctx, "", "Browser.setDownloadBehavior", params, nil)
}

// waitLock makes contention cancellable as well as the network request itself.
func waitLock(ctx context.Context, try func() bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if try() {
		return nil
	}
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if try() {
				return nil
			}
		}
	}
}
