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
type verifier struct {
	self string // public key of this node: requests have to be signed for it
	now  func() time.Time
	mu   sync.Mutex
	seen map[string]time.Time
}

func newVerifier(self string) *verifier {
	return &verifier{self: self, now: time.Now, seen: map[string]time.Time{}}
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
	if !v.remember(publicKey, nonce) {
		return "", errors.New("replayed request")
	}
	return publicKey, nil
}

// remember records a nonce and reports whether it was new.
func (v *verifier) remember(publicKey, nonce string) bool {
	key := publicKey + "|" + nonce
	v.mu.Lock()
	defer v.mu.Unlock()
	if _, ok := v.seen[key]; ok {
		return false
	}
	if len(v.seen) > 4096 {
		for k, at := range v.seen {
			if v.now().Sub(at) > clockSkew {
				delete(v.seen, k)
			}
		}
	}
	v.seen[key] = v.now()
	return true
}

// callbackAddr is the address to reach the sender again: the IP of the TCP
// connection plus the A2A port the sender advertised in its signed headers.
func callbackAddr(r *http.Request, port string) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if port == "" {
		return host
	}
	return net.JoinHostPort(host, port)
}
