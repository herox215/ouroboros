package claudecode

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"ouroboros/internal/chat"
	"strings"

	"ouroboros/internal/openai"
)

// capture puts together the message the stream of the API brings, event by
// event, as the relay passes it on.
type capture struct {
	line     []byte // of the stream, not ended yet
	id       string
	model    string
	blocks   []*block
	stop     string
	usage    apiUsage
	complete bool   // the stream said that the message is complete
	failed   string // what an event "error" said
	calls    int    // bytes of calls of tools so far
}

// block is a block of the content of the message: what content_block_start
// brought, and what the deltas added to it.
type block struct {
	start                              map[string]json.RawMessage
	text, thinking, signature, partial strings.Builder
}

// apiUsage is how the API tells what a call cost.
type apiUsage struct {
	InputTokens              int `json:"input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	OutputTokens             int `json:"output_tokens"`
}

// take takes the counts of u that it has: the stream tells them in parts.
func (a *apiUsage) take(u apiUsage) {
	a.InputTokens = max(a.InputTokens, u.InputTokens)
	a.CacheCreationInputTokens = max(a.CacheCreationInputTokens, u.CacheCreationInputTokens)
	a.CacheReadInputTokens = max(a.CacheReadInputTokens, u.CacheReadInputTokens)
	a.OutputTokens = max(a.OutputTokens, u.OutputTokens)
}

// streamEvent is an event of the stream of the Messages API.
type streamEvent struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message struct {
		ID    string   `json:"id"`
		Model string   `json:"model"`
		Usage apiUsage `json:"usage"`
	} `json:"message"`
	ContentBlock map[string]json.RawMessage `json:"content_block"`
	Delta        struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		Thinking    string `json:"thinking"`
		Signature   string `json:"signature"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Usage apiUsage `json:"usage"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// feed reads chunk of the stream, telling answering of the text the model
// writes and calling how much of calls it has written, if they are not nil.
func (c *capture) feed(chunk []byte, answering func(string), calling func(int)) {
	c.line = append(c.line, chunk...)
	for {
		end := bytes.IndexByte(c.line, '\n')
		if end < 0 {
			return
		}
		line := bytes.TrimSpace(c.line[:end])
		c.line = c.line[end+1:]
		data, ok := bytes.CutPrefix(line, []byte("data:"))
		if !ok {
			continue // the name of the event, or the empty line between events
		}
		var e streamEvent
		if json.Unmarshal(bytes.TrimSpace(data), &e) != nil {
			continue
		}
		c.event(e, answering, calling)
	}
}

func (c *capture) event(e streamEvent, answering func(string), calling func(int)) {
	var b *block
	if e.Index >= 0 && e.Index < len(c.blocks) {
		b = c.blocks[e.Index]
	}
	switch e.Type {
	case "message_start":
		c.id, c.model = e.Message.ID, e.Message.Model
		c.usage.take(e.Message.Usage)
	case "content_block_start":
		for len(c.blocks) <= e.Index {
			c.blocks = append(c.blocks, &block{})
		}
		c.blocks[e.Index].start = e.ContentBlock
	case "content_block_delta":
		if b == nil {
			return
		}
		switch e.Delta.Type {
		case "text_delta":
			b.text.WriteString(e.Delta.Text)
			if answering != nil && e.Delta.Text != "" {
				answering(e.Delta.Text)
			}
		case "thinking_delta":
			b.thinking.WriteString(e.Delta.Thinking)
		case "signature_delta":
			b.signature.WriteString(e.Delta.Signature)
		case "input_json_delta":
			b.partial.WriteString(e.Delta.PartialJSON)
			c.calls += len(e.Delta.PartialJSON)
			if calling != nil && e.Delta.PartialJSON != "" {
				calling(c.calls)
			}
		}
	case "message_delta":
		c.stop = e.Delta.StopReason
		c.usage.take(e.Usage)
	case "message_stop":
		c.complete = true
	case "error":
		c.failed = orDefault(e.Error.Message, e.Error.Type)
		if e.Error.Type == "overloaded_error" {
			c.failed = "overloaded: " + c.failed
		}
	}
}

// failure is why the stream did not bring a whole message, nil if it did.
func (c *capture) failure() error {
	switch {
	case c.complete:
		return nil
	case strings.HasPrefix(c.failed, "overloaded: "):
		return &apiError{Status: 529, Type: "overloaded_error", Message: strings.TrimPrefix(c.failed, "overloaded: ")}
	case c.failed != "":
		return &apiError{Status: http.StatusInternalServerError, Type: "api_error", Message: c.failed}
	}
	return fmt.Errorf("claude-code: the answer of the API ended before it was complete: %w", io.ErrUnexpectedEOF)
}

// content is the block as the API sent it, whole, as it goes back with the
// next request.
func (b *block) content() json.RawMessage {
	fields := map[string]json.RawMessage{}
	for k, v := range b.start {
		fields[k] = v
	}
	text := func(s string) json.RawMessage { j, _ := json.Marshal(s); return j } // a text: cannot fail
	switch b.kind() {
	case "text":
		fields["text"] = text(b.text.String())
	case "thinking":
		fields["thinking"] = text(b.thinking.String())
		fields["signature"] = text(b.signature.String())
	case "tool_use", "server_tool_use":
		fields["input"] = b.input()
	}
	out, _ := json.Marshal(fields) // JSON that was read: cannot fail
	return out
}

// kind is the type of the block.
func (b *block) kind() string {
	var kind string
	json.Unmarshal(b.start["type"], &kind) // a block without one is of no kind
	return kind
}

// input is the input of a call as the deltas brought it: an object, the
// empty one for none.
func (b *block) input() json.RawMessage {
	partial := strings.TrimSpace(b.partial.String())
	if partial == "" {
		if start := b.start["input"]; len(start) > 0 && json.Valid(start) {
			return start
		}
		return json.RawMessage(`{}`)
	}
	var compact bytes.Buffer
	if json.Compact(&compact, []byte(partial)) != nil {
		return json.RawMessage(`{}`) // the model wrote no JSON: the tool is told it got nothing
	}
	return compact.Bytes()
}

// message is the message the capture brought, in the form of the agents: its
// text, the calls of tools and what goes back with the next request of it.
func (c *capture) message() chat.Message {
	msg := chat.Message{Role: "assistant"}
	var text strings.Builder
	var blocks []json.RawMessage
	thought := false
	for _, b := range c.blocks {
		if b == nil || b.start == nil {
			continue
		}
		blocks = append(blocks, b.content())
		switch b.kind() {
		case "text":
			text.WriteString(b.text.String())
		case "tool_use":
			var id, name string
			json.Unmarshal(b.start["id"], &id)
			json.Unmarshal(b.start["name"], &name)
			msg.ToolCalls = append(msg.ToolCalls, chat.ToolCall{ID: id, Type: "function",
				Function: chat.FunctionCall{Name: name, Arguments: string(b.input())}})
		case "thinking", "redacted_thinking":
			thought = true
		}
	}
	msg.Content = text.String()
	if c.stop == "refusal" {
		msg.ToolCalls = nil // a call the refusal cut off is not run
		if msg.Content == "" {
			msg.Content = "(Claude declined to answer this.)"
		}
	}
	// What the model thought goes back with the next request, signed as the
	// API sent it: the model goes on from it between calls of tools.
	if thought {
		msg.Reasoning, msg.ReasoningField = keepThoughts(blocks), chat.ReasoningItems
	}
	u := c.usage
	msg.Usage = chat.Usage{Prompt: u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens,
		Cached: u.CacheReadInputTokens, Output: u.OutputTokens, InPlan: true}
	return msg
}

// thoughts are the blocks of an assistant message as the API sent them, kept
// in Reasoning to go back with the next request: Claude marks that they are
// of this provider, as the reasoning items of another one are no object.
type thoughts struct {
	Claude  int               `json:"claude_code"`
	Content []json.RawMessage `json:"content"`
}

// keepThoughts is blocks as they are kept.
func keepThoughts(blocks []json.RawMessage) string {
	b, _ := json.Marshal(thoughts{Claude: 1, Content: blocks}) // JSON that was read: cannot fail
	return string(b)
}

// thoughtsOf are the blocks kept of the assistant message m, nil if it keeps
// none of this provider, or if m is no longer what they were sent as: what
// was changed of it since goes back as it is now, without them.
func thoughtsOf(m chat.Message) []json.RawMessage {
	if m.ReasoningField != chat.ReasoningItems || m.Reasoning == "" {
		return nil
	}
	var t thoughts
	if json.Unmarshal([]byte(m.Reasoning), &t) != nil || t.Claude != 1 || len(t.Content) == 0 {
		return nil
	}
	var text strings.Builder
	var calls []chat.ToolCall
	for _, raw := range t.Content {
		var b struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		}
		json.Unmarshal(raw, &b) // written here: can be read
		switch b.Type {
		case "text":
			text.WriteString(b.Text)
		case "tool_use":
			calls = append(calls, chat.ToolCall{ID: b.ID, Function: chat.FunctionCall{Name: b.Name, Arguments: string(b.Input)}})
		}
	}
	if text.String() != m.Content || len(calls) != len(m.ToolCalls) {
		return nil
	}
	for i, call := range calls {
		was := m.ToolCalls[i]
		if call.ID != was.ID || call.Function.Name != was.Function.Name || !sameJSON(call.Function.Arguments, was.Function.Arguments) {
			return nil
		}
	}
	return t.Content
}

// sameJSON reports whether a and b are the same JSON, written alike or not.
func sameJSON(a, b string) bool {
	var x, y bytes.Buffer
	return json.Compact(&x, []byte(a)) == nil && json.Compact(&y, []byte(b)) == nil && bytes.Equal(x.Bytes(), y.Bytes())
}

// apiError is an answer of the API that is no message.
type apiError struct {
	Status        int
	Type, Message string
}

func (e *apiError) Error() string {
	status := fmt.Sprintf("claude-code: HTTP %d", e.Status)
	if e.Type != "" {
		status += " " + e.Type
	}
	return status + ": " + e.Message
}

// Unwrap is e as an error of an OpenAI-compatible API, for what tells by the
// status whether to try again or to go on with a fallback.
func (e *apiError) Unwrap() error { return &openai.APIError{Status: e.Status, Message: e.Message} }

// known is e as the owner is told: what to do about the errors that have
// something to be done about, and e itself for the others.
func (e *apiError) known() error {
	switch e.Status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%w (the API said: %s)", ErrSignedOut, orDefault(e.Message, "nothing more"))
	case http.StatusTooManyRequests:
		return chat.Mark(fmt.Errorf("%w: %s", ErrLimit, orDefault(e.Message, "the API did not say more")), chat.Unavailable|chat.RateLimited)
	}
	return e
}
