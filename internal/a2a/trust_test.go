package a2a

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"ouroboros/internal/contacts"
	"ouroboros/internal/identity"
)

// node is a running server with its own identity and contact list.
type node struct {
	srv   *httptest.Server
	id    *identity.Identity
	trust *contacts.Store
	// respond answers the messages of trusted peers; echo if nil.
	respond Responder
	// methods are added to the built-in ones.
	methods []Method
}

func newNode(t *testing.T, name string) *node {
	t.Helper()
	trust := contacts.Open(filepath.Join(t.TempDir(), "contacts.json"))
	id := identity.Generate()
	n := &node{id: id, trust: trust}
	respond := func(ctx context.Context, contextID, text string) (string, error) {
		if n.respond != nil {
			return n.respond(ctx, contextID, text)
		}
		return echo(ctx, contextID, text)
	}
	n.srv = httptest.NewServer((&Server{Card: func() AgentCard { return testCard }, Identity: id, Trust: trust, Respond: respond, Methods: func() []Method { return n.methods }}).Handler())
	t.Cleanup(n.srv.Close)
	return n
}

func (n *node) client() *Client {
	port := n.srv.Listener.Addr().(*net.TCPAddr).Port
	return &Client{Identity: n.id, Name: "node", Port: port}
}

// TestTrustHandshake walks the whole flow: a stranger's message becomes a
// pending request, the human approves it, the peer is notified and the
// conversation works afterwards.
func TestTrustHandshake(t *testing.T) {
	alice, bob := newNode(t, "alice"), newNode(t, "bob")
	ctx := context.Background()

	if _, err := bob.client().Send(ctx, alice.srv.URL, "c1", "hi"); !IsPending(err) {
		t.Fatalf("want pending, got %v", err)
	}
	// The caller records the outgoing request when its message is pending.
	bob.trust.AddOutgoing(alice.id.Public(), "alice", "", "")
	incoming, _ := alice.trust.Snapshot()
	if len(incoming.Incoming) != 1 || incoming.Incoming[0].PublicKey != bob.id.Public() {
		t.Fatalf("alice incoming: %+v", incoming.Incoming)
	}
	if incoming.Incoming[0].Addr == "" {
		t.Fatal("alice should know how to reach bob")
	}

	req, ok := alice.trust.Approve(bob.id.Public())
	if !ok {
		t.Fatal("approve failed")
	}
	if err := alice.client().Accept(ctx, "http://"+req.Addr+"/"); err != nil {
		t.Fatalf("accept callback: %v", err)
	}
	if trusted, _ := bob.trust.State(alice.id.Public()); !trusted {
		t.Fatal("bob should trust alice after the callback")
	}

	got, err := bob.client().Send(ctx, alice.srv.URL, "c1", "again")
	if err != nil || got != "c1:again" {
		t.Fatalf("trusted chat: %q, %v", got, err)
	}
}

func TestServerRejectsUnsigned(t *testing.T) {
	alice := newNode(t, "alice")
	if _, err := new(Client).Send(context.Background(), alice.srv.URL, "c", "hi"); err == nil {
		t.Fatal("unsigned request must be rejected")
	} else if !code(err, CodeUnauthorized) {
		t.Fatalf("got %v, want unauthorized", err)
	}
}

func TestRejectedPeerIsBlocked(t *testing.T) {
	alice, bob := newNode(t, "alice"), newNode(t, "bob")
	ctx := context.Background()

	if _, err := bob.client().Send(ctx, alice.srv.URL, "c", "hi"); !IsPending(err) {
		t.Fatalf("want pending, got %v", err)
	}
	alice.trust.Reject(bob.id.Public())
	if _, err := bob.client().Send(ctx, alice.srv.URL, "c", "hi"); !IsBlocked(err) {
		t.Fatalf("want blocked, got %v", err)
	}
}

// A trusted peer's message reaches the Responder with its origin, and what the
// Responder sends on counts one hop more, up to MaxHops.
func TestOriginAndHops(t *testing.T) {
	alice, bob := newNode(t, "alice"), newNode(t, "bob")
	bob.trust.AddTrusted(alice.id.Public(), "alice", "")
	var got Origin // of the message that went furthest
	stopped := false
	bob.respond = func(ctx context.Context, _, text string) (string, error) {
		got, _ = OriginFrom(ctx)
		// Bob's agent passes every message on, here back to itself: a circle.
		if _, err := bob.client().Send(ctx, bob.srv.URL, "ctx-2", "onward"); errors.Is(err, ErrTooManyHops) {
			stopped = true
		}
		return text, nil
	}

	ctx := context.Background()
	if _, err := alice.client().Send(ctx, bob.srv.URL, "ctx-1", "hi"); err != nil {
		t.Fatal(err)
	}
	if got.PublicKey != bob.id.Public() || got.Hops != MaxHops || !stopped {
		t.Fatalf("origin %+v, stopped %v", got, stopped)
	}

	// A message that claims more hops than allowed is not answered at all.
	late := WithOrigin(ctx, Origin{Hops: MaxHops})
	if _, err := alice.client().Send(late, bob.srv.URL, "ctx-3", "hi"); !errors.Is(err, ErrTooManyHops) {
		t.Fatalf("client should refuse: %v", err)
	}
	if _, err := alice.client().call(ctx, bob.srv.URL, MethodSend, sendParams{NewMessage("user", "c", "hi"), Peer{}, MaxHops + 1}); !code(err, CodeTooManyHops) {
		t.Fatalf("server should refuse: %v", err)
	}
	if _, ok := OriginFrom(ctx); ok {
		t.Fatal("the owner's context has no origin")
	}
}

// A method added to the server is for connected peers only and cannot take
// the place of a built-in one.
func TestAddedMethods(t *testing.T) {
	alice, bob := newNode(t, "alice"), newNode(t, "bob")
	bob.methods = []Method{
		{Name: "ping", Handle: func(_ context.Context, from Caller, _ json.RawMessage) (*Message, *Error) {
			msg := NewMessage("agent", "", "pong "+from.PublicKey)
			return &msg, nil
		}},
		{Name: MethodSend, Handle: func(context.Context, Caller, json.RawMessage) (*Message, *Error) {
			return nil, &Error{CodeInternalError, "must not be called"}
		}},
	}
	ctx := context.Background()
	if _, err := alice.client().call(ctx, bob.srv.URL, "ping", struct{}{}); !code(err, CodeUnauthorized) {
		t.Fatalf("a stranger should not reach an added method: %v", err)
	}
	bob.trust.AddTrusted(alice.id.Public(), "alice", "")
	res, err := alice.client().call(ctx, bob.srv.URL, "ping", struct{}{})
	if err != nil || res.Result.Text() != "pong "+alice.id.Public() {
		t.Fatalf("got %+v, %v", res, err)
	}
	if got, err := alice.client().Send(ctx, bob.srv.URL, "ctx", "hi"); err != nil || got != "ctx:hi" {
		t.Fatalf("message/send should stay built in: %q, %v", got, err)
	}
	if _, err := alice.client().call(ctx, bob.srv.URL, "nope", struct{}{}); !code(err, CodeMethodNotFound) {
		t.Fatalf("unknown method: %v", err)
	}
}

// A peer that refuses a request this node made takes the request back and
// leaves no trust behind.
func TestRejectCallback(t *testing.T) {
	alice, bob := newNode(t, "alice"), newNode(t, "bob")
	ctx := context.Background()

	if _, err := bob.client().Send(ctx, alice.srv.URL, "c", "hi"); !IsPending(err) {
		t.Fatalf("want pending, got %v", err)
	}
	bob.trust.AddOutgoing(alice.id.Public(), "alice", "", "")
	req, ok := alice.trust.Reject(bob.id.Public())
	if !ok {
		t.Fatal("reject failed")
	}
	if err := alice.client().Reject(ctx, "http://"+req.Addr+"/"); err != nil {
		t.Fatalf("reject callback: %v", err)
	}
	if trusted, _ := bob.trust.State(alice.id.Public()); trusted || bob.trust.HadOutgoing(alice.id.Public()) {
		t.Fatal("bob should have dropped his request without trusting alice")
	}
}

// Trust comes from a decision of the owner only: a peer cannot connect itself
// by accepting or rejecting a request this node never made.
func TestCallbacksNeedOutgoingRequest(t *testing.T) {
	alice, mallory := newNode(t, "alice"), newNode(t, "mallory")
	ctx := context.Background()
	alice.trust.AddOutgoing("someone else", "carol", "", "")

	if err := mallory.client().Accept(ctx, alice.srv.URL); err != nil {
		t.Fatal(err)
	}
	if err := mallory.client().Reject(ctx, alice.srv.URL); err != nil {
		t.Fatal(err)
	}
	if trusted, _ := alice.trust.State(mallory.id.Public()); trusted {
		t.Fatal("mallory accepted a request nobody made and is trusted")
	}
	if !alice.trust.HadOutgoing("someone else") {
		t.Fatal("mallory's rejection dropped the request to another peer")
	}
	if _, err := mallory.client().Send(ctx, alice.srv.URL, "c", "hi"); !IsPending(err) {
		t.Fatalf("mallory should still wait for a decision, got %v", err)
	}
}

func TestSignedRequestsWithBadParams(t *testing.T) {
	alice, bob := newNode(t, "alice"), newNode(t, "bob")
	ctx := context.Background()
	alice.trust.AddOutgoing(bob.id.Public(), "bob", "", "")
	if _, err := bob.client().call(ctx, alice.srv.URL, MethodAccept, "no object"); !code(err, CodeInvalidParams) {
		t.Fatalf("accept: %v", err)
	}
	if trusted, _ := alice.trust.State(bob.id.Public()); trusted {
		t.Fatal("an acceptance that cannot be read must not connect")
	}
	if _, err := bob.client().call(ctx, alice.srv.URL, MethodSend, sendParams{}); !code(err, CodeInvalidParams) {
		t.Fatalf("send without text: %v", err)
	}
	if d, _ := alice.trust.Snapshot(); len(d.Incoming) != 0 {
		t.Fatalf("a message without text should leave no request: %+v", d.Incoming)
	}
}

// A server without added methods knows the built-in ones only.
func TestServerWithoutAddedMethods(t *testing.T) {
	id := identity.Generate()
	trust := contacts.Open(filepath.Join(t.TempDir(), "contacts.json"))
	srv := httptest.NewServer((&Server{Card: func() AgentCard { return testCard }, Identity: id, Trust: trust, Respond: echo}).Handler())
	defer srv.Close()
	client := &Client{Identity: id}
	if _, err := client.call(context.Background(), srv.URL, "ping", struct{}{}); !code(err, CodeMethodNotFound) {
		t.Fatalf("got %v", err)
	}
	// The node trusts its own key.
	if got, err := client.Send(context.Background(), srv.URL, "c", "hi"); err != nil || got != "c:hi" {
		t.Fatalf("got %q, %v", got, err)
	}
}
