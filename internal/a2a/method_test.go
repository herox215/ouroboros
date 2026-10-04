package a2a

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"ouroboros/internal/identity"
)

// counted returns a method called name and how often it ran.
func counted(name string) (Method, *int) {
	ran := new(int)
	return Method{Name: name, Handle: func(_ context.Context, from Caller, params json.RawMessage) (*Message, *Error) {
		*ran++
		if string(params) == `"quiet"` {
			return nil, nil
		}
		if string(params) == `"fail"` {
			return nil, &Error{CodeInvalidParams, "no such file"}
		}
		msg := NewMessage("agent", "", "shared with "+from.PublicKey)
		return &msg, nil
	}}, ran
}

// A peer runs a method that a plugin of the other node added with Call, and
// gets what it answers with.
func TestCallRunsAnAddedMethod(t *testing.T) {
	ctx := context.Background()
	alice, bob := newNode(t, "alice"), newNode(t, "bob")
	share, ran := counted("files/share")
	bob.methods = []Method{share}
	bob.trust.AddTrusted(alice.id.Public(), "alice", "")

	msg, err := alice.client().Call(ctx, bob.srv.URL, bob.id.Public(), "files/share", map[string]string{"path": "a.txt"})
	if err != nil || msg.Text() != "shared with "+alice.id.Public() {
		t.Fatalf("got %+v, %v", msg, err)
	}
	// A method may answer with nothing, or with an error of its own.
	if msg, err := alice.client().Call(ctx, bob.srv.URL, bob.id.Public(), "files/share", "quiet"); err != nil || msg != nil {
		t.Fatalf("no answer: %+v, %v", msg, err)
	}
	if _, err := alice.client().Call(ctx, bob.srv.URL, bob.id.Public(), "files/share", "fail"); !code(err, CodeInvalidParams) || !strings.Contains(err.Error(), "no such file") {
		t.Fatalf("an error of the method: %v", err)
	}
	if *ran != 3 {
		t.Fatalf("the method ran %d times", *ran)
	}
	if _, err := alice.client().Call(ctx, bob.srv.URL, bob.id.Public(), "files/unknown", nil); !code(err, CodeMethodNotFound) {
		t.Fatalf("a method nobody added: %v", err)
	}
}

// The built-in methods decide about trust. They are not run by name: nothing
// is sent, whatever the params are.
func TestCallRefusesBuiltinMethods(t *testing.T) {
	alice, bob := newNode(t, "alice"), newNode(t, "bob")
	bob.trust.AddOutgoing(alice.id.Public(), "alice", "", "")
	for _, name := range []string{MethodSend, MethodAccept, MethodReject} {
		if !Builtin(name) {
			t.Errorf("%s is built in", name)
		}
		if _, err := alice.client().Call(context.Background(), bob.srv.URL, bob.id.Public(), name, contactParams{}); !errors.Is(err, ErrBuiltin) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if Builtin("files/share") {
		t.Error("files/share is not built in")
	}
	// An accept that had gone out would have made bob trust alice.
	if trusted, _, _ := bob.trust.State(alice.id.Public()); trusted || !hadOutgoing(bob.trust, alice.id.Public()) {
		t.Fatal("a refused call changed the contacts of the peer")
	}
}

// A method a plugin added is behind the same checks as the built-in ones:
// the request has to be signed by the caller, for this node and this method,
// fresh and new, and the caller has to be a peer the owner is connected to.
// Whoever fails one of them is refused before the method runs.
func TestAddedMethodIsBehindTheSameChecks(t *testing.T) {
	alice, bob, carol := newNode(t, "alice"), newNode(t, "bob"), newNode(t, "carol")
	share, ran := counted("files/share")
	other, otherRan := counted("files/delete")
	bob.methods = []Method{share, other}
	bob.trust.AddTrusted(alice.id.Public(), "alice", "")

	body := `{"jsonrpc":"2.0","id":1,"method":"files/share","params":{}}`
	send := func(change func(*http.Request)) *Error {
		t.Helper()
		r, err := http.NewRequest(http.MethodPost, bob.srv.URL, nil)
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
	refused := func(what string, err *Error, message string) {
		t.Helper()
		if err == nil || err.Code != CodeUnauthorized || err.Message != message {
			t.Errorf("%s: got %+v, want %q", what, err, message)
		}
	}

	var recorded http.Header
	if err := send(func(r *http.Request) {
		sign(r, []byte(body), alice.id, 0, bob.id.Public(), "files/share")
		recorded = r.Header.Clone()
	}); err != nil || *ran != 1 {
		t.Fatalf("a connected peer should be answered: %v, ran %d", err, *ran)
	}

	refused("no signature", send(func(*http.Request) {}), "missing signature")
	refused("replay", send(func(r *http.Request) { r.Header = recorded.Clone() }), "replayed request")
	refused("forged signature", send(func(r *http.Request) {
		sign(r, []byte(body), alice.id, 0, bob.id.Public(), "files/share")
		r.Header.Set(HeaderSig, base64.StdEncoding.EncodeToString(make([]byte, 64)))
	}), "bad signature")
	// A stranger who claims the key of the connected peer.
	refused("claimed key", send(func(r *http.Request) {
		sign(r, []byte(body), identity.Generate(), 0, bob.id.Public(), "files/share")
		r.Header.Set(HeaderPublic, alice.id.Public())
	}), "bad signature")
	// What alice signed for carol, who added the same method, is nothing bob runs.
	refused("signed for another node", send(func(r *http.Request) {
		sign(r, []byte(body), alice.id, 0, carol.id.Public(), "files/share")
	}), "request is for another node")
	// Nor what she signed for another method, built in or added.
	for _, method := range []string{MethodSend, "files/delete"} {
		refused("signed for "+method, send(func(r *http.Request) {
			sign(r, []byte(body), alice.id, 0, bob.id.Public(), method)
		}), "bad signature")
	}
	// Signed correctly, but by nodes the owner is not connected to: a
	// stranger, and a node the owner refused.
	bob.trust.AddIncoming(carol.id.Public(), "carol", "", "")
	bob.trust.Reject(carol.id.Public())
	for name, id := range map[string]*identity.Identity{"stranger": identity.Generate(), "refused node": carol.id} {
		refused(name, send(func(r *http.Request) {
			sign(r, []byte(body), id, 0, bob.id.Public(), "files/share")
		}), "not connected")
	}
	// Not even the node itself, which answers its own messages unasked.
	refused("the node itself", send(func(r *http.Request) {
		sign(r, []byte(body), bob.id, 0, bob.id.Public(), "files/share")
	}), "not connected")

	if *ran != 1 || *otherRan != 0 {
		t.Fatalf("refused requests ran the method %d times, the other method %d times", *ran-1, *otherRan)
	}
	// And none of them left a connection request behind.
	if data, err := bob.trust.Snapshot(); err != nil || len(data.Incoming) != 0 {
		t.Fatalf("contacts of bob: %+v, %v", data, err)
	}
}

// A call reaches the node that holds the key it is made for and nobody else:
// who answers in its place gets nothing to read.
func TestCallReachesOnlyTheHolderOfTheKey(t *testing.T) {
	ctx := context.Background()
	alice, bob, mallory := newNode(t, "alice"), newNode(t, "bob"), newNode(t, "mallory")
	share, ran := counted("files/share")
	bob.methods, mallory.methods = []Method{share}, []Method{share}
	bob.trust.AddTrusted(alice.id.Public(), "alice", "")
	mallory.trust.AddTrusted(alice.id.Public(), "alice", "")

	// Mallory is a real node, trusts alice and has the method, but announces
	// bob's key at its own address.
	if _, err := alice.client().Call(ctx, mallory.srv.URL, bob.id.Public(), "files/share", nil); !errors.Is(err, ErrUnverified) || *ran != 0 {
		t.Fatalf("got %v, ran %d", err, *ran)
	}
	if _, err := alice.client().Call(ctx, plain(bob.srv), bob.id.Public(), "files/share", nil); err == nil || *ran != 0 {
		t.Fatalf("without TLS: %v, ran %d", err, *ran)
	}
	if _, err := alice.client().Call(ctx, bob.srv.URL, bob.id.Public(), "files/share", nil); err != nil || *ran != 1 {
		t.Fatalf("bob: %v, ran %d", err, *ran)
	}
}
