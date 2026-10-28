package a2a

import (
	"bufio"
	"context"
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

// openTimeout is how long a node has to open a channel: reach the node and be
// let in. Tests shorten it.
var openTimeout = 30 * time.Second

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

// ServerName is the name of the server a node with publicKey is asked for in
// the TLS handshake. Several agents of one node, its profiles, answer on one
// port: the name tells it which of them is to answer, with its certificate.
// A node that runs one agent answers with it whatever the name is.
func ServerName(publicKey string) string { return identity.IDOf(publicKey) + ".ouro" }

// pin is the public key of the node a connection is for.
type pin string

// anyone is the pin that takes whatever node answers: for the first contact
// with an address. No public key is "*".
const anyone pin = "*"

// pinned is how a node connects to the node of p: TLS 1.3, and the
// handshake fails with ErrUnverified unless the certificate is of its key.
func pinned(p pin) *tls.Config {
	c := &tls.Config{
		MinVersion: tls.VersionTLS13,
		// No authority signs the certificate of a node and its name is not
		// what counts: its key is, checked right below.
		InsecureSkipVerify: true,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if key := identity.CertificateKey(cs.PeerCertificates[0]); key == "" || p != anyone && key != string(p) {
				return ErrUnverified
			}
			return nil
		},
	}
	if p != anyone {
		c.ServerName = ServerName(string(p))
	}
	return c
}

// exchange sends req to a node and returns the body of its answer with the
// public key the node proved to hold in the TLS handshake. Unless that is
// p, the key of the node req is meant for (see pinned), the handshake
// fails with ErrUnverified before anything of req is sent.
//
// Every exchange has its own connection, so a key is never taken over from an
// earlier one, and the node has to answer itself: a redirect is not followed.
func exchange(req *http.Request, p pin) (answer []byte, key string, err error) {
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
			TLSClientConfig:     pinned(p),
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

// upgrade sends req to a node over a connection of its own and, if the node
// answers with "101 Switching Protocols", returns that connection, with r to
// read it through: what the node sent after its answer may be in r already.
// Any other answer is returned as answer, like exchange does, and the
// connection is closed.
//
// The connection is pinned like every other: unless the node proves to hold
// p, the key of the node req is meant for, the handshake fails with
// ErrUnverified before anything of req is sent. The
// node has openTimeout, and no longer than the context of req, to answer.
func upgrade(req *http.Request, p pin) (conn net.Conn, r *bufio.Reader, answer []byte, err error) {
	if req.URL.Scheme != "https" {
		return nil, nil, nil, errPlain
	}
	ctx, cancel := context.WithTimeout(req.Context(), openTimeout)
	defer cancel()
	dialer := &tls.Dialer{NetDialer: &net.Dialer{Timeout: dialTimeout}, Config: pinned(p)}
	if conn, err = dialer.DialContext(ctx, "tcp", req.URL.Host); err != nil {
		return nil, nil, nil, err
	}
	// Waiting for the answer ends with ctx: the connection is cut. An answer
	// is only whole with its body, so the supervision lasts until that is
	// read too: a node that stalls halfway through the body is cut off like
	// one that does not answer at all.
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	r = bufio.NewReader(conn)
	var resp *http.Response
	if err = req.Write(conn); err == nil {
		resp, err = http.ReadResponse(r, req)
	}
	// A channel that opened is the connection now and outlives this request:
	// it is no longer cut by the timeout for opening it.
	opened := err == nil && resp.StatusCode == http.StatusSwitchingProtocols
	if opened && stop() {
		return conn, r, nil, nil
	}
	if err != nil || opened {
		// ctx ended first and cut the connection, also under a channel that
		// opened just then: what came is no answer.
		if !stop() {
			err = ctx.Err()
		}
		conn.Close()
		return nil, nil, nil, err
	}
	defer conn.Close()
	defer resp.Body.Close()
	defer stop()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, nil, fmt.Errorf("a2a: HTTP %d", resp.StatusCode)
	}
	answer, err = io.ReadAll(io.LimitReader(resp.Body, maxAnswerBytes))
	if !stop() {
		// ctx ended the read and the connection was cut under it: what came
		// is no answer.
		return nil, nil, nil, ctx.Err()
	}
	return nil, nil, answer, err
}
