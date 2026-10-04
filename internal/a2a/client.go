package a2a

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"ouroboros/internal/identity"
)

var httpClient = &http.Client{Timeout: 5 * time.Minute}

// maxAnswerBytes is how much of an answer is read. A longer one is cut off
// and fails the signature check.
const maxAnswerBytes = 16 << 20

// Client talks to another node. Requests are signed with Identity so the peer
// can authenticate this node; Port is advertised for callbacks.
//
// Every request is addressed to a node by its public key: it is signed for
// that key, so no other node accepts it, and the answer has to be signed by
// that key, or the call fails with ErrUnverified.
type Client struct {
	Identity *identity.Identity
	Name     string
	Model    string
	Port     int
	HTTP     *http.Client
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
// params in a request, signs it for that node, checks that the response is
// signed by it and decodes the response. An error the peer answers with is
// returned as *Error. Every method of Client is a thin wrapper around call.
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

	client := c.HTTP
	if client == nil {
		client = httpClient
	}
	answer, signer, err := fetch(client, httpReq, func(answer []byte) []byte {
		return responseMessage(httpReq.Header.Get(HeaderNonce), httpReq.Header.Get(HeaderSig), answer)
	})
	if err != nil {
		return nil, err
	}
	// Whoever answers in place of the node that was asked is not believed.
	if signer != publicKey {
		return nil, ErrUnverified
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

// fetch sends req and returns the body of the answer together with the public
// key of the node that signed it. message returns the string the signature
// has to cover for that body. An answer without a valid signature fails with
// ErrUnverified.
func fetch(client *http.Client, req *http.Request, message func(answer []byte) []byte) (answer []byte, signer string, err error) {
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
	signer, err = answeredBy(resp.Header, message(answer))
	return answer, signer, err
}

// FetchCard reads the agent card of the node with publicKey at baseURL. The
// card has to be signed by that key just now, which proves that the node
// holding the key is the one that answers at baseURL; otherwise FetchCard
// fails with ErrUnverified. What the card says is still only what that node
// claims.
func FetchCard(ctx context.Context, baseURL, publicKey string) (AgentCard, error) {
	card, signer, err := fetchCard(ctx, baseURL)
	if err == nil && signer != publicKey {
		err = ErrUnverified
	}
	return card, err
}

// FetchKey returns the public key of the node at baseURL, which proves that
// it holds the key by signing its agent card. It is for a node known by its
// address only: whoever answers there is taken for the node.
func FetchKey(ctx context.Context, baseURL string) (string, error) {
	_, signer, err := fetchCard(ctx, baseURL)
	return signer, err
}

// fetchCard reads the agent card of the node at baseURL and returns it with
// the public key that signed it.
func fetchCard(ctx context.Context, baseURL string) (AgentCard, string, error) {
	var card AgentCard
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(baseURL, "/")+CardPath, nil)
	if err != nil {
		return card, "", err
	}
	nonce := rand.Text()
	req.Header.Set(HeaderNonce, nonce)
	body, signer, err := fetch(httpClient, req, func(body []byte) []byte { return cardMessage(nonce, body) })
	if err != nil {
		return card, "", err
	}
	if err := json.Unmarshal(body, &card); err != nil {
		return card, "", fmt.Errorf("a2a: invalid agent card: %w", err)
	}
	if card.Name == "" {
		return card, "", errors.New("a2a: agent card has no name")
	}
	return card, signer, nil
}
