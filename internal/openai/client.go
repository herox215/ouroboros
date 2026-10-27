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

	"ouroboros/internal/chat"
	"ouroboros/internal/credentials"
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

// ProviderName names the provider of OpenAI-compatible APIs in the accesses
// of a node.
const ProviderName = "openai"

// Credentials are what is kept of an access to the OpenAI-compatible API at
// baseURL with apiKey, which may be empty: the endpoint, which is shown, and
// the key, which is a secret.
func Credentials(baseURL, apiKey string) credentials.Credentials {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	data, _ := json.Marshal(map[string]string{"base_url": baseURL}) // texts: cannot fail
	c := credentials.Credentials{Title: Provider(baseURL), About: baseURL, Data: data}
	if apiKey != "" {
		c.Secret, _ = json.Marshal(map[string]string{"api_key": apiKey})
	}
	return c
}

// Saved reads the endpoint and the key of an access as Credentials keeps
// them.
func Saved(c credentials.Credentials) (baseURL, apiKey string, err error) {
	var data struct {
		BaseURL string `json:"base_url"`
	}
	var secret struct {
		APIKey string `json:"api_key"`
	}
	if err := json.Unmarshal(c.Data, &data); err != nil || data.BaseURL == "" {
		return "", "", errors.New("openai: the access has no endpoint")
	}
	if len(c.Secret) > 0 {
		if err := json.Unmarshal(c.Secret, &secret); err != nil {
			return "", "", errors.New("openai: the key of the access cannot be read")
		}
	}
	return data.BaseURL, secret.APIKey, nil
}

// usage is how APIs tell what a call cost: OpenAI and most others the cached
// tokens in prompt_tokens_details, DeepSeek in prompt_cache_hit_tokens.
type usage struct {
	PromptTokens        int      `json:"prompt_tokens"`
	CompletionTokens    int      `json:"completion_tokens"`
	CostUSD             *float64 `json:"cost"`
	PromptTokensDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	PromptCacheHitTokens int `json:"prompt_cache_hit_tokens"`
}

func (u *usage) read() Usage {
	if u == nil {
		return Usage{}
	}
	return Usage{Prompt: u.PromptTokens, Cached: max(u.PromptTokensDetails.CachedTokens, u.PromptCacheHitTokens), Output: u.CompletionTokens, CostUSD: u.CostUSD}
}

// reasoning is what a model thought, in the fields APIs send it in: vLLM and
// OpenRouter in "reasoning", llama.cpp and DeepSeek in "reasoning_content".
type reasoning struct {
	ReasoningContent string `json:"reasoning_content,omitempty"`
	Reasoning        string `json:"reasoning,omitempty"`
	ReasoningText    string `json:"reasoning_text,omitempty"`
	ReasoningItems   string `json:"reasoning_items,omitempty"`
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
	if r.ReasoningItems != "" {
		return r.ReasoningItems, ReasoningItems
	}
	return "", ""
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

// Unavailable marks err as a call the API cannot answer for now.
// Deprecated: use chat.Unavailable from internal/chat.
func Unavailable(err error) error { return chat.NewUnavailable(err) }

// IsUnavailable reports whether err means the API cannot answer for now.
// Deprecated: use chat.IsUnavailable from internal/chat.
func IsUnavailable(err error) bool { return chat.IsUnavailable(err) }

// IsTransient reports whether err is likely answered when asked again.
// Deprecated: use chat.IsTransient from internal/chat.
func IsTransient(err error) bool { return chat.IsTransient(err) }

// Client talks to an OpenAI-compatible API.
type Client struct {
	baseURL string
	apiKey  string
	session string // fallback session ID for requests without one
	http    *http.Client
	// refused are the optional parameters the API refused: they are not
	// sent again.
	refused [len(optional)]atomic.Bool
	efforts Efforts // the efforts the models took
	sent    sent    // the last requests of the sessions, for a trace
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
type options struct {
	IncludeUsage bool `json:"include_usage"`
}

func (c *Client) Complete(ctx context.Context, model string, msgs []Message, tools []Tool) (Message, error) {
	msgs = shown(own(MessagesForModel(msgs)))
	effort := EffortOf(ctx)
	efforts := c.efforts.Of(model, effort)
	in := struct {
		Model           string        `json:"model"`
		Messages        []wireMessage `json:"messages"`
		Tools           []Tool        `json:"tools,omitempty"`
		Stream          bool          `json:"stream"`
		StreamOptions   *options      `json:"stream_options,omitempty"`
		PromptCacheKey  string        `json:"prompt_cache_key,omitempty"`
		ReasoningEffort string        `json:"reasoning_effort,omitempty"`
	}{Model: model, Tools: tools, Stream: true, ReasoningEffort: efforts[0]}
	in.Messages, _ = wireMessages(msgs, false)
	if !c.refused[streamOptions].Load() {
		in.StreamOptions = &options{IncludeUsage: true}
	}
	if !c.refused[promptCacheKey].Load() {
		in.PromptCacheKey = c.sessionOf(ctx)
	}
	marked := 0
	if claude(model) && !c.refused[cacheControl].Load() {
		in.Messages, marked = wireMessages(msgs, true)
	}
	t := traceOf(ctx)
	if t != nil {
		c.traceRequest(t, c.sessionOf(ctx), model, in.Messages, tools, in.StreamOptions != nil, in.PromptCacheKey != "", marked)
		if in.ReasoningEffort != "" {
			t("llm request: reasoning_effort %q", in.ReasoningEffort)
		}
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
			in.Messages, marked = wireMessages(msgs, false)
		}
		if t != nil {
			t("llm request: the API refused %s, asked again without it", optional[i])
		}
		msg, err = c.complete(ctx, in, func() { idle.Reset(timeout) })
	}
	// An API that does not know the effort may not say so: it is asked
	// again with the next one that is near, and at last with none.
	for _, next := range efforts[1:] {
		if !badRequest(err) {
			break
		}
		if t != nil {
			t("llm request: the API refused reasoning_effort %q, asked again with %q", in.ReasoningEffort, next)
		}
		in.ReasoningEffort = next
		msg, err = c.complete(ctx, in, func() { idle.Reset(timeout) })
	}
	if err == nil {
		c.efforts.Took(model, effort, in.ReasoningEffort)
		msg.Effort = Took{Asked: effort, Value: in.ReasoningEffort, Told: true}
	}
	if cause := context.Cause(ctx); err != nil && cause != nil && !errors.Is(cause, context.Canceled) {
		return Message{}, cause
	}
	return msg, err
}

// own returns msgs without what another kind of provider kept of the
// thoughts of its model: see ReasoningItems. msgs is not changed.
func own(msgs []Message) []Message {
	foreign := func(m Message) bool { return m.ReasoningField == ReasoningItems }
	if !slices.ContainsFunc(msgs, foreign) {
		return msgs
	}
	msgs = slices.Clone(msgs)
	for i := range msgs {
		if foreign(msgs[i]) {
			msgs[i].Reasoning, msgs[i].ReasoningField = "", ""
		}
	}
	return msgs
}

// shown returns msgs as they are sent with their pictures: those of the
// results of tools moved to a message of the user right after them, as the
// API takes the result of a tool only as text. msgs is not changed.
func shown(msgs []Message) []Message {
	if !slices.ContainsFunc(msgs, func(m Message) bool { return len(m.Images) > 0 }) {
		return msgs
	}
	var out []Message
	var images []Image
	for i, m := range msgs {
		if m.Role == "tool" {
			images = append(images, m.Images...)
			m.Images = nil
		}
		out = append(out, m)
		last := i == len(msgs)-1 || msgs[i+1].Role != "tool"
		if last && len(images) > 0 {
			out = append(out, Message{Role: "user", Content: "[The pictures the tools above returned.]", Images: images})
			images = nil
		}
	}
	return out
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
func breakpoints(msgs []wireMessage) ([]wireMessage, int) {
	out := slices.Clone(msgs)
	marked := 0
	first := slices.IndexFunc(out, func(m wireMessage) bool { return m.Role == "system" })
	if first >= 0 && textOf(out[first]) != "" {
		out[first].breakpoint = true
		marked++
	}
	last := 0
	for i := len(out) - 1; i > first && last < maxBreakpoints; i-- {
		if textOf(out[i]) != "" {
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
				Message wireReply `json:"message"`
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
		msg := out.Choices[0].Message.message()
		msg.Usage = out.Usage.read()
		return msg, nil
	}
	return readStream(resp.Body, alive, t, chat.ThoughtOf(ctx), AnsweringOf(ctx), CallingOf(ctx))
}

// readStream puts together the message a stream of server-sent events tells
// in pieces. The usage comes after the choice is finished, in an event of its
// own or in the last one: the stream is read on until it came, or until it
// ends. A trace t, if not nil, is told what the API said besides the message.
func readStream(body io.Reader, alive func(), t trace, thought, answering func(text string), calling func(bytes int)) (Message, error) {
	written := 0 // of calls of tools, see WithCalling
	var said *answered
	events := 0
	if t != nil {
		said = &answered{}
		defer func() { said.tell(t, events) }()
	}
	var msg Message
	var calls []ToolCall
	var content, thoughts strings.Builder
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
			if answering != nil && d.Content != "" {
				answering(d.Content) // a front end shows the answer as it comes
			}
			text, field := d.first()
			thoughts.WriteString(text)
			if thought != nil && text != "" {
				thought(text) // a front end shows the thinking as it comes
			}
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
				if n := len(part.Function.Name) + len(part.Function.Arguments); calling != nil && n > 0 {
					written += n
					calling(written) // a front end shows that the model writes a call
				}
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
	msg.Content, msg.Reasoning, msg.ToolCalls = content.String(), thoughts.String(), calls
	msg.Usage = used.read()
	return msg, nil
}

// sessionOf is the session requests under ctx are sent with.
func (c *Client) sessionOf(ctx context.Context) string {
	return cmp.Or(SessionOf(ctx), c.session)
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

// textOf is the text content of a message that has not been made parts.
func textOf(w wireMessage) string {
	text, _ := w.Content.(string)
	return text
}

// Category says what the status means to fallbacks, retries and diagnosis.
func (e *APIError) Category() chat.Category { return chat.StatusCategory(e.Status) }
