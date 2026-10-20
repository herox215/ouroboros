package claudecode

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"ouroboros/internal/openai"
)

func TestCaptureReadsWhatItCan(t *testing.T) {
	var c capture
	c.feed([]byte("data: no json\n: a comment\n"), nil, nil)
	// A delta of a block that never started is left out, and so is a gap.
	c.feed([]byte(sse(
		`{"type":"content_block_delta","index":4,"delta":{"type":"text_delta","text":"lost"}}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"redacted_thinking","data":"secret"}}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"t1","name":"a","input":{"x":1}}}`,
		`{"type":"content_block_start","index":3,"content_block":{"type":"tool_use","id":"t2","name":"b"}}`,
		`{"type":"content_block_start","index":4,"content_block":{"type":"tool_use","id":"t3","name":"c"}}`,
		`{"type":"content_block_delta","index":4,"delta":{"type":"input_json_delta","partial_json":"{broken"}}`,
		`{"type":"message_delta","delta":{"stop_reason":"refusal"}}`,
		`{"type":"message_stop"}`,
	)), nil, nil)
	msg := c.message()
	if msg.Content != "(Claude declined to answer this.)" || msg.ToolCalls != nil {
		t.Errorf("refusal %+v", msg)
	}
	if !strings.Contains(msg.Reasoning, `"data":"secret"`) || !strings.Contains(msg.Reasoning, `"input":{"x":1}`) ||
		strings.Count(msg.Reasoning, `"input":{}`) != 2 {
		t.Errorf("kept %s", msg.Reasoning)
	}
	c = capture{}
	c.feed([]byte(sse(
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"No."}}`,
		`{"type":"message_delta","delta":{"stop_reason":"refusal"}}`,
	)), nil, nil)
	if msg := c.message(); msg.Content != "No." || msg.Reasoning != "" {
		t.Errorf("refusal with a text %+v", msg)
	}
}

func TestThoughtsGoBackWithWhatTheyWereSentAs(t *testing.T) {
	kept := keepThoughts([]json.RawMessage{
		json.RawMessage(`{"type":"thinking","thinking":"","signature":"s"}`),
		json.RawMessage(`{"type":"text","text":"Hi"}`),
		json.RawMessage(`{"type":"tool_use","id":"t1","name":"a","input":{"x":1}}`),
	})
	call := openai.ToolCall{ID: "t1", Function: openai.FunctionCall{Name: "a", Arguments: `{ "x": 1 }`}}
	m := openai.Message{Role: "assistant", Content: "Hi", ToolCalls: []openai.ToolCall{call}, Reasoning: kept, ReasoningField: openai.ReasoningItems}
	if got := thoughtsOf(m); len(got) != 3 {
		t.Errorf("as it was: %s", got)
	}
	changed := func(f func(m *openai.Message)) openai.Message {
		c := m
		c.ToolCalls = []openai.ToolCall{call}
		f(&c)
		return c
	}
	for name, m := range map[string]openai.Message{
		"another field": changed(func(m *openai.Message) { m.ReasoningField = "reasoning" }),
		"no JSON":       changed(func(m *openai.Message) { m.Reasoning = "[" }),
		"of another":    changed(func(m *openai.Message) { m.Reasoning = `{"claude_code":2,"content":[{}]}` }),
		"empty":         changed(func(m *openai.Message) { m.Reasoning = `{"claude_code":1,"content":[]}` }),
		"text":          changed(func(m *openai.Message) { m.Content = "Hello" }),
		"calls":         changed(func(m *openai.Message) { m.ToolCalls = nil }),
		"id":            changed(func(m *openai.Message) { m.ToolCalls[0].ID = "t2" }),
		"arguments":     changed(func(m *openai.Message) { m.ToolCalls[0].Function.Arguments = `{"x":2}` }),
		"no arguments":  changed(func(m *openai.Message) { m.ToolCalls[0].Function.Arguments = `{` }),
	} {
		if got := thoughtsOf(m); got != nil {
			t.Errorf("%s: kept %s", name, got)
		}
	}
}

func TestTranslate(t *testing.T) {
	png := openai.Image{Type: "image/png", Data: []byte("png")}
	system, frames, err := translate([]openai.Message{
		{Role: "system", Content: "one"},
		{Role: "user", Content: "", Images: []openai.Image{png}},
		{Role: "system", Content: "later"},
		{Role: "assistant"},
		{Role: "user", Content: ""},
		{Role: "assistant", Content: "calls", ToolCalls: []openai.ToolCall{
			{ID: "a", Function: openai.FunctionCall{Name: "x", Arguments: "not json"}},
			{ID: "b", Function: openai.FunctionCall{Name: "y", Arguments: "[1]"}},
			{ID: "c", Function: openai.FunctionCall{Name: "z", Arguments: `{"k":1}`}},
		}},
		{Role: "tool", ToolCallID: "a", Content: "seen", Images: []openai.Image{png}},
	})
	if err != nil || system != "one" || len(frames) != 3 {
		t.Fatalf("%q, %+v, %v", system, frames, err)
	}
	if got := string(raw(frames[0].Message.Content)); got != `[{"source":{"data":"cG5n","media_type":"image/png","type":"base64"},"type":"image"},{"text":"later","type":"text"}]` {
		t.Errorf("user %s", got)
	}
	if got := string(raw(frames[1].Message.Content)); !strings.Contains(got, `"id":"a","input":{}`) || !strings.Contains(got, `"id":"b","input":{}`) || !strings.Contains(got, `"input":{"k":1}`) {
		t.Errorf("assistant %s", got)
	}
	if got := string(raw(frames[2].Message.Content)); got != `[{"content":[{"text":"seen","type":"text"},{"source":{"data":"cG5n","media_type":"image/png","type":"base64"},"type":"image"}],"tool_use_id":"a","type":"tool_result"}]` {
		t.Errorf("tool %s", got)
	}
	_, frames, err = translate([]openai.Message{
		{Role: "user", Content: "first"},
		{Role: "user", Content: "second"},
	})
	if got := string(raw(frames[0].Message.Content)); err != nil || got != `[{"text":"first","type":"text"},{"text":"\n\nsecond","type":"text"}]` {
		t.Errorf("two messages %s, %v", got, err)
	}
	if _, _, err := translate(nil); err == nil {
		t.Error("an empty conversation")
	}
}

func TestTranslateEmptyText(t *testing.T) {
	png := openai.Image{Type: "image/png", Data: []byte("png")}
	kept := keepThoughts([]json.RawMessage{
		json.RawMessage(`{"type":"thinking","thinking":"thought","signature":"s"}`),
		json.RawMessage(`{"type":"text","text":""}`),
		json.RawMessage(`{"type":"text","text":"answer"}`),
	})
	_, frames, err := translate([]openai.Message{
		{Role: "user", Content: "first"},
		{Role: "system"},
		{Role: "user"},
		{Role: "user", Content: "second"},
		{Role: "assistant", Content: "answer", Reasoning: kept, ReasoningField: openai.ReasoningItems},
		{Role: "assistant", ToolCalls: []openai.ToolCall{{ID: "a", Function: openai.FunctionCall{Name: "x", Arguments: "{}"}}}},
		{Role: "tool", ToolCallID: "a", Images: []openai.Image{png}},
	})
	if err != nil || len(frames) != 3 {
		t.Fatalf("frames %+v, %v", frames, err)
	}
	for i, want := range []string{
		`[{"text":"first","type":"text"},{"text":"\n\nsecond","type":"text"}]`,
		`[{"type":"thinking","thinking":"thought","signature":"s"},{"type":"text","text":"answer"},{"id":"a","input":{},"name":"x","type":"tool_use"}]`,
		`[{"content":[{"source":{"data":"cG5n","media_type":"image/png","type":"base64"},"type":"image"}],"tool_use_id":"a","type":"tool_result"}]`,
	} {
		if got := string(raw(frames[i].Message.Content)); got != want {
			t.Errorf("frame %d: %s, want %s", i, got, want)
		}
	}
	// An empty tool result is valid as a string, unlike an empty text block.
	_, frames, err = translate([]openai.Message{{Role: "tool", ToolCallID: "a"}})
	if err != nil || string(raw(frames[0].Message.Content)) != `[{"content":"","tool_use_id":"a","type":"tool_result"}]` {
		t.Fatalf("empty tool result %+v, %v", frames, err)
	}
}

func TestEfforts(t *testing.T) {
	for effort, want := range map[string]string{"": "", "none": "low", "minimal": "low", "medium": "medium", "max": "max", "turbo": ""} {
		if got := effortOf(effort); got != want {
			t.Errorf("%q: %q", effort, got)
		}
	}
}

func TestSchema(t *testing.T) {
	for in, want := range map[string]string{
		``:                     `{"type":"object","properties":{}}`,
		`{"properties":{}}`:    `{"properties":{},"type":"object"}`,
		`{"type":"object"}`:    `{"type":"object"}`,
		`["not", "a", "type"]`: `{"type":"object","properties":{}}`,
	} {
		if got := string(schema(json.RawMessage(in))); got != want {
			t.Errorf("%s: %s", in, got)
		}
	}
}

func TestStabilizeLeavesWhatItDoesNotKnow(t *testing.T) {
	frames := func(roles ...string) []frame {
		var out []frame
		for _, r := range roles {
			f := frame{Type: r}
			f.Message.Role, f.Message.Content = r, []json.RawMessage{text("ouro " + r)}
			out = append(out, f)
		}
		return out
	}
	for name, c := range map[string]struct {
		messages string
		frames   []frame
		want     string
	}{
		"no messages": {`"x"`, frames("user"), `"x"`},
		"more messages": {`[{"role":"user","content":"a"},{"role":"user","content":"b"}]`, frames("user"),
			`[{"content":"a","role":"user"},{"content":"b","role":"user"}]`},
		"other roles": {`[{"role":"assistant","content":"a"}]`, frames("user"), `[{"content":"a","role":"assistant"}]`},
		"a mark moves": {`[{"role":"user","content":[{"type":"text","text":"x","cache_control":{"type":"ephemeral"}}]},{"role":"assistant","content":"kept"}]`,
			frames("user", "assistant"),
			`[{"content":[{"cache_control":{"type":"ephemeral"},"text":"ouro user","type":"text"}],"role":"user"},{"content":"kept","role":"assistant"}]`},
		"system first": {`[{"role":"system","content":"other"},{"role":"system","content":[{"type":"image"}]},{"role":"user","content":"a"},{"role":"system","content":"# Environment\nhere\nToday's date is 2026-01-02."},{"role":"system","content":"Today's date is 2026-01-02."}]`,
			frames("user"),
			`[{"content":"other","role":"system"},{"content":[{"type":"image"}],"role":"system"},{"content":[{"text":"ouro user","type":"text"}],"role":"user"},{"content":[{"text":"# Environment\nhere","type":"text"}],"role":"system"},{"content":"Today's date is 2026-01-02.","role":"system"}]`},
		"no environment": {`[{"role":"user","content":"a"},{"role":"system","content":7}]`, frames("user"),
			`[{"content":[{"text":"ouro user","type":"text"}],"role":"user"},{"content":7,"role":"system"}]`},
	} {
		fields := map[string]json.RawMessage{"messages": json.RawMessage(c.messages)}
		stabilize(fields, c.frames)
		if got := string(fields["messages"]); got != c.want {
			t.Errorf("%s:\n%s\nwant\n%s", name, got, c.want)
		}
	}
	if got := string(marked(json.RawMessage(`"text"`), json.RawMessage(`{}`))); got != `"text"` {
		t.Errorf("marked %s", got)
	}
}

func TestErrors(t *testing.T) {
	err := (&apiError{Status: 500, Message: "down"}).known()
	if err.Error() != "claude-code: HTTP 500: down" || !openai.IsUnavailable(err) {
		t.Errorf("%v", err)
	}
	var c capture
	if err := c.failure(); !strings.Contains(err.Error(), "ended before") {
		t.Errorf("%v", err)
	}
	if err := failed(nil, ""); !strings.Contains(err.Error(), "without a request") {
		t.Errorf("%v", err)
	}
}

func TestRelayGivesUpWithItsCall(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	r, err := newRelay(ctx, relayOptions{upstream: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatal(err)
	}
	defer r.close()
	r.used = true
	time.AfterFunc(20*time.Millisecond, cancel)
	if sent, err := r.result(ctx); !sent || !errors.Is(err, context.Canceled) {
		t.Errorf("%t, %v", sent, err)
	}
}
