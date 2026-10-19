package claudecode

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"ouroboros/internal/llm"
	"ouroboros/internal/openai"
)

// tempDir makes the directory of a call; tests replace it.
var tempDir = os.MkdirTemp

// client is the model of an access: every call runs the CLI once.
type client struct {
	o      Options
	access llm.Access
}

// frame is a message as the CLI takes it on stdin. A message of the user
// that is not the last is not asked about: the CLI adds it to the
// conversation and says so with a result of no turns.
type frame struct {
	Type        string `json:"type"`
	ShouldQuery *bool  `json:"shouldQuery,omitempty"`
	Message     struct {
		Role    string            `json:"role"`
		Content []json.RawMessage `json:"content"`
	} `json:"message"`
}

// raw is v as JSON, of texts and JSON that was read: it cannot fail.
func raw(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// text is a block of text.
func text(s string) json.RawMessage { return raw(map[string]string{"type": "text", "text": s}) }

// image is a block with a picture.
func image(i openai.Image) json.RawMessage {
	return raw(map[string]any{"type": "image", "source": map[string]string{
		"type": "base64", "media_type": i.Type, "data": base64.StdEncoding.EncodeToString(i.Data)}})
}

// translate turns the conversation msgs into the system prompt and the
// frames of the CLI. The system messages before the first other one are the
// system prompt; one that follows is told the model as a text of the user.
// What the model thought before an assistant message goes back with it, as
// far as it is of this provider and the message is what the model wrote.
func translate(msgs []openai.Message) (system string, frames []frame, err error) {
	var prompt []string
	add := func(role string, blocks ...json.RawMessage) {
		if len(blocks) == 0 {
			return
		}
		if n := len(frames); n > 0 && frames[n-1].Type == role {
			frames[n-1].Message.Content = append(frames[n-1].Message.Content, blocks...)
			return
		}
		f := frame{Type: role}
		f.Message.Role, f.Message.Content = role, blocks
		frames = append(frames, f)
	}
	for _, m := range msgs {
		switch m.Role {
		case "system":
			if len(frames) == 0 {
				prompt = append(prompt, m.Content)
			} else {
				add("user", text(m.Content))
			}
		case "assistant":
			if kept := thoughtsOf(m); kept != nil {
				add("assistant", kept...)
				continue
			}
			var blocks []json.RawMessage
			if m.Content != "" {
				blocks = append(blocks, text(m.Content))
			}
			for _, call := range m.ToolCalls {
				input := json.RawMessage(call.Function.Arguments)
				if !json.Valid(input) || !strings.HasPrefix(strings.TrimSpace(call.Function.Arguments), "{") {
					input = json.RawMessage(`{}`)
				}
				blocks = append(blocks, raw(map[string]any{"type": "tool_use", "id": call.ID, "name": call.Function.Name, "input": input}))
			}
			add("assistant", blocks...)
		case "tool":
			result := map[string]any{"type": "tool_result", "tool_use_id": m.ToolCallID, "content": m.Content}
			if len(m.Images) > 0 {
				parts := []json.RawMessage{text(m.Content)}
				for _, i := range m.Images {
					parts = append(parts, image(i))
				}
				result["content"] = parts
			}
			add("user", raw(result))
		default:
			var blocks []json.RawMessage
			if m.Content != "" || len(m.Images) == 0 {
				blocks = append(blocks, text(m.Content))
			}
			for _, i := range m.Images {
				blocks = append(blocks, image(i))
			}
			add("user", blocks...)
		}
	}
	if len(frames) == 0 || frames[len(frames)-1].Type != "user" {
		return "", nil, errors.New("claude-code: the conversation does not end with a message of the user or the result of a tool")
	}
	return strings.Join(prompt, "\n\n"), frames, nil
}

// sessionID is the ID the CLI names the conversation by, which it tells the
// model: the same for every call of it, so that the API finds what it cached
// of it. It is made of the session of ouro, and without one of what the
// conversation begins with.
func sessionID(session, system string, frames []frame) string {
	seed := "ouro session " + session
	if session == "" {
		seed = string(raw([]any{system, frames[0]}))
	}
	sum := sha256.Sum256([]byte(seed))
	sum[6] = sum[6]&0x0f | 0x40 // version 4
	sum[8] = sum[8]&0x3f | 0x80 // the variant of RFC 9562
	return fmt.Sprintf("%x-%x-%x-%x-%x", sum[0:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
}

// efforts are the efforts the CLI takes; a less one is sent as the least.
var efforts = []string{"low", "medium", "high", "xhigh", "max"}

// effortOf is what the CLI is asked for effort, "" for nothing.
func effortOf(effort string) string {
	switch effort {
	case "":
		return ""
	case "none", "minimal":
		return "low"
	}
	if slices.Contains(efforts, effort) {
		return effort
	}
	return ""
}

// Complete sends msgs to model through the CLI, which may answer with calls
// to tools instead of a reply. The answer is the one request the relay let
// through, read as it passed: what the CLI does after it is of no account.
func (c *client) Complete(ctx context.Context, model string, msgs []openai.Message, tools []openai.Tool) (openai.Message, error) {
	acct, err := c.o.saved(c.access)
	if err != nil {
		return openai.Message{}, err
	}
	system, frames, err := translate(msgs)
	if err != nil {
		return openai.Message{}, err
	}
	effort := openai.EffortOf(ctx)
	value := effortOf(effort)

	// Given up when nothing comes for idle, from the start on.
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	watch := time.AfterFunc(idle, func() {
		cancel(fmt.Errorf("claude-code: no sign of Claude Code or the API for %v: %w", idle, context.DeadlineExceeded))
	})
	defer watch.Stop()
	alive := func() { watch.Reset(idle) }

	o := relayOptions{upstream: c.o.upstream(), frames: frames, answering: openai.AnsweringOf(ctx), calling: openai.CallingOf(ctx), alive: alive}
	for _, t := range tools {
		o.tools = append(o.tools, apiTool{Name: t.Function.Name, Description: t.Function.Description, InputSchema: schema(t.Function.Parameters)})
	}
	r, err := newRelay(ctx, o)
	if err != nil {
		return openai.Message{}, err
	}
	defer r.close()

	tmp, err := tempDir("", "ouro-claude-")
	if err != nil {
		return openai.Message{}, fmt.Errorf("claude-code: %w", err)
	}
	defer os.RemoveAll(tmp)
	prompt := filepath.Join(tmp, "system.md")
	if err := os.WriteFile(prompt, []byte(system), 0o600); err != nil {
		return openai.Message{}, fmt.Errorf("claude-code: %w", err)
	}
	session := sessionID(openai.SessionOf(ctx), system, frames)
	args := slices.Concat(streamArgs, []string{"--model", model, "--system-prompt-file", prompt, "--max-turns", "1",
		"--permission-mode", "dontAsk", "--session-id", session})
	if value != "" {
		args = append(args, "--effort", value)
	}
	if t := openai.TraceOf(ctx); t != nil {
		t("llm request: Claude Code %s, model %s, session %s, system %.1f KiB, %d frames, %d tools, effort %q",
			acct.Command, model, session, float64(len(system))/1024, len(frames), len(tools), value)
	}
	n, err := c.o.start(ctx, acct, r.url, args, alive)
	if err != nil {
		return openai.Message{}, err
	}
	defer n.end()

	events, err := converse(ctx, n, frames)
	if err != nil {
		return openai.Message{}, err
	}
	sent, err := r.result(ctx)
	switch {
	case !sent:
		return openai.Message{}, failed(events, n.said())
	case err != nil:
		return openai.Message{}, err
	}
	msg := r.capture.message()
	msg.Effort = openai.Took{Asked: effort, Value: value, Told: true}
	if t := openai.TraceOf(ctx); t != nil {
		t("llm answered: id %q, model %q, stop_reason %q, usage %s", r.capture.id, r.capture.model, r.capture.stop, raw(r.capture.usage))
	}
	return msg, nil
}

// converse hands the CLI n the frames, the last one asked about, and reads
// what it writes until its result. Every frame before the last of the user
// is answered with a result of no turns.
func converse(ctx context.Context, n *native, frames []frame) ([]event, error) {
	no := false
	for i, f := range frames {
		last := i == len(frames)-1
		if f.Type == "user" && !last {
			f.ShouldQuery = &no
		}
		n.send(f)
		if f.ShouldQuery == nil {
			continue
		}
		for {
			e, ok, err := n.next(ctx)
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, fmt.Errorf("claude-code: Claude Code ended while it was handed the conversation%s", n.said())
			}
			if e.Type == "result" {
				if e.IsError || e.NumTurns != 0 {
					return nil, fmt.Errorf("claude-code: Claude Code does not take the conversation as it was (%s)%s", e.Subtype, n.said())
				}
				break
			}
		}
	}
	n.stdin.Close()
	var events []event
	for {
		e, ok, err := n.next(ctx)
		if err != nil {
			return events, err
		}
		if !ok {
			return events, nil
		}
		events = append(events, e)
		if e.Type == "result" {
			return events, nil
		}
	}
}

// schema is the schema of the parameters of a tool as the API takes it: an
// object, the empty one for none.
func schema(parameters json.RawMessage) json.RawMessage {
	var s map[string]json.RawMessage
	if json.Unmarshal(parameters, &s) != nil {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	if _, ok := s["type"]; !ok {
		s["type"] = json.RawMessage(`"object"`)
	}
	return raw(s)
}
