// Package chatgpttest provides a fake of what the chatgpt provider talks to:
// the sign-in service of OpenAI and the Responses API that takes its tokens.
package chatgpttest

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// Scopes are the scopes the fake grants unless told otherwise.
const Scopes = "chatgpt.tokens.use.direct email offline_access openid profile resource.invoke"

// Server is the fake. Its fields are what it answers with: a test changes
// them with Set, and reads what it was asked with Seen.
type Server struct {
	// URL is the base of the sign-in service, and API the base of the API.
	URL, API string

	mu    sync.Mutex
	key   *rsa.PrivateKey
	State State
	seen  Seen
	codes map[string]attempt
	n     int
	// current numbers the tokens that are valid, and expired those whose
	// access token the API refuses.
	current, expired string
}

// State is how the fake answers.
type State struct {
	Email, Subject string
	Scopes         string // granted with the tokens
	Models         []string
	ExpiresIn      int // of an access token, in seconds
	// Deny makes the owner decline: the browser comes back with this error.
	Deny string
	// NoClient makes a registration come back without a client ID, and
	// Client is the ID that comes back for a client that is registered.
	NoClient bool
	Client   string
	// BadState makes the browser come back with another state.
	BadState bool
	// Token changes the claims of the ID token before it is signed, and
	// Sign the token itself afterwards.
	Token func(claims map[string]any)
	Sign  func(token string) string
	// Keys changes the keys that are published to check the ID token with:
	// it gets the one that signs, and returns what is published.
	Keys func(signing map[string]string) []map[string]string
	// NoRefresh leaves the refresh token out of what is granted.
	NoRefresh bool
	// Earliest is sent as earliest_refresh_at, 0 for none.
	Earliest int64
	// TokenError is what the token endpoint refuses with: its status, and
	// the body.
	TokenStatus int
	TokenBody   string
	// RevokeStatus is what revoking answers with, 200 if 0.
	RevokeStatus []int
	// Responses is what the Responses API answers with instead of a reply:
	// each call takes the first that is left. Nil for a reply.
	Responses []Response
	// Reasoning makes a reply think first: an item of reasoning with this
	// as its encrypted content.
	Reasoning string
	// Usage is what a reply says it cost: tokens sent, and of those cached.
	Usage [2]int
	// NoCacheKey makes the API refuse the key of the cache.
	NoCacheKey bool
	// ModelsStatus and ModelsBody are what the list of models answers with
	// instead of the list.
	ModelsStatus int
	ModelsBody   string
	// Slow is how long the token endpoint takes.
	Slow time.Duration
}

// Response is an answer of the Responses API that is no reply.
type Response struct {
	Status int               // 200 if 0
	Header map[string]string // besides the content type
	Body   string            // of an answer that is no stream
	Events []string          // of a stream: the JSON of each event
}

// Seen is what the fake was asked.
type Seen struct {
	Authorize []url.Values // the parameters of each sign-in
	Token     []url.Values // the forms the token endpoint got
	Revoked   []url.Values
	Requests  []map[string]any // the bodies the Responses API got
	Bearers   []string         // the tokens the API was asked with
	Sessions  []string         // the session_id headers the Responses API got, "" for none
	Refreshes int
}

// attempt is a sign-in on its way.
type attempt struct {
	challenge, redirect, client, nonce string
}

// New starts the fake. It signs in "me@example.com" and grants everything.
func New(t testing.TB) *Server {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{key: key, codes: map[string]attempt{}, State: State{
		Email: "me@example.com", Subject: "user-1", Scopes: Scopes, Client: "oaiapp_test",
		Models: []string{"gpt-6.1-sol", "gpt-6.1-mini"}, ExpiresIn: 3600, Usage: [2]int{100, 80},
	}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", s.discovery)
	mux.HandleFunc("GET /jwks", s.jwks)
	mux.HandleFunc("GET /api/accounts/authorize", s.authorize)
	mux.HandleFunc("POST /api/accounts/oauth/token", s.token)
	mux.HandleFunc("POST /revoke", s.revoke)
	mux.HandleFunc("GET /v1/models", s.models)
	mux.HandleFunc("POST /v1/responses", s.responses)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	s.URL, s.API = srv.URL, srv.URL+"/v1"
	return s
}

// Set changes how the fake answers.
func (s *Server) Set(change func(*State)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	change(&s.State)
}

// Seen returns what the fake was asked so far.
func (s *Server) Seen() Seen {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := s.seen
	seen.Authorize, seen.Token, seen.Revoked = slices.Clone(seen.Authorize), slices.Clone(seen.Token), slices.Clone(seen.Revoked)
	seen.Requests, seen.Bearers, seen.Sessions = slices.Clone(seen.Requests), slices.Clone(seen.Bearers), slices.Clone(seen.Sessions)
	return seen
}

// Browser is the browser of an owner who agrees: it opens the page of the
// sign-in and follows it back to the machine it came from.
func (s *Server) Browser(page string) {
	resp, err := http.Get(page)
	if err == nil {
		resp.Body.Close()
	}
}

// Elsewhere is the browser of an owner on another machine: it opens the page
// and returns the address it is sent back to, which it cannot reach.
func (s *Server) Elsewhere(page string) string {
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(page)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	return resp.Header.Get("Location")
}

func (s *Server) discovery(w http.ResponseWriter, _ *http.Request) {
	json.NewEncoder(w).Encode(map[string]string{"issuer": s.URL, "jwks_uri": s.URL + "/jwks", "revocation_endpoint": s.URL + "/revoke"})
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func (s *Server) jwks(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pub := s.key.PublicKey
	signing := map[string]string{"kty": "RSA", "kid": "key-1", "n": b64(pub.N.Bytes()), "e": b64(big.NewInt(int64(pub.E)).Bytes())}
	// Before the key that signs: one of another kind, and one that is none.
	keys := []map[string]string{{"kty": "EC", "kid": "other"}, {"kty": "RSA", "kid": "key-1", "n": "!", "e": "AQAB"}, signing}
	if s.State.Keys != nil {
		keys = s.State.Keys(signing)
	}
	json.NewEncoder(w).Encode(map[string]any{"keys": keys})
}

// authorize is the page the owner agrees on: it sends the browser back
// right away.
func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	q := r.URL.Query()
	s.seen.Authorize = append(s.seen.Authorize, q)
	back := url.Values{"state": {q.Get("state")}}
	if s.State.BadState {
		back.Set("state", "another")
	}
	if s.State.Deny != "" {
		back.Set("error", s.State.Deny)
		back.Set("error_description", "declined")
	} else {
		s.n++
		code := fmt.Sprintf("code-%d", s.n)
		client := q.Get("client_id")
		if client == "dynamic_agent_client" {
			client = s.State.Client
		}
		s.codes[code] = attempt{q.Get("code_challenge"), q.Get("redirect_uri"), client, q.Get("nonce")}
		back.Set("code", code)
		back.Set("scope", s.State.Scopes)
		if !s.State.NoClient {
			back.Set("client_id", s.State.Client)
		}
	}
	http.Redirect(w, r, q.Get("redirect_uri")+"?"+back.Encode(), http.StatusFound)
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	s.mu.Lock()
	slow := s.State.Slow
	s.mu.Unlock()
	time.Sleep(slow)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen.Token = append(s.seen.Token, r.PostForm)
	refuse := func(code string) {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": code, "error_description": "refused"})
	}
	if s.State.TokenStatus != 0 {
		w.WriteHeader(s.State.TokenStatus)
		io.WriteString(w, s.State.TokenBody)
		return
	}
	granted := map[string]any{"token_type": "Bearer", "expires_in": s.State.ExpiresIn, "scope": s.State.Scopes}
	if s.State.Earliest != 0 {
		granted["earliest_refresh_at"] = s.State.Earliest
	}
	s.n++
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		a, ok := s.codes[r.PostForm.Get("code")]
		delete(s.codes, r.PostForm.Get("code"))
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		if !ok || b64(sum[:]) != a.challenge || r.PostForm.Get("redirect_uri") != a.redirect || r.PostForm.Get("client_id") != a.client || r.PostForm.Get("resource") != s.API {
			refuse("invalid_grant")
			return
		}
		granted["id_token"] = s.idToken(a.client, a.nonce)
	case "refresh_token":
		s.seen.Refreshes++
		if r.PostForm.Get("refresh_token") != s.refresh() || r.PostForm.Get("client_id") != s.State.Client || r.PostForm.Get("resource") != s.API || r.PostForm.Has("scope") {
			refuse("refresh_token_reused")
			return
		}
	default:
		refuse("unsupported_grant_type")
		return
	}
	s.current = fmt.Sprintf("%d", s.n)
	granted["access_token"] = s.access()
	if !s.State.NoRefresh {
		granted["refresh_token"] = s.refresh()
	}
	json.NewEncoder(w).Encode(granted)
}

// Access and Refresh are the tokens that are valid right now.
func (s *Server) access() string  { return "at-" + s.current }
func (s *Server) refresh() string { return "rt-" + s.current }

// Expire makes the API refuse the access token that is out, as when it ran
// out or was revoked: the refresh token still works.
func (s *Server) Expire() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expired = s.current
}

// Disconnect makes the tokens that are out of no use, as when the owner
// disconnected the app.
func (s *Server) Disconnect() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expired, s.current = s.current, "gone"
}

// idToken signs who signed in, for the client and the attempt.
func (s *Server) idToken(client, nonce string) string {
	claims := map[string]any{"iss": s.URL, "sub": s.State.Subject, "aud": client, "email": s.State.Email,
		"nonce": nonce, "exp": time.Now().Add(time.Hour).Unix()}
	if s.State.Token != nil {
		s.State.Token(claims)
	}
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "key-1"})
	payload, _ := json.Marshal(claims)
	signed := b64(header) + "." + b64(payload)
	digest := sha256.Sum256([]byte(signed))
	signature, _ := rsa.SignPKCS1v15(rand.Reader, s.key, crypto.SHA256, digest[:])
	token := signed + "." + b64(signature)
	if s.State.Sign != nil {
		token = s.State.Sign(token)
	}
	return token
}

func (s *Server) revoke(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen.Revoked = append(s.seen.Revoked, r.PostForm)
	if len(s.State.RevokeStatus) > 0 {
		w.WriteHeader(s.State.RevokeStatus[0])
		s.State.RevokeStatus = s.State.RevokeStatus[1:]
	}
}

// allowed checks the token of a request to the API.
func (s *Server) allowed(w http.ResponseWriter, r *http.Request) bool {
	bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	s.seen.Bearers = append(s.seen.Bearers, bearer)
	if bearer != s.access() || s.expired == s.current {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"code":"invalid_token","message":"the token is not valid"}}`)
		return false
	}
	return true
}

func (s *Server) models(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.allowed(w, r) {
		return
	}
	if s.State.ModelsStatus != 0 || s.State.ModelsBody != "" {
		if s.State.ModelsStatus != 0 {
			w.WriteHeader(s.State.ModelsStatus)
		}
		io.WriteString(w, s.State.ModelsBody)
		return
	}
	list := []map[string]string{{"slug": "hidden-model", "display_name": "Hidden", "visibility": "hide"}}
	for _, m := range s.State.Models {
		list = append(list, map[string]string{"slug": m, "display_name": strings.ToUpper(m), "visibility": "list"})
	}
	json.NewEncoder(w).Encode(map[string]any{"models": list})
}

// responses answers a request for a response. A reply is
// "[MODEL] items=N last=TEXT", where N counts the items of the input and
// TEXT is the content of the last. An input that ends with a message
// "!TOOL ARGS" of the user is answered with a call of TOOL with the arguments
// ARGS, if the namespace of the request has the tool.
func (s *Server) responses(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.allowed(w, r) {
		return
	}
	var body map[string]any
	json.NewDecoder(r.Body).Decode(&body)
	s.seen.Requests = append(s.seen.Requests, body)
	s.seen.Sessions = append(s.seen.Sessions, r.Header.Get("session_id"))
	if _, sent := body["prompt_cache_key"]; sent && s.State.NoCacheKey {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":{"code":"subscription_sharing_unsupported_capability","message":"unsupported","param":"prompt_cache_key"}}`)
		return
	}
	if len(s.State.Responses) > 0 {
		next := s.State.Responses[0]
		s.State.Responses = s.State.Responses[1:]
		for k, v := range next.Header {
			w.Header().Set(k, v)
		}
		if next.Events != nil {
			w.Header().Set("Content-Type", "text/event-stream")
		}
		if next.Status != 0 {
			w.WriteHeader(next.Status)
		}
		io.WriteString(w, next.Body)
		for _, e := range next.Events {
			fmt.Fprintf(w, "event: something\ndata: %s\n\n", e)
		}
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	event := func(v any) {
		b, _ := json.Marshal(v)
		fmt.Fprintf(w, ": keep-alive\n\ndata: %s\n\n", b)
	}
	input, _ := body["input"].([]any)
	last := map[string]any{}
	if len(input) > 0 {
		last, _ = input[len(input)-1].(map[string]any)
	}
	text, _ := last["content"].(string)
	if text == "" {
		text, _ = last["output"].(string)
	}
	event(map[string]any{"type": "response.created"})
	if s.State.Reasoning != "" {
		event(map[string]any{"type": "response.output_item.done", "item": map[string]any{
			"type": "reasoning", "id": "rs_1", "status": "completed", "summary": []any{}, "encrypted_content": s.State.Reasoning}})
	}
	name, args, _ := strings.Cut(strings.TrimPrefix(text, "!"), " ")
	if strings.HasPrefix(text, "!") && last["role"] == "user" && offered(body, name) {
		if args == "" {
			args = "{}"
		}
		event(map[string]any{"type": "response.output_item.done", "item": map[string]any{
			"type": "function_call", "id": "fc_1", "call_id": "call-1", "namespace": "ouro", "name": name, "arguments": args}})
	} else {
		reply := fmt.Sprintf("[%v] items=%d last=%s", body["model"], len(input), text)
		event(map[string]any{"type": "response.output_item.done", "item": map[string]any{"type": "message", "role": "assistant",
			"content": []any{map[string]string{"type": "output_text", "text": reply[:len(reply)/2]}, map[string]string{"type": "refusal", "text": "no"},
				map[string]string{"type": "output_text", "text": reply[len(reply)/2:]}}}})
	}
	event(map[string]any{"type": "response.completed", "response": map[string]any{"usage": map[string]any{
		"input_tokens": s.State.Usage[0], "input_tokens_details": map[string]int{"cached_tokens": s.State.Usage[1]}}}})
}

// offered reports whether the namespace of the request has the tool name.
func offered(body map[string]any, name string) bool {
	tools, _ := body["tools"].([]any)
	for _, t := range tools {
		group, _ := t.(map[string]any)
		inner, _ := group["tools"].([]any)
		for _, f := range inner {
			if function, _ := f.(map[string]any); function["name"] == name && group["type"] == "namespace" {
				return true
			}
		}
	}
	return false
}
