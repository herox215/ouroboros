package a2a

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"ouroboros/internal/contacts"
	"ouroboros/internal/identity"
)

// node is a running server with its own identity and contact list.
type node struct {
	srv    *httptest.Server
	server *Server
	id     *identity.Identity
	trust  *contacts.Store
	path   string // of the file the contacts are in
	// respond answers the messages of trusted peers; echo if nil.
	respond Responder
	// methods are added to the built-in ones.
	methods []Method
	// channels are what peers can open.
	channels []ChannelHandler
	// log is what the server logged.
	log []string
	// fail makes the contacts of the node fail, see faulty.
	fail faults
	// failed are the failed answers the server reported.
	failed []Failure
}

// faults are the errors the contacts of a node fail with, by what is asked
// of them. Without one, they work.
type faults struct{ state, hadOutgoing, addIncoming, addTrusted, removeOutgoing error }

// faulty is a contact list that fails on demand.
type faulty struct {
	*contacts.Store
	fail *faults
}

func (f faulty) State(publicKey string) (bool, bool, error) {
	if f.fail.state != nil {
		return false, false, f.fail.state
	}
	return f.Store.State(publicKey)
}

func (f faulty) HadOutgoing(publicKey string) (bool, error) {
	if f.fail.hadOutgoing != nil {
		return false, f.fail.hadOutgoing
	}
	return f.Store.HadOutgoing(publicKey)
}

func (f faulty) AddIncoming(publicKey, name, model, addr string) error {
	if f.fail.addIncoming != nil {
		return f.fail.addIncoming
	}
	return f.Store.AddIncoming(publicKey, name, model, addr)
}

func (f faulty) AddTrusted(publicKey, name, model string) error {
	if f.fail.addTrusted != nil {
		return f.fail.addTrusted
	}
	return f.Store.AddTrusted(publicKey, name, model)
}

func (f faulty) RemoveOutgoing(publicKey string) error {
	if f.fail.removeOutgoing != nil {
		return f.fail.removeOutgoing
	}
	return f.Store.RemoveOutgoing(publicKey)
}

// hadOutgoing reports whether the node with the contacts trust asked
// publicKey to connect.
func hadOutgoing(trust *contacts.Store, publicKey string) bool {
	asked, _ := trust.HadOutgoing(publicKey)
	return asked
}

func newNode(t *testing.T, name string) *node {
	t.Helper()
	path := filepath.Join(t.TempDir(), "contacts.json")
	trust := contacts.Open(path)
	id := identity.Generate()
	n := &node{id: id, trust: trust, path: path}
	respond := func(ctx context.Context, contextID, text string) (string, error) {
		if n.respond != nil {
			return n.respond(ctx, contextID, text)
		}
		return echo(ctx, contextID, text)
	}
	n.server = &Server{Card: func() AgentCard { return testCard }, Identity: id, Trust: faulty{trust, &n.fail}, Respond: respond, Methods: func() []Method { return n.methods },
		Channels: func() []ChannelHandler { return n.channels },
		Log:      func(format string, args ...any) { n.log = append(n.log, fmt.Sprintf(format, args...)) },
		Failed:   func(f Failure) { n.failed = append(n.failed, f) }}
	n.srv = serve(t, id, n.server.Handler())
	// The channels go before the server, which waits for nothing of theirs.
	t.Cleanup(n.server.Close)
	return n
}

// serve runs h the way the node with id listens: over TLS, with the
// certificate of its key.
func serve(t *testing.T, id *identity.Identity, h http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(h)
	srv.TLS = TLSConfig(id)
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

// unpinned talks TLS to whatever answers. It is for requests that are made by
// hand to see what a node says to them.
var unpinned = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}

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

	if _, err := bob.client().Send(ctx, alice.srv.URL, alice.id.Public(), "c1", "hi"); !IsPending(err) {
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

	req, ok, _ := alice.trust.Approve(bob.id.Public())
	if !ok {
		t.Fatal("approve failed")
	}
	if err := alice.client().Accept(ctx, "https://"+req.Addr+"/", bob.id.Public()); err != nil {
		t.Fatalf("accept callback: %v", err)
	}
	if trusted, _, _ := bob.trust.State(alice.id.Public()); !trusted {
		t.Fatal("bob should trust alice after the callback")
	}

	got, err := bob.client().Send(ctx, alice.srv.URL, alice.id.Public(), "c1", "again")
	if err != nil || got != "c1:again" {
		t.Fatalf("trusted chat: %q, %v", got, err)
	}
}

// A node asks to connect without a message: the request is recorded like the
// message of a stranger, and the Responder never runs for it.
func TestRequestWithoutMessage(t *testing.T) {
	alice, bob := newNode(t, "alice"), newNode(t, "bob")
	ctx := context.Background()
	alice.respond = func(context.Context, string, string) (string, error) {
		t.Error("a request to connect reached the agent")
		return "", nil
	}

	if err := bob.client().Request(ctx, alice.srv.URL, alice.id.Public()); !IsPending(err) {
		t.Fatalf("a stranger: %v", err)
	}
	d, _ := alice.trust.Snapshot()
	if len(d.Incoming) != 1 || d.Incoming[0].PublicKey != bob.id.Public() || d.Incoming[0].Name != "node" || d.Incoming[0].Addr == "" {
		t.Fatalf("alice incoming: %+v", d.Incoming)
	}

	// Once alice trusts bob, it says so, and nothing waits any more.
	alice.trust.Approve(bob.id.Public())
	if err := bob.client().Request(ctx, alice.srv.URL, alice.id.Public()); err != nil {
		t.Fatalf("a peer: %v", err)
	}
	if d, _ := alice.trust.Snapshot(); len(d.Incoming) != 0 {
		t.Fatalf("a peer left a request: %+v", d.Incoming)
	}

	carol := newNode(t, "carol")
	alice.trust.AddIncoming(carol.id.Public(), "carol", "", "")
	alice.trust.Reject(carol.id.Public())
	if err := carol.client().Request(ctx, alice.srv.URL, alice.id.Public()); !IsBlocked(err) {
		t.Fatalf("a node alice refused: %v", err)
	}
	if _, err := bob.client().call(ctx, alice.srv.URL, alice.id.Public(), MethodRequest, "no object"); !code(err, CodeInvalidParams) {
		t.Fatalf("params that cannot be read: %v", err)
	}
}

func TestServerRejectsUnsigned(t *testing.T) {
	alice := newNode(t, "alice")
	body := `{"jsonrpc":"2.0","id":1,"method":"message/send","params":{"message":{"parts":[{"kind":"text","text":"hi"}]}}}`
	resp, err := unpinned.Post(alice.srv.URL, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	answer, _ := io.ReadAll(resp.Body)
	var res response
	if json.Unmarshal(answer, &res) != nil || res.Error == nil || res.Error.Code != CodeUnauthorized {
		t.Fatalf("unsigned request must be rejected: %s", answer)
	}
}

func TestRejectedPeerIsBlocked(t *testing.T) {
	alice, bob := newNode(t, "alice"), newNode(t, "bob")
	ctx := context.Background()

	if _, err := bob.client().Send(ctx, alice.srv.URL, alice.id.Public(), "c", "hi"); !IsPending(err) {
		t.Fatalf("want pending, got %v", err)
	}
	alice.trust.Reject(bob.id.Public())
	if _, err := bob.client().Send(ctx, alice.srv.URL, alice.id.Public(), "c", "hi"); !IsBlocked(err) {
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
		if _, err := bob.client().Send(ctx, bob.srv.URL, bob.id.Public(), "ctx-2", "onward"); errors.Is(err, ErrTooManyHops) {
			stopped = true
		}
		return text, nil
	}

	ctx := context.Background()
	if _, err := alice.client().Send(ctx, bob.srv.URL, bob.id.Public(), "ctx-1", "hi"); err != nil {
		t.Fatal(err)
	}
	if got.PublicKey != bob.id.Public() || got.Hops != MaxHops || !stopped {
		t.Fatalf("origin %+v, stopped %v", got, stopped)
	}

	// A message that claims more hops than allowed is not answered at all.
	late := WithOrigin(ctx, Origin{Hops: MaxHops})
	if _, err := alice.client().Send(late, bob.srv.URL, bob.id.Public(), "ctx-3", "hi"); !errors.Is(err, ErrTooManyHops) {
		t.Fatalf("client should refuse: %v", err)
	}
	if _, err := alice.client().call(ctx, bob.srv.URL, bob.id.Public(), MethodSend, sendParams{NewMessage("user", "c", "hi"), Peer{}, MaxHops + 1}); !code(err, CodeTooManyHops) {
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
		{Name: MethodSend, Handle: func(context.Context, Caller, json.RawMessage) (*Message, *Error) {
			return nil, &Error{CodeInternalError, "must not be called"}
		}},
		{Name: "ping", Handle: func(_ context.Context, from Caller, _ json.RawMessage) (*Message, *Error) {
			msg := NewMessage("agent", "", "pong "+from.PublicKey)
			return &msg, nil
		}},
	}
	ctx := context.Background()
	if _, err := alice.client().call(ctx, bob.srv.URL, bob.id.Public(), "ping", struct{}{}); !code(err, CodeUnauthorized) {
		t.Fatalf("a stranger should not reach an added method: %v", err)
	}
	bob.trust.AddTrusted(alice.id.Public(), "alice", "")
	res, err := alice.client().call(ctx, bob.srv.URL, bob.id.Public(), "ping", struct{}{})
	if err != nil || res.Result.Text() != "pong "+alice.id.Public() {
		t.Fatalf("got %+v, %v", res, err)
	}
	if got, err := alice.client().Send(ctx, bob.srv.URL, bob.id.Public(), "ctx", "hi"); err != nil || got != "ctx:hi" {
		t.Fatalf("message/send should stay built in: %q, %v", got, err)
	}
	if _, err := alice.client().call(ctx, bob.srv.URL, bob.id.Public(), "nope", struct{}{}); !code(err, CodeMethodNotFound) {
		t.Fatalf("unknown method: %v", err)
	}
}

// A peer that refuses a request this node made takes the request back and
// leaves no trust behind.
func TestRejectCallback(t *testing.T) {
	alice, bob := newNode(t, "alice"), newNode(t, "bob")
	ctx := context.Background()

	if _, err := bob.client().Send(ctx, alice.srv.URL, alice.id.Public(), "c", "hi"); !IsPending(err) {
		t.Fatalf("want pending, got %v", err)
	}
	bob.trust.AddOutgoing(alice.id.Public(), "alice", "", "")
	req, ok, _ := alice.trust.Reject(bob.id.Public())
	if !ok {
		t.Fatal("reject failed")
	}
	if err := alice.client().Reject(ctx, "https://"+req.Addr+"/", bob.id.Public()); err != nil {
		t.Fatalf("reject callback: %v", err)
	}
	if trusted, _, _ := bob.trust.State(alice.id.Public()); trusted || hadOutgoing(bob.trust, alice.id.Public()) {
		t.Fatal("bob should have dropped his request without trusting alice")
	}
}

// Trust comes from a decision of the owner only: a peer cannot connect itself
// by accepting or rejecting a request this node never made.
func TestCallbacksNeedOutgoingRequest(t *testing.T) {
	alice, mallory := newNode(t, "alice"), newNode(t, "mallory")
	ctx := context.Background()
	alice.trust.AddOutgoing("someone else", "carol", "", "")

	if err := mallory.client().Accept(ctx, alice.srv.URL, alice.id.Public()); err != nil {
		t.Fatal(err)
	}
	// The rejection of a request nobody made changes nothing in the contacts:
	// it does not even try.
	alice.fail.removeOutgoing = errors.New("must not be called")
	if err := mallory.client().Reject(ctx, alice.srv.URL, alice.id.Public()); err != nil {
		t.Fatal(err)
	}
	alice.fail.removeOutgoing = nil
	if trusted, _, _ := alice.trust.State(mallory.id.Public()); trusted {
		t.Fatal("mallory accepted a request nobody made and is trusted")
	}
	if !hadOutgoing(alice.trust, "someone else") {
		t.Fatal("mallory's rejection dropped the request to another peer")
	}
	if _, err := mallory.client().Send(ctx, alice.srv.URL, alice.id.Public(), "c", "hi"); !IsPending(err) {
		t.Fatalf("mallory should still wait for a decision, got %v", err)
	}
}

func TestSignedRequestsWithBadParams(t *testing.T) {
	alice, bob := newNode(t, "alice"), newNode(t, "bob")
	ctx := context.Background()
	alice.trust.AddOutgoing(bob.id.Public(), "bob", "", "")
	if _, err := bob.client().call(ctx, alice.srv.URL, alice.id.Public(), MethodAccept, "no object"); !code(err, CodeInvalidParams) {
		t.Fatalf("accept: %v", err)
	}
	if trusted, _, _ := alice.trust.State(bob.id.Public()); trusted {
		t.Fatal("an acceptance that cannot be read must not connect")
	}
	if _, err := bob.client().call(ctx, alice.srv.URL, alice.id.Public(), MethodSend, sendParams{}); !code(err, CodeInvalidParams) {
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
	srv := serve(t, id, (&Server{Card: func() AgentCard { return testCard }, Identity: id, Trust: trust, Respond: echo}).Handler())
	client := &Client{Identity: id}
	if _, err := client.call(context.Background(), srv.URL, id.Public(), "ping", struct{}{}); !code(err, CodeMethodNotFound) {
		t.Fatalf("got %v", err)
	}
	// The node trusts its own key.
	if got, err := client.Send(context.Background(), srv.URL, id.Public(), "c", "hi"); err != nil || got != "c:hi" {
		t.Fatalf("got %q, %v", got, err)
	}
}

// Contacts that cannot be read or changed are an internal error for the peer
// and a line in the log: nobody is trusted, connected or left waiting by a
// change that did not happen.
func TestBrokenContactsAreAnInternalError(t *testing.T) {
	boom := errors.New("open /home/alice/contacts.json: disk on fire")
	ctx := context.Background()
	ping := Method{Name: "ping", Handle: func(context.Context, Caller, json.RawMessage) (*Message, *Error) {
		t.Error("an added method ran without knowing who is trusted")
		return nil, nil
	}}
	tests := map[string]struct {
		fail    faults
		trusted bool // alice trusts bob
		call    func(alice, bob *node) error
	}{
		"send, reading": {faults{state: boom}, true, func(alice, bob *node) error {
			_, err := bob.client().Send(ctx, alice.srv.URL, alice.id.Public(), "c", "hi")
			return err
		}},
		"send, recording the request": {faults{addIncoming: boom}, false, func(alice, bob *node) error {
			_, err := bob.client().Send(ctx, alice.srv.URL, alice.id.Public(), "c", "hi")
			return err
		}},
		"accept, reading": {faults{hadOutgoing: boom}, false, func(alice, bob *node) error {
			return bob.client().Accept(ctx, alice.srv.URL, alice.id.Public())
		}},
		"accept, trusting": {faults{addTrusted: boom}, false, func(alice, bob *node) error {
			return bob.client().Accept(ctx, alice.srv.URL, alice.id.Public())
		}},
		"reject, reading": {faults{hadOutgoing: boom}, false, func(alice, bob *node) error {
			return bob.client().Reject(ctx, alice.srv.URL, alice.id.Public())
		}},
		"reject, dropping the request": {faults{removeOutgoing: boom}, false, func(alice, bob *node) error {
			return bob.client().Reject(ctx, alice.srv.URL, alice.id.Public())
		}},
		"added method": {faults{state: boom}, true, func(alice, bob *node) error {
			_, err := bob.client().call(ctx, alice.srv.URL, alice.id.Public(), "ping", struct{}{})
			return err
		}},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			alice, bob := newNode(t, "alice"), newNode(t, "bob")
			alice.methods = []Method{ping}
			alice.trust.AddOutgoing(bob.id.Public(), "bob", "", "")
			if tt.trusted {
				alice.trust.AddTrusted(bob.id.Public(), "bob", "")
			}
			before, _ := alice.trust.Snapshot()
			alice.fail = tt.fail
			err := tt.call(alice, bob)
			var rpcErr *Error
			if !errors.As(err, &rpcErr) || rpcErr.Code != CodeInternalError || rpcErr.Message != "internal error" {
				t.Fatalf("the peer was told %v", err)
			}
			if len(alice.log) != 1 || !strings.Contains(alice.log[0], boom.Error()) || !strings.Contains(alice.log[0], bob.id.Public()) {
				t.Fatalf("log %q should say what failed for whom", alice.log)
			}
			alice.fail = faults{}
			if d, _ := alice.trust.Snapshot(); !reflect.DeepEqual(d, before) {
				t.Fatalf("a failure changed the contacts: %+v", d)
			}
		})
	}

	// The owner asks with the key of the node, which needs no contacts.
	alice := newNode(t, "alice")
	alice.fail = faults{state: boom}
	if got, err := alice.client().Send(ctx, alice.srv.URL, alice.id.Public(), "c", "hi"); err != nil || got != "c:hi" {
		t.Fatalf("the owner got %q, %v", got, err)
	}
}
