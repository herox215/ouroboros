package a2a

import (
	"context"
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
}

func newNode(t *testing.T, name string) *node {
	t.Helper()
	trust := contacts.Open(filepath.Join(t.TempDir(), "contacts.json"))
	id := identity.Generate()
	srv := httptest.NewServer((&Server{Card: testCard, Identity: id, Trust: trust, Respond: echo}).Handler())
	t.Cleanup(srv.Close)
	return &node{srv: srv, id: id, trust: trust}
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
	if _, err := Send(context.Background(), alice.srv.URL, "c", "hi"); err == nil {
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
