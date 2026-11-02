package mcp

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"

	"ouroboros/internal/config"
	"ouroboros/internal/credentials"
)

// Where the browser is sent back to once the owner agreed on the page of the
// authorization server. The port is fixed: a client that registers itself
// names its redirect before the owner starts to sign in.
const (
	callbackPort = 53682
	callbackPath = "/callback"
	redirectURL  = "http://127.0.0.1:53682/callback"
)

// signInTimeout bounds how long ouro waits for the browser to come back.
const signInTimeout = 5 * time.Minute

// stored is the secret part of the credentials of a server: what it signs in
// with, and the values of the env and the headers of the configuration.
type stored struct {
	Env          map[string]string `json:"env,omitempty"`
	Headers      map[string]string `json:"headers,omitempty"`
	AccessToken  string            `json:"access_token,omitempty"`
	Refresh      string            `json:"refresh_token,omitempty"`
	ClientSecret string            `json:"client_secret,omitempty"`
	Expiry       time.Time         `json:"expiry,omitempty"`
}

// data is the part of the credentials of a server that is no secret.
type data struct {
	URL           string   `json:"url,omitempty"`
	TokenEndpoint string   `json:"token_endpoint,omitempty"`
	ClientID      string   `json:"client_id,omitempty"`
	Scopes        []string `json:"scopes,omitempty"`
	Redirect      string   `json:"redirect,omitempty"`
}

// oauth is how the server signs in, or nil when the owner has nothing to sign
// in with yet and the server asks for no sign-in.
//
// The flow is the one of the MCP authorization spec, as the SDK implements it:
// discovery of the resource and authorization server metadata, a client that
// registers itself, the authorization code with PKCE, and refresh tokens. What
// it gets is kept in the credentials of the node, as the access of the server.
func (m *manager) oauth(st *server, interactive bool) (auth.OAuthHandler, error) {
	a, err := m.access(st.name)
	if err != nil && !errors.Is(err, credentials.ErrGone) {
		return nil, err
	}
	var secret stored
	var about data
	_ = json.Unmarshal(a.Secret, &secret)
	_ = json.Unmarshal(a.Data, &about)

	handler := &auth.AuthorizationCodeHandlerConfig{
		DynamicClientRegistrationConfig: &auth.DynamicClientRegistrationConfig{Metadata: &oauthex.ClientRegistrationMetadata{
			RedirectURIs:            []string{redirectURL},
			ClientName:              "ouro",
			GrantTypes:              []string{"authorization_code", "refresh_token"},
			ResponseTypes:           []string{"code"},
			TokenEndpointAuthMethod: "none",
		}},
		RedirectURL:              redirectURL,
		AuthorizationCodeFetcher: m.fetcher(st.name, interactive),
		// A token to renew what the owner agreed to use: this node is on a
		// work machine, and the tokens sit in a file only the owner reads.
		RequestRefreshToken:   true,
		AcceptUnadvertisedIss: true,
		Client:                &http.Client{Timeout: time.Minute},
	}
	cfg := st.configuration()
	if about.TokenEndpoint != "" && (secret.Refresh != "" || secret.AccessToken != "") {
		handler.InitialTokenSource = m.tokenSource(st.name, cfg, about, secret)
	}
	handler.NewTokenSource = func(ctx context.Context, conf *oauth2.Config, token *oauth2.Token) (oauth2.TokenSource, error) {
		m.saveToken(st.name, cfg, conf, token)
		return &persisted{source: conf.TokenSource(ctx, token), save: func(t *oauth2.Token) {
			m.saveToken(st.name, cfg, conf, t)
		}}, nil
	}
	return auth.NewAuthorizationCodeHandler(handler)
}

// persisted is a token source that keeps every token it gets: a refresh
// token that the server replaced is lost otherwise.
type persisted struct {
	source oauth2.TokenSource
	save   func(*oauth2.Token)

	mu   sync.Mutex // guards last: the token source is asked from many requests at once
	last *oauth2.Token
}

func (p *persisted) Token() (*oauth2.Token, error) {
	token, err := p.source.Token()
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	fresh := p.last != token
	if fresh {
		p.last = token
	}
	p.mu.Unlock()
	if fresh && token.AccessToken != "" {
		p.save(token)
	}
	return token, nil
}

// tokenSource renews the token the node kept, without the owner signing in
// again.
func (m *manager) tokenSource(name string, cfg config.Server, about data, secret stored) oauth2.TokenSource {
	conf := &oauth2.Config{
		ClientID:     about.ClientID,
		ClientSecret: secret.ClientSecret,
		RedirectURL:  cmp.Or(about.Redirect, redirectURL),
		Scopes:       about.Scopes,
		Endpoint:     oauth2.Endpoint{TokenURL: about.TokenEndpoint, AuthStyle: oauth2.AuthStyleInParams},
	}
	token := &oauth2.Token{AccessToken: secret.AccessToken, RefreshToken: secret.Refresh, TokenType: "Bearer", Expiry: secret.Expiry}
	return &persisted{source: conf.TokenSource(context.Background(), token), save: func(t *oauth2.Token) {
		m.saveToken(name, cfg, conf, t)
	}}
}

// saveToken keeps what the server issued for the next answer, and for the
// next start of the node.
func (m *manager) saveToken(name string, cfg config.Server, conf *oauth2.Config, token *oauth2.Token) {
	a, _ := m.access(name)
	var secret stored
	var about data
	_ = json.Unmarshal(a.Secret, &secret)
	_ = json.Unmarshal(a.Data, &about)
	secret.AccessToken = token.AccessToken
	secret.Refresh = cmp.Or(token.RefreshToken, secret.Refresh)
	secret.Expiry = token.Expiry
	if conf != nil {
		if conf.ClientSecret != "" {
			secret.ClientSecret = conf.ClientSecret
		}
		about.TokenEndpoint = cmp.Or(conf.Endpoint.TokenURL, about.TokenEndpoint)
		about.ClientID = cmp.Or(conf.ClientID, about.ClientID)
		if len(conf.Scopes) > 0 {
			about.Scopes = conf.Scopes
		}
		about.Redirect = cmp.Or(conf.RedirectURL, about.Redirect, redirectURL)
	}
	m.put(name, cfg, about, secret)
}

// put writes the credentials of the server, keeping what it needs to be
// reached.
func (m *manager) put(name string, cfg config.Server, about data, secret stored) {
	about.URL = cmp.Or(cfg.URL, about.URL)
	aboutJSON, _ := json.Marshal(about)
	secretJSON, _ := json.Marshal(secret)
	title := name
	aboutText := cmp.Or(cfg.URL, cfg.Command)
	store := credentials.Open(string(m.dir))
	err := store.Change(func(list []credentials.Access) ([]credentials.Access, error) {
		access := credentials.Access{Provider: providerOf(name),
			Credentials: credentials.Credentials{Title: title, About: aboutText, Data: aboutJSON, Secret: secretJSON}}
		for i, a := range list {
			if a.Provider == providerOf(name) {
				access.ID = a.ID
				list[i] = access
				return list, nil
			}
		}
		return append(list, access), nil
	})
	_ = err // a node without credentials keeps what it got for this start
}

// forget drops what the server signs in with, which logs the owner out of it.
func (m *manager) forget(name string) error {
	access, err := m.access(name)
	if err != nil {
		if errors.Is(err, credentials.ErrGone) {
			return nil
		}
		return err
	}
	return credentials.Open(string(m.dir)).Remove(access.ID)
}

// errNeedsSignIn says that the server asks for a sign-in, which only the
// owner starts, from /mcp or with ouro mcp login.
var errNeedsSignIn = errors.New("the server needs a sign-in: sign in to it with /mcp or ouro mcp login")

// fetcher opens the page of the authorization server in the browser of the
// owner and waits for it to come back to this machine.
//
// Unless the owner asked for the sign-in, it does not open anything: a 401
// during the start of the node or in the middle of a tool call is no reason
// to take the browser of a work machine.
func (m *manager) fetcher(name string, interactive bool) auth.AuthorizationCodeFetcher {
	return func(ctx context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
		if !interactive {
			return nil, fmt.Errorf("%s: %w", name, errNeedsSignIn)
		}
		listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", callbackPort))
		if err != nil {
			return nil, fmt.Errorf("the sign-in of %s waits for the browser on port %d, which is taken: %w", name, callbackPort, err)
		}
		defer listener.Close()
		type answer struct {
			result *auth.AuthorizationResult
			err    error
		}
		answers := make(chan answer, 1)
		server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != callbackPath {
				http.NotFound(w, r)
				return
			}
			q := r.URL.Query()
			if failed := q.Get("error"); failed != "" {
				fmt.Fprint(w, "<!doctype html><title>ouroboros</title><p>The sign-in failed. Go back to ouro.</p>")
				select {
				case answers <- answer{err: fmt.Errorf("the sign-in of %s failed: %s %s", name, failed, q.Get("error_description"))}:
				default:
				}
				return
			}
			fmt.Fprint(w, "<!doctype html><title>ouroboros</title><p>Signed in. You can close this tab and go back to ouro.</p>")
			select {
			case answers <- answer{result: &auth.AuthorizationResult{Code: q.Get("code"), State: q.Get("state"), Iss: q.Get("iss")}}:
			default: // one answer is enough
			}
		})}
		go server.Serve(listener)
		defer server.Close()
		open(args.URL)
		select {
		case a := <-answers:
			if a.err != nil {
				return nil, a.err
			}
			if a.result.Code == "" {
				return nil, fmt.Errorf("the sign-in of %s came back without a code", name)
			}
			return a.result, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(signInTimeout):
			return nil, fmt.Errorf("the sign-in of %s did not come back within %s", name, signInTimeout)
		}
	}
}

// open opens the address in the browser of the machine, and says nothing when
// there is none: the owner can open it from the log.
func open(address string) {
	program, args := "xdg-open", []string{address}
	switch runtime.GOOS {
	case "darwin":
		program = "open"
	case "windows":
		program, args = "rundll32", []string{"url.dll,FileProtocolHandler", address}
	}
	cmd := exec.Command(program, args...)
	cmd.Stdout, cmd.Stderr = nil, nil
	_ = cmd.Start()
	if cmd.Process != nil {
		go cmd.Wait()
	}
}
