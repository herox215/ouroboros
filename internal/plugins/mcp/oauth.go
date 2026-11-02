package mcp

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
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

// listen and openBrowser are what tests replace: the port the browser comes
// back to, and the browser itself.
var (
	listen      = net.Listen
	openBrowser = open
)

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
	URL                string   `json:"url,omitempty"`
	TokenEndpoint      string   `json:"token_endpoint,omitempty"`
	RevocationEndpoint string   `json:"revocation_endpoint,omitempty"`
	ClientID           string   `json:"client_id,omitempty"`
	AuthStyle          int      `json:"auth_style,omitempty"` // oauth2.AuthStyle of the token endpoint
	Scopes             []string `json:"scopes,omitempty"`
	Redirect           string   `json:"redirect,omitempty"`
}

// oauth is how the server signs in, or nil when the owner has nothing to sign
// in with yet and the server asks for no sign-in.
func (m *manager) oauth(st *server, si signIn) (auth.OAuthHandler, error) {
	handler, err := m.authConfig(st, si)
	if err != nil {
		return nil, err
	}
	return auth.NewAuthorizationCodeHandler(handler)
}

// authConfig is the authorization code flow of the server: the client it uses
// and the fetcher that signs it in.
//
// The flow is the one of the MCP authorization spec, as the SDK implements it:
// discovery of the resource and authorization server metadata, a client that
// is registered already or registers itself, the authorization code with PKCE,
// and refresh tokens. What it gets is kept in the credentials of the node, as
// the access of the server.
func (m *manager) authConfig(st *server, si signIn) (*auth.AuthorizationCodeHandlerConfig, error) {
	a, err := m.access(st.name)
	if err != nil && !errors.Is(err, credentials.ErrGone) {
		return nil, err
	}
	var secret stored
	var about data
	_ = json.Unmarshal(a.Secret, &secret)
	_ = json.Unmarshal(a.Data, &about)

	handler := &auth.AuthorizationCodeHandlerConfig{
		RedirectURL:              redirectURL,
		AuthorizationCodeFetcher: m.fetcher(st.name, si),
		// A token to renew what the owner agreed to use: this node is on a
		// work machine, and the tokens sit in a file only the owner reads.
		RequestRefreshToken:   true,
		AcceptUnadvertisedIss: true,
		Client:                &http.Client{Timeout: time.Minute},
	}
	cfg := st.configuration()
	// A client that is registered already is used as it is: the one the owner
	// wrote in the configuration, or the one this node registered itself when
	// it first signed in, so that a sign-in again is the same client.
	clientID := cfg.ClientID
	clientSecret := expand(cfg.ClientSecret, m.secrets(st.name))
	if clientID == "" && about.ClientID != "" {
		clientID, clientSecret = about.ClientID, secret.ClientSecret
	} else if clientID != "" && clientSecret == "" && about.ClientID == clientID {
		clientSecret = secret.ClientSecret
	}
	if clientID != "" {
		preregistered := &oauthex.ClientCredentials{ClientID: clientID}
		if clientSecret != "" {
			preregistered.ClientSecretAuth = &oauthex.ClientSecretAuth{ClientSecret: clientSecret}
		}
		handler.PreregisteredClient = preregistered
	} else {
		handler.DynamicClientRegistrationConfig = &auth.DynamicClientRegistrationConfig{Metadata: &oauthex.ClientRegistrationMetadata{
			RedirectURIs:            []string{redirectURL},
			ClientName:              "ouro",
			GrantTypes:              []string{"authorization_code", "refresh_token"},
			ResponseTypes:           []string{"code"},
			TokenEndpointAuthMethod: "none",
		}}
	}
	if about.TokenEndpoint != "" && (secret.Refresh != "" || secret.AccessToken != "") {
		handler.InitialTokenSource = m.tokenSource(st.name, cfg, about, secret)
	}
	handler.NewTokenSource = func(ctx context.Context, conf *oauth2.Config, token *oauth2.Token) (oauth2.TokenSource, error) {
		m.saveToken(st.name, cfg, conf, token)
		return &persisted{source: conf.TokenSource(ctx, token), save: func(t *oauth2.Token) {
			m.saveToken(st.name, cfg, conf, t)
		}}, nil
	}
	return handler, nil
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
	style := oauth2.AuthStyle(about.AuthStyle)
	if style == oauth2.AuthStyleAutoDetect {
		style = oauth2.AuthStyleInParams // what this node kept before the style was stored
	}
	conf := &oauth2.Config{
		ClientID:     about.ClientID,
		ClientSecret: secret.ClientSecret,
		RedirectURL:  cmp.Or(about.Redirect, redirectURL),
		Scopes:       about.Scopes,
		Endpoint:     oauth2.Endpoint{TokenURL: about.TokenEndpoint, AuthStyle: style},
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
		if conf.Endpoint.AuthStyle != oauth2.AuthStyleAutoDetect {
			about.AuthStyle = int(conf.Endpoint.AuthStyle)
		}
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
// It also ends the token at the authorization server when that server says
// where: a token that is only forgotten here stays valid there.
func (m *manager) forget(ctx context.Context, name string) error {
	access, err := m.access(name)
	if err != nil {
		if errors.Is(err, credentials.ErrGone) {
			return nil
		}
		return err
	}
	var about data
	var secret stored
	_ = json.Unmarshal(access.Data, &about)
	_ = json.Unmarshal(access.Secret, &secret)
	revokeErr := m.revoke(ctx, name, about, secret)
	removeErr := credentials.Open(string(m.dir)).Remove(access.ID)
	return errors.Join(revokeErr, removeErr)
}

// revoke ends the token of the server at its authorization server, when the
// metadata of the server has a revocation endpoint. It is no failure when no
// token is kept or the server has no such endpoint.
func (m *manager) revoke(ctx context.Context, name string, about data, secret stored) error {
	token, hint := secret.Refresh, "refresh_token"
	if token == "" {
		token, hint = secret.AccessToken, "access_token"
	}
	if token == "" {
		return nil
	}
	endpoint := about.RevocationEndpoint
	if endpoint == "" && about.URL != "" {
		var err error
		endpoint, err = revocationEndpoint(ctx, about.URL)
		if err != nil {
			return fmt.Errorf("the token of %s was not revoked: %w", name, err)
		}
	}
	if endpoint == "" {
		return nil
	}
	form := url.Values{"token": {token}, "token_type_hint": {hint}}
	basic := about.ClientID != "" && secret.ClientSecret != "" && oauth2.AuthStyle(about.AuthStyle) == oauth2.AuthStyleInHeader
	if !basic {
		if about.ClientID != "" {
			form.Set("client_id", about.ClientID)
		}
		if secret.ClientSecret != "" {
			form.Set("client_secret", secret.ClientSecret)
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("the token of %s was not revoked: %w", name, err)
	}
	if basic {
		req.SetBasicAuth(about.ClientID, secret.ClientSecret)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := (&http.Client{Timeout: time.Minute}).Do(req)
	if err != nil {
		return fmt.Errorf("the token of %s was not revoked: %w", name, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("the token of %s was not revoked: the authorization server answered HTTP %d", name, resp.StatusCode)
	}
	return nil
}

// revocationEndpoint reads the revocation endpoint of the authorization
// server of a remote MCP server: its protected resource metadata names the
// authorization server, and the server's metadata says where it revokes.
func revocationEndpoint(ctx context.Context, mcpURL string) (string, error) {
	client := &http.Client{Timeout: time.Minute}
	issuer := ""
	for _, u := range resourceMetadataURLs(mcpURL) {
		prm, err := oauthex.GetProtectedResourceMetadata(ctx, u, mcpURL, client)
		if err != nil || prm == nil || len(prm.AuthorizationServers) == 0 {
			continue
		}
		issuer = prm.AuthorizationServers[0]
		break
	}
	if issuer == "" {
		u, err := url.Parse(mcpURL)
		if err != nil {
			return "", err
		}
		u.Path = ""
		issuer = u.String()
	}
	asm, err := auth.GetAuthServerMetadata(ctx, issuer, client)
	if err != nil {
		return "", err
	}
	if asm == nil {
		return "", nil
	}
	return asm.RevocationEndpoint, nil
}

// resourceMetadataURLs are where the protected resource metadata of an MCP
// server may be: at the path of its endpoint, then at the root, as the MCP
// specification says.
func resourceMetadataURLs(mcpURL string) []string {
	u, err := url.Parse(mcpURL)
	if err != nil {
		return nil
	}
	path := *u
	path.Path = "/.well-known/oauth-protected-resource/" + strings.TrimLeft(u.Path, "/")
	root := *u
	root.Path = "/.well-known/oauth-protected-resource"
	return []string{path.String(), root.String()}
}

// errNeedsSignIn says that the server asks for a sign-in, which only the
// owner starts, from /mcp or with ouro mcp login.
var errNeedsSignIn = errors.New("the server needs a sign-in: sign in to it with /mcp or ouro mcp login")

// fetcher opens the page of the authorization server in the browser of the
// owner and waits for it to come back to this machine. On a node without a
// browser the owner pastes the address the browser ended up at instead.
//
// Unless the owner asked for the sign-in, it does not open anything: a 401
// during the start of the node or in the middle of a tool call is no reason
// to take the browser of a work machine.
func (m *manager) fetcher(name string, si signIn) auth.AuthorizationCodeFetcher {
	return func(ctx context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
		if !si.interactive {
			return nil, fmt.Errorf("%s: %w", name, errNeedsSignIn)
		}
		type answer struct {
			result *auth.AuthorizationResult
			err    error
		}
		answers := make(chan answer, 2)
		listener, err := listen("tcp", fmt.Sprintf("127.0.0.1:%d", callbackPort))
		if err == nil {
			defer listener.Close()
			server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != callbackPath {
					http.NotFound(w, r)
					return
				}
				result, err := resultFromQuery(r.URL.Query())
				if err != nil {
					fmt.Fprint(w, "<!doctype html><title>ouroboros</title><p>The sign-in failed. Go back to ouro.</p>")
					select {
					case answers <- answer{err: err}:
					default:
					}
					return
				}
				fmt.Fprint(w, "<!doctype html><title>ouroboros</title><p>Signed in. You can close this tab and go back to ouro.</p>")
				select {
				case answers <- answer{result: result}:
				default: // one answer is enough
				}
			})}
			go server.Serve(listener)
			defer server.Close()
		} else if si.paste == nil {
			return nil, fmt.Errorf("the sign-in of %s waits for the browser on port %d, which is taken: %w", name, callbackPort, err)
		}
		if si.paste != nil {
			go func() {
				text, err := si.paste(args.URL)
				if err == nil {
					var result *auth.AuthorizationResult
					result, err = parsePasted(text)
					if err == nil {
						select {
						case answers <- answer{result: result}:
						case <-ctx.Done():
						}
						return
					}
				}
				select {
				case answers <- answer{err: err}:
				case <-ctx.Done():
				}
			}()
		}
		openBrowser(args.URL)
		select {
		case a := <-answers:
			if a.err != nil {
				return nil, a.err
			}
			return a.result, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(signInTimeout):
			return nil, fmt.Errorf("the sign-in of %s did not come back within %s", name, signInTimeout)
		}
	}
}

// paste shows the address to open and takes the address the browser ended up
// at, for a node whose browser is elsewhere.
func (m *manager) paste(address string) (string, error) {
	if m.console == nil {
		return "", errors.New("there is no console to paste the address into: open the address in a browser instead")
	}
	fmt.Fprintf(m.console.Out, "Open this address in a browser on a machine that has one:\n\n  %s\n\nPaste the address the browser ends up at: ", address)
	for {
		line, err := m.console.ReadLine()
		if err != nil {
			return "", err
		}
		if line == "" {
			fmt.Fprint(m.console.Out, "Paste the whole address: ")
			continue
		}
		if _, err := parsePasted(line); err != nil {
			fmt.Fprintf(m.console.Out, "%v\nPaste the whole address: ", err)
			continue
		}
		return line, nil
	}
}

// resultFromQuery reads the answer of an authorization server from the query
// of the redirect address, as the browser comes back with it or as the owner
// pastes it.
func resultFromQuery(q url.Values) (*auth.AuthorizationResult, error) {
	if failed := q.Get("error"); failed != "" {
		return nil, fmt.Errorf("the sign-in failed: %s %s", failed, q.Get("error_description"))
	}
	if q.Get("code") == "" {
		return nil, errors.New("the sign-in came back without a code")
	}
	return &auth.AuthorizationResult{Code: q.Get("code"), State: q.Get("state"), Iss: q.Get("iss")}, nil
}

// parsePasted reads the address the browser ended up at, as the owner pasted
// it: the whole address, or what follows its question mark.
func parsePasted(text string) (*auth.AuthorizationResult, error) {
	text = strings.TrimSpace(text)
	if _, query, ok := strings.Cut(text, "?"); ok {
		text = query
	}
	q, err := url.ParseQuery(text)
	if err != nil {
		return nil, fmt.Errorf("the pasted address: %w", err)
	}
	return resultFromQuery(q)
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
