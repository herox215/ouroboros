package a2a

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"ouroboros/internal/identity"
)

// Client talks to another node. Requests are signed with Identity so the peer
// can authenticate this node; Port is advertised for callbacks.
//
// Every request is addressed to a node by its public key. The TLS connection
// is pinned to that key: unless the node that answers holds it, the call
// fails with ErrUnverified and nothing is sent. The request is signed for the
// same key, so no other node accepts it either.
type Client struct {
	Identity *identity.Identity
	Name     string
	Model    string
	Port     int
}

// Send delivers text to the agent of the node with publicKey at baseURL and
// returns its reply. Messages with the same contextID belong to the same
// conversation.
func (c *Client) Send(ctx context.Context, baseURL, publicKey, contextID, text string) (string, error) {
	// A message sent while answering a peer is that peer's message passed on.
	hops := 0
	if origin, ok := OriginFrom(ctx); ok {
		hops = origin.Hops + 1
	}
	if hops > MaxHops {
		return "", ErrTooManyHops
	}
	res, err := c.call(ctx, baseURL, publicKey, MethodSend, sendParams{NewMessage("user", contextID, text), c.peer(), hops})
	if err != nil {
		return "", err
	}
	if res.Result == nil {
		return "", errors.New("a2a: response has no message")
	}
	return res.Result.Text(), nil
}

// Accept tells the peer with publicKey at baseURL (which asked to connect)
// that this node trusts it now.
func (c *Client) Accept(ctx context.Context, baseURL, publicKey string) error {
	return c.notify(ctx, baseURL, publicKey, MethodAccept)
}

// Reject tells the peer with publicKey at baseURL that this node refused its
// request.
func (c *Client) Reject(ctx context.Context, baseURL, publicKey string) error {
	return c.notify(ctx, baseURL, publicKey, MethodReject)
}

func (c *Client) notify(ctx context.Context, baseURL, publicKey, method string) error {
	_, err := c.call(ctx, baseURL, publicKey, method, contactParams{c.peer()})
	return err
}

// peer is what this node tells the other side about itself.
func (c *Client) peer() Peer { return Peer{Name: c.Name, Model: c.Model} }

// call runs a JSON-RPC method of the node with publicKey at baseURL: it wraps
// params in a request, signs it for that node, sends it over a connection
// pinned to the same key and decodes the response. An error the peer answers
// with is returned as *Error. Every method of Client is a thin wrapper around
// call.
func (c *Client) call(ctx context.Context, baseURL, publicKey, method string, params any) (*response, error) {
	rawParams, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	// Cannot fail: the ID and the params are valid JSON already.
	body, _ := json.Marshal(request{JSONRPC: "2.0", ID: json.RawMessage(`"` + NewID() + `"`), Method: method, Params: rawParams})
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", "ouroboros")
	sign(httpReq, body, c.Identity, c.Port, publicKey, method)

	// Whoever answers in place of the node that was asked gets nothing to read.
	answer, _, err := exchange(httpReq, holder(publicKey))
	if err != nil {
		return nil, err
	}
	var res response
	if err := json.Unmarshal(answer, &res); err != nil {
		return nil, fmt.Errorf("a2a: invalid response: %w", err)
	}
	if res.Error != nil {
		return nil, res.Error
	}
	return &res, nil
}

// FetchCard reads the agent card of the node with publicKey at baseURL. The
// connection is pinned to that key, which proves that the node holding it is
// the one that answers at baseURL; otherwise FetchCard fails with
// ErrUnverified. What the card says is still only what that node claims.
func FetchCard(ctx context.Context, baseURL, publicKey string) (AgentCard, error) {
	card, _, err := fetchCard(ctx, baseURL, holder(publicKey))
	return card, err
}

// FetchKey returns the public key of the node at baseURL: the key of the
// certificate it presents, which the handshake proves it to hold. It is for a
// node known by its address only: whoever answers there is taken for the
// node. Pin what follows to the key that is returned.
func FetchKey(ctx context.Context, baseURL string) (string, error) {
	_, key, err := fetchCard(ctx, baseURL, anyone)
	return key, err
}

// fetchCard reads the agent card of the node at baseURL, if accept takes its
// public key, and returns the card with that key.
func fetchCard(ctx context.Context, baseURL string, accept func(key string) bool) (AgentCard, string, error) {
	var card AgentCard
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(baseURL, "/")+CardPath, nil)
	if err != nil {
		return card, "", err
	}
	body, key, err := exchange(req, accept)
	if err != nil {
		return card, "", err
	}
	if err := json.Unmarshal(body, &card); err != nil {
		return card, "", fmt.Errorf("a2a: invalid agent card: %w", err)
	}
	if card.Name == "" {
		return card, "", errors.New("a2a: agent card has no name")
	}
	return card, key, nil
}
