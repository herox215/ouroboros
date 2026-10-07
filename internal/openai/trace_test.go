package openai

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// traced returns a context with a trace and the lines it was told.
func traced() (context.Context, *[]string) {
	var lines []string
	return WithTrace(WithSession(context.Background(), "conv-1"), func(format string, args ...any) {
		lines = append(lines, fmt.Sprintf(format, args...))
	}), &lines
}

// has fails unless one of lines has every one of want.
func has(t *testing.T, lines []string, want ...string) {
	t.Helper()
	for _, l := range lines {
		all := true
		for _, w := range want {
			all = all && strings.Contains(l, w)
		}
		if all {
			return
		}
	}
	t.Errorf("no line has %q in:\n%s", want, strings.Join(lines, "\n"))
}

// A trace is told the request, how far it begins like the last of its
// session, the headers of the response and what the API said besides the
// message, the usage as it was sent.
func TestTrace(t *testing.T) {
	c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Upstream", "deepseek-1")
		w.Header().Set("Set-Cookie", "secret=1")
		sse(`{"id":"cmpl-1","model":"deepseek-v4","provider":"zen","choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":1200,"prompt_cache_hit_tokens":0,"cache_read":7}}`, "[DONE]")(w, r)
	})
	ctx, lines := traced()
	tool := NewTool("read", "reads", `{"type":"object"}`)
	msgs := []Message{{Role: "system", Content: "be brief"}, {Role: "user", Content: "hi"}}
	if _, err := c.Complete(ctx, "deepseek-v4", msgs, []Tool{tool}); err != nil {
		t.Fatal(err)
	}
	has(t, *lines, "llm request:", "/chat/completions", "model deepseek-v4", "session conv-1", "2 messages", "1 tools", "stream_options true", "prompt_cache_key true", "cache_control breakpoints 0")
	has(t, *lines, "llm prefix: first request of the session")
	has(t, *lines, "llm response: HTTP 200", "X-Upstream: deepseek-1")
	has(t, *lines, `llm answered: id "cmpl-1", model "deepseek-v4", provider "zen"`, `finish_reason "stop"`, "events 2",
		`usage {"prompt_tokens":1200,"prompt_cache_hit_tokens":0,"cache_read":7}`)
	for _, l := range *lines {
		if strings.Contains(l, "secret") {
			t.Errorf("a cookie in %q", l)
		}
	}

	// Whatever is only added keeps the prefix.
	*lines = nil
	msgs = append(msgs, Message{Role: "assistant", Content: "ok"}, Message{Role: "tool", Content: "the file", ToolCallID: "c1"})
	c.Complete(ctx, "deepseek-v4", msgs, []Tool{tool})
	has(t, *lines, "llm prefix: all 2 messages of the last request are kept, 2 are new")

	// A note added to the last result of a tool breaks the prefix there, and
	// once it is gone again too.
	*lines = nil
	noted := append(msgs[:3:3], Message{Role: "tool", Content: "the file\n\n[a note]", ToolCallID: "c1"}, Message{Role: "user", Content: "go on"})
	c.Complete(ctx, "deepseek-v4", noted, []Tool{tool})
	has(t, *lines, "llm prefix: breaks at message 3 of 5", `role "tool"`, `it is 68 bytes long, from there "\\n\\n[a note]`)
	*lines = nil
	c.Complete(ctx, "deepseek-v4", append(msgs, Message{Role: "user", Content: "go on"}), []Tool{tool})
	has(t, *lines, "llm prefix: breaks at message 3 of 5", `it was 68 bytes long, from there "\\n\\n[a note]`)

	// Other tools change all.
	*lines = nil
	c.Complete(ctx, "deepseek-v4", msgs, nil)
	has(t, *lines, "the tools changed", "this request is the first 4 of the 5 messages")
}

// A response that fails is traced with its body, and a request that gets no
// response with why.
func TestTraceFailure(t *testing.T) {
	c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"message":"slow down"}}`))
	})
	ctx, lines := traced()
	if _, err := c.Complete(ctx, "gpt", nil, nil); err == nil {
		t.Fatal("no error")
	}
	has(t, *lines, "llm response: HTTP 429")
	has(t, *lines, "llm response: body", "slow down")

	*lines = nil
	if _, err := New("http://127.0.0.1:1", "").Complete(ctx, "gpt", nil, nil); err == nil {
		t.Fatal("no error")
	}
	has(t, *lines, "llm response: none after")

	// A parameter the API refuses is traced, and so is the request without it.
	*lines = nil
	c = newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if b, _ := io.ReadAll(r.Body); strings.Contains(string(b), "prompt_cache_key") {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":{"message":"unknown field prompt_cache_key"}}`))
			return
		}
		sse(`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`, "[DONE]")(w, r)
	})
	if _, err := c.Complete(ctx, "gpt", nil, nil); err != nil {
		t.Fatal(err)
	}
	has(t, *lines, "llm request: the API refused prompt_cache_key, asked again without it")
	has(t, *lines, "llm response: HTTP 200")
}

// Without a trace, nothing of the requests is kept.
func TestNoTraceKeepsNothing(t *testing.T) {
	c := newServer(t, sse(`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`, "[DONE]"))
	if _, err := c.Complete(context.Background(), "gpt", []Message{{Role: "user", Content: "hi"}}, nil); err != nil {
		t.Fatal(err)
	}
	if len(c.sent.last) != 0 {
		t.Fatalf("kept %v", c.sent.last)
	}
}

// A break far from the end of a message is shortened, a request without
// messages is the same as the last, and an answer that is not JSON tells
// nothing besides the message.
func TestTraceEdges(t *testing.T) {
	c := newServer(t, sse(`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`, "[DONE]"))
	ctx, lines := traced()
	long := strings.Repeat("x", 300)
	c.Complete(ctx, "gpt", []Message{{Role: "user", Content: "a" + long}}, nil)
	*lines = nil
	c.Complete(ctx, "gpt", []Message{{Role: "user", Content: "b" + long}}, nil)
	has(t, *lines, "llm prefix: breaks at message 0 of 1", "… (+")

	ctx, lines = traced()
	c.Complete(ctx, "gpt", nil, nil)
	*lines = nil
	c.Complete(ctx, "gpt", nil, nil)
	has(t, *lines, "all 0 messages of the last request are kept", "100% of the bytes")

	c = newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("not json"))
	})
	if _, err := c.Complete(ctx, "gpt", nil, nil); err == nil {
		t.Fatal("no error")
	}
}

// A response that breaks off before its body is read is an error.
func TestBodyCutOff(t *testing.T) {
	c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", "100")
		w.Write([]byte(`{"choices":`))
	})
	if _, err := c.Complete(context.Background(), "gpt", nil, nil); err == nil {
		t.Fatal("no error")
	}
}

// Another kind of provider tells the same lines of its own requests: it gets
// the trace of the context, compares its requests by session and tells the
// headers of its responses.
func TestTraceForOtherProviders(t *testing.T) {
	if TraceOf(context.Background()) != nil {
		t.Fatal("a context without a trace has one")
	}
	var lines []string
	tell := TraceOf(WithTrace(context.Background(), func(format string, args ...any) {
		lines = append(lines, fmt.Sprintf(format, args...))
	}))
	var sent Sent
	first := [][]byte{[]byte(`{"role":"user","content":"hi"}`)}
	if got := sent.Compare("s", "tools", first); got != "first request of the session in the trace" {
		t.Fatalf("the first request: %q", got)
	}
	next := append(first, []byte(`{"role":"assistant","content":"ho"}`))
	if got := sent.Compare("s", "tools", next); !strings.Contains(got, "all 1 messages of the last request are kept, 1 are new") {
		t.Fatalf("the next request: %q", got)
	}
	if got := sent.Compare("other", "tools", next); got != "first request of the session in the trace" {
		t.Fatalf("another session: %q", got)
	}
	resp := &http.Response{StatusCode: 200, Header: http.Header{"X-Request-Id": {"req_1"}, "Set-Cookie": {"secret"}}}
	TraceResponse(tell, resp, 1500*time.Millisecond)
	if len(lines) != 1 || lines[0] != "llm response: HTTP 200 after 1.5s, headers {X-Request-Id: req_1}" {
		t.Fatalf("told %q", lines)
	}
	if Hash("a") != hash("a") || Hash("a") == Hash("b") {
		t.Fatal("the hash of tools")
	}
}
