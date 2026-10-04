package a2a

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"ouroboros/internal/identity"
)

var testCard = AgentCard{ProtocolVersion: ProtocolVersion, Name: "alpha"}

// echo answers with the context ID and text it received.
func echo(_ context.Context, contextID, text string) (string, error) {
	return contextID + ":" + text, nil
}

// newServer runs a node whose agent is respond. A node trusts its own key, so
// its own client is answered without a handshake.
func newServer(t *testing.T, respond Responder) *node {
	t.Helper()
	n := newNode(t, "alpha")
	n.respond = respond
	return n
}

// answering runs a server that holds the key of id and answers every request
// with body.
func answering(t *testing.T, id *identity.Identity, body string) *httptest.Server {
	t.Helper()
	return serve(t, id, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, body) }))
}

// post sends body to n as a request n signed itself.
func post(t *testing.T, n *node, body string) response {
	t.Helper()
	r, err := http.NewRequest(http.MethodPost, n.srv.URL, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var req request
	json.Unmarshal([]byte(body), &req) // what cannot be parsed has no method
	sign(r, []byte(body), n.id, 0, n.id.Public(), req.Method)
	resp, err := unpinned.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var res response
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		t.Fatal(err)
	}
	return res
}

func TestAgentCardURLFromHost(t *testing.T) {
	n := newServer(t, echo)
	srv := n.srv
	resp, err := unpinned.Get(srv.URL + CardPath)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var card AgentCard
	json.NewDecoder(resp.Body).Decode(&card)
	if card.Name != "alpha" || card.URL != srv.URL+"/" || !strings.HasPrefix(card.URL, "https://") {
		t.Fatalf("got %+v", card)
	}
	// Nothing in the answer vouches for the node: the connection does.
	for _, header := range []string{HeaderPublic, HeaderSig} {
		if got := resp.Header.Get(header); got != "" {
			t.Errorf("the card is answered with %s: %q", header, got)
		}
	}

	fetched, err := FetchCard(context.Background(), srv.URL+"/", n.id.Public())
	if err != nil || fetched.Name != "alpha" {
		t.Fatalf("FetchCard: %+v, %v", fetched, err)
	}
	if _, err := FetchCard(context.Background(), srv.URL+"/nowhere", n.id.Public()); err == nil {
		t.Fatal("FetchCard should fail without a card")
	}
}

// A card is only taken from the node that holds the key it is expected from.
func TestCardProvesKey(t *testing.T) {
	ctx := context.Background()
	alice, mallory := newNode(t, "alice"), newNode(t, "mallory")
	if key, err := FetchKey(ctx, alice.srv.URL); err != nil || key != alice.id.Public() {
		t.Fatalf("FetchKey = %q, %v", key, err)
	}
	// Mallory answers at an address where alice is expected.
	if _, err := FetchCard(ctx, mallory.srv.URL, alice.id.Public()); !errors.Is(err, ErrUnverified) {
		t.Fatalf("a card of another node: %v", err)
	}

	// Alice's own card, word for word, proves nothing when another node hands
	// it out: that node does not hold her key.
	relay := serve(t, mallory.id, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp, err := unpinned.Get(alice.srv.URL + CardPath)
		if err != nil {
			t.Error(err)
			return
		}
		defer resp.Body.Close()
		io.Copy(w, resp.Body)
	}))
	if key, err := FetchKey(ctx, relay.URL); err != nil || key != mallory.id.Public() {
		t.Fatalf("the key of a relay is its own: %q, %v", key, err)
	}
	if _, err := FetchCard(ctx, relay.URL, alice.id.Public()); !errors.Is(err, ErrUnverified) {
		t.Fatalf("a card passed on by another node: %v", err)
	}
}

func TestSendRoundTrip(t *testing.T) {
	srv := newServer(t, echo)
	got, err := srv.client().Send(context.Background(), srv.srv.URL, srv.id.Public(), "ctx-1", "hello")
	if err != nil || got != "ctx-1:hello" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestMissingContextIDIsGenerated(t *testing.T) {
	srv := newServer(t, echo)
	res := post(t, srv, `{"jsonrpc":"2.0","id":7,"method":"message/send",
		"params":{"message":{"kind":"message","role":"user","parts":[{"kind":"text","text":"hi"}]}}}`)
	if res.Error != nil || res.Result == nil || string(res.ID) != "7" {
		t.Fatalf("got %+v", res)
	}
	if res.Result.ContextID == "" || res.Result.Role != "agent" || res.Result.Text() != res.Result.ContextID+":hi" {
		t.Fatalf("got %+v", res.Result)
	}
}

func TestRPCErrors(t *testing.T) {
	failing := func(context.Context, string, string) (string, error) { return "", errors.New("llm down") }
	tests := []struct {
		name    string
		respond Responder
		body    string
		code    int
	}{
		{"parse error", echo, `{`, CodeParseError},
		{"unknown method", echo, `{"jsonrpc":"2.0","id":1,"method":"tasks/get"}`, CodeMethodNotFound},
		{"no text", echo, `{"jsonrpc":"2.0","id":1,"method":"message/send","params":{"message":{"parts":[]}}}`, CodeInvalidParams},
		{"responder fails", failing, `{"jsonrpc":"2.0","id":1,"method":"message/send",
			"params":{"message":{"parts":[{"kind":"text","text":"x"}]}}}`, CodeInternalError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := post(t, newServer(t, tt.respond), tt.body)
			if res.Error == nil || res.Error.Code != tt.code {
				t.Fatalf("got %+v", res)
			}
		})
	}
}

// Why the agent failed stays on the node: the peer learns that it failed and
// under which ID the node logged the cause, nothing of the cause itself and
// nothing that promises an explanation. The node keeps the failure under
// that ID with the peer and the message it belongs to.
func TestResponderErrorIsNotSentToPeers(t *testing.T) {
	const cause = `Post "http://10.0.0.5:11434/v1/chat/completions": connection refused`
	failing := func(context.Context, string, string) (string, error) { return "", errors.New(cause) }
	srv, peer := newServer(t, failing), newNode(t, "peer")
	srv.trust.AddTrusted(peer.id.Public(), "peer", "")
	ctx := context.Background()
	_, err := peer.client().Send(ctx, srv.srv.URL, srv.id.Public(), "c", "x")
	var rpcErr *Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != CodeInternalError {
		t.Fatalf("got %v", err)
	}
	said := regexp.MustCompile(`^the agent could not answer \(error ([A-Z2-7]{8})\)$`).FindStringSubmatch(rpcErr.Message)
	if said == nil {
		t.Fatalf("the peer was told %q", rpcErr.Message)
	}
	if len(srv.log) != 1 || !strings.Contains(srv.log[0], "error "+said[1]) || !strings.Contains(srv.log[0], cause) || !strings.Contains(srv.log[0], peer.id.Public()) {
		t.Fatalf("log %q should explain error %s", srv.log, said[1])
	}

	if len(srv.failed) != 1 {
		t.Fatalf("failures reported: %+v", srv.failed)
	}
	if f := srv.failed[0]; f.ID != said[1] || f.Peer != peer.id.Public() || f.ContextID != "c" || f.MessageID == "" || f.Err.Error() != cause {
		t.Fatalf("failure %+v", f)
	}

	// The owner asks with the key of the node and reads the cause.
	if _, err := srv.client().Send(ctx, srv.srv.URL, srv.id.Public(), "c", "x"); !code(err, CodeInternalError) || !strings.Contains(err.Error(), cause) {
		t.Fatalf("the owner was told %v", err)
	}
	if len(srv.failed) != 1 {
		t.Fatalf("what the owner read is nothing to keep for a peer: %+v", srv.failed)
	}

	// A server without a log keeps the cause from peers just the same.
	quiet := serve(t, srv.id, (&Server{Card: func() AgentCard { return testCard }, Identity: srv.id, Trust: srv.trust, Respond: failing}).Handler())
	if _, err := peer.client().Send(ctx, quiet.URL, srv.id.Public(), "c", "x"); !code(err, CodeInternalError) || strings.Contains(err.Error(), "10.0.0.5") {
		t.Fatalf("got %v", err)
	}
}

// What the right node answers wrongly is an error of its own, not a doubt
// about who answered.
func TestSendBadResponses(t *testing.T) {
	id := identity.Generate()
	elsewhere := answering(t, id, `{"jsonrpc":"2.0","id":1,"result":{"parts":[{"kind":"text","text":"from elsewhere"}]}}`)
	tests := map[string]*httptest.Server{
		"http status": serve(t, id, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) })),
		"cut off": serve(t, id, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", "100")
			io.WriteString(w, "{")
		})),
		// A node answers itself, even when it points to one with the same key.
		"redirect": serve(t, id, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, elsewhere.URL, http.StatusTemporaryRedirect)
		})),
		"invalid json": answering(t, id, "nope"),
		"no result":    answering(t, id, `{"jsonrpc":"2.0","id":1}`),
	}
	for name, srv := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := (&Client{Identity: id}).Send(context.Background(), srv.URL, id.Public(), "c", "x")
			if err == nil || errors.Is(err, ErrUnverified) {
				t.Fatalf("got %q, %v", got, err)
			}
		})
	}
}

func TestMessageTextJoinsTextParts(t *testing.T) {
	m := Message{Parts: []Part{{Kind: "text", Text: "a"}, {Kind: "file"}, {Kind: "text", Text: "b"}}}
	if got := m.Text(); got != "a\nb" {
		t.Fatalf("got %q", got)
	}
}

func TestNewIDIsUUIDv4(t *testing.T) {
	re := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	if id := NewID(); !re.MatchString(id) {
		t.Fatalf("bad uuid %q", id)
	}
}

// What cannot be sent is an error, never a silent success.
func TestCallErrors(t *testing.T) {
	ctx := context.Background()
	srv := newServer(t, echo)
	gone := answering(t, srv.id, "")
	gone.Close()

	c := &Client{Identity: identity.Generate()}
	if _, err := c.call(ctx, srv.srv.URL, srv.id.Public(), MethodSend, make(chan int)); err == nil {
		t.Error("params that are no JSON should fail")
	}
	if _, err := c.call(ctx, "://no-url", "key", MethodSend, struct{}{}); err == nil {
		t.Error("an invalid URL should fail")
	}
	if _, err := c.call(ctx, gone.URL, "key", MethodSend, struct{}{}); err == nil {
		t.Error("a node that is gone should fail")
	}
	if err := c.Accept(ctx, gone.URL, "key"); err == nil {
		t.Error("Accept should report a node that is gone")
	}
	if err := c.Reject(ctx, gone.URL, "key"); err == nil {
		t.Error("Reject should report a node that is gone")
	}
}

func TestFetchCardErrors(t *testing.T) {
	ctx := context.Background()
	id := identity.Generate()
	gone := answering(t, id, "")
	gone.Close()
	for name, url := range map[string]string{
		"invalid url":  "://no-url",
		"node is gone": gone.URL,
		"no card":      serve(t, id, http.NotFoundHandler()).URL,
		"invalid json": answering(t, id, "nope").URL,
		"no name":      answering(t, id, `{"protocolVersion":"0.3.0"}`).URL,
	} {
		if _, err := FetchCard(ctx, url, id.Public()); err == nil || errors.Is(err, ErrUnverified) {
			t.Errorf("%s: FetchCard = %v", name, err)
		}
		if key, err := FetchKey(ctx, url); err == nil || key != "" {
			t.Errorf("%s: FetchKey = %q, %v", name, key, err)
		}
	}
}

func TestErrorKinds(t *testing.T) {
	pending, blocked := &Error{CodePending, "wait"}, &Error{CodeBlocked, "no"}
	if !IsPending(pending) || IsPending(blocked) || !IsBlocked(blocked) || IsBlocked(pending) || IsPending(errors.New("other")) {
		t.Fatal("IsPending and IsBlocked should tell the codes apart")
	}
	if got := pending.Error(); got != "a2a: wait (-32002)" {
		t.Fatalf("Error = %q", got)
	}
}
