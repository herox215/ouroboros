package mcp

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"golang.org/x/oauth2"

	"ouroboros/internal/config"
	"ouroboros/internal/home"
)

// staticToken hands out the same token, as a renewed one does.
type staticToken struct{ token *oauth2.Token }

func (s staticToken) Token() (*oauth2.Token, error) { return s.token, nil }

// TestPersistedConcurrent asks the token source from several requests at once,
// as the HTTP client of a transport does: no race, and one save.
func TestPersistedConcurrent(t *testing.T) {
	var saves atomic.Int32
	p := &persisted{
		source: staticToken{token: &oauth2.Token{AccessToken: "a", RefreshToken: "r"}},
		save:   func(*oauth2.Token) { saves.Add(1) },
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := p.Token(); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if got := saves.Load(); got != 1 {
		t.Fatalf("the token was saved %d times, want 1", got)
	}
}

// TestNoSignInUnasked keeps the browser closed: a server that asks for a
// sign-in during the start of the node or in a tool call says so instead, and
// has no token to show for it.
func TestNoSignInUnasked(t *testing.T) {
	m := &manager{dir: home.Dir(t.TempDir()), servers: map[string]*server{}, added: map[string]string{}}
	handler, err := m.oauth(&server{name: "jira", cfg: config.Server{URL: "https://example.com/mcp"}}, false)
	if err != nil {
		t.Fatalf("oauth: %v", err)
	}
	// The transport asks for a token, gets none, and comes back with a 401.
	// Then the flow asks for the authorization code, and that is the step the
	// owner alone starts: no browser, no listener, a plain error.
	if source, err := handler.TokenSource(context.Background()); err != nil || source != nil {
		t.Fatalf("TokenSource = %v, %v, want no source and no error", source, err)
	}
	_, err = m.fetcher("jira", false)(context.Background(), &auth.AuthorizationArgs{URL: "https://example.com/authorize"})
	if !errors.Is(err, errNeedsSignIn) {
		t.Fatalf("fetcher = %v, want %v", err, errNeedsSignIn)
	}
	if status := authStatus(err); status != statusNeedsAuth {
		t.Fatalf("status = %q, want %q", status, statusNeedsAuth)
	}
}

// TestSessionRefusesADisabledServer: a tool call connects no server that the
// owner turned off in the meantime.
func TestSessionRefusesADisabledServer(t *testing.T) {
	dir := home.Dir(t.TempDir())
	if err := config.Update(string(dir), func(c *config.Config) error {
		c.MCP = &config.MCP{Servers: map[string]config.Server{
			"off": {URL: "https://example.com/mcp", Disabled: true},
		}}
		return nil
	}); err != nil {
		t.Fatalf("config: %v", err)
	}
	m := &manager{dir: dir, servers: map[string]*server{}, added: map[string]string{}}
	st := &server{name: "off", cfg: config.Server{URL: "https://example.com/mcp"}}
	if _, err := m.session(context.Background(), st); err == nil {
		t.Fatal("a disabled server was connected")
	} else if want := "is disabled"; !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want one that says %q", err, want)
	}
	gone := &server{name: "gone", cfg: config.Server{URL: "https://example.com/mcp"}}
	if _, err := m.session(context.Background(), gone); err == nil {
		t.Fatal("a removed server was connected")
	} else if want := "no longer configured"; !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want one that says %q", err, want)
	}
}
