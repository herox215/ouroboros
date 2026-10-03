package a2a

import (
	"bytes"
	"context"
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

// Client talks to another node. Requests are signed with Identity so the peer
// can authenticate this node; Port is advertised for callbacks.
type Client struct {
	Identity *identity.Identity
	Name     string
	Model    string
	Port     int
	HTTP     *http.Client
}

// Send delivers text to the agent at baseURL and returns its reply.
// Messages with the same contextID belong to the same conversation.
func (c *Client) Send(ctx context.Context, baseURL, contextID, text string) (string, error) {
	res, err := c.call(ctx, baseURL, MethodSend, NewMessage("user", contextID, text))
	if err != nil {
		return "", err
	}
	if res.Error != nil {
		return "", res.Error
	}
	if res.Result == nil {
		return "", errors.New("a2a: response has no message")
	}
	return res.Result.Text(), nil
}

// Accept tells the peer at baseURL (which asked to connect) that this node
// trusts it now.
func (c *Client) Accept(ctx context.Context, baseURL string) error {
	return c.notify(ctx, baseURL, MethodAccept)
}

// Reject tells the peer at baseURL that this node refused its request.
func (c *Client) Reject(ctx context.Context, baseURL string) error {
	return c.notify(ctx, baseURL, MethodReject)
}

func (c *Client) notify(ctx context.Context, baseURL, method string) error {
	res, err := c.call(ctx, baseURL, method, Message{})
	if err != nil {
		return err
	}
	if res.Error != nil {
		return res.Error
	}
	return nil
}

func (c *Client) call(ctx context.Context, baseURL, method string, msg Message) (*response, error) {
	req := request{JSONRPC: "2.0", ID: json.RawMessage(`"` + NewID() + `"`), Method: method}
	req.Params.Message = msg
	req.Params.Peer = Peer{Name: c.Name, Model: c.Model}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", "ouroboros")
	if c.Identity != nil {
		sign(httpReq, body, c.Identity, c.Port)
	}

	client := c.HTTP
	if client == nil {
		client = httpClient
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("a2a: HTTP %d", resp.StatusCode)
	}
	var res response
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, fmt.Errorf("a2a: invalid response: %w", err)
	}
	return &res, nil
}

// FetchCard reads the agent card of the node at baseURL. Cards are public:
// nothing is signed, and what a card says is only what that node claims.
func FetchCard(ctx context.Context, baseURL string) (AgentCard, error) {
	var card AgentCard
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(baseURL, "/")+CardPath, nil)
	if err != nil {
		return card, err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return card, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return card, fmt.Errorf("a2a: HTTP %d", resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBodyBytes)).Decode(&card); err != nil {
		return card, fmt.Errorf("a2a: invalid agent card: %w", err)
	}
	if card.Name == "" {
		return card, errors.New("a2a: agent card has no name")
	}
	return card, nil
}

// Send delivers text to baseURL without authentication. It only works against
// peers that trust everyone; the node itself always uses Client.
func Send(ctx context.Context, baseURL, contextID, text string) (string, error) {
	req := request{JSONRPC: "2.0", ID: json.RawMessage(`"` + NewID() + `"`), Method: MethodSend}
	req.Params.Message = NewMessage("user", contextID, text)
	body, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("a2a: HTTP %d", resp.StatusCode)
	}
	var res response
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return "", fmt.Errorf("a2a: invalid response: %w", err)
	}
	switch {
	case res.Error != nil:
		return "", res.Error
	case res.Result == nil:
		return "", errors.New("a2a: response has no message")
	}
	return res.Result.Text(), nil
}
