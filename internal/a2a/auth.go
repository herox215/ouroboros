package a2a

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"ouroboros/internal/identity"
)

// Headers that carry a signed request. Nothing of the kind comes back: the
// node that answers is authenticated by the TLS handshake (see exchange).
const (
	HeaderPublic    = "X-Ouro-Public-Key"
	HeaderTime      = "X-Ouro-Time"
	HeaderNonce     = "X-Ouro-Nonce"
	HeaderSig       = "X-Ouro-Signature"
	HeaderPort      = "X-Ouro-Port"
	HeaderRecipient = "X-Ouro-Recipient" // public key of the node the request is for
)

// signedRequest is the first line of the string a request signature covers,
// so that a signature made for anything else is never valid as a request.
const signedRequest = "ouro-request"

// clockSkew is how far a request timestamp may deviate from local time.
const clockSkew = 2 * time.Minute

// A nonce is remembered for as long as its request could be sent again: a
// timestamp from clockSkew ahead is accepted until clockSkew behind.
const nonceLife = 2 * clockSkew

// maxNonces is how many nonces are remembered per nonceLife. It is a hard
// bound: when it is reached, requests are refused until the nonces of the
// last nonceLife are forgotten. Forgetting them early would let recorded
// requests in again.
const maxNonces = 1 << 14

// authMessage is the canonical byte string a request signature covers. It
// binds the body to the timestamp, the nonce, the sender's A2A port, the
// public key of the node the request is for and the JSON-RPC method.
func authMessage(timestamp, nonce, port, recipient, method string, body []byte) []byte {
	sum := sha256.Sum256(body)
	return []byte(strings.Join([]string{signedRequest, timestamp, nonce, port, recipient, method, hex.EncodeToString(sum[:])}, "\n"))
}

// sign sets the authentication headers on req for the given body: a request
// of id for the method of the node with the public key recipient. That is the
// key the connection is pinned to, see Client.call.
func sign(req *http.Request, body []byte, id *identity.Identity, port int, recipient, method string) {
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := rand.Text()
	portStr := ""
	if port > 0 {
		portStr = strconv.Itoa(port)
	}
	sig := id.Sign(authMessage(timestamp, nonce, portStr, recipient, method, body))
	req.Header.Set(HeaderPublic, id.Public())
	req.Header.Set(HeaderTime, timestamp)
	req.Header.Set(HeaderNonce, nonce)
	req.Header.Set(HeaderPort, portStr)
	req.Header.Set(HeaderRecipient, recipient)
	req.Header.Set(HeaderSig, base64.StdEncoding.EncodeToString(sig))
}

// verifier checks request signatures and remembers nonces to stop replays.
//
// The nonces are kept in two generations: seen takes the new ones, and once
// it is nonceLife old it becomes old and a new one is started. A nonce is
// therefore kept for at least nonceLife, both generations together hold at
// most 2*maxNonces, and forgetting costs nothing per request.
type verifier struct {
	self  string // public key of this node: requests have to be signed for it
	now   func() time.Time
	mu    sync.Mutex
	seen  map[string]struct{} // nonces since since
	old   map[string]struct{} // nonces of the generation before
	since time.Time
}

func newVerifier(self string) *verifier {
	return &verifier{self: self, now: time.Now, seen: map[string]struct{}{}}
}

// verify returns the authenticated public key of r, a request for method.
func (v *verifier) verify(r *http.Request, body []byte, method string) (string, error) {
	publicKey := r.Header.Get(HeaderPublic)
	timestamp, nonce, port := r.Header.Get(HeaderTime), r.Header.Get(HeaderNonce), r.Header.Get(HeaderPort)
	if publicKey == "" || timestamp == "" || nonce == "" {
		return "", errors.New("missing signature")
	}
	sec, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || v.now().Sub(time.Unix(sec, 0)) > clockSkew || time.Unix(sec, 0).Sub(v.now()) > clockSkew {
		return "", errors.New("stale request")
	}
	// What was signed for another node is refused, however valid it is there.
	recipient := r.Header.Get(HeaderRecipient)
	if recipient != v.self {
		return "", errors.New("request is for another node")
	}
	sig, err := base64.StdEncoding.DecodeString(r.Header.Get(HeaderSig))
	if err != nil || !identity.Verify(publicKey, authMessage(timestamp, nonce, port, recipient, method, body), sig) {
		return "", errors.New("bad signature")
	}
	if err := v.remember(publicKey, nonce); err != nil {
		return "", err
	}
	return publicKey, nil
}

// remember records a nonce. It fails for a nonce that was seen before, and
// for any nonce while maxNonces are remembered already.
func (v *verifier) remember(publicKey, nonce string) error {
	key := publicKey + "|" + nonce
	v.mu.Lock()
	defer v.mu.Unlock()
	if now := v.now(); now.Sub(v.since) >= nonceLife {
		v.old, v.seen, v.since = v.seen, map[string]struct{}{}, now
	}
	_, seen := v.seen[key]
	_, old := v.old[key]
	if seen || old {
		return errors.New("replayed request")
	}
	if len(v.seen) >= maxNonces {
		return errors.New("too many requests")
	}
	v.seen[key] = struct{}{}
	return nil
}

// remoteHost is the IP of the TCP connection r came in on.
func remoteHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// callbackAddr is the address to reach the sender again: the IP of the TCP
// connection plus the A2A port the sender advertised in its signed headers.
func callbackAddr(r *http.Request, port string) string {
	host := remoteHost(r)
	if port == "" {
		return host
	}
	return net.JoinHostPort(host, port)
}
