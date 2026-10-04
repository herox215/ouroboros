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

// Headers that carry a signed request. A response is signed as well: it
// carries HeaderPublic and HeaderSig of the node that answers.
const (
	HeaderPublic    = "X-Ouro-Public-Key"
	HeaderTime      = "X-Ouro-Time"
	HeaderNonce     = "X-Ouro-Nonce"
	HeaderSig       = "X-Ouro-Signature"
	HeaderPort      = "X-Ouro-Port"
	HeaderRecipient = "X-Ouro-Recipient" // public key of the node the request is for
)

// What a signature is for. It is the first line of the signed string, so that
// a signature made for one purpose is never valid for another.
const (
	signedRequest  = "ouro-request"
	signedResponse = "ouro-response"
	signedCard     = "ouro-card"
)

// clockSkew is how far a request timestamp may deviate from local time.
const clockSkew = 2 * time.Minute

// ErrUnverified means the answer does not come from the node that was asked:
// it is not signed, or not by the public key the request was addressed to.
var ErrUnverified = errors.New("a2a: the answer is not signed by the node that was asked")

// authMessage is the canonical byte string a request signature covers. It
// binds the body to the timestamp, the nonce, the sender's A2A port, the
// public key of the node the request is for and the JSON-RPC method.
func authMessage(timestamp, nonce, port, recipient, method string, body []byte) []byte {
	return signedMessage(signedRequest, body, timestamp, nonce, port, recipient, method)
}

// responseMessage is the canonical byte string the signature of a response
// covers. It binds the body to the one request it answers, named by its nonce
// and its signature: the response to another request does not verify.
func responseMessage(nonce, requestSig string, body []byte) []byte {
	return signedMessage(signedResponse, body, nonce, requestSig)
}

// cardMessage is the canonical byte string the signature of an agent card
// covers. It binds the card to the nonce of whoever asked for it.
func cardMessage(nonce string, body []byte) []byte {
	return signedMessage(signedCard, body, nonce)
}

// signedMessage joins what a signature is for, the fields it binds and the
// hash of the body, one per line.
func signedMessage(purpose string, body []byte, fields ...string) []byte {
	sum := sha256.Sum256(body)
	lines := append(append([]string{purpose}, fields...), hex.EncodeToString(sum[:]))
	return []byte(strings.Join(lines, "\n"))
}

// sign sets the authentication headers on req for the given body: a request
// of id for the method of the node with the public key recipient.
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

// signAnswer sets the headers that prove id wrote the answer, where msg is
// the responseMessage or cardMessage of its body.
func signAnswer(h http.Header, id *identity.Identity, msg []byte) {
	h.Set(HeaderPublic, id.Public())
	h.Set(HeaderSig, base64.StdEncoding.EncodeToString(id.Sign(msg)))
}

// answeredBy returns the public key of the node that signed an answer with
// the headers h, where msg is the responseMessage or cardMessage of its body.
func answeredBy(h http.Header, msg []byte) (string, error) {
	publicKey := h.Get(HeaderPublic)
	sig, err := base64.StdEncoding.DecodeString(h.Get(HeaderSig))
	if err != nil || !identity.Verify(publicKey, msg, sig) {
		return "", ErrUnverified
	}
	return publicKey, nil
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
