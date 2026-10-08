package chatgpt

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"ouroboros/internal/llm"
	"ouroboros/internal/openai"
)

// idle is how long the stream of an answer may go without data: a model
// that thinks long goes on as long as it sends something. Tests shorten it.
var idle = 5 * time.Minute

// maxEvent is the most bytes one event of a stream may have.
const maxEvent = 4 << 20

// namespace is the namespace the tools of the agent are in: the API takes
// functions for the plan only grouped in one.
const namespace = "ouro"

// client talks to the Responses API with the tokens of an access. It
// translates the messages of the agents, which are those of chat
// completions, to the items of a response and back.
type client struct {
	o      Options
	access llm.Access
	// noCacheKey says that the API refused the key of the cache of the
	// conversation: it is not sent again.
	noCacheKey atomic.Bool
	sent       openai.Sent // the last requests of the sessions, for a trace
}

// sessionHeader is the header that tells the API which conversation a
// request belongs to. The API sends the requests of one conversation to
// where it has cached that conversation so far only with it: the
// prompt_cache_key alone does not do that on this route. Measured against
// the API: without the header, a call that goes on from the one before found
// nothing cached, call after call; with it, every call from the third on
// found what the call before it had sent.
const sessionHeader = "session_id"

// item is one item of the input of a response, or of its output.
type item struct {
	Type string `json:"type,omitempty"`
	// Of a message.
	Role    string `json:"role,omitempty"`
	Content any    `json:"content,omitempty"`
	// Of a call of a function, and of its output.
	CallID    string  `json:"call_id,omitempty"`
	Name      string  `json:"name,omitempty"`
	Namespace string  `json:"namespace,omitempty"`
	Arguments *string `json:"arguments,omitempty"`
	Output    *string `json:"output,omitempty"`
}

// function is a tool of the agent, as the namespace lists it.
type function struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// group is the namespace with the tools of the agent.
type group struct {
	Type        string     `json:"type"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Tools       []function `json:"tools"`
}

// request is what is sent. The plan takes requests that are streamed and
// not stored, with all that the model needs in the input.
type request struct {
	Model          string            `json:"model"`
	Instructions   string            `json:"instructions,omitempty"`
	Input          []json.RawMessage `json:"input"`
	Tools          []group           `json:"tools,omitempty"`
	Store          bool              `json:"store"`
	Stream         bool              `json:"stream"`
	PromptCacheKey string            `json:"prompt_cache_key,omitempty"`
}

// translate turns the conversation msgs into the instructions and the input
// of a response. The first system message is the instructions; one that
// follows is a message of the developer, since the plan takes none of the
// system in the input. What the model thought before a message goes back
// before it, as far as it is of this provider: the thoughts of another one
// are left out.
func translate(msgs []openai.Message) (instructions string, input []json.RawMessage) {
	add := func(v any) {
		b, _ := json.Marshal(v) // texts: cannot fail
		input = append(input, b)
	}
	for i, m := range msgs {
		switch m.Role {
		case "system":
			if i == 0 {
				instructions = m.Content
			} else {
				add(item{Role: "developer", Content: m.Content})
			}
		case "tool":
			add(item{Type: "function_call_output", CallID: m.ToolCallID, Output: &m.Content})
		case "assistant":
			if m.ReasoningField == openai.ReasoningItems {
				var thoughts []json.RawMessage
				json.Unmarshal([]byte(m.Reasoning), &thoughts) // what cannot be read is left out
				input = append(input, thoughts...)
			}
			if m.Content != "" {
				add(item{Role: "assistant", Content: m.Content})
			}
			for _, call := range m.ToolCalls {
				add(item{Type: "function_call", CallID: call.ID, Name: call.Function.Name, Namespace: namespace, Arguments: &call.Function.Arguments})
			}
		default:
			add(item{Role: "user", Content: m.Content})
		}
	}
	return instructions, input
}

// Complete sends msgs to model, which may answer with calls to tools instead
// of a reply. The answer is streamed, and it counts only once the API said
// that it is complete.
func (c *client) Complete(ctx context.Context, model string, msgs []openai.Message, tools []openai.Tool) (openai.Message, error) {
	in := request{Model: model, Stream: true}
	in.Instructions, in.Input = translate(msgs)
	if len(tools) > 0 {
		g := group{Type: "namespace", Name: namespace, Description: "What the agent of this Ouroboros node can do."}
		for _, t := range tools {
			g.Tools = append(g.Tools, function{"function", t.Function.Name, t.Function.Description, t.Function.Parameters})
		}
		in.Tools = []group{g}
	}
	if !c.noCacheKey.Load() {
		in.PromptCacheKey = openai.SessionOf(ctx)
	}
	// Given up when nothing comes for idle, from the request on.
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	watch := time.AfterFunc(idle, func() {
		cancel(fmt.Errorf("chatgpt: no data from the API for %v: %w", idle, context.DeadlineExceeded))
	})
	defer watch.Stop()
	alive := func() { watch.Reset(idle) }

	session := openai.SessionOf(ctx)
	t := openai.TraceOf(ctx)
	if t != nil {
		c.traceRequest(t, session, in)
	}
	var msg openai.Message
	var err error
	for try := 0; ; {
		msg, err = c.complete(ctx, session, in, alive, t)
		failed, _ := errors.AsType[*apiError](err)
		switch {
		case failed != nil && failed.Code == "subscription_sharing_unsupported_capability" && failed.Param == "prompt_cache_key" && in.PromptCacheKey != "":
			// The API does not take the key of the cache here: without it.
			c.noCacheKey.Store(true)
			in.PromptCacheKey = ""
			if t != nil {
				t("llm request: the API refused prompt_cache_key, asked again without it; it is not sent again while the node runs")
			}
			continue
		case failed != nil && failed.later() && try < len(backoff):
			// It could not take the request right now: a little later.
			select {
			case <-time.After(backoff[try]):
				try++
				continue
			case <-ctx.Done():
			}
		}
		break
	}
	if cause := context.Cause(ctx); err != nil && cause != nil && !errors.Is(cause, context.Canceled) {
		return openai.Message{}, cause
	}
	return msg, err
}

// traceRequest tells t what the request in of session sends, and how far it
// begins like the last request of the session: the instructions first, then
// the items of the input, as the API is sent them.
func (c *client) traceRequest(t func(string, ...any), session string, in request) {
	instructions, _ := json.Marshal(item{Role: "system", Content: in.Instructions}) // a text: cannot fail
	items := [][]byte{instructions}
	size := 0
	for _, raw := range in.Input {
		items = append(items, raw)
		size += len(raw)
	}
	tools, n := []byte(nil), 0
	if len(in.Tools) > 0 {
		tools, _ = json.Marshal(in.Tools)
		n = len(in.Tools[0].Tools)
	}
	t("llm request: %s/responses, model %s, session %s, instructions %.1f KiB, %d items (%.1f KiB), %d tools (%.1f KiB, hash %s), prompt_cache_key %t, session_id header %t",
		c.o.api(), in.Model, session, float64(len(in.Instructions))/1024, len(in.Input), float64(size)/1024, n, float64(len(tools))/1024,
		openai.Hash(in.Tools), in.PromptCacheKey != "", session != "")
	t("llm prefix: %s", c.sent.Compare(session, in.Tools, items))
}

// complete sends in and reads the answer, telling alive whenever data came
// and the trace t, if not nil, what the API said besides the answer.
func (c *client) complete(ctx context.Context, session string, in request, alive func(), t func(string, ...any)) (openai.Message, error) {
	body, _ := json.Marshal(in) // texts and JSON that was read: cannot fail
	start := time.Now()
	resp, err := c.o.send(ctx, c.access, func(string) (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.o.api()+"/responses", bytes.NewReader(body))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "text/event-stream")
			if session != "" {
				req.Header.Set(sessionHeader, session)
			}
		}
		return req, err
	})
	if err != nil {
		if t != nil {
			t("llm response: none after %s: %v", time.Since(start).Round(time.Millisecond), err)
		}
		return openai.Message{}, err
	}
	defer resp.Body.Close()
	alive()
	if t != nil {
		openai.TraceResponse(t, resp, time.Since(start))
	}
	if resp.StatusCode != http.StatusOK {
		err := failure(resp)
		if t != nil {
			t("llm response: failed: %v", err)
		}
		return openai.Message{}, err
	}
	return readStream(resp.Body, alive, t)
}

// event is one event of the stream of a response.
type event struct {
	Type string `json:"type"`
	// Item is the item an "response.output_item.done" brings.
	Item json.RawMessage `json:"item"`
	// Response is the response as "response.completed", "response.failed"
	// and "response.incomplete" tell it.
	Response struct {
		ID                string  `json:"id"`
		Model             string  `json:"model"`
		Error             *failed `json:"error"`
		IncompleteDetails struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
		// Usage is kept as the API sent it, for a trace.
		Usage json.RawMessage `json:"usage"`
	} `json:"response"`
	// Of an "error" event.
	failed
}

// failed is an error as the API tells it.
type failed struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Param   string `json:"param"`
}

// output is an item of the output of a response.
type output struct {
	Type    string `json:"type"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// readStream puts together the message the events of a response tell: its
// texts, the calls of tools it asks for and what the model thought, which is
// kept as the API sent it to go back with the next request. Only a stream
// that ends with "response.completed" is an answer. A trace t, if not nil,
// is told what the API said besides the message.
func readStream(body io.Reader, alive func(), t func(string, ...any)) (openai.Message, error) {
	events := 0
	msg := openai.Message{Role: "assistant"}
	var content strings.Builder
	var thoughts []json.RawMessage
	lines := bufio.NewScanner(body)
	lines.Buffer(make([]byte, 0, 64<<10), maxEvent)
	for lines.Scan() {
		alive()
		data, ok := strings.CutPrefix(lines.Text(), "data:")
		if !ok {
			continue // the name of the event, a comment or the empty line between events
		}
		events++
		var e event
		if err := json.Unmarshal([]byte(strings.TrimSpace(data)), &e); err != nil {
			return openai.Message{}, fmt.Errorf("chatgpt: the stream has an event that is no JSON: %w", err)
		}
		switch e.Type {
		case "response.output_item.done":
			var o output
			if err := json.Unmarshal(e.Item, &o); err != nil {
				return openai.Message{}, fmt.Errorf("chatgpt: the stream has an item that cannot be read: %w", err)
			}
			switch o.Type {
			case "message":
				for _, part := range o.Content {
					if part.Type == "output_text" {
						content.WriteString(part.Text)
					}
				}
			case "function_call":
				msg.ToolCalls = append(msg.ToolCalls, openai.ToolCall{ID: o.CallID, Type: "function",
					Function: openai.FunctionCall{Name: o.Name, Arguments: o.Arguments}})
			case "reasoning":
				thoughts = append(thoughts, thought(e.Item))
			}
		case "response.completed":
			msg.Content = content.String()
			var used struct {
				InputTokens        int `json:"input_tokens"`
				OutputTokens       int `json:"output_tokens"`
				InputTokensDetails struct {
					CachedTokens int `json:"cached_tokens"`
				} `json:"input_tokens_details"`
			}
			json.Unmarshal(e.Response.Usage, &used) // a usage that cannot be read is none
			msg.Usage = openai.Usage{Prompt: used.InputTokens, Cached: used.InputTokensDetails.CachedTokens, Output: used.OutputTokens, InPlan: true}
			if t != nil {
				t("llm answered: id %q, model %q, events %d, usage %s", e.Response.ID, e.Response.Model, events, orDefault(string(e.Response.Usage), "none"))
			}
			if len(thoughts) > 0 {
				b, _ := json.Marshal(thoughts) // JSON that was read: cannot fail
				msg.Reasoning, msg.ReasoningField = string(b), openai.ReasoningItems
			}
			return msg, nil
		case "response.failed":
			if e.Response.Error == nil {
				return openai.Message{}, errors.New("chatgpt: the response failed, the API did not say why")
			}
			return openai.Message{}, (&apiError{Status: http.StatusOK, failed: *e.Response.Error}).known()
		case "response.incomplete":
			return openai.Message{}, fmt.Errorf("chatgpt: the response is incomplete: %s", orDefault(e.Response.IncompleteDetails.Reason, "the API did not say why"))
		case "error":
			return openai.Message{}, (&apiError{Status: http.StatusOK, failed: e.failed}).known()
		}
	}
	if err := lines.Err(); err != nil {
		return openai.Message{}, err
	}
	return openai.Message{}, errors.New("chatgpt: the stream ended before the response was complete")
}

// thought is an item of reasoning as it goes back with the next request:
// what names it and what it holds, without how far it got.
func thought(raw json.RawMessage) json.RawMessage {
	var fields map[string]json.RawMessage
	json.Unmarshal(raw, &fields) // read once already
	delete(fields, "status")
	b, _ := json.Marshal(fields)
	return b
}

// apiError is an answer of the API that is no response.
type apiError struct {
	Status int
	failed
	// RetryAfter is what the API said of when to try again, "" if nothing.
	RetryAfter string
}

func (e *apiError) Error() string {
	return strings.TrimSpace(fmt.Sprintf("chatgpt: HTTP %d %s %s", e.Status, e.Code, e.Message))
}

// later reports whether the API could not take the request right now, and
// may a little later.
func (e *apiError) later() bool {
	return e.Status == http.StatusServiceUnavailable ||
		slices.Contains([]string{"subscription_sharing_usage_unavailable", "subscription_sharing_user_unavailable"}, e.Code)
}

// known is e as the owner is told: what to do about the errors that have
// something to be done about, and e itself for the others.
func (e *apiError) known() error {
	switch {
	case e.Code == "subscription_sharing_usage_limit_exceeded":
		// When it is over is told only if the API says so.
		if e.RetryAfter != "" {
			return openai.Unavailable(fmt.Errorf("%w (the API says to try again after %s)", ErrLimit, e.RetryAfter))
		}
		return openai.Unavailable(ErrLimit)
	case e.Code == "subscription_sharing_user_not_eligible":
		return fmt.Errorf("chatgpt: using the ChatGPT plan is not available for this account or workspace: %s", orDefault(e.Message, "the API did not say why"))
	case e.Code == "subscription_sharing_invalid_user", e.Status == http.StatusUnauthorized:
		return ErrSignedOut
	case e.later():
		return openai.Unavailable(fmt.Errorf("chatgpt: the ChatGPT plan cannot be used right now, try again later: %w", e))
	}
	return e
}

// failure reads why the API did not answer resp with a response. Before a
// response starts, the body need not hold the usual error: it may be a
// "detail" alone, which is told as it is.
func failure(resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxEvent))
	var body struct {
		Error  *failed         `json:"error"`
		Detail json.RawMessage `json:"detail"`
	}
	json.Unmarshal(raw, &body)
	e := &apiError{Status: resp.StatusCode, RetryAfter: resp.Header.Get("Retry-After")}
	if body.Error != nil {
		e.failed = *body.Error
	} else if body.Detail != nil {
		if json.Unmarshal(body.Detail, &e.Message) != nil {
			e.Message = string(body.Detail)
		}
	}
	return e.known()
}
