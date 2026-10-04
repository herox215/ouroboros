package a2a

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"ouroboros/internal/identity"
)

// Nodes talk over TLS 1.3 only. A node presents a self-signed certificate
// made from its key (identity.Certificate), and whoever connects compares the
// key of that certificate with the key it knows the node by: the handshake
// proves that the other side holds the key, the connection is encrypted, and
// no certificate authority is involved. TLSConfig is the serving side,
// exchange the connecting one.

const (
	timeout          = 5 * time.Minute  // for a whole exchange: an agent may think for a while
	dialTimeout      = 30 * time.Second // to reach the node
	handshakeTimeout = 10 * time.Second // to learn who it is
)

// maxAnswerBytes is how much of an answer is read. A longer one is cut off
// and cannot be parsed.
const maxAnswerBytes = 16 << 20

// ErrUnverified means the node that answers is not the node that was asked:
// it does not hold the public key the request was addressed to. Nothing was
// sent to it.
var ErrUnverified = errors.New("a2a: the node that answers does not hold the key of the node that was asked")

// errPlain refuses to talk to a node without TLS.
var errPlain = errors.New("a2a: nodes are only reached over https")

// TLSConfig is how the node with id listens: TLS 1.3 with the certificate of
// its key. Serve the Handler of a Server with the same identity through it,
// so that the key a peer pins is the key requests have to be signed for.
func TLSConfig(id *identity.Identity) *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{id.Certificate()}}
}

// holder accepts the node with publicKey and no other.
func holder(publicKey string) func(key string) bool {
	return func(key string) bool { return key == publicKey }
}

// anyone accepts whatever node answers: for the first contact with an address.
func anyone(string) bool { return true }

// exchange sends req to a node and returns the body of its answer with the
// public key the node proved to hold in the TLS handshake. accept decides
// whether that key is the node req is meant for; if not, the handshake fails
// with ErrUnverified before anything of req is sent.
//
// Every exchange has its own connection, so a key is never taken over from an
// earlier one, and the node has to answer itself: a redirect is not followed.
func exchange(req *http.Request, accept func(key string) bool) (answer []byte, key string, err error) {
	if req.URL.Scheme != "https" {
		return nil, "", errPlain
	}
	client := &http.Client{
		Timeout:       timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{
			DialContext:         (&net.Dialer{Timeout: dialTimeout}).DialContext,
			TLSHandshakeTimeout: handshakeTimeout,
			DisableKeepAlives:   true,
			TLSClientConfig: &tls.Config{
				MinVersion: tls.VersionTLS13,
				// No authority signs the certificate of a node and its name
				// is not what counts: its key is, checked right below.
				InsecureSkipVerify: true,
				VerifyConnection: func(cs tls.ConnectionState) error {
					if key := identity.CertificateKey(cs.PeerCertificates[0]); key == "" || !accept(key) {
						return ErrUnverified
					}
					return nil
				},
			},
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("a2a: HTTP %d", resp.StatusCode)
	}
	if answer, err = io.ReadAll(io.LimitReader(resp.Body, maxAnswerBytes)); err != nil {
		return nil, "", err
	}
	return answer, identity.CertificateKey(resp.TLS.PeerCertificates[0]), nil
}
