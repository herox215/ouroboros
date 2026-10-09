// Package chatgpt is the provider "chatgpt": the agents answer within the
// ChatGPT plan of the owner, who signs in with "Sign in with ChatGPT" instead
// of entering an API key.
//
// It follows the way OpenAI documents for open-source apps that run where
// their user is (https://developers.openai.com/siwc/token-sharing-open-source):
// the first sign-in registers a client of its own for the account, without a
// secret, and the tokens it is given are sent to the public Responses API.
// Nothing here is taken from another app: no client ID, no file and no
// endpoint of the Codex CLI.
package chatgpt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"ouroboros/internal/agent"
	"ouroboros/internal/llm"
)

// Name names the provider in the accesses of a node.
const Name = "chatgpt"

const (
	// DefaultAuth is where the owner signs in, and DefaultAPI the API the
	// agents talk to with what that gives.
	DefaultAuth = "https://auth.openai.com"
	DefaultAPI  = "https://api.openai.com/v1"
	// UsagePage is where the owner sees and limits what this app uses of
	// the plan.
	UsagePage = "https://chatgpt.com/settings/usage"

	// planScope is the scope that lets the tokens use the plan.
	planScope = "chatgpt.tokens.use.direct"
	scopes    = "openid profile email offline_access resource.invoke " + planScope
	// appName is what the client is called when it is registered; the owner
	// may change it before agreeing.
	appName = "Ouroboros"
	// callbackPort is the port the browser is sent back to, and
	// callbackPath the path: only the port may be another one.
	callbackPort = 1455
	callbackPath = "/auth/callback"
	userAgent    = "ouroboros"
)

var (
	// now is the time; tests replace it.
	now = time.Now
	// early is how long before a token runs out it is renewed.
	early = 5 * time.Minute
	// authTime is how long a request to sign in, renew or sign out may take.
	authTime = 30 * time.Second
	// backoff is how long is waited before a request is tried again that the
	// API could not take right now, once for each time; tests shorten it.
	backoff = []time.Duration{time.Second, 3 * time.Second}
)

// ErrSignedOut means the login is no longer valid: it ran out, was revoked
// or the app was disconnected in the settings of ChatGPT. The owner signs in
// again in the setup.
var ErrSignedOut = errors.New("the ChatGPT login is no longer valid: sign in again in /setup (or with: ouro start --setup)")

// ErrLimit means the plan, or what the owner allows this app of it, is used
// up. Nothing is switched to another access: the answer stops.
var ErrLimit = errors.New("the usage limit of your ChatGPT plan, or the one set for this app, is reached: see " + UsagePage)

// Options are how the provider reaches OpenAI.
type Options struct {
	// Auth and API are the bases of the two services, the defaults if empty.
	Auth, API string
	// HostID returns what names this installation, see home.Dir.HostID.
	HostID func() (string, error)
	// Port is the port the browser is sent back to, callbackPort if 0;
	// tests pick another.
	Port int
}

func (o Options) auth() string { return strings.TrimRight(orDefault(o.Auth, DefaultAuth), "/") }
func (o Options) api() string  { return strings.TrimRight(orDefault(o.API, DefaultAPI), "/") }

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// Provider returns the provider.
func Provider(o Options) llm.Provider {
	return llm.Provider{
		Name:   Name,
		Title:  "ChatGPT",
		Login:  o.login,
		Models: o.models,
		Model:  func(a llm.Access) agent.LLM { return &client{o: o, access: a} },
		Logout: o.logout,
		// The models of a ChatGPT plan are those of OpenAI.
		Catalog: func(llm.Access) string { return "openai" },
	}
}

// account is what is kept of an access that is no secret, and tokens what
// is.
type (
	account struct {
		Issuer   string   `json:"issuer"`
		ClientID string   `json:"client_id"` // issued for this account when it was registered
		HostID   string   `json:"host_id"`
		Subject  string   `json:"subject"` // who signed in, as the ID token says
		Email    string   `json:"email,omitempty"`
		Scopes   []string `json:"scopes"`
		// Expires is when the access token runs out, and Earliest the time
		// before which it is not renewed, if the API named one.
		Expires  time.Time `json:"expires"`
		Earliest time.Time `json:"earliest_refresh,omitzero"`
	}
	tokens struct {
		Access  string `json:"access_token,omitempty"`
		Refresh string `json:"refresh_token,omitempty"`
		// ID is kept to name the account when it is signed in to again.
		ID string `json:"id_token,omitempty"`
	}
)

// read reads what is kept of an access.
func read(c llm.Credentials) (account, tokens, error) {
	var a account
	var t tokens
	if err := json.Unmarshal(c.Data, &a); err != nil || a.ClientID == "" {
		return a, t, errors.New("chatgpt: the access cannot be read: sign in again")
	}
	if len(c.Secret) > 0 {
		if err := json.Unmarshal(c.Secret, &t); err != nil {
			return a, t, errors.New("chatgpt: the tokens of the access cannot be read: sign in again")
		}
	}
	return a, t, nil
}

// keep is a and t as they are kept.
func keep(a account, t tokens) llm.Credentials {
	data, _ := json.Marshal(a) // texts and times: cannot fail
	secret, _ := json.Marshal(t)
	return llm.Credentials{Title: "ChatGPT", About: a.Email, Data: data, Secret: secret}
}

// granted is what the API answers a code or a refresh token with.
type granted struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
	// EarliestRefreshAt is when the token may be renewed at the earliest,
	// in seconds since 1970, 0 if the API did not say.
	EarliestRefreshAt int64 `json:"earliest_refresh_at"`
}

// take puts what was granted into a and t. A refresh need not grant a new
// ID token or name the scopes again: what is there stays.
func (g granted) take(a *account, t *tokens) {
	t.Access = g.AccessToken
	if g.RefreshToken != "" {
		t.Refresh = g.RefreshToken
	}
	if g.IDToken != "" {
		t.ID = g.IDToken
	}
	if g.Scope != "" {
		a.Scopes = strings.Fields(g.Scope)
	}
	a.Expires = now().Add(time.Duration(g.ExpiresIn) * time.Second).UTC()
	a.Earliest = time.Time{}
	if g.EarliestRefreshAt > 0 {
		a.Earliest = time.Unix(g.EarliestRefreshAt, 0).UTC()
	}
}

// oauthError is what the token endpoint refuses a request with.
type oauthError struct {
	Status int
	Code   string
	Text   string
}

func (e *oauthError) Error() string {
	return fmt.Sprintf("chatgpt: sign-in failed: HTTP %d %s %s", e.Status, e.Code, e.Text)
}

// unusable are the codes of a refresh token that will never work again.
var unusable = []string{"invalid_grant", "invalid_refresh_token", "token_expired", "refresh_token_expired",
	"refresh_token_invalidated", "refresh_token_reused"}

// grant asks the token endpoint with form.
func (o Options) grant(ctx context.Context, form url.Values) (granted, error) {
	var g granted
	ctx, cancel := context.WithTimeout(ctx, authTime)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, o.auth()+"/api/accounts/oauth/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", userAgent)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return g, fmt.Errorf("chatgpt: the sign-in service is not reached: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		// The code is the text of "error", or the code of an error object.
		var e struct {
			Error       json.RawMessage `json:"error"`
			Description string          `json:"error_description"`
		}
		json.Unmarshal(body, &e)
		failed := &oauthError{Status: resp.StatusCode, Text: e.Description}
		var object struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		if json.Unmarshal(e.Error, &failed.Code) != nil && json.Unmarshal(e.Error, &object) == nil {
			failed.Code, failed.Text = object.Code, orDefault(failed.Text, object.Message)
		}
		return g, failed
	}
	if err := json.Unmarshal(body, &g); err != nil || g.AccessToken == "" {
		return g, errors.New("chatgpt: the sign-in service answered without a token")
	}
	return g, nil
}

// token returns the access token of the access a, renewed if it runs out
// soon or if it is refused, the token the API did not take. Whoever renews holds the
// lock of the credentials: answers that find the token run out at the same
// time renew it once, and the others take what the first one saved. The
// refresh token is replaced with every renewal and saved at once, in one
// step with the access token.
func (o Options) token(ctx context.Context, a llm.Access, refused string) (string, error) {
	c, err := a.Credentials()
	if err != nil {
		return "", err
	}
	acct, t, err := read(c)
	if err != nil {
		return "", err
	}
	if t.Access == "" || t.Refresh == "" {
		return "", ErrSignedOut
	}
	if refused == "" && !due(acct) {
		return t.Access, nil
	}
	token := ""
	err = a.Renew(func(c *llm.Credentials) error {
		acct, t, err := read(*c)
		if err != nil {
			return err
		}
		if t.Refresh == "" {
			return ErrSignedOut
		}
		// Another answer renewed it in the meantime.
		if t.Access != "" && t.Access != refused && !due(acct) {
			token = t.Access
			return nil
		}
		g, err := o.grant(ctx, url.Values{"grant_type": {"refresh_token"}, "client_id": {acct.ClientID},
			"refresh_token": {t.Refresh}, "resource": {o.api()}})
		if failed, ok := errors.AsType[*oauthError](err); ok && slices.Contains(unusable, failed.Code) {
			// The tokens are of no use any more. Who the access is of stays,
			// to sign in to it again.
			t.Access, t.Refresh = "", ""
			*c = keep(acct, t)
			return nil
		}
		if err != nil {
			return err
		}
		g.take(&acct, &t)
		*c, token = keep(acct, t), t.Access
		return nil
	})
	if err == nil && token == "" {
		err = ErrSignedOut
	}
	return token, err
}

// due reports whether the access token of a is to be renewed: it runs out
// soon, and the API allows renewing it by now.
func due(a account) bool {
	return now().Add(early).After(a.Expires) && !now().Before(a.Earliest)
}

// send sends the request build makes to the API, with the token of the access
// a. A token the API refuses is renewed once, and the request made anew.
func (o Options) send(ctx context.Context, a llm.Access, build func(token string) (*http.Request, error)) (*http.Response, error) {
	refused := ""
	for {
		token, err := o.token(ctx, a, refused)
		if err != nil {
			return nil, err
		}
		req, err := build(token)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("User-Agent", userAgent)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusUnauthorized || refused != "" {
			return resp, nil
		}
		resp.Body.Close()
		refused = token
	}
}

// models lists the models the account of the access a may use, in the order
// the API names them.
func (o Options) models(ctx context.Context, a llm.Access) (llm.Models, error) {
	ctx, cancel := context.WithTimeout(ctx, authTime)
	defer cancel()
	resp, err := o.send(ctx, a, func(string) (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodGet, o.api()+"/models", nil)
	})
	if err != nil {
		return llm.Models{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return llm.Models{}, failure(resp)
	}
	var out struct {
		Models []struct {
			Slug       string `json:"slug"`
			Visibility string `json:"visibility"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return llm.Models{}, fmt.Errorf("chatgpt: the list of models cannot be read: %w", err)
	}
	var list []string
	for _, m := range out.Models {
		if m.Visibility == "list" && m.Slug != "" {
			list = append(list, m.Slug)
		}
	}
	if len(list) == 0 {
		return llm.Models{}, errors.New("chatgpt: the account has no model to choose")
	}
	return llm.Models{Chat: list, All: list, Default: list[0]}, nil
}

// logout ends the session of the access a at OpenAI: its refresh token is
// revoked, so that nothing renews it any more. The client that was
// registered stays; the owner disconnects it in the settings of ChatGPT.
func (o Options) logout(ctx context.Context, a llm.Access) error {
	c, err := a.Credentials()
	if err != nil {
		return err
	}
	acct, t, err := read(c)
	if err != nil || t.Refresh == "" {
		return nil // nothing to end
	}
	conf, err := o.discover(ctx)
	if err != nil {
		return err
	}
	form := url.Values{"token": {t.Refresh}, "token_type_hint": {"refresh_token"}, "client_id": {acct.ClientID}}
	for try := 0; ; try++ {
		err = revoke(ctx, conf.Revocation, form)
		if err == nil || try == len(backoff) {
			return err
		}
		select {
		case <-time.After(backoff[try]):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// revoke posts form to the endpoint that revokes tokens. An empty 200 is
// what it answers with, also for a token that was of no use already.
func revoke(ctx context.Context, endpoint string, form url.Values) error {
	ctx, cancel := context.WithTimeout(ctx, authTime)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", userAgent)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("chatgpt: the login was not revoked: HTTP %d", resp.StatusCode)
	}
	return nil
}

// discovered is what OpenAI publishes of its sign-in service.
type discovered struct {
	Issuer     string `json:"issuer"`
	JWKS       string `json:"jwks_uri"`
	Revocation string `json:"revocation_endpoint"`
}

// discover reads what the sign-in service publishes of itself.
func (o Options) discover(ctx context.Context) (discovered, error) {
	var d discovered
	if err := getJSON(ctx, o.auth()+"/.well-known/openid-configuration", &d); err != nil {
		return d, err
	}
	if d.Issuer != o.auth() || d.JWKS == "" || d.Revocation == "" {
		return d, errors.New("chatgpt: the sign-in service does not say how its tokens are checked")
	}
	return d, nil
}

// getJSON reads the JSON at url into out.
func getJSON(ctx context.Context, url string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, authTime)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("chatgpt: the sign-in service is not reached: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("chatgpt: %s: HTTP %d", url, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}
