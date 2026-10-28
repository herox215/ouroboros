package chatgpt

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"ouroboros/internal/llm"
)

// listen opens the port the browser is sent back to; tests replace it.
var listen = net.Listen

// login signs in with the browser: the owner agrees on the page of OpenAI,
// which sends the browser back to this machine with a code, and the code is
// exchanged for the tokens. A new access registers its client on the way; one
// that is signed in to again uses the client it has, and has to be the same
// account.
//
// Where the browser is on another machine, it cannot reach this one: the
// owner pastes the address it ended up at, which holds the same code. There
// is no way to sign in by a code alone: OpenAI offers none for this flow.
func (o Options) login(ctx context.Context, d llm.Dialog, have *llm.Credentials) (llm.Credentials, error) {
	var prior account
	var old tokens
	if have != nil {
		prior, old, _ = read(*have) // an access that cannot be read is registered anew
	}
	acct := account{Issuer: o.auth(), ClientID: prior.ClientID, HostID: prior.HostID}
	if acct.HostID == "" {
		id, err := o.HostID()
		if err != nil {
			return llm.Credentials{}, fmt.Errorf("chatgpt: this host has no ID to sign in with: %w", err)
		}
		acct.HostID = id
	}
	conf, err := o.discover(ctx)
	if err != nil {
		return llm.Credentials{}, err
	}

	// The browser is sent back to a port of this machine. The usual one may
	// be taken: then any that is free, since only the port may vary.
	port := o.Port
	if port == 0 {
		port = callbackPort
	}
	listener, err := listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		listener, err = listen("tcp", "127.0.0.1:0")
	}
	back := make(chan url.Values, 1)
	if err == nil {
		port = listener.Addr().(*net.TCPAddr).Port
		server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != callbackPath {
				http.NotFound(w, r)
				return
			}
			fmt.Fprint(w, "<!doctype html><title>ouroboros</title><p>Signed in. You can close this tab and go back to ouro.</p>")
			select {
			case back <- r.URL.Query():
			default: // one answer is enough
			}
		})}
		go server.Serve(listener)
		defer server.Close()
	}
	redirect := "http://127.0.0.1:" + strconv.Itoa(port) + callbackPath

	state, nonce, verifier := rand.Text(), rand.Text(), rand.Text()+rand.Text()
	challenge := sha256.Sum256([]byte(verifier))
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {orDefault(acct.ClientID, "dynamic_agent_client")},
		"ext_agent_host_id":     {acct.HostID},
		"redirect_uri":          {redirect},
		"scope":                 {scopes},
		"resource":              {o.api()},
		"state":                 {state},
		"nonce":                 {nonce},
		"code_challenge_method": {"S256"},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(challenge[:])},
	}
	if acct.ClientID == "" {
		q.Set("agent_name_hint", appName) // only when the client is registered
	} else {
		if old.ID != "" {
			q.Set("id_token_hint", old.ID)
		}
		if prior.Email != "" {
			q.Set("login_hint", prior.Email)
		}
	}
	d.Tell("Your agent answers within the limits of your ChatGPT plan, also when it answers peers.")
	d.Open(o.auth() + "/api/accounts/authorize?" + q.Encode())

	// Whichever comes first: the browser, or the address pasted.
	asking, done := context.WithCancel(ctx)
	defer done()
	pasted, typed := make(chan error, 1), make(chan url.Values, 1)
	asked := make(chan struct{})
	// Nothing is asked any more once this returns: the dialog is the
	// owner's again.
	defer func() {
		done()
		<-asked
	}()
	go func() {
		defer close(asked)
		for {
			answer, err := d.Ask(asking, llm.Question{Title: "Continue with ChatGPT", Hint: "waiting for the browser",
				Text: []string{"Sign in on the page that opened.", "", "Is the browser on another machine?",
					"Paste the address it ends up at:", "it starts with http://127.0.0.1"}})
			if err != nil {
				pasted <- err
				return
			}
			if values, ok := parsePasted(answer); ok {
				typed <- values
				return
			}
			d.Reject("that is not the address the browser ended up at")
		}
	}()
	var result url.Values
	select {
	case result = <-back:
	case result = <-typed:
	case err := <-pasted:
		return llm.Credentials{}, err
	}
	done()
	<-asked

	// The answer has to be the one to this attempt.
	if result.Get("state") != state {
		return llm.Credentials{}, errors.New("chatgpt: the sign-in does not belong to this attempt: start again")
	}
	switch failed := result.Get("error"); {
	case failed == "access_denied":
		return llm.Credentials{}, errors.New("chatgpt: the sign-in was declined, nothing is changed")
	case failed != "":
		return llm.Credentials{}, fmt.Errorf("chatgpt: the sign-in failed: %s %s", failed, result.Get("error_description"))
	}
	issued := result.Get("client_id")
	switch {
	case acct.ClientID == "" && (issued == "" || issued == "dynamic_agent_client"):
		return llm.Credentials{}, errors.New("chatgpt: the registration is incomplete: no client was issued. Start again")
	case acct.ClientID != "" && issued != "" && issued != acct.ClientID:
		return llm.Credentials{}, errors.New("chatgpt: the sign-in is of another registration than this access: add a new access for it")
	case acct.ClientID == "":
		acct.ClientID = issued
	}

	g, err := o.grant(ctx, url.Values{"grant_type": {"authorization_code"}, "client_id": {acct.ClientID},
		"code": {result.Get("code")}, "code_verifier": {verifier}, "redirect_uri": {redirect}, "resource": {o.api()}})
	if err != nil {
		return llm.Credentials{}, err
	}
	claims, err := verify(ctx, conf, g.IDToken, acct.ClientID, nonce)
	if err != nil {
		return llm.Credentials{}, err
	}
	if prior.Subject != "" && claims.Subject != prior.Subject {
		return llm.Credentials{}, errors.New("chatgpt: that is another ChatGPT account than the one of this access: add a new access for it")
	}
	acct.Subject, acct.Email = claims.Subject, claims.Email
	var t tokens
	g.take(&acct, &t)
	// A valid ID token alone does not allow using the plan.
	if !slices.Contains(acct.Scopes, planScope) {
		return llm.Credentials{}, errors.New("chatgpt: you are signed in, but using your ChatGPT plan was not allowed. " +
			"Sign in again and allow it, or use an API key with the provider for OpenAI-compatible APIs")
	}
	if t.Refresh == "" {
		return llm.Credentials{}, errors.New("chatgpt: the sign-in gave no token to renew the login with")
	}
	return keep(acct, t), nil
}

// parsePasted reads the address the browser ended up at, as the owner pasted
// it: the whole of it, or what follows its question mark.
func parsePasted(text string) (url.Values, bool) {
	text = strings.TrimSpace(text)
	if _, query, ok := strings.Cut(text, "?"); ok {
		text = query
	}
	values, err := url.ParseQuery(text)
	if err != nil || values.Get("state") == "" || values.Get("code") == "" && values.Get("error") == "" {
		return nil, false
	}
	return values, true
}

// claims are what an ID token says of who signed in.
type claims struct {
	Issuer   string   `json:"iss"`
	Subject  string   `json:"sub"`
	Audience audience `json:"aud"`
	Expires  int64    `json:"exp"`
	Nonce    string   `json:"nonce"`
	Email    string   `json:"email"`
}

// audience is whom a token is for: one name, or a list of them.
type audience []string

func (a *audience) UnmarshalJSON(b []byte) error {
	var one string
	if json.Unmarshal(b, &one) == nil {
		*a = audience{one}
		return nil
	}
	return json.Unmarshal(b, (*[]string)(a))
}

// verify checks the ID token: that OpenAI signed it, for this client and
// this attempt, and that it has not run out. Only then it says who signed in.
func verify(ctx context.Context, conf discovered, token, clientID, nonce string) (claims, error) {
	var c claims
	bad := func(why string) (claims, error) {
		return claims{}, errors.New("chatgpt: the ID token is not valid: " + why)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return bad("it is no signed token")
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || json.Unmarshal(raw, &header) != nil || header.Alg != "RS256" {
		return bad("it is not signed with RS256")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return bad("its signature cannot be read")
	}
	var keys struct {
		Keys []struct {
			Kty string `json:"kty"`
			Kid string `json:"kid"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := getJSON(ctx, conf.JWKS, &keys); err != nil {
		return claims{}, err
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	signed := false
	for _, k := range keys.Keys {
		if k.Kty != "RSA" || k.Kid != header.Kid {
			continue
		}
		// A key that cannot be read is one that checks nothing.
		n, _ := base64.RawURLEncoding.DecodeString(k.N)
		e, _ := base64.RawURLEncoding.DecodeString(k.E)
		key := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
		if rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature) == nil {
			signed = true
		}
	}
	if !signed {
		return bad("no key of OpenAI signed it")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || json.Unmarshal(payload, &c) != nil {
		return bad("it cannot be read")
	}
	switch {
	case c.Issuer != conf.Issuer:
		return bad("it is of another issuer")
	case !slices.Contains(c.Audience, clientID):
		return bad("it is for another client")
	case now().Unix() >= c.Expires:
		return bad("it ran out")
	case c.Nonce != nonce:
		return bad("it does not belong to this attempt")
	case c.Subject == "":
		return bad("it names nobody")
	}
	return c, nil
}
