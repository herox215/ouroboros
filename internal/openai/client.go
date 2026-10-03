// Package openai is a minimal client for OpenAI-compatible APIs
// (OpenAI, Ollama, LM Studio, vLLM, OpenRouter, ...).
package openai

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
)

// DefaultBaseURL is the official OpenAI endpoint.
const DefaultBaseURL = "https://api.openai.com/v1"

// UserAgent identifies ouro to the API. Some providers (e.g. OpenCode Go)
// reject generic HTTP-library user agents.
const UserAgent = "ouroboros"

type sessionKey struct{}

// WithSession returns a context whose requests are sent with the stable
// session ID id, letting providers route and cache per conversation.
func WithSession(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, sessionKey{}, id)
}

// Message is a single chat message.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	// ToolCalls are the tools an assistant message wants to have run.
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	// ToolCallID names the call a "tool" message answers.
	ToolCallID string `json:"tool_call_id,omitempty"`
}

// ToolCall is the model's request to run a tool.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

// FunctionCall names a function and its arguments as a JSON object.
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Tool describes a function the model may call.
type Tool struct {
	Type     string   `json:"type"`
	Function Function `json:"function"`
}

// Function is the signature of a tool; Parameters is a JSON schema.
type Function struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// NewTool returns a function tool taking the arguments described by the JSON schema.
func NewTool(name, description, schema string) Tool {
	return Tool{Type: "function", Function: Function{name, description, json.RawMessage(schema)}}
}

// APIError is a non-2xx response from the API.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("openai: HTTP %d: %s", e.Status, e.Message)
}

// Unauthorized reports whether the API key was rejected.
func (e *APIError) Unauthorized() bool {
	return e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden
}

// Client talks to an OpenAI-compatible API.
type Client struct {
	baseURL string
	apiKey  string
	session string // fallback session ID for requests without one
	http    *http.Client
}

// New returns a client for baseURL. An empty apiKey sends no credentials.
func New(baseURL, apiKey string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		session: rand.Text(),
		http:    &http.Client{Timeout: 5 * time.Minute},
	}
}

// Models returns the sorted IDs of all models available to the key.
func (c *Client) Models(ctx context.Context) ([]string, error) {
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, "/models", nil, &out); err != nil {
		return nil, err
	}
	ids := make([]string, len(out.Data))
	for i, m := range out.Data {
		ids[i] = m.ID
	}
	slices.Sort(ids)
	return ids, nil
}

// Chat sends msgs to model and returns the assistant's reply.
func (c *Client) Chat(ctx context.Context, model string, msgs []Message) (string, error) {
	msg, err := c.Complete(ctx, model, msgs, nil)
	return msg.Content, err
}

// Complete sends msgs to model, which may answer with calls to tools instead
// of a reply.
func (c *Client) Complete(ctx context.Context, model string, msgs []Message, tools []Tool) (Message, error) {
	in := struct {
		Model    string    `json:"model"`
		Messages []Message `json:"messages"`
		Tools    []Tool    `json:"tools,omitempty"`
	}{model, msgs, tools}
	var out struct {
		Choices []struct {
			Message Message `json:"message"`
		} `json:"choices"`
	}
	if err := c.do(ctx, http.MethodPost, "/chat/completions", in, &out); err != nil {
		return Message{}, err
	}
	if len(out.Choices) == 0 {
		return Message{}, errors.New("openai: response has no choices")
	}
	return out.Choices[0].Message, nil
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", UserAgent)
	session, _ := ctx.Value(sessionKey{}).(string)
	if session == "" {
		session = c.session
	}
	req.Header.Set("X-Opencode-Session", session)

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		json.NewDecoder(resp.Body).Decode(&e)
		return &APIError{Status: resp.StatusCode, Message: e.Error.Message}
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
