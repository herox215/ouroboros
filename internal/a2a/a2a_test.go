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

// post sends body to n as a request n signed itself.
func post(t *testing.T, n *node, body string) response {
	t.Helper()
	r, err := http.NewRequest(http.MethodPost, n.srv.URL, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	sign(r, []byte(body), n.id, 0)
	resp, err := http.DefaultClient.Do(r)
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
	srv := newServer(t, echo).srv
	resp, err := http.Get(srv.URL + CardPath)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var card AgentCard
	json.NewDecoder(resp.Body).Decode(&card)
	if card.Name != "alpha" || card.URL != srv.URL+"/" {
		t.Fatalf("got %+v", card)
	}

	fetched, err := FetchCard(context.Background(), srv.URL+"/")
	if err != nil || fetched.Name != "alpha" {
		t.Fatalf("FetchCard: %+v, %v", fetched, err)
	}
	if _, err := FetchCard(context.Background(), srv.URL+"/nowhere"); err == nil {
		t.Fatal("FetchCard should fail without a card")
	}
}

func TestSendRoundTrip(t *testing.T) {
	srv := newServer(t, echo)
	got, err := srv.client().Send(context.Background(), srv.srv.URL, "ctx-1", "hello")
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

func TestSendReturnsRPCError(t *testing.T) {
	srv := newServer(t, func(context.Context, string, string) (string, error) {
		return "", errors.New("llm down")
	})
	_, err := srv.client().Send(context.Background(), srv.srv.URL, "c", "x")
	var rpcErr *Error
	if !errors.As(err, &rpcErr) || rpcErr.Code != CodeInternalError || !strings.Contains(err.Error(), "llm down") {
		t.Fatalf("got %v", err)
	}
}

func TestSendBadResponses(t *testing.T) {
	tests := map[string]http.HandlerFunc{
		"http status":  func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) },
		"invalid json": func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "nope") },
		"no result":    func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, `{"jsonrpc":"2.0","id":1}`) },
	}
	for name, h := range tests {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(h)
			defer srv.Close()
			if _, err := new(Client).Send(context.Background(), srv.URL, "c", "x"); err == nil {
				t.Fatal("want error")
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
	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close()

	c := new(Client)
	if _, err := c.call(ctx, srv.srv.URL, MethodSend, make(chan int)); err == nil {
		t.Error("params that are no JSON should fail")
	}
	if _, err := c.call(ctx, "://no-url", MethodSend, struct{}{}); err == nil {
		t.Error("an invalid URL should fail")
	}
	if _, err := c.call(ctx, gone.URL, MethodSend, struct{}{}); err == nil {
		t.Error("a node that is gone should fail")
	}
	if err := c.Accept(ctx, gone.URL); err == nil {
		t.Error("Accept should report a node that is gone")
	}
	if err := c.Reject(ctx, gone.URL); err == nil {
		t.Error("Reject should report a node that is gone")
	}
}

func TestFetchCardErrors(t *testing.T) {
	ctx := context.Background()
	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close()
	serve := func(body string) string {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, body) }))
		t.Cleanup(srv.Close)
		return srv.URL
	}
	for name, url := range map[string]string{
		"invalid url":  "://no-url",
		"node is gone": gone.URL,
		"invalid json": serve("nope"),
		"no name":      serve(`{"protocolVersion":"0.3.0"}`),
	} {
		if _, err := FetchCard(ctx, url); err == nil {
			t.Errorf("%s: want error", name)
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
