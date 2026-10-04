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

// ErrBuiltin is the error of Call for a method that is built in.
var ErrBuiltin = errors.New("a2a: a built-in method cannot be called by its name")

// Call runs method, which a plugin added to the node with publicKey at
// baseURL (see Method), and returns the message it answers with: nil if it
// answers with none. Like every request it is signed for that node and that
// method and sent over a connection pinned to its key. The node only runs the
// method for a peer its owner is connected to; an error it answers with is
// returned as *Error.
//
// The built-in methods are not reached this way: they decide about trust and
// have their own calls, Send, Accept, Reject and Open.
func (c *Client) Call(ctx context.Context, baseURL, publicKey, method string, params any) (*Message, error) {
	if Builtin(method) {
		return nil, ErrBuiltin
	}
	res, err := c.call(ctx, baseURL, publicKey, method, params)
	if err != nil {
		return nil, err
	}
	return res.Result, nil
}

func (c *Client) notify(ctx context.Context, baseURL, publicKey, method string) error {
	_, err := c.call(ctx, baseURL, publicKey, method, contactParams{c.peer()})
	return err
}

// peer is what this node tells the other side about itself.
func (c *Client) peer() Peer { return Peer{Name: c.Name, Model: c.Model} }

// Open opens the channel name, which a plugin added to the node with
// publicKey at baseURL (see ChannelHandler), and returns this end of it. The
// opening request is signed for that node like every request and sent over a
// connection pinned to its key, which then is the channel. The node only
// opens it for a peer its owner is connected to; an error it answers with is
// returned as *Error.
//
// The channel lives no longer than ctx: when ctx is done, it is closed. It is
// also closed by Close on either side and after ChannelIdleTimeout without a
// message. Whoever calls Open hands the channel to a ChannelSet (Hold), which
// closes it with the trust in the peer.
func (c *Client) Open(ctx context.Context, baseURL, publicKey, name string) (*Channel, error) {
	if Builtin(name) {
		return nil, ErrBuiltin
	}
	req, err := c.request(ctx, baseURL, publicKey, MethodOpen, channelParams{name})
	if err != nil {
		return nil, err
	}
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", ChannelProtocol)
	conn, r, answer, err := upgrade(req, holder(publicKey))
	if err != nil {
		return nil, err
	}
	if conn == nil {
		// The node answered instead of opening the channel: with why not.
		if _, err := decode(answer); err != nil {
			return nil, err
		}
		return nil, errors.New("a2a: the node did not open the channel")
	}
	ch := newChannel(conn, r, publicKey, ChannelIdleTimeout, closeWait)
	stop := context.AfterFunc(ctx, func() { ch.shut(context.Cause(ctx)) })
	ch.onClose(func() { stop() })
	return ch, nil
}

// call runs a JSON-RPC method of the node with publicKey at baseURL: it wraps
// params in a request, signs it for that node, sends it over a connection
// pinned to the same key and decodes the response. An error the peer answers
// with is returned as *Error. Every method of Client but Open is a thin
// wrapper around call, and Open sends the same request: so every request is
// signed and pinned the same way.
func (c *Client) call(ctx context.Context, baseURL, publicKey, method string, params any) (*response, error) {
	httpReq, err := c.request(ctx, baseURL, publicKey, method, params)
	if err != nil {
		return nil, err
	}
	// Whoever answers in place of the node that was asked gets nothing to read.
	answer, _, err := exchange(httpReq, holder(publicKey))
	if err != nil {
		return nil, err
	}
	return decode(answer)
}

// request is the signed JSON-RPC request that runs method with params on the
// node with publicKey at baseURL.
func (c *Client) request(ctx context.Context, baseURL, publicKey, method string, params any) (*http.Request, error) {
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
	return httpReq, nil
}

// decode reads the JSON-RPC response a node answered with. An error in it is
// returned as *Error.
func decode(answer []byte) (*response, error) {
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
