package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"golang.org/x/oauth2"

	"ouroboros/internal/config"
	"ouroboros/internal/credentials"
	"ouroboros/internal/home"
	"ouroboros/plugin"
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
	handler, err := m.oauth(&server{name: "jira", cfg: config.Server{URL: "https://example.com/mcp"}}, signIn{})
	if err != nil {
		t.Fatalf("oauth: %v", err)
	}
	// The transport asks for a token, gets none, and comes back with a 401.
	// Then the flow asks for the authorization code, and that is the step the
	// owner alone starts: no browser, no listener, a plain error.
	if source, err := handler.TokenSource(context.Background()); err != nil || source != nil {
		t.Fatalf("TokenSource = %v, %v, want no source and no error", source, err)
	}
	_, err = m.fetcher("jira", signIn{})(context.Background(), &auth.AuthorizationArgs{URL: "https://example.com/authorize"})
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

// TestAuthConfigUsesTheConfiguredClient: a client the owner wrote in the
// configuration is used as it is, with its secret, instead of registering one.
func TestAuthConfigUsesTheConfiguredClient(t *testing.T) {
	m := &manager{dir: home.Dir(t.TempDir()), servers: map[string]*server{}, added: map[string]string{}}
	st := &server{name: "jira", cfg: config.Server{URL: "https://example.com/mcp", ClientID: "config-id", ClientSecret: "config-secret"}}
	cfg, err := m.authConfig(st, signIn{})
	if err != nil {
		t.Fatalf("authConfig: %v", err)
	}
	if cfg.PreregisteredClient == nil || cfg.PreregisteredClient.ClientID != "config-id" {
		t.Fatalf("pre-registered client = %+v", cfg.PreregisteredClient)
	}
	if cfg.PreregisteredClient.ClientSecretAuth == nil || cfg.PreregisteredClient.ClientSecretAuth.ClientSecret != "config-secret" {
		t.Fatalf("client secret = %+v", cfg.PreregisteredClient.ClientSecretAuth)
	}
	if cfg.DynamicClientRegistrationConfig != nil {
		t.Fatal("a configured client was registered dynamically as well")
	}
}

// TestAuthConfigReusesTheRegisteredClient: the client this node registered
// when it first signed in is used again, not registered anew.
func TestAuthConfigReusesTheRegisteredClient(t *testing.T) {
	dir := home.Dir(t.TempDir())
	m := &manager{dir: dir, servers: map[string]*server{}, added: map[string]string{}}
	url := "https://example.com/mcp"
	m.put("jira", config.Server{URL: url}, data{ClientID: "registered-id", TokenEndpoint: "https://example.com/token"}, stored{ClientSecret: "registered-secret"})
	st := &server{name: "jira", cfg: config.Server{URL: url}}
	cfg, err := m.authConfig(st, signIn{})
	if err != nil {
		t.Fatalf("authConfig: %v", err)
	}
	if cfg.PreregisteredClient == nil || cfg.PreregisteredClient.ClientID != "registered-id" {
		t.Fatalf("pre-registered client = %+v", cfg.PreregisteredClient)
	}
	if cfg.PreregisteredClient.ClientSecretAuth == nil || cfg.PreregisteredClient.ClientSecretAuth.ClientSecret != "registered-secret" {
		t.Fatalf("client secret = %+v", cfg.PreregisteredClient.ClientSecretAuth)
	}
	if cfg.DynamicClientRegistrationConfig != nil {
		t.Fatal("a client that was registered already was registered again")
	}
}

// TestAuthConfigRegistersWhenThereIsNoClient: a server without a client of
// its own still registers one dynamically.
func TestAuthConfigRegistersWhenThereIsNoClient(t *testing.T) {
	m := &manager{dir: home.Dir(t.TempDir()), servers: map[string]*server{}, added: map[string]string{}}
	st := &server{name: "jira", cfg: config.Server{URL: "https://example.com/mcp"}}
	cfg, err := m.authConfig(st, signIn{})
	if err != nil {
		t.Fatalf("authConfig: %v", err)
	}
	if cfg.DynamicClientRegistrationConfig == nil {
		t.Fatal("a server without a client did not register one")
	}
	if cfg.PreregisteredClient != nil {
		t.Fatalf("pre-registered client = %+v, want none", cfg.PreregisteredClient)
	}
}

// TestForgetRevokesTheToken: logging out ends the refresh token at the
// authorization server that has a revocation endpoint, and removes the access.
func TestForgetRevokesTheToken(t *testing.T) {
	var form url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		form = r.PostForm
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	m := &manager{dir: home.Dir(t.TempDir()), servers: map[string]*server{}, added: map[string]string{}}
	cfg := config.Server{URL: "https://example.com/mcp"}
	m.put("jira", cfg, data{RevocationEndpoint: srv.URL, ClientID: "cid"}, stored{Refresh: "refresh", ClientSecret: "secret"})
	if err := m.forget(context.Background(), "jira"); err != nil {
		t.Fatalf("forget: %v", err)
	}
	if got := form.Get("token"); got != "refresh" {
		t.Fatalf("revoked token = %q, want the refresh token", got)
	}
	if got := form.Get("token_type_hint"); got != "refresh_token" {
		t.Fatalf("token type hint = %q", got)
	}
	if got := form.Get("client_id"); got != "cid" {
		t.Fatalf("client id = %q", got)
	}
	if got := form.Get("client_secret"); got != "secret" {
		t.Fatalf("client secret = %q", got)
	}
	if _, err := m.access("jira"); !errors.Is(err, credentials.ErrGone) {
		t.Fatalf("access after logout = %v, want %v", err, credentials.ErrGone)
	}
}

// TestForgetRemovesTheAccessWhenRevocationFails: a token that cannot be
// revoked is said so, but the local credentials go anyway.
func TestForgetRemovesTheAccessWhenRevocationFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	m := &manager{dir: home.Dir(t.TempDir()), servers: map[string]*server{}, added: map[string]string{}}
	m.put("jira", config.Server{URL: "https://example.com/mcp"}, data{RevocationEndpoint: srv.URL}, stored{AccessToken: "access"})
	err := m.forget(context.Background(), "jira")
	if err == nil || !strings.Contains(err.Error(), "was not revoked") {
		t.Fatalf("forget = %v, want an error that says it was not revoked", err)
	}
	if _, err := m.access("jira"); !errors.Is(err, credentials.ErrGone) {
		t.Fatalf("access after a failed logout = %v, want %v", err, credentials.ErrGone)
	}
}

// TestRevocationEndpointFallsBackToTheRoot: the server has no protected
// resource metadata, as Atlassian has none, and its authorization server
// metadata is at the origin. That is where the revocation endpoint is found.
func TestRevocationEndpointFallsBackToTheRoot(t *testing.T) {
	var base string
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                           base,
			"authorization_endpoint":           base + "/authorize",
			"token_endpoint":                   base + "/token",
			"jwks_uri":                         base + "/jwks",
			"response_types_supported":         []string{"code"},
			"code_challenge_methods_supported": []string{"S256"},
			"revocation_endpoint":              base + "/revoke",
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	base = srv.URL

	got, err := revocationEndpoint(context.Background(), srv.URL+"/v1/mcp")
	if err != nil {
		t.Fatalf("revocation endpoint: %v", err)
	}
	if got != srv.URL+"/revoke" {
		t.Fatalf("revocation endpoint = %q, want %q", got, srv.URL+"/revoke")
	}
}

// TestParsePasted: the owner may paste the whole address the browser ended up
// at, or only its query; an error and a missing code are no sign-in.
func TestParsePasted(t *testing.T) {
	for _, tt := range []struct {
		text, code, state, iss string
		wantErr                bool
	}{
		{"http://127.0.0.1:53682/callback?code=c&state=s&iss=i", "c", "s", "i", false},
		{"code=c&state=s", "c", "s", "", false},
		{"https://example.com/callback?error=access_denied&error_description=no", "", "", "", true},
		{"https://example.com/callback?state=s", "", "", "", true},
		{"not an address", "", "", "", true},
	} {
		result, err := parsePasted(tt.text)
		if tt.wantErr {
			if err == nil {
				t.Fatalf("parsePasted(%q) = %+v, want an error", tt.text, result)
			}
			continue
		}
		if err != nil {
			t.Fatalf("parsePasted(%q): %v", tt.text, err)
		}
		if result.Code != tt.code || result.State != tt.state || result.Iss != tt.iss {
			t.Fatalf("parsePasted(%q) = %+v, want code %q, state %q, iss %q", tt.text, result, tt.code, tt.state, tt.iss)
		}
	}
}

// TestSignInWithAPastedAddress: a node whose port the browser cannot reach
// still signs in: the address the browser ended up at is pasted.
func TestSignInWithAPastedAddress(t *testing.T) {
	oldListen, oldOpen := listen, openBrowser
	t.Cleanup(func() { listen, openBrowser = oldListen, oldOpen })
	listen = func(string, string) (net.Listener, error) { return nil, errors.New("the port is taken") }
	openBrowser = func(string) {}

	m := &manager{dir: home.Dir(t.TempDir()), servers: map[string]*server{}, added: map[string]string{}}
	fetch := m.fetcher("jira", signIn{interactive: true, paste: func(string) (string, error) {
		return "http://127.0.0.1:53682/callback?code=c&state=s&iss=i", nil
	}})
	result, err := fetch(context.Background(), &auth.AuthorizationArgs{URL: "https://example.com/authorize"})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if result.Code != "c" || result.State != "s" || result.Iss != "i" {
		t.Fatalf("result = %+v", result)
	}
}

// TestSignInWithoutABrowserOrAPaste: a taken port and no paste is an error,
// not a wait for a browser that cannot come.
func TestSignInWithoutABrowserOrAPaste(t *testing.T) {
	oldListen := listen
	t.Cleanup(func() { listen = oldListen })
	listen = func(string, string) (net.Listener, error) { return nil, errors.New("the port is taken") }

	m := &manager{dir: home.Dir(t.TempDir()), servers: map[string]*server{}, added: map[string]string{}}
	_, err := m.fetcher("jira", signIn{interactive: true})(context.Background(), &auth.AuthorizationArgs{URL: "https://example.com/authorize"})
	if err == nil || !strings.Contains(err.Error(), "port 53682") {
		t.Fatalf("fetch = %v, want the port in the error", err)
	}
}

// TestPasteReadsTheAddress: the console shows the address to open and takes
// the address the browser ended up at, until it is one.
func TestPasteReadsTheAddress(t *testing.T) {
	var out bytes.Buffer
	m := &manager{console: plugin.NewConsole(plugin.Terminal{In: strings.NewReader("not an address\ncode=c&state=s\n"), Out: &out})}
	line, err := m.paste("https://example.com/authorize")
	if err != nil {
		t.Fatalf("paste: %v", err)
	}
	if line != "code=c&state=s" {
		t.Fatalf("pasted address = %q", line)
	}
	if got := out.String(); !strings.Contains(got, "https://example.com/authorize") || !strings.Contains(got, "Paste the whole address") {
		t.Fatalf("console = %q", got)
	}
	if _, err := (&manager{}).paste("https://example.com/authorize"); err == nil {
		t.Fatal("a manager without a console pasted")
	}
}

// TestForgetRevokesWithBasicAuth: a client whose token endpoint takes the
// secret in the Authorization header revokes the same way.
func TestForgetRevokesWithBasicAuth(t *testing.T) {
	var user, pass string
	var form url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, _ = r.BasicAuth()
		_ = r.ParseForm()
		form = r.PostForm
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	m := &manager{dir: home.Dir(t.TempDir()), servers: map[string]*server{}, added: map[string]string{}}
	m.put("jira", config.Server{URL: "https://example.com/mcp"}, data{
		RevocationEndpoint: srv.URL, ClientID: "cid", AuthStyle: int(oauth2.AuthStyleInHeader),
	}, stored{Refresh: "refresh", ClientSecret: "secret"})
	if err := m.forget(context.Background(), "jira"); err != nil {
		t.Fatalf("forget: %v", err)
	}
	if user != "cid" || pass != "secret" {
		t.Fatalf("basic auth = %q, %q", user, pass)
	}
	if got := form.Get("client_secret"); got != "" {
		t.Fatalf("client secret in the form = %q, want it in the header", got)
	}
	if got := form.Get("token"); got != "refresh" {
		t.Fatalf("revoked token = %q", got)
	}
}
