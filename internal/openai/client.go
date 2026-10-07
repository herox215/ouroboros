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
	// breakpoint makes the message a cache_control breakpoint: see
	// breakpoints.
	breakpoint bool
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

// MarshalJSON writes the reasoning of m into the field it came in. The
// content of a breakpoint is a part of text marked for the cache.
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
	if !m.breakpoint {
		return json.Marshal(out)
	}
	type part struct {
		Type         string            `json:"type"`
		Text         string            `json:"text"`
		CacheControl map[string]string `json:"cache_control"`
	}
	return json.Marshal(struct {
		Role    string `json:"role"`
		Content []part `json:"content"`
		plain
		reasoning
	}{m.Role, []part{{"text", m.Content, map[string]string{"type": "ephemeral"}}}, out.plain, out.reasoning})
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
	// refused are the optional parameters the API refused: they are not
	// sent again.
	refused [len(optional)]atomic.Bool
	sent    sent // the last requests of the sessions, for a trace
}

// optional are the parameters of a completion that ouro sends to save, which
// an API may not know: the usage of a streamed answer, the key of the cache
// of the session, and the breakpoints of the cache of a Claude model. An API
// that refuses one with HTTP 400, naming it, is asked again without it, and
// not sent it again.
var optional = [...]string{"stream_options", "prompt_cache_key", "cache_control"}

const (
	streamOptions = iota
	promptCacheKey
	cacheControl
)

// maxBreakpoints is how many messages after the first system message are
// breakpoints of the cache: Anthropic takes 4 in all.
const maxBreakpoints = 3

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
// The usage of the call is asked for along with the stream, and the session
// goes as prompt_cache_key: calls of one conversation then hit the same
// cache, also behind a gateway that spreads them over servers. A Claude
// model caches only up to breakpoints, which are set, see breakpoints. An API
// that refuses one of these parameters is asked without it from then on.
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
	if !c.refused[streamOptions].Load() {
		in.StreamOptions = &options{IncludeUsage: true}
	}
	if !c.refused[promptCacheKey].Load() {
		in.PromptCacheKey = c.sessionOf(ctx)
	}
	marked := 0
	if claude(model) && !c.refused[cacheControl].Load() {
		in.Messages, marked = breakpoints(msgs)
	}
	t := traceOf(ctx)
	if t != nil {
		c.traceRequest(t, c.sessionOf(ctx), model, msgs, tools, in.StreamOptions != nil, in.PromptCacheKey != "", marked)
	}
	// Given up when nothing comes for timeout, from the request on.
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	idle := time.AfterFunc(timeout, func() {
		cancel(fmt.Errorf("openai: no data from the API for %v: %w", timeout, context.DeadlineExceeded))
	})
	defer idle.Stop()
	msg, err := c.complete(ctx, in, func() { idle.Reset(timeout) })
	for range optional {
		sent := []bool{in.StreamOptions != nil, in.PromptCacheKey != "", marked > 0}
		i := slices.IndexFunc(optional[:], func(name string) bool { return refuses(err, name) })
		if i < 0 || !sent[i] {
			break
		}
		c.refused[i].Store(true)
		switch i {
		case streamOptions:
			in.StreamOptions = nil
		case promptCacheKey:
			in.PromptCacheKey = ""
		case cacheControl:
			in.Messages, marked = msgs, 0
		}
		if t != nil {
			t("llm request: the API refused %s, asked again without it", optional[i])
		}
		msg, err = c.complete(ctx, in, func() { idle.Reset(timeout) })
	}
	if cause := context.Cause(ctx); err != nil && cause != nil && !errors.Is(cause, context.Canceled) {
		return Message{}, cause
	}
	return msg, err
}

// refuses reports whether err is the API refusing the parameter name.
func refuses(err error, name string) bool {
	var refused *APIError
	return errors.As(err, &refused) && refused.Status == http.StatusBadRequest && strings.Contains(refused.Message, name)
}

// claude reports whether model is one of Anthropic, also behind a gateway,
// like "anthropic/claude-sonnet-4.5": it caches only up to cache_control
// breakpoints.
func claude(model string) bool { return strings.Contains(strings.ToLower(model), "claude") }

// breakpoints returns msgs with the first system message and the last
// maxBreakpoints messages with content after it as breakpoints of the cache,
// and how many there are. A provider like Anthropic caches a request up to
// each breakpoint, and the next request finds what it has of that: the
// system prompt, along with the tools before it, and the conversation up to
// one of the last messages of the request before, also when the last of
// them is not sent again. msgs is not changed.
func breakpoints(msgs []Message) ([]Message, int) {
	out := slices.Clone(msgs)
	marked := 0
	first := slices.IndexFunc(out, func(m Message) bool { return m.Role == "system" })
	if first >= 0 && out[first].Content != "" {
		out[first].breakpoint = true
		marked++
	}
	last := 0
	for i := len(out) - 1; i > first && last < maxBreakpoints; i-- {
		if out[i].Content != "" {
			out[i].breakpoint = true
			last++
		}
	}
	return out, marked + last
}

// complete sends in and reads the answer, telling alive whenever data came.
func (c *Client) complete(ctx context.Context, in any, alive func()) (Message, error) {
	resp, err := c.do(ctx, http.MethodPost, "/chat/completions", in)
	if err != nil {
		return Message{}, err
	}
	defer resp.Body.Close()
	alive()
	t := traceOf(ctx)
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		var out struct {
			Choices []struct {
				Message Message `json:"message"`
			} `json:"choices"`
			Usage *usage `json:"usage"`
		}
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return Message{}, err
		}
		if t != nil {
			var a answered
			a.read(body)
			a.tell(t, 0)
		}
		if err := json.Unmarshal(body, &out); err != nil {
			return Message{}, err
		}
		if len(out.Choices) == 0 {
			return Message{}, errors.New("openai: response has no choices")
		}
		msg := out.Choices[0].Message
		msg.Usage = out.Usage.read()
		return msg, nil
	}
	return readStream(resp.Body, alive, t)
}

// readStream puts together the message a stream of server-sent events tells
// in pieces. The usage comes after the choice is finished, in an event of its
// own or in the last one: the stream is read on until it came, or until it
// ends. A trace t, if not nil, is told what the API said besides the message.
func readStream(body io.Reader, alive func(), t trace) (Message, error) {
	var said *answered
	events := 0
	if t != nil {
		said = &answered{}
		defer func() { said.tell(t, events) }()
	}
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
		if said != nil {
			events++
			said.read([]byte(data))
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

	start := time.Now()
	resp, err := c.http.Do(req)
	t := traceOf(ctx)
	if err != nil {
		if t != nil {
			t("llm response: none after %s: %v", time.Since(start).Round(time.Millisecond), err)
		}
		return nil, err
	}
	if t != nil {
		traceResponse(t, resp, time.Since(start).Round(time.Millisecond).String())
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxEvent))
		if t != nil {
			t("llm response: body %s", around(raw, 0))
		}
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		json.Unmarshal(raw, &e)
		return nil, &APIError{Status: resp.StatusCode, Message: e.Error.Message}
	}
	return resp, nil
}
