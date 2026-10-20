package chatgpt

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"ouroboros/internal/chatgpt/chatgpttest"
	"ouroboros/internal/credentials"
	"ouroboros/internal/llm"
	"ouroboros/internal/llm/llmtest"
	"ouroboros/internal/openai"
)

const hostID = "urn:uuid:11111111-2222-4333-8444-555555555555"

// fake starts the fake of OpenAI and returns it with the options that reach
// it. What waits between tries does not wait here.
func fake(t *testing.T) (*chatgpttest.Server, Options) {
	t.Helper()
	srv := chatgpttest.New(t)
	oldBackoff := backoff
	t.Cleanup(func() { backoff = oldBackoff })
	backoff = []time.Duration{time.Millisecond, time.Millisecond}
	return srv, Options{Auth: srv.URL + "/", API: srv.API + "/", HostID: func() (string, error) { return hostID, nil }, Port: freePort(t)}
}

// freePort is a port nothing listens on.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// owner is the dialog of an owner whose browser is on this machine and who
// agrees.
func owner(srv *chatgpttest.Server) *llmtest.Dialog {
	return &llmtest.Dialog{OnOpen: func(page string) { go srv.Browser(page) }}
}

// signIn signs in to a new access.
func signIn(t *testing.T, srv *chatgpttest.Server, o Options) llm.Credentials {
	t.Helper()
	c, err := Provider(o).Login(context.Background(), owner(srv), nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// saved signs in and saves the access in a store of its own: it returns the
// book that opens it for the provider, and its ID.
func saved(t *testing.T, srv *chatgpttest.Server, o Options) (llm.Book, string) {
	t.Helper()
	store := credentials.Open(t.TempDir())
	a, err := store.Put(credentials.Access{Provider: Name, Credentials: signIn(t, srv, o)})
	if err != nil {
		t.Fatal(err)
	}
	return llm.Book{Store: store, Providers: func() []llm.Provider { return []llm.Provider{Provider(o)} }}, a.ID
}

// The owner signs in with the browser: the first time registers a client for
// the account, and what comes back is checked and kept, the tokens as secrets.
func TestLogin(t *testing.T) {
	srv, o := fake(t)
	d := owner(srv)
	p := Provider(o)
	if p.Name != "chatgpt" || p.Title == "" {
		t.Fatalf("provider %+v", p)
	}
	c, err := p.Login(context.Background(), d, nil)
	if err != nil {
		t.Fatal(err)
	}
	acct, tok, err := read(c)
	if err != nil {
		t.Fatal(err)
	}
	if c.Title != "ChatGPT" || c.About != "me@example.com" {
		t.Fatalf("shown as %q, %q", c.Title, c.About)
	}
	if acct.ClientID != "oaiapp_test" || acct.HostID != hostID || acct.Subject != "user-1" || acct.Email != "me@example.com" ||
		acct.Issuer != srv.URL || !slices.Contains(acct.Scopes, planScope) || time.Until(acct.Expires) < 50*time.Minute || !acct.Earliest.IsZero() {
		t.Fatalf("kept %+v", acct)
	}
	if tok.Access != "at-2" || tok.Refresh != "rt-2" || strings.Count(tok.ID, ".") != 2 {
		t.Fatalf("tokens %+v", tok)
	}
	// The tokens are secrets; what is shown and what is no secret holds none.
	if got := c.Secrets(); !reflect.DeepEqual(got, slices.Sorted(slices.Values([]string{tok.Access, tok.Refresh, tok.ID}))) {
		t.Fatalf("secrets %q", got)
	}
	for _, secret := range c.Secrets() {
		if strings.Contains(string(c.Data), secret) {
			t.Fatalf("%q is kept where it is no secret: %s", secret, c.Data)
		}
	}

	// What the sign-in was asked: a registration of its own, without the
	// ID of any other app, for this host, with PKCE.
	seen := srv.Seen()
	q := seen.Authorize[0]
	want := map[string]string{"client_id": "dynamic_agent_client", "agent_name_hint": "Ouroboros", "ext_agent_host_id": hostID,
		"response_type": "code", "scope": scopes, "resource": srv.API, "code_challenge_method": "S256"}
	for k, v := range want {
		if q.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, q.Get(k), v)
		}
	}
	if !strings.HasPrefix(q.Get("redirect_uri"), "http://127.0.0.1:") || !strings.HasSuffix(q.Get("redirect_uri"), "/auth/callback") ||
		len(q.Get("state")) < 20 || len(q.Get("nonce")) < 20 || len(q.Get("code_challenge")) != 43 || q.Has("id_token_hint") || q.Has("login_hint") {
		t.Errorf("asked %v", q)
	}
	form := seen.Token[0]
	if form.Get("grant_type") != "authorization_code" || form.Get("client_id") != "oaiapp_test" || form.Get("redirect_uri") != q.Get("redirect_uri") ||
		len(form.Get("code_verifier")) < 43 || form.Has("client_secret") {
		t.Errorf("exchanged with %v", form)
	}
	// The owner is told that peers answer within the plan too, and shown
	// where to sign in; the page is opened, and asked for only once.
	if told := d.Told(); len(told) != 1 || !strings.Contains(told[0], "also when it answers peers") {
		t.Errorf("told %q", told)
	}
	if opened := d.Opened(); len(opened) != 1 || !strings.HasPrefix(opened[0], srv.URL+"/api/accounts/authorize?") {
		t.Errorf("opened %q", opened)
	}
	if asked := d.Asked(); len(asked) != 1 || asked[0].Title != "Continue with ChatGPT" || asked[0].Secret {
		t.Errorf("asked %+v", asked)
	}
}

// Where the browser is on another machine, it cannot come back: the owner
// pastes the address it ended up at. And where the usual port is taken, any
// other is used.
func TestLoginByPastingTheAddress(t *testing.T) {
	srv, o := fake(t)
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	o.Port = taken.Addr().(*net.TCPAddr).Port

	d := &llmtest.Dialog{}
	d.OnOpen = func(page string) {
		back := srv.Elsewhere(page)
		_, query, _ := strings.Cut(back, "?")
		d.Answers = map[string][]string{"Continue with ChatGPT": {"nonsense", "http://127.0.0.1/?state=x", " " + back + " ", query}}
	}
	c, err := Provider(o).Login(context.Background(), d, nil)
	if err != nil {
		t.Fatal(err)
	}
	if acct, _, _ := read(c); acct.ClientID != "oaiapp_test" {
		t.Fatalf("kept %+v", acct)
	}
	if got := d.Rejected(); len(got) != 2 || !strings.Contains(got[0], "not the address the browser ended up at") {
		t.Fatalf("rejected %q", got)
	}
	redirect, _ := url.Parse(srv.Seen().Authorize[0].Get("redirect_uri"))
	if redirect.Port() == "" || redirect.Port() == taken.Addr().String()[strings.LastIndex(taken.Addr().String(), ":")+1:] || redirect.Path != callbackPath {
		t.Fatalf("sent back to %s", redirect)
	}

	// Without any port to listen on, pasting is all there is: the address
	// is the usual one.
	old := listen
	t.Cleanup(func() { listen = old })
	listen = func(string, string) (net.Listener, error) { return nil, errors.New("no ports") }
	o.Port = 0
	if _, err := Provider(o).Login(context.Background(), d, nil); err != nil {
		t.Fatal(err)
	}
	if got := srv.Seen().Authorize[1].Get("redirect_uri"); got != "http://127.0.0.1:1455/auth/callback" {
		t.Fatalf("sent back to %s", got)
	}
	// What the pasted address may be.
	for text, ok := range map[string]bool{"": false, "code=1": false, "state=s": false, "%zz": false, "x?code=1&state=s": true, "error=access_denied&state=s": true} {
		if _, got := parsePasted(text); got != ok {
			t.Errorf("parsePasted(%q) = %v", text, got)
		}
	}
}

// The page of the sign-in answers only its own path, and only once.
func TestLoginCallback(t *testing.T) {
	srv, o := fake(t)
	d := &llmtest.Dialog{}
	// The page is opened and answered before Login waits for the callback, in
	// the caller's goroutine: the server closes once Login returns, and a
	// request of a goroutine left behind then fails or reports after the test.
	d.OnOpen = func(page string) {
		back := srv.Elsewhere(page)
		u, _ := url.Parse(back)
		if resp, err := http.Get(u.Scheme + "://" + u.Host + "/other"); err != nil || resp.StatusCode != http.StatusNotFound {
			t.Errorf("another path: %v, %v", resp, err)
		}
		for range 2 {
			if resp, err := http.Get(back); err != nil || resp.StatusCode != http.StatusOK {
				t.Errorf("the callback: %v, %v", resp, err)
			}
		}
	}
	if _, err := Provider(o).Login(context.Background(), d, nil); err != nil {
		t.Fatal(err)
	}
}

// An access that is signed in to again uses the client it has and names its
// account; it has to be the same account.
func TestLoginAgain(t *testing.T) {
	srv, o := fake(t)
	have := signIn(t, srv, o)
	_, old, _ := read(have)
	other := o
	other.HostID = func() (string, error) { return "", errors.New("the access has its host") }
	again, err := Provider(other).Login(context.Background(), owner(srv), &have)
	if err != nil {
		t.Fatal(err)
	}
	acct, tok, _ := read(again)
	if acct.ClientID != "oaiapp_test" || acct.HostID != hostID || tok.Access == old.Access {
		t.Fatalf("signed in again as %+v", acct)
	}
	q := srv.Seen().Authorize[1]
	if q.Get("client_id") != "oaiapp_test" || q.Get("id_token_hint") != old.ID || q.Get("login_hint") != "me@example.com" || q.Has("agent_name_hint") || q.Get("ext_agent_host_id") != hostID {
		t.Fatalf("asked %v", q)
	}
	// A callback that names no client is of the client that asked.
	srv.Set(func(s *chatgpttest.State) { s.NoClient = true })
	if _, err := Provider(o).Login(context.Background(), owner(srv), &have); err != nil {
		t.Fatalf("a callback without a client: %v", err)
	}

	// Another account, and another registration, are refused: the access
	// keeps what it has.
	srv.Set(func(s *chatgpttest.State) { s.NoClient, s.Subject = false, "user-2" })
	if _, err := Provider(o).Login(context.Background(), owner(srv), &have); err == nil || !strings.Contains(err.Error(), "another ChatGPT account") {
		t.Fatalf("another account: %v", err)
	}
	srv.Set(func(s *chatgpttest.State) { s.Subject, s.Client = "user-1", "oaiapp_other" })
	if _, err := Provider(o).Login(context.Background(), owner(srv), &have); err == nil || !strings.Contains(err.Error(), "another registration") {
		t.Fatalf("another registration: %v", err)
	}
	// An access that cannot be read is registered anew.
	srv.Set(func(s *chatgpttest.State) { s.Client = "oaiapp_test" })
	broken := llm.Credentials{Data: []byte(`{`)}
	if c, err := Provider(o).Login(context.Background(), owner(srv), &broken); err != nil || c.About != "me@example.com" {
		t.Fatalf("an access that cannot be read: %+v, %v", c, err)
	}
	if q := srv.Seen().Authorize; q[len(q)-1].Get("client_id") != "dynamic_agent_client" {
		t.Fatalf("asked %v", q[len(q)-1])
	}
}

func TestLoginFails(t *testing.T) {
	for name, tt := range map[string]struct {
		change func(*chatgpttest.State)
		want   string
	}{
		"declined":            {func(s *chatgpttest.State) { s.Deny = "access_denied" }, "the sign-in was declined, nothing is changed"},
		"another error":       {func(s *chatgpttest.State) { s.Deny = "server_error" }, "the sign-in failed: server_error declined"},
		"another attempt":     {func(s *chatgpttest.State) { s.BadState = true }, "does not belong to this attempt"},
		"no client issued":    {func(s *chatgpttest.State) { s.NoClient = true }, "the registration is incomplete"},
		"the entry as client": {func(s *chatgpttest.State) { s.Client = "dynamic_agent_client" }, "the registration is incomplete"},
		"a code refused": {func(s *chatgpttest.State) {
			s.TokenStatus, s.TokenBody = 400, `{"error":"invalid_grant","error_description":"used"}`
		}, "HTTP 400 invalid_grant used"},
		"an error object": {func(s *chatgpttest.State) {
			s.TokenStatus, s.TokenBody = 403, `{"error":{"code":"blocked","message":"no"}}`
		}, "HTTP 403 blocked no"},
		"no token":         {func(s *chatgpttest.State) { s.TokenStatus, s.TokenBody = 200, `{}` }, "answered without a token"},
		"plan not allowed": {func(s *chatgpttest.State) { s.Scopes = "openid email profile offline_access" }, "using your ChatGPT plan was not allowed"},
		"nothing to renew": {func(s *chatgpttest.State) { s.NoRefresh = true }, "no token to renew the login with"},
		"another nonce":    {func(s *chatgpttest.State) { s.Token = func(c map[string]any) { c["nonce"] = "other" } }, "it does not belong to this attempt"},
		"another audience": {func(s *chatgpttest.State) { s.Token = func(c map[string]any) { c["aud"] = []string{"a", "b"} } }, "it is for another client"},
		"another issuer":   {func(s *chatgpttest.State) { s.Token = func(c map[string]any) { c["iss"] = "https://evil.example" } }, "it is of another issuer"},
		"ran out": {func(s *chatgpttest.State) {
			s.Token = func(c map[string]any) { c["exp"] = time.Now().Add(-time.Minute).Unix() }
		}, "it ran out"},
		// The second a token runs out in, it has run out.
		"runs out now": {func(s *chatgpttest.State) {
			s.Token = func(c map[string]any) { c["exp"] = time.Now().Unix() }
		}, "it ran out"},
		"nobody":            {func(s *chatgpttest.State) { s.Token = func(c map[string]any) { c["sub"] = "" } }, "it names nobody"},
		"an odd audience":   {func(s *chatgpttest.State) { s.Token = func(c map[string]any) { c["aud"] = 7 } }, "it cannot be read"},
		"no token at all":   {func(s *chatgpttest.State) { s.Sign = func(string) string { return "token" } }, "it is no signed token"},
		"not signed":        {func(s *chatgpttest.State) { s.Sign = unsigned }, "it is not signed with RS256"},
		"an odd header":     {func(s *chatgpttest.State) { s.Sign = func(t string) string { return "!" + t } }, "it is not signed with RS256"},
		"an odd signature":  {func(s *chatgpttest.State) { s.Sign = func(t string) string { return t + "!" } }, "its signature cannot be read"},
		"signed by another": {func(s *chatgpttest.State) { s.Sign = func(t string) string { return t[:len(t)-4] + "AAAA" } }, "no key of OpenAI signed it"},
		"an odd payload":    {func(s *chatgpttest.State) { s.Sign = oddPayload }, "no key of OpenAI signed it"},
		// The key that signed, published as a key of another kind, or under
		// another name, is not the key of this token.
		"a key of another kind": {func(s *chatgpttest.State) { s.Keys = republished("kty", "EC") }, "no key of OpenAI signed it"},
		"a key of another name": {func(s *chatgpttest.State) { s.Keys = republished("kid", "key-2") }, "no key of OpenAI signed it"},
		"no key":                {func(s *chatgpttest.State) { s.Keys = func(map[string]string) []map[string]string { return nil } }, "no key of OpenAI signed it"},
	} {
		srv, o := fake(t)
		srv.Set(tt.change)
		c, err := Provider(o).Login(context.Background(), owner(srv), nil)
		if err == nil || !strings.Contains(err.Error(), tt.want) || len(c.Secret) != 0 {
			t.Errorf("%s: got %+v, %v", name, c, err)
		}
	}

	// A token whose audience is a list that has the client is taken.
	srv, o := fake(t)
	srv.Set(func(s *chatgpttest.State) {
		s.Token = func(c map[string]any) { c["aud"] = []string{"x", "oaiapp_test"} }
	})
	if _, err := Provider(o).Login(context.Background(), owner(srv), nil); err != nil {
		t.Errorf("an audience that is a list: %v", err)
	}

	// A host without an ID, and a sign-in service that is not there or not
	// the one it says.
	bad := o
	bad.HostID = func() (string, error) { return "", errors.New("no home") }
	if _, err := Provider(bad).Login(context.Background(), owner(srv), nil); err == nil || !strings.Contains(err.Error(), "this host has no ID") {
		t.Errorf("no host ID: %v", err)
	}
	bad = o
	bad.Auth = "http://127.0.0.1:1"
	if _, err := Provider(bad).Login(context.Background(), owner(srv), nil); err == nil || !strings.Contains(err.Error(), "the sign-in service is not reached") {
		t.Errorf("no sign-in service: %v", err)
	}
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/missing/.well-known/openid-configuration") {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"issuer":"https://elsewhere.example","jwks_uri":"x","revocation_endpoint":"y"}`))
	}))
	defer other.Close()
	bad.Auth = other.URL
	if _, err := Provider(bad).Login(context.Background(), owner(srv), nil); err == nil || !strings.Contains(err.Error(), "does not say how its tokens are checked") {
		t.Errorf("another issuer: %v", err)
	}
	bad.Auth = other.URL + "/missing"
	if _, err := Provider(bad).Login(context.Background(), owner(srv), nil); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Errorf("no configuration: %v", err)
	}
	// The owner gives up while the browser is waited for.
	ctx, cancel := context.WithCancel(context.Background())
	waiting := &llmtest.Dialog{OnOpen: func(string) { cancel() }}
	if _, err := Provider(o).Login(ctx, waiting, nil); !errors.Is(err, context.Canceled) {
		t.Errorf("given up: %v", err)
	}
}

// republished publishes the key that signs with field set to value, alone.
func republished(field, value string) func(map[string]string) []map[string]string {
	return func(signing map[string]string) []map[string]string {
		signing[field] = value
		return []map[string]string{signing}
	}
}

// unsigned is a token with the algorithm "none" in place of a signature.
func unsigned(token string) string {
	parts := strings.Split(token, ".")
	return "eyJhbGciOiJub25lIn0." + parts[1] + "."
}

// oddPayload is a token whose payload was changed after it was signed.
func oddPayload(token string) string {
	parts := strings.Split(token, ".")
	return parts[0] + ".e30." + parts[2]
}

// The keys that check a token have to come, and be keys.
func TestVerifyNeedsTheKeys(t *testing.T) {
	srv, o := fake(t)
	c := signIn(t, srv, o)
	acct, tok, _ := read(c)
	conf := discovered{Issuer: srv.URL, JWKS: "http://127.0.0.1:1/jwks"}
	if _, err := verify(context.Background(), conf, tok.ID, acct.ClientID, ""); err == nil || !strings.Contains(err.Error(), "not reached") {
		t.Fatalf("no keys: %v", err)
	}
	if err := getJSON(context.Background(), "http://bad url", nil); err == nil {
		t.Fatal("a URL that is none should fail")
	}
	// A payload that is no base64, signed all the same.
	if _, err := verify(context.Background(), discovered{Issuer: srv.URL, JWKS: srv.URL + "/jwks"}, "eyJhbGciOiJSUzI1NiJ9.!.AAAA", "c", ""); err == nil {
		t.Fatal("want error")
	}
}

// The models of the account are listed with the token of the access, in the
// order the API names them, without those that are not for choosing.
func TestModels(t *testing.T) {
	srv, o := fake(t)
	c := signIn(t, srv, o)
	p := Provider(o)
	models, err := p.Models(context.Background(), llm.Unsaved(&c))
	want := llm.Models{Chat: []string{"gpt-6.1-sol", "gpt-6.1-mini"}, All: []string{"gpt-6.1-sol", "gpt-6.1-mini"}, Default: "gpt-6.1-sol"}
	if err != nil || !reflect.DeepEqual(models, want) {
		t.Fatalf("got %+v, %v", models, err)
	}
	if bearers := srv.Seen().Bearers; len(bearers) != 1 || bearers[0] != "at-2" {
		t.Fatalf("asked with %v", bearers)
	}
	for name, tt := range map[string]struct {
		change func(*chatgpttest.State)
		want   string
	}{
		"none":        {func(s *chatgpttest.State) { s.Models = nil }, "the account has no model to choose"},
		"no JSON":     {func(s *chatgpttest.State) { s.ModelsBody = "{" }, "the list of models cannot be read"},
		"refused":     {func(s *chatgpttest.State) { s.ModelsStatus, s.ModelsBody = 403, `{"detail":"not in this region"}` }, "chatgpt: HTTP 403  not in this region"},
		"unavailable": {func(s *chatgpttest.State) { s.ModelsStatus, s.ModelsBody = 503, `{"detail":{"why":"down"}}` }, `cannot be used right now, try again later: chatgpt: HTTP 503  {"why":"down"}`},
	} {
		srv.Set(tt.change)
		if _, err := p.Models(context.Background(), llm.Unsaved(&c)); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: %v", name, err)
		}
		srv.Set(func(s *chatgpttest.State) { s.ModelsStatus, s.ModelsBody, s.Models = 0, "", []string{"m"} })
	}
	// An access that cannot be read lists nothing.
	for _, broken := range []llm.Credentials{{}, {Data: c.Data, Secret: []byte(`[`)}} {
		if _, err := p.Models(context.Background(), llm.Unsaved(&broken)); err == nil || !strings.Contains(err.Error(), "sign in again") {
			t.Errorf("an access that cannot be read: %v", err)
		}
	}
	if _, err := p.Models(context.Background(), failing{}); err == nil {
		t.Error("an access that is gone lists models")
	}
	o.API = "http://127.0.0.1:1"
	if _, err := Provider(o).Models(context.Background(), llm.Unsaved(&c)); err == nil {
		t.Error("an API that is not there lists models")
	}
}

// failing is an access that cannot be read.
type failing struct{}

func (failing) Credentials() (llm.Credentials, error)    { return llm.Credentials{}, errors.New("gone") }
func (failing) Renew(func(*llm.Credentials) error) error { return errors.New("gone") }

// A token that runs out soon is renewed before it is used, once: answers
// that find it run out at the same time take what the first one saved, and
// the refresh token, which is replaced with every renewal, is saved with it.
// The access keeps its revision, so no agent is built anew.
func TestTokenIsRenewedOnce(t *testing.T) {
	srv, o := fake(t)
	book, id := saved(t, srv, o)
	before, _ := book.Store.Get(id)
	srv.Set(func(s *chatgpttest.State) {
		s.Slow, s.Earliest = 20*time.Millisecond, time.Now().Add(-time.Minute).Unix()
	})

	// The token is fresh: nothing is renewed.
	if _, err := book.Models(context.Background(), id); err != nil || srv.Seen().Refreshes != 0 {
		t.Fatalf("a fresh token: %v, %d refreshes", err, srv.Seen().Refreshes)
	}
	// It runs out in less than early: renewed, by one of many.
	old := now
	t.Cleanup(func() { now = old })
	now = func() time.Time { return time.Now().Add(58 * time.Minute) }
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			if _, err := book.Models(context.Background(), id); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	seen := srv.Seen()
	if seen.Refreshes != 1 {
		t.Fatalf("%d answers renewed the token", seen.Refreshes)
	}
	form := seen.Token[len(seen.Token)-1]
	if form.Get("grant_type") != "refresh_token" || form.Get("refresh_token") != "rt-2" || form.Get("client_id") != "oaiapp_test" || form.Get("resource") != srv.API || form.Has("scope") {
		t.Fatalf("renewed with %v", form)
	}
	after, _ := book.Store.Get(id)
	acct, tok, _ := read(after.Credentials)
	if tok.Access != "at-3" || tok.Refresh != "rt-3" || tok.ID == "" || after.Rev != before.Rev || acct.Earliest.IsZero() || !slices.Contains(acct.Scopes, planScope) {
		t.Fatalf("after renewing: %+v, %+v (rev %q, was %q)", acct, tok, after.Rev, before.Rev)
	}
	for _, bearer := range seen.Bearers[1:] {
		if bearer != "at-3" {
			t.Fatalf("asked with %v", seen.Bearers)
		}
	}

	// Before the time the API named, the token is not renewed.
	srv.Set(func(s *chatgpttest.State) { s.Earliest = time.Now().Add(10 * time.Hour).Unix() })
	now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if _, err := book.Models(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if _, err := book.Models(context.Background(), id); err != nil || srv.Seen().Refreshes != 2 {
		t.Fatalf("renewed before the API allows: %v, %d refreshes", err, srv.Seen().Refreshes)
	}
}

// A token the API refuses is renewed and the request made anew; a login that
// cannot be renewed any more says that the owner has to sign in again, and
// its tokens are dropped, not the account.
func TestTokenThatIsRefused(t *testing.T) {
	srv, o := fake(t)
	book, id := saved(t, srv, o)
	srv.Expire()
	if models, err := book.Models(context.Background(), id); err != nil || len(models.Chat) != 2 {
		t.Fatalf("with a token that was revoked: %+v, %v", models, err)
	}
	if seen := srv.Seen(); seen.Refreshes != 1 || !slices.Equal(seen.Bearers, []string{"at-2", "at-3"}) {
		t.Fatalf("asked with %v, %d refreshes", seen.Bearers, seen.Refreshes)
	}
	// The sign-in service is down: the login stays as it is.
	srv.Expire()
	srv.Set(func(s *chatgpttest.State) { s.TokenStatus, s.TokenBody = 500, "oops" })
	if _, err := book.Models(context.Background(), id); err == nil || errors.Is(err, ErrSignedOut) || !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("a sign-in service that is down: %v", err)
	}
	a, _ := book.Store.Get(id)
	if _, tok, _ := read(a.Credentials); tok.Refresh != "rt-3" {
		t.Fatalf("the login was dropped for a failure that passes: %+v", tok)
	}
	// The API refuses the renewed token too.
	srv.Set(func(s *chatgpttest.State) {
		s.TokenStatus, s.TokenBody = 200, `{"access_token":"at-nope","expires_in":3600}`
	})
	if _, err := book.Models(context.Background(), id); !errors.Is(err, ErrSignedOut) {
		t.Fatalf("a token the API never takes: %v", err)
	}
	a, _ = book.Store.Get(id)
	if _, tok, _ := read(a.Credentials); tok.Access != "at-nope" || tok.Refresh != "rt-3" || tok.ID == "" {
		t.Fatalf("a renewal without a new refresh token: %+v", tok)
	}

	// The owner disconnected the app: the refresh token is of no use.
	srv.Set(func(s *chatgpttest.State) { s.TokenStatus, s.TokenBody = 0, "" })
	srv.Disconnect()
	if _, err := book.Models(context.Background(), id); !errors.Is(err, ErrSignedOut) || !strings.Contains(err.Error(), "sign in again in /setup") {
		t.Fatalf("a login that was disconnected: %v", err)
	}
	a, _ = book.Store.Get(id)
	acct, tok, _ := read(a.Credentials)
	if tok.Access != "" || tok.Refresh != "" || tok.ID == "" || acct.ClientID != "oaiapp_test" || acct.Subject != "user-1" {
		t.Fatalf("after the login was disconnected: %+v, %+v", acct, tok)
	}
	// From then on nothing is asked.
	refreshes := srv.Seen().Refreshes
	if _, err := book.Models(context.Background(), id); !errors.Is(err, ErrSignedOut) || srv.Seen().Refreshes != refreshes {
		t.Fatalf("a login that is gone: %v", err)
	}
	// And the access can be signed in to again, as the same account.
	have := a.Credentials
	srv2, o2 := srv, o
	c, err := Provider(o2).Login(context.Background(), owner(srv2), &have)
	if err != nil {
		t.Fatal(err)
	}
	if _, tok, _ := read(c); tok.Access == "" || tok.Refresh == "" {
		t.Fatalf("signed in again: %+v", tok)
	}
}

// Renewing reads what is kept anew, under the lock: what cannot be read
// there, or has no token to renew with, is no login.
func TestTokenOfAnAccessThatChanged(t *testing.T) {
	srv, o := fake(t)
	c := signIn(t, srv, o)
	acct, tok, _ := read(c)
	acct.Expires = time.Now().Add(-time.Hour)
	for name, in := range map[string]llm.Credentials{
		"unreadable": {Data: []byte(`{`)},
		"no refresh": keep(acct, tokens{Access: "at"}),
	} {
		a := &changing{first: keep(acct, tok), then: in}
		if _, err := o.token(context.Background(), a, ""); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
	// An access with one of its two tokens alone has no login: nothing is
	// asked with it, and nothing renewed.
	fresh := acct
	fresh.Expires = time.Now().Add(time.Hour)
	for name, tok := range map[string]tokens{"no refresh token": {Access: "at"}, "no access token": {Refresh: "rt"}} {
		half := keep(fresh, tok)
		if got, err := o.token(context.Background(), llm.Unsaved(&half), ""); !errors.Is(err, ErrSignedOut) || got != "" {
			t.Errorf("%s: %q, %v", name, got, err)
		}
	}
	if srv.Seen().Refreshes != 0 {
		t.Fatalf("%d refreshes for accesses without a login", srv.Seen().Refreshes)
	}
	// Another process renewed it while this one waited for the lock.
	a := &changing{first: keep(acct, tok), then: keep(fresh, tokens{Access: "at-new", Refresh: "rt-new"})}
	if got, err := o.token(context.Background(), a, ""); err != nil || got != "at-new" || srv.Seen().Refreshes != 0 {
		t.Fatalf("renewed by another: %q, %v", got, err)
	}
	if _, err := o.token(context.Background(), failing{}, ""); err == nil {
		t.Fatal("an access that is gone has a token")
	}
}

// changing is an access whose credentials are others once it is renewed.
type changing struct{ first, then llm.Credentials }

func (c *changing) Credentials() (llm.Credentials, error) { return c.first, nil }
func (c *changing) Renew(renew func(*llm.Credentials) error) error {
	next := c.then
	return renew(&next)
}

// Removing an access ends its login at OpenAI: the refresh token is revoked.
func TestLogout(t *testing.T) {
	srv, o := fake(t)
	c := signIn(t, srv, o)
	p := Provider(o)
	if err := p.Logout(context.Background(), llm.Unsaved(&c)); err != nil {
		t.Fatal(err)
	}
	revoked := srv.Seen().Revoked
	if len(revoked) != 1 || revoked[0].Get("token") != "rt-2" || revoked[0].Get("token_type_hint") != "refresh_token" || revoked[0].Get("client_id") != "oaiapp_test" {
		t.Fatalf("revoked %v", revoked)
	}
	// A failure that passes is tried again; one that stays is told.
	srv.Set(func(s *chatgpttest.State) { s.RevokeStatus = []int{503, 500} })
	if err := p.Logout(context.Background(), llm.Unsaved(&c)); err != nil || len(srv.Seen().Revoked) != 4 {
		t.Fatalf("tried again: %v, %d times", err, len(srv.Seen().Revoked)-1)
	}
	srv.Set(func(s *chatgpttest.State) { s.RevokeStatus = []int{503, 503, 503} })
	if err := p.Logout(context.Background(), llm.Unsaved(&c)); err == nil || !strings.Contains(err.Error(), "was not revoked: HTTP 503") {
		t.Fatalf("a login that cannot be revoked: %v", err)
	}
	// Given up while it waits to try again.
	old := backoff
	backoff = []time.Duration{time.Hour}
	srv.Set(func(s *chatgpttest.State) { s.RevokeStatus = []int{503} })
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := p.Logout(ctx, llm.Unsaved(&c)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("given up: %v", err)
	}
	backoff = old

	// An access without a login has nothing to end.
	acct, _, _ := read(c)
	none := keep(acct, tokens{})
	before := len(srv.Seen().Revoked)
	if err := p.Logout(context.Background(), llm.Unsaved(&none)); err != nil || len(srv.Seen().Revoked) != before {
		t.Fatalf("nothing to revoke: %v", err)
	}
	if err := p.Logout(context.Background(), failing{}); err == nil {
		t.Fatal("an access that is gone was signed out")
	}
	bad := o
	bad.Auth = "http://127.0.0.1:1"
	if err := Provider(bad).Logout(context.Background(), llm.Unsaved(&c)); err == nil {
		t.Fatal("signed out without a sign-in service")
	}
	if err := revoke(context.Background(), "http://bad url", nil); err == nil {
		t.Fatal("a URL that is none should fail")
	}
	if err := revoke(context.Background(), "http://127.0.0.1:1/revoke", nil); err == nil {
		t.Fatal("an endpoint that is not there should fail")
	}
}

// The defaults are the services of OpenAI.
func TestDefaults(t *testing.T) {
	var o Options
	if o.auth() != "https://auth.openai.com" || o.api() != "https://api.openai.com/v1" {
		t.Fatalf("defaults %q, %q", o.auth(), o.api())
	}
	if (&oauthError{Status: 400, Code: "invalid_grant", Text: "used"}).Error() != "chatgpt: sign-in failed: HTTP 400 invalid_grant used" {
		t.Fatal("the error of the token endpoint")
	}
	// What is kept can be read back.
	a := account{ClientID: "c", Email: "e"}
	if got, tok, err := read(keep(a, tokens{Access: "x"})); err != nil || got.ClientID != "c" || tok.Access != "x" {
		t.Fatalf("read back %+v, %+v, %v", got, tok, err)
	}
	// An access needs the client it was registered as; its tokens may be
	// gone, as after a login that ran out.
	if _, _, err := read(llm.Credentials{Data: []byte(`{"issuer":"x"}`)}); err == nil {
		t.Fatal("an access without a client was read")
	}
	if got, tok, err := read(llm.Credentials{Data: []byte(`{"client_id":"c"}`)}); err != nil || got.ClientID != "c" || tok != (tokens{}) {
		t.Fatalf("an access without tokens: %+v, %+v, %v", got, tok, err)
	}
	var claims audience
	if json.Unmarshal([]byte(`"one"`), &claims); !slices.Equal(claims, audience{"one"}) {
		t.Fatalf("audience %v", claims)
	}
	_ = openai.ReasoningItems
}

// The models of a ChatGPT plan are those of OpenAI, as models.dev lists them.
func TestCatalog(t *testing.T) {
	if got := Provider(Options{}).Catalog(nil); got != "openai" {
		t.Fatalf("catalog %q", got)
	}
}
