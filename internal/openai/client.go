// Package openai is a minimal client for OpenAI-compatible APIs
// (OpenAI, Ollama, LM Studio, vLLM, OpenRouter, ...).
package openai

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"time"
)

// DefaultBaseURL is the official OpenAI endpoint.
const DefaultBaseURL = "https://api.openai.com/v1"

// timeout is how long a request may take, and how long the stream of a
// completion may go without data: a model that thinks long goes on as long
// as it sends something. Tests shorten it.
var timeout = 5 * time.Minute

// maxEvent is the most bytes one event of a stream may have.
const maxEvent = 4 << 20

// UserAgent identifies ouro to the API. Some providers (e.g. OpenCode Go)
// reject generic HTTP-library user agents.
const UserAgent = "ouroboros"

// Provider is the name of the service a base URL points at, as it is
// commonly called: from its host, like "iGate" for igate.example.com,
// "OpenAI" for api.openai.com or "localhost" for one on this machine. A URL
// that cannot be read has no provider.
func Provider(baseURL string) string {
	host, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	h := host.Hostname()
	switch h {
	case "":
		return ""
	case "localhost", "127.0.0.1", "::1":
		return "localhost"
	case "api.openai.com":
		return "OpenAI"
	case "openrouter.ai":
		return "OpenRouter"
	}
	name, _, _ := strings.Cut(h, ".")
	if strings.HasPrefix(name, "igate") {
		return "iGate"
	}
	return name
}

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
	// Reasoning is what the model thought before it wrote an assistant
	// message, and ReasoningField the field of the API it came in. Sent back
	// in that field ("reasoning" if none), the model goes on from what it
	// thought between calls of tools instead of thinking it all anew.
	Reasoning      string `json:"-"`
	ReasoningField string `json:"-"`
	// ToolCalls are the tools an assistant message wants to have run.
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	// ToolCallID names the call a "tool" message answers.
	ToolCallID string `json:"tool_call_id,omitempty"`
	// Usage is what the call that brought an assistant message cost, as far
	// as the API told. It is not sent back.
	Usage Usage `json:"-"`
}

// Usage is what one call of the LLM cost in tokens: Prompt are the tokens of
// what was sent, and Cached those of them the provider had cached from an
// earlier call. Both are 0 when the API did not say.
type Usage struct {
	Prompt, Cached int
}

// usage is how APIs tell what a call cost: OpenAI and most others the cached
// tokens in prompt_tokens_details, DeepSeek in prompt_cache_hit_tokens.
type usage struct {
	PromptTokens        int `json:"prompt_tokens"`
	PromptTokensDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	PromptCacheHitTokens int `json:"prompt_cache_hit_tokens"`
}

func (u *usage) read() Usage {
	if u == nil {
		return Usage{}
	}
	return Usage{u.PromptTokens, max(u.PromptTokensDetails.CachedTokens, u.PromptCacheHitTokens)}
}

// reasoning is what a model thought, in the fields APIs send it in: vLLM and
// OpenRouter in "reasoning", llama.cpp and DeepSeek in "reasoning_content".
type reasoning struct {
	ReasoningContent string `json:"reasoning_content,omitempty"`
	Reasoning        string `json:"reasoning,omitempty"`
	ReasoningText    string `json:"reasoning_text,omitempty"`
}

// first returns the first field that is not empty, with its name. An API
// may send the same text in two of them, and it counts once.
func (r reasoning) first() (text, field string) {
	if r.ReasoningContent != "" {
		return r.ReasoningContent, "reasoning_content"
	}
	if r.Reasoning != "" {
		return r.Reasoning, "reasoning"
	}
	if r.ReasoningText != "" {
		return r.ReasoningText, "reasoning_text"
	}
	return "", ""
}

// MarshalJSON writes the reasoning of m into the field it came in.
func (m Message) MarshalJSON() ([]byte, error) {
	type plain Message
	out := struct {
		plain
		reasoning
	}{plain: plain(m)}
	switch m.ReasoningField {
	case "reasoning_content":
		out.ReasoningContent = m.Reasoning
	case "reasoning_text":
		out.ReasoningText = m.Reasoning
	default:
		out.reasoning.Reasoning = m.Reasoning
	}
	return json.Marshal(out)
}

// UnmarshalJSON reads a message with its reasoning from any of the fields.
func (m *Message) UnmarshalJSON(b []byte) error {
	type plain Message
	var in struct {
		plain
		reasoning
	}
	if err := json.Unmarshal(b, &in); err != nil {
		return err
	}
	*m = Message(in.plain)
	m.Reasoning, m.ReasoningField = in.first()
	return nil
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
	// plain is set once the API refused stream_options: then the usage of a
	// streamed answer is not asked for again.
	plain atomic.Bool
}

// New returns a client for baseURL. An empty apiKey sends no credentials.
func New(baseURL, apiKey string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		session: rand.Text(),
		http:    &http.Client{},
	}
}

// Models returns the sorted IDs of all models available to the key.
func (c *Client) Models(ctx context.Context) ([]string, error) {
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	resp, err := c.do(ctx, http.MethodGet, "/models", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	ids := make([]string, len(out.Data))
	for i, m := range out.Data {
		ids[i] = m.ID
	}
	slices.Sort(ids)
	return ids, nil
}

// Complete sends msgs to model, which may answer with calls to tools instead
// of a reply.
//
// The answer is streamed: a gateway that gives up on a request without data
// for a while (HTTP 504) keeps waiting while the model thinks and writes. An
// API that does not stream answers in one piece, which is taken as well.
//
// The usage of the call is asked for along with the stream, and OpenAI is
// given the session as prompt_cache_key: calls of one conversation then hit
// the same cache. An API that refuses stream_options is asked without them
// from then on.
func (c *Client) Complete(ctx context.Context, model string, msgs []Message, tools []Tool) (Message, error) {
	type options struct {
		IncludeUsage bool `json:"include_usage"`
	}
	in := struct {
		Model          string    `json:"model"`
		Messages       []Message `json:"messages"`
		Tools          []Tool    `json:"tools,omitempty"`
		Stream         bool      `json:"stream"`
		StreamOptions  *options  `json:"stream_options,omitempty"`
		PromptCacheKey string    `json:"prompt_cache_key,omitempty"`
	}{Model: model, Messages: msgs, Tools: tools, Stream: true}
	if !c.plain.Load() {
		in.StreamOptions = &options{IncludeUsage: true}
	}
	if Provider(c.baseURL) == "OpenAI" {
		in.PromptCacheKey = c.sessionOf(ctx)
	}
	// Given up when nothing comes for timeout, from the request on.
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	idle := time.AfterFunc(timeout, func() {
		cancel(fmt.Errorf("openai: no data from the API for %v: %w", timeout, context.DeadlineExceeded))
	})
	defer idle.Stop()
	msg, err := c.complete(ctx, in, func() { idle.Reset(timeout) })
	var refused *APIError
	if in.StreamOptions != nil && errors.As(err, &refused) && refused.Status == http.StatusBadRequest &&
		strings.Contains(refused.Message, "stream_options") {
		c.plain.Store(true)
		in.StreamOptions = nil
		msg, err = c.complete(ctx, in, func() { idle.Reset(timeout) })
	}
	if cause := context.Cause(ctx); err != nil && cause != nil && !errors.Is(cause, context.Canceled) {
		return Message{}, cause
	}
	return msg, err
}

// complete sends in and reads the answer, telling alive whenever data came.
func (c *Client) complete(ctx context.Context, in any, alive func()) (Message, error) {
	resp, err := c.do(ctx, http.MethodPost, "/chat/completions", in)
	if err != nil {
		return Message{}, err
	}
	defer resp.Body.Close()
	alive()
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		var out struct {
			Choices []struct {
				Message Message `json:"message"`
			} `json:"choices"`
			Usage *usage `json:"usage"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return Message{}, err
		}
		if len(out.Choices) == 0 {
			return Message{}, errors.New("openai: response has no choices")
		}
		msg := out.Choices[0].Message
		msg.Usage = out.Usage.read()
		return msg, nil
	}
	return readStream(resp.Body, alive)
}

// readStream puts together the message a stream of server-sent events tells
// in pieces. The usage comes after the choice is finished, in an event of its
// own or in the last one: the stream is read on until it came, or until it
// ends.
func readStream(body io.Reader, alive func()) (Message, error) {
	var msg Message
	var calls []ToolCall
	var content, thought strings.Builder
	var used *usage
	chosen, finished := false, false
	lines := bufio.NewScanner(body)
	lines.Buffer(make([]byte, 0, 64<<10), maxEvent)
	for !(finished && used != nil) && lines.Scan() {
		alive()
		data, ok := strings.CutPrefix(lines.Text(), "data:")
		if !ok {
			continue // a comment, an event name or the empty line between events
		}
		data = strings.TrimSpace(data)
		if data == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Role    string `json:"role"`
					Content string `json:"content"`
					reasoning
					ToolCalls []struct {
						Index    *int   `json:"index"`
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
			Usage *usage `json:"usage"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return Message{}, fmt.Errorf("openai: the stream has an event that is no JSON: %w", err)
		}
		if chunk.Error != nil {
			return Message{}, fmt.Errorf("openai: the stream broke off: %s", chunk.Error.Message)
		}
		if chunk.Usage != nil {
			used = chunk.Usage
		}
		// Only the first choice counts, as with an answer in one piece.
		for _, choice := range chunk.Choices[:min(1, len(chunk.Choices))] {
			chosen = true
			d := choice.Delta
			msg.Role = cmp.Or(d.Role, msg.Role)
			content.WriteString(d.Content)
			text, field := d.first()
			thought.WriteString(text)
			msg.ReasoningField = cmp.Or(msg.ReasoningField, field)
			for _, part := range d.ToolCalls {
				// Without an index, a part with an ID starts a call and one
				// without goes on with the last.
				i := len(calls) - 1
				if part.Index != nil {
					i = *part.Index
				} else if part.ID != "" {
					i = len(calls)
				}
				i = max(i, 0)
				if i > len(calls) {
					return Message{}, fmt.Errorf("openai: the stream has a tool call with index %d", i)
				}
				if i == len(calls) {
					calls = append(calls, ToolCall{})
				}
				call := &calls[i]
				call.ID = cmp.Or(part.ID, call.ID)
				call.Type = cmp.Or(part.Type, call.Type)
				call.Function.Name += part.Function.Name
				call.Function.Arguments += part.Function.Arguments
			}
			finished = finished || choice.FinishReason != ""
		}
	}
	if err := lines.Err(); err != nil {
		return Message{}, err
	}
	if !chosen {
		return Message{}, errors.New("openai: response has no choices")
	}
	for i := range calls {
		calls[i].Type = cmp.Or(calls[i].Type, "function")
	}
	msg.Content, msg.Reasoning, msg.ToolCalls = content.String(), thought.String(), calls
	msg.Usage = used.read()
	return msg, nil
}

// sessionOf is the session requests under ctx are sent with.
func (c *Client) sessionOf(ctx context.Context) string {
	if session, _ := ctx.Value(sessionKey{}).(string); session != "" {
		return session
	}
	return c.session
}

// do sends a request and returns the response of one that succeeded, whose
// body the caller closes.
func (c *Client) do(ctx context.Context, method, path string, in any) (*http.Response, error) {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", UserAgent)
	req.Header.Set("X-Opencode-Session", c.sessionOf(ctx))

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		json.NewDecoder(resp.Body).Decode(&e)
		return nil, &APIError{Status: resp.StatusCode, Message: e.Error.Message}
	}
	return resp, nil
}
