package a2a

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ouroboros/internal/identity"
)

// plain is the address of srv as a URL without TLS.
func plain(srv *httptest.Server) string {
	return "http://" + srv.Listener.Addr().String()
}

// A node listens for TLS 1.3 and presents the certificate of its key.
func TestTLSConfig(t *testing.T) {
	alice := newNode(t, "alice")
	addr := alice.srv.Listener.Addr().String()

	conn, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	state := conn.ConnectionState()
	conn.Close()
	if state.Version != tls.VersionTLS13 || len(state.PeerCertificates) != 1 {
		t.Fatalf("TLS version %x with %d certificates", state.Version, len(state.PeerCertificates))
	}
	if got := identity.CertificateKey(state.PeerCertificates[0]); got != alice.id.Public() {
		t.Fatalf("the certificate is for %q, want the key of the node", got)
	}

	if conn, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true, MaxVersion: tls.VersionTLS12}); err == nil {
		conn.Close()
		t.Fatal("a node should refuse TLS before 1.3")
	}
}

// A node is reached over TLS or not at all, in both directions.
func TestPlainHTTPIsRefused(t *testing.T) {
	ctx := context.Background()
	alice := newNode(t, "alice")
	asked := 0
	alice.respond = func(context.Context, string, string) (string, error) {
		asked++
		return "ok", nil
	}

	// Whoever talks plain HTTP to a node gets no card and no answer.
	resp, err := http.Get(plain(alice.srv) + CardPath)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || strings.Contains(string(body), "alpha") {
		t.Fatalf("plain HTTP was answered with %d: %s", resp.StatusCode, body)
	}

	// The client does not send anything to a node without TLS, whatever the
	// URL says: neither where it is told to nor where no TLS is spoken.
	seen := 0
	open := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		seen++
		io.WriteString(w, `{"name":"alpha"}`)
	}))
	defer open.Close()
	for _, url := range []string{plain(alice.srv), open.URL, "https://" + open.Listener.Addr().String()} {
		if _, err := alice.client().Send(ctx, url, alice.id.Public(), "c", "hi"); err == nil {
			t.Errorf("Send to %s should fail", url)
		}
		if _, err := FetchCard(ctx, url, alice.id.Public()); err == nil {
			t.Errorf("FetchCard from %s should fail", url)
		}
		if key, err := FetchKey(ctx, url); err == nil || key != "" {
			t.Errorf("FetchKey from %s = %q, %v", url, key, err)
		}
	}
	if _, err := FetchCard(ctx, open.URL, alice.id.Public()); !errors.Is(err, errPlain) {
		t.Errorf("a URL without TLS: %v", err)
	}
	if seen != 0 || asked != 0 {
		t.Fatalf("%d requests went out in plain, the agent was asked %d times", seen, asked)
	}
}

// A message reaches the node that holds the key it is sent to and nobody
// else: the handshake fails before a byte of the request is sent.
func TestSendReachesOnlyTheHolderOfTheKey(t *testing.T) {
	ctx := context.Background()
	alice, bob, mallory := newNode(t, "alice"), newNode(t, "bob"), newNode(t, "mallory")
	bob.trust.AddTrusted(alice.id.Public(), "alice", "")
	mallory.trust.AddTrusted(alice.id.Public(), "alice", "")
	reached := 0 // requests that got to a node they were not for
	count := func(ctx context.Context, contextID, text string) (string, error) {
		reached++
		return echo(ctx, contextID, text)
	}
	mallory.respond = count

	if got, err := alice.client().Send(ctx, bob.srv.URL, bob.id.Public(), "c", "hi"); err != nil || got != "c:hi" {
		t.Fatalf("bob: %q, %v", got, err)
	}

	// relay holds mallory's key and passes on to bob what it gets to read.
	relay := serve(t, mallory.id, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached++
		fresh, _ := http.NewRequest(r.Method, bob.srv.URL+r.URL.Path, r.Body)
		fresh.Header = r.Header.Clone()
		resp, err := unpinned.Do(fresh)
		if err != nil {
			t.Error(err)
			return
		}
		defer resp.Body.Close()
		io.Copy(w, resp.Body)
	}))
	// stranger has a certificate like any web server: valid, but for no node.
	stranger := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached++
		io.WriteString(w, `{"name":"alpha"}`)
	}))
	defer stranger.Close()

	tests := []struct{ name, url, key string }{
		// Mallory is a real node that announces bob's key at its own address.
		{"another node", mallory.srv.URL, bob.id.Public()},
		{"a node in the middle", relay.URL, bob.id.Public()},
		{"no node", stranger.URL, bob.id.Public()},
		{"no key to hold", bob.srv.URL, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, err := alice.client().Send(ctx, tt.url, tt.key, "c", "hi"); !errors.Is(err, ErrUnverified) {
				t.Fatalf("Send: %q, %v; want ErrUnverified", got, err)
			}
			if err := alice.client().Accept(ctx, tt.url, tt.key); !errors.Is(err, ErrUnverified) {
				t.Fatalf("Accept: %v; want ErrUnverified", err)
			}
			if card, err := FetchCard(ctx, tt.url, tt.key); !errors.Is(err, ErrUnverified) {
				t.Fatalf("FetchCard: %+v, %v; want ErrUnverified", card, err)
			}
		})
	}
	// What is no node has no key to be known by either.
	if key, err := FetchKey(ctx, stranger.URL); !errors.Is(err, ErrUnverified) || key != "" {
		t.Fatalf("FetchKey: %q, %v", key, err)
	}
	if reached != 0 {
		t.Fatalf("%d requests reached a node they were not for", reached)
	}

	// Addressed to mallory, the same request is refused by bob: the key that
	// is pinned is the key it is signed for.
	if _, err := alice.client().Send(ctx, mallory.srv.URL, mallory.id.Public(), "c", "hi"); err != nil || reached != 1 {
		t.Fatalf("mallory: %v, reached %d", err, reached)
	}
	if _, err := alice.client().Send(ctx, relay.URL, mallory.id.Public(), "c", "hi"); !code(err, CodeUnauthorized) || !strings.Contains(err.Error(), "request is for another node") {
		t.Fatalf("a request for mallory passed on to bob: %v", err)
	}
}

// The first contact with an address takes the key that is presented there,
// and what follows is pinned to it.
func TestFetchKeyPinsTheFirstContact(t *testing.T) {
	ctx := context.Background()
	alice, bob, mallory := newNode(t, "alice"), newNode(t, "bob"), newNode(t, "mallory")
	bob.trust.AddTrusted(alice.id.Public(), "alice", "")

	key, err := FetchKey(ctx, bob.srv.URL)
	if err != nil || key != bob.id.Public() {
		t.Fatalf("FetchKey = %q, %v", key, err)
	}
	if got, err := alice.client().Send(ctx, bob.srv.URL, key, "c", "hi"); err != nil || got != "c:hi" {
		t.Fatalf("got %q, %v", got, err)
	}
	// Whoever takes over the address afterwards is not the node that was met.
	if _, err := alice.client().Send(ctx, mallory.srv.URL, key, "c", "hi"); !errors.Is(err, ErrUnverified) {
		t.Fatalf("another node at the address: %v", err)
	}
}

// A connection for one node names it to whoever answers, so that a node of
// several profiles knows which of them is meant; one for anyone names none.
func TestPinnedNamesTheNode(t *testing.T) {
	if got := pinned(anyone).ServerName; got != "" {
		t.Errorf("for anyone the connection names %q", got)
	}
	if got := pinned("the key").ServerName; got != ServerName("the key") {
		t.Errorf("for a node the connection names %q", got)
	}
}
