package a2a

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"ouroboros/internal/identity"
)

// testSelf is the public key of the node the requests of signed are for.
const testSelf = "key of the node that is asked"

// signed returns a request with body that id signed just now, for
// MethodSend of the node testSelf.
func signed(id *identity.Identity, body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	sign(r, []byte(body), id, 41337, testSelf, MethodSend)
	return r
}

// sentAt is the time r says it was signed at.
func sentAt(r *http.Request) time.Time {
	sec, _ := strconv.ParseInt(r.Header.Get(HeaderTime), 10, 64)
	return time.Unix(sec, 0)
}

// A sender whose node does not run has no port to be called back on, and
// names none: not the port 0.
func TestSignWithoutPort(t *testing.T) {
	id := identity.Generate()
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}"))
	sign(r, []byte("{}"), id, 0, testSelf, MethodSend)
	if port := r.Header.Get(HeaderPort); port != "" {
		t.Fatalf("port %q", port)
	}
	if key, err := newVerifier(testSelf).verify(r, []byte("{}"), MethodSend); err != nil || key != id.Public() {
		t.Fatalf("got %q, %v", key, err)
	}
}

// Only a request that is signed, fresh and new is taken for its sender's.
func TestVerify(t *testing.T) {
	id := identity.Generate()
	const body = `{"jsonrpc":"2.0"}`
	tests := []struct {
		name   string
		change func(r *http.Request, v *verifier)
		body   string
		want   string // error, "" for a request that passes
	}{
		{"signed", func(*http.Request, *verifier) {}, body, ""},
		{"no key", func(r *http.Request, _ *verifier) { r.Header.Del(HeaderPublic) }, body, "missing signature"},
		{"no timestamp", func(r *http.Request, _ *verifier) { r.Header.Del(HeaderTime) }, body, "missing signature"},
		{"no nonce", func(r *http.Request, _ *verifier) { r.Header.Del(HeaderNonce) }, body, "missing signature"},
		{"timestamp is no number", func(r *http.Request, _ *verifier) { r.Header.Set(HeaderTime, "now") }, body, "stale request"},
		{"as old as it may be", func(r *http.Request, v *verifier) {
			v.now = func() time.Time { return sentAt(r).Add(clockSkew) }
		}, body, ""},
		{"as far ahead as it may be", func(r *http.Request, v *verifier) {
			v.now = func() time.Time { return sentAt(r).Add(-clockSkew) }
		}, body, ""},
		{"too old", func(_ *http.Request, v *verifier) {
			v.now = func() time.Time { return time.Now().Add(clockSkew + time.Minute) }
		}, body, "stale request"},
		{"from the future", func(_ *http.Request, v *verifier) {
			v.now = func() time.Time { return time.Now().Add(-clockSkew - time.Minute) }
		}, body, "stale request"},
		{"no signature", func(r *http.Request, _ *verifier) { r.Header.Del(HeaderSig) }, body, "bad signature"},
		{"signature is not base64", func(r *http.Request, _ *verifier) { r.Header.Set(HeaderSig, "!") }, body, "bad signature"},
		{"other body", func(*http.Request, *verifier) {}, `{"jsonrpc":"2.1"}`, "bad signature"},
		{"other key", func(r *http.Request, _ *verifier) {
			r.Header.Set(HeaderPublic, identity.Generate().Public())
		}, body, "bad signature"},
		{"other timestamp", func(r *http.Request, _ *verifier) {
			r.Header.Set(HeaderTime, fmt.Sprint(time.Now().Unix()+1))
		}, body, "bad signature"},
		{"other nonce", func(r *http.Request, _ *verifier) { r.Header.Set(HeaderNonce, "again") }, body, "bad signature"},
		{"other port", func(r *http.Request, _ *verifier) { r.Header.Set(HeaderPort, "1") }, body, "bad signature"},
		{"signed by another key", func(r *http.Request, _ *verifier) {
			other := signed(identity.Generate(), body)
			r.Header.Set(HeaderSig, other.Header.Get(HeaderSig))
		}, body, "bad signature"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, v := signed(id, body), newVerifier(testSelf)
			tt.change(r, v)
			key, err := v.verify(r, []byte(tt.body), MethodSend)
			switch {
			case tt.want == "" && (err != nil || key != id.Public()):
				t.Fatalf("got %q, %v", key, err)
			case tt.want != "" && (err == nil || err.Error() != tt.want || key != ""):
				t.Fatalf("got %q, %v; want %q", key, err, tt.want)
			}
		})
	}
}

// A request is valid at the node and for the method it was signed for only:
// what one node accepted cannot be sent to another that trusts the sender too.
func TestVerifyBindsRecipientAndMethod(t *testing.T) {
	id := identity.Generate()
	body := []byte("{}")
	verify := func(self, method string, change func(*http.Request)) string {
		r := signed(id, "{}")
		change(r)
		if _, err := newVerifier(self).verify(r, body, method); err != nil {
			return err.Error()
		}
		return ""
	}
	unchanged := func(*http.Request) {}
	if got := verify(testSelf, MethodSend, unchanged); got != "" {
		t.Fatalf("at the node it is for: %q", got)
	}
	if got := verify("another node", MethodSend, unchanged); got != "request is for another node" {
		t.Fatalf("at another node: %q", got)
	}
	// Naming the other node does not help: the signature covers the name.
	if got := verify("another node", MethodSend, func(r *http.Request) {
		r.Header.Set(HeaderRecipient, "another node")
	}); got != "bad signature" {
		t.Fatalf("readdressed: %q", got)
	}
	if got := verify(testSelf, MethodSend, func(r *http.Request) { r.Header.Del(HeaderRecipient) }); got != "request is for another node" {
		t.Fatalf("for nobody: %q", got)
	}
	if got := verify(testSelf, MethodAccept, unchanged); got != "bad signature" {
		t.Fatalf("for another method: %q", got)
	}
}

// A request is accepted once: what was recorded on the wire cannot be sent again.
func TestVerifyRejectsReplay(t *testing.T) {
	id, v := identity.Generate(), newVerifier(testSelf)
	r := signed(id, "{}")
	if _, err := v.verify(r, []byte("{}"), MethodSend); err != nil {
		t.Fatal(err)
	}
	if _, err := v.verify(r, []byte("{}"), MethodSend); err == nil || err.Error() != "replayed request" {
		t.Fatalf("got %v", err)
	}
	// Another request of the same node has its own nonce.
	if _, err := v.verify(signed(id, "{}"), []byte("{}"), MethodSend); err != nil {
		t.Fatal(err)
	}
	// A request that failed does not use up its nonce.
	late := signed(id, "{}")
	if _, err := v.verify(late, []byte("other"), MethodSend); err == nil {
		t.Fatal("a changed body should fail")
	}
	if _, err := v.verify(late, []byte("{}"), MethodSend); err != nil {
		t.Fatal(err)
	}
}

// A nonce is remembered for as long as its request could be sent again, and
// forgotten after that without looking at the others.
func TestVerifierForgetsOnlyStaleNonces(t *testing.T) {
	v := newVerifier(testSelf)
	now := time.Now()
	v.now = func() time.Time { return now }
	replayed := func(nonce string) bool {
		err := v.remember("key", nonce)
		return err != nil && err.Error() == "replayed request"
	}
	if err := v.remember("key", "first"); err != nil {
		t.Fatal(err)
	}
	// A new generation of nonces begins; the one before is still known.
	now = now.Add(nonceLife)
	if err := v.remember("key", "second"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(nonceLife - time.Second)
	if !replayed("first") || !replayed("second") {
		t.Fatal("a nonce was forgotten while its request could still be sent again")
	}
	// By now a request with the first nonce is too old to be accepted anyway.
	now = now.Add(time.Second)
	if !replayed("second") || len(v.old) != 1 {
		t.Fatalf("second should be the generation before: %v", v.old)
	}
	if err := v.remember("key", "first"); err != nil {
		t.Fatalf("a stale nonce should be forgotten: %v", err)
	}
}

// The nonces a node remembers are bounded. At the bound it refuses requests
// rather than forgetting nonces that could be replayed.
func TestVerifierBoundsNonces(t *testing.T) {
	v := newVerifier(testSelf)
	now := time.Now()
	v.now = func() time.Time { return now }
	for i := range maxNonces {
		if err := v.remember("key", fmt.Sprint(i)); err != nil {
			t.Fatalf("nonce %d: %v", i, err)
		}
	}
	if err := v.remember("key", "one more"); err == nil || err.Error() != "too many requests" {
		t.Fatalf("beyond the bound: %v", err)
	}
	if err := v.remember("key", "0"); err == nil || err.Error() != "replayed request" {
		t.Fatalf("a replay at the bound: %v", err)
	}
	if len(v.seen) != maxNonces {
		t.Fatalf("%d nonces remembered, want %d", len(v.seen), maxNonces)
	}
	// The refusal reaches whoever sent the request.
	if _, err := v.verify(signed(identity.Generate(), "{}"), []byte("{}"), MethodSend); err == nil || err.Error() != "too many requests" {
		t.Fatalf("verify at the bound: %v", err)
	}

	// Later there is room again, and what was seen is still refused.
	now = now.Add(nonceLife)
	if err := v.remember("key", "one more"); err != nil {
		t.Fatal(err)
	}
	if err := v.remember("key", "0"); err == nil {
		t.Fatal("making room forgot a nonce that could be replayed")
	}
	if len(v.seen)+len(v.old) > 2*maxNonces {
		t.Fatalf("%d nonces remembered", len(v.seen)+len(v.old))
	}
}

// The server answers what the signature check refuses without running anything.
func TestServerRejectsReplayAndForgery(t *testing.T) {
	alice, bob := newNode(t, "alice"), newNode(t, "bob")
	alice.trust.AddTrusted(bob.id.Public(), "bob", "")
	answered := 0
	alice.respond = func(context.Context, string, string) (string, error) {
		answered++
		return "ok", nil
	}
	body := `{"jsonrpc":"2.0","id":1,"method":"message/send","params":{"message":{"parts":[{"kind":"text","text":"hi"}]}}}`
	send := func(change func(*http.Request)) *Error {
		t.Helper()
		r, err := http.NewRequest(http.MethodPost, alice.srv.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		change(r)
		r.Body = io.NopCloser(strings.NewReader(body))
		resp, err := unpinned.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var res response
		if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
			t.Fatal(err)
		}
		return res.Error
	}

	var recorded http.Header
	if err := send(func(r *http.Request) {
		sign(r, []byte(body), bob.id, 0, alice.id.Public(), MethodSend)
		recorded = r.Header.Clone()
	}); err != nil || answered != 1 {
		t.Fatalf("a signed request should be answered: %v, answered %d", err, answered)
	}
	if err := send(func(r *http.Request) { r.Header = recorded.Clone() }); err == nil || err.Code != CodeUnauthorized || err.Message != "replayed request" {
		t.Fatalf("replay: %+v", err)
	}
	if err := send(func(r *http.Request) {
		sign(r, []byte(body), bob.id, 0, alice.id.Public(), MethodSend)
		r.Header.Set(HeaderSig, base64.StdEncoding.EncodeToString(make([]byte, 64)))
	}); err == nil || err.Code != CodeUnauthorized || err.Message != "bad signature" {
		t.Fatalf("forged signature: %+v", err)
	}
	// A stranger who claims the key of a trusted peer.
	if err := send(func(r *http.Request) {
		sign(r, []byte(body), identity.Generate(), 0, alice.id.Public(), MethodSend)
		r.Header.Set(HeaderPublic, bob.id.Public())
	}); err == nil || err.Code != CodeUnauthorized {
		t.Fatalf("claimed key: %+v", err)
	}
	// What bob sent to carol, who trusts him as well, is nothing alice answers.
	carol := newNode(t, "carol")
	if err := send(func(r *http.Request) {
		sign(r, []byte(body), bob.id, 0, carol.id.Public(), MethodSend)
	}); err == nil || err.Code != CodeUnauthorized || err.Message != "request is for another node" {
		t.Fatalf("request for another node: %+v", err)
	}
	// Nor a request that bob signed for another method.
	if err := send(func(r *http.Request) {
		sign(r, []byte(body), bob.id, 0, alice.id.Public(), MethodAccept)
	}); err == nil || err.Code != CodeUnauthorized || err.Message != "bad signature" {
		t.Fatalf("request for another method: %+v", err)
	}
	if answered != 1 {
		t.Fatalf("refused requests were answered %d times", answered-1)
	}
}

func TestCallbackAddr(t *testing.T) {
	tests := []struct{ remote, port, want string }{
		{"10.0.0.9:50000", "41337", "10.0.0.9:41337"},
		{"10.0.0.9:50000", "", "10.0.0.9"},
		{"[fe80::1]:50000", "41337", "[fe80::1]:41337"},
		{"unknown", "41337", "unknown:41337"},
	}
	for _, tt := range tests {
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		r.RemoteAddr = tt.remote
		if got := callbackAddr(r, tt.port); got != tt.want {
			t.Errorf("callbackAddr(%q, %q) = %q, want %q", tt.remote, tt.port, got, tt.want)
		}
	}
}
