package a2a

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

// upgraded is what a call of upgrade in the background returned.
type upgraded struct {
	conn   net.Conn
	r      *bufio.Reader
	answer []byte
	err    error
}

// upgradeIn runs upgrade in the background: an answer whose read is not cut
// would hold the caller for the whole test run.
func upgradeIn(req *http.Request, p pin) <-chan upgraded {
	done := make(chan upgraded, 1)
	go func() {
		conn, r, answer, err := upgrade(req, p)
		done <- upgraded{conn, r, answer, err}
	}()
	return done
}

// waited returns what upgradeIn got, and fails where upgrade did not return
// at all: the read of an answer that is not cut never ends.
func waited(t *testing.T, done <-chan upgraded) upgraded {
	t.Helper()
	select {
	case got := <-done:
		return got
	case <-time.After(10 * time.Second):
		t.Fatal("upgrade did not return: the stalled answer was not cut off")
		return upgraded{}
	}
}

// out waits until the node has sent what a test needs it to have sent.
func out(t *testing.T, sent <-chan struct{}) {
	t.Helper()
	select {
	case <-sent:
	case <-time.After(10 * time.Second):
		t.Fatal("the node did not send the start of its answer")
	}
}

// stalling serves the headers of a 200 answer and a piece of the body it
// announces, then waits for the end of the test. sent is closed once the
// headers are out, so that a test can time out or cancel after them.
func stalling(t *testing.T, id *identity.Identity) (*httptest.Server, <-chan struct{}) {
	t.Helper()
	sent, done := make(chan struct{}), make(chan struct{})
	srv := serve(t, id, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{"result":`)
		w.(http.Flusher).Flush()
		close(sent)
		// Until the test is over the node stalls: when it is, the handler
		// returns and the server closes the connection an upgrade that was
		// not cut off still reads.
		<-done
	}))
	// Registered after the server, so that this runs before its Close waits
	// for the handler.
	t.Cleanup(func() { close(done) })
	return srv, sent
}

// An answer is only whole with its body: a node that sends the headers of a
// 200 and then stalls is cut off when openTimeout ends, not only one that
// does not answer at all.
func TestUpgradeCutsAStalledAnswerAtTheTimeout(t *testing.T) {
	was := openTimeout
	defer func() { openTimeout = was }()
	openTimeout = 500 * time.Millisecond

	id := identity.Generate()
	srv, sent := stalling(t, id)
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	done := upgradeIn(req, pin(id.Public()))
	out(t, sent)
	got := waited(t, done)
	if !errors.Is(got.err, context.DeadlineExceeded) {
		t.Fatalf("upgrade: %v; want the deadline of opening the channel", got.err)
	}
	if got.conn != nil || got.answer != nil {
		t.Fatalf("upgrade returned a connection and the answer %q", got.answer)
	}
}

// The same without the timeout: a caller that cancels while the body of a 200
// answer is read ends the read too.
func TestUpgradeCutsAStalledAnswerOnCancel(t *testing.T) {
	id := identity.Generate()
	srv, sent := stalling(t, id)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	done := upgradeIn(req, pin(id.Public()))
	out(t, sent)
	// The headers are out; the client reads them and then blocks in the body
	// that does not come. That is where the cancellation has to end the read.
	time.Sleep(100 * time.Millisecond)
	cancel()
	if got := waited(t, done); !errors.Is(got.err, context.Canceled) {
		t.Fatalf("upgrade: %v; want the cancellation of the request", got.err)
	}
}

// The connection of a channel that opened is handed over as it is, and the
// timeout for opening it does not cut it afterwards.
func TestUpgradeHandsOverTheConnectionOfAChannel(t *testing.T) {
	was := openTimeout
	defer func() { openTimeout = was }()
	openTimeout = 100 * time.Millisecond

	id := identity.Generate()
	srv := serve(t, id, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body) // the request is read before the connection changes hands
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer conn.Close()
		io.WriteString(buf, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: "+ChannelProtocol+"\r\n\r\n")
		buf.Flush()
		io.Copy(conn, buf) // what the client sends over the channel comes back
	}))
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	conn, r, answer, err := upgrade(req, pin(id.Public()))
	if err != nil || conn == nil {
		t.Fatalf("upgrade: %v, connection %v", err, conn)
	}
	defer conn.Close()
	if answer != nil {
		t.Fatalf("a channel that opened answered with %q", answer)
	}
	// The timeout for opening the channel passes while it is in use.
	time.Sleep(3 * openTimeout)
	if _, err := io.WriteString(conn, "hi"); err != nil {
		t.Fatal(err)
	}
	echo := make([]byte, 2)
	if _, err := io.ReadFull(r, echo); err != nil || string(echo) != "hi" {
		t.Fatalf("over the channel: %q, %v", echo, err)
	}
}
