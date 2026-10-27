package openai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ouroboros/internal/credentials"
)

func newServer(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return New(srv.URL+"/", "sk-test")
}

func TestModelsSortedAndAuthenticated(t *testing.T) {
	c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" || r.Header.Get("Authorization") != "Bearer sk-test" {
			t.Errorf("unexpected request %s %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		w.Write([]byte(`{"data":[{"id":"b"},{"id":"a"},{"id":"c"}]}`))
	})
	got, err := c.Models(context.Background())
	if err != nil || !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestComplete(t *testing.T) {
	c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Model    string    `json:"model"`
			Messages []Message `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		if r.Method != http.MethodPost || r.URL.Path != "/chat/completions" || in.Model != "gpt" || len(in.Messages) != 1 {
			t.Errorf("unexpected request %s %s %+v", r.Method, r.URL.Path, in)
		}
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"hi"}}]}`))
	})
	got, err := c.Complete(context.Background(), "gpt", []Message{{Role: "user", Content: "hello"}}, nil)
	if err != nil || got.Content != "hi" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestNoKeySendsNoAuthorization(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h := r.Header.Get("Authorization"); h != "" {
			t.Errorf("unexpected Authorization %q", h)
		}
		w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()
	if _, err := New(srv.URL, "").Models(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestCompleteNoChoices(t *testing.T) {
	c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"choices":[]}`))
	})
	if _, err := c.Complete(context.Background(), "gpt", nil, nil); err == nil {
		t.Fatal("want error")
	}
}

func TestAPIError(t *testing.T) {
	c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"message":"bad key"}}`))
	})
	_, err := c.Models(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || !apiErr.Unauthorized() || apiErr.Message != "bad key" {
		t.Fatalf("got %v", err)
	}
	if apiErr.Error() != "openai: HTTP 401: bad key" {
		t.Fatalf("message %q", apiErr.Error())
	}
}

func TestServerErrorIsNotUnauthorized(t *testing.T) {
	c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	_, err := c.Models(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Unauthorized() {
		t.Fatalf("got %v", err)
	}
}

func TestNetworkError(t *testing.T) {
	c := New("http://127.0.0.1:1", "k")
	if _, err := c.Models(context.Background()); err == nil {
		t.Fatal("want error")
	}
}

func TestSessionAndUserAgentHeaders(t *testing.T) {
	var sessions []string
	c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		if ua := r.Header.Get("User-Agent"); ua != UserAgent {
			t.Errorf("User-Agent = %q", ua)
		}
		sessions = append(sessions, r.Header.Get("X-Opencode-Session"))
		w.Write([]byte(`{"data":[]}`))
	})
	ctx := context.Background()
	c.Models(ctx)
	c.Models(ctx)
	c.Models(WithSession(ctx, "ctx-1"))
	if sessions[0] == "" || sessions[0] != sessions[1] || sessions[2] != "ctx-1" {
		t.Fatalf("sessions = %q", sessions)
	}
}

// A tool is offered to the model as a function with its schema, as it is.
func TestNewTool(t *testing.T) {
	const schema = `{"type":"object","properties":{"peer":{"type":"string"}}}`
	tool := NewTool("ask_peer", "Asks a peer.", schema)
	got, err := json.Marshal(tool)
	want := `{"type":"function","function":{"name":"ask_peer","description":"Asks a peer.","parameters":` + schema + `}}`
	if err != nil || string(got) != want {
		t.Fatalf("got %s, %v\nwant %s", got, err, want)
	}
}

// The model gets the tools it may call, and its calls come back.
func TestCompleteWithTools(t *testing.T) {
	c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Tools []Tool `json:"tools"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		if len(in.Tools) != 1 || in.Tools[0].Type != "function" || in.Tools[0].Function.Name != "greet" || string(in.Tools[0].Function.Parameters) != `{"type":"object"}` {
			t.Errorf("unexpected tools %+v", in.Tools)
		}
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call-1","type":"function","function":{"name":"greet","arguments":"{}"}}]}}]}`))
	})
	got, err := c.Complete(context.Background(), "gpt", []Message{{Role: "user", Content: "hello"}}, []Tool{NewTool("greet", "Greets.", `{"type":"object"}`)})
	if err != nil || len(got.ToolCalls) != 1 || got.ToolCalls[0] != (ToolCall{ID: "call-1", Type: "function", Function: FunctionCall{Name: "greet", Arguments: "{}"}}) {
		t.Fatalf("got %+v, %v", got, err)
	}
}

// A completion the API refuses is the error of the API, not an empty reply.
func TestCompleteAPIError(t *testing.T) {
	c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"message":"slow down"}}`))
	})
	got, err := c.Complete(context.Background(), "gpt", []Message{{Role: "user", Content: "hello"}}, nil)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusTooManyRequests || apiErr.Message != "slow down" || apiErr.Unauthorized() {
		t.Fatalf("got %v", err)
	}
	if got.Content != "" || got.Role != "" || got.ToolCalls != nil {
		t.Fatalf("got %+v", got)
	}
}

// A request that cannot be made is not sent.
func TestRequestThatCannotBeMade(t *testing.T) {
	var asked atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		w.Write([]byte(`{"choices":[{"message":{"content":"hi"}}],"data":[]}`))
	}))
	defer srv.Close()
	ctx := context.Background()

	// A schema that is not JSON cannot be put into a request.
	_, err := New(srv.URL, "").Complete(ctx, "gpt", nil, []Tool{NewTool("greet", "Greets.", "{")})
	var marshalErr *json.MarshalerError
	if !errors.As(err, &marshalErr) {
		t.Fatalf("invalid schema: %v", err)
	}
	// Neither is there a request to an address that is none.
	_, err = New(srv.URL+"\x7f", "").Models(ctx)
	var urlErr *url.Error
	if !errors.As(err, &urlErr) || urlErr.Op != "parse" {
		t.Fatalf("invalid address: %v", err)
	}
	if n := asked.Load(); n != 0 {
		t.Fatalf("the API was asked %d times", n)
	}
}

// Both a rejected and a forbidden key are a matter of the key.
func TestUnauthorized(t *testing.T) {
	for status, want := range map[int]bool{
		http.StatusUnauthorized:        true,
		http.StatusForbidden:           true,
		http.StatusNotFound:            false,
		http.StatusPaymentRequired:     false,
		http.StatusInternalServerError: false,
	} {
		if got := (&APIError{Status: status}).Unauthorized(); got != want {
			t.Errorf("Unauthorized() with HTTP %d = %v", status, got)
		}
	}
}

// sse answers with the events, each as a line "data: EVENT" and an empty line.
func sse(events ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, e := range events {
			w.Write([]byte("data: " + e + "\n\n"))
		}
	}
}

// A completion is asked for as a stream and put together from its pieces.
func TestCompleteStreams(t *testing.T) {
	c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Stream bool `json:"stream"`
		}
		json.NewDecoder(r.Body).Decode(&in)
		if !in.Stream {
			t.Error("the completion was not asked for as a stream")
		}
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Write([]byte(": keep-alive\n\nevent: message\n" +
			`data: {"choices":[{"delta":{"role":"assistant","content":""}}]}` + "\n\n" +
			`data: {"choices":[{"delta":{"reasoning":"thinking"}}]}` + "\n\n" +
			`data:{"choices":[{"delta":{"content":"Hal"}}]}` + "\n\n" +
			`data: {"choices":[{"delta":{"content":"lo"}},{"delta":{"content":"other choice"}}]}` + "\n\n" +
			`data: {"choices":[{"delta":{},"finish_reason":"stop"}]}` + "\n\n" +
			`data: {"choices":[],"usage":{"total_tokens":3}}` + "\n\n" +
			"data: [DONE]\n\n"))
	})
	got, err := c.Complete(context.Background(), "gpt", []Message{{Role: "user", Content: "hello"}}, nil)
	if err != nil || got.Role != "assistant" || got.Content != "Hallo" || got.Reasoning != "thinking" || got.ReasoningField != "reasoning" || got.ToolCalls != nil {
		t.Fatalf("got %+v, %v", got, err)
	}
}

// What the model writes comes to the sink of ctx in the pieces the stream
// brings it in, before the message holds the whole of it.
func TestCompleteStreamsAnswer(t *testing.T) {
	c := newServer(t, sse(
		`{"choices":[{"delta":{"role":"assistant","content":"Hel"}}]}`,
		`{"choices":[{"delta":{"content":"lo"}}]}`,
		`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
	))
	var pieces []string
	ctx := WithAnswering(context.Background(), func(text string) { pieces = append(pieces, text) })
	got, err := c.Complete(ctx, "gpt", []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil || got.Content != "Hello" {
		t.Fatalf("got %+v, %v", got, err)
	}
	if len(pieces) != 2 || pieces[0] != "Hel" || pieces[1] != "lo" {
		t.Errorf("the sink was told %q", pieces)
	}
}

// What the model thinks comes to the sink of ctx in the pieces the stream
// brings it in; a piece with nothing of it tells the sink nothing.
func TestCompleteStreamsThought(t *testing.T) {
	c := newServer(t, sse(
		`{"choices":[{"delta":{"role":"assistant","reasoning_content":"I will "}}]}`,
		`{"choices":[{"delta":{"reasoning_content":"greet."}}]}`,
		`{"choices":[{"delta":{"content":"Hello"},"finish_reason":"stop"}]}`,
	))
	var pieces []string
	ctx := WithThought(context.Background(), func(text string) { pieces = append(pieces, text) })
	got, err := c.Complete(ctx, "gpt", []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil || got.Content != "Hello" || got.Reasoning != "I will greet." {
		t.Fatalf("got %+v, %v", got, err)
	}
	if len(pieces) != 2 || pieces[0] != "I will " || pieces[1] != "greet." {
		t.Errorf("the sink was told %q", pieces)
	}
}

// What the model thought comes in pieces in one of several fields, and an API
// that sends it in two of them at once has it counted once.
func TestCompleteStreamsReasoning(t *testing.T) {
	for _, tt := range []struct {
		name, field string
		h           http.HandlerFunc
	}{
		{"reasoning_content", "reasoning_content", sse(
			`{"choices":[{"delta":{"reasoning_content":"I will ","reasoning":"I will "}}]}`,
			`{"choices":[{"delta":{"reasoning_content":"read it."}}]}`,
			`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`)},
		{"reasoning", "reasoning", sse(
			`{"choices":[{"delta":{"reasoning":"I will "}}]}`,
			`{"choices":[{"delta":{"reasoning":"read it."}}]}`,
			`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`)},
		{"reasoning_text", "reasoning_text", sse(
			`{"choices":[{"delta":{"reasoning_text":"I will "}}]}`,
			`{"choices":[{"delta":{"reasoning_text":"read it."}}]}`,
			`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`)},
	} {
		got, err := newServer(t, tt.h).Complete(context.Background(), "gpt", nil, nil)
		if err != nil || got.Content != "ok" || got.Reasoning != "I will read it." || got.ReasoningField != tt.field {
			t.Errorf("%s: got %+v, %v", tt.name, got, err)
		}
	}
}

// An answer in one piece has its reasoning too.
func TestCompleteReasoningInOnePiece(t *testing.T) {
	c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"hi","reasoning_text":"greet"}}]}`))
	})
	got, err := c.Complete(context.Background(), "gpt", nil, nil)
	if err != nil || got.Content != "hi" || got.Reasoning != "greet" || got.ReasoningField != "reasoning_text" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

// A message in an answer in one piece that does not fit is an error.
func TestCompleteMessageThatIsNoMessage(t *testing.T) {
	c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"choices":[{"message":{"role":1}}]}`))
	})
	if got, err := c.Complete(context.Background(), "gpt", nil, nil); err == nil {
		t.Fatalf("got %+v, want error", got)
	}
}

// What the model thought goes back to the API in the field it came in, and a
// message without reasoning has none of the fields.
func TestReasoningIsSentBack(t *testing.T) {
	for _, tt := range []struct {
		msg  Message
		want string
	}{
		{Message{Role: "user", Content: "hi"}, `{"role":"user","content":"hi"}`},
		{Message{Role: "assistant", Reasoning: "r", ReasoningField: "reasoning_content"}, `{"role":"assistant","content":"","reasoning_content":"r"}`},
		{Message{Role: "assistant", Reasoning: "r", ReasoningField: "reasoning"}, `{"role":"assistant","content":"","reasoning":"r"}`},
		{Message{Role: "assistant", Reasoning: "r", ReasoningField: "reasoning_text"}, `{"role":"assistant","content":"","reasoning_text":"r"}`},
		{Message{Role: "assistant", Reasoning: "r"}, `{"role":"assistant","content":"","reasoning":"r"}`},
		{Message{Role: "assistant", Reasoning: "r", ReasoningField: "thoughts"}, `{"role":"assistant","content":"","reasoning":"r"}`},
		// What another kind of provider kept of the thoughts of its model is
		// not sent to an OpenAI-compatible API.
		{Message{Role: "assistant", Content: "a", Reasoning: `[{"type":"reasoning"}]`, ReasoningField: ReasoningItems}, `{"role":"assistant","content":"a"}`},
	} {
		var sent string
		c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
			var in struct {
				Messages []json.RawMessage `json:"messages"`
			}
			json.NewDecoder(r.Body).Decode(&in)
			sent = string(in.Messages[0])
			w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
		})
		if _, err := c.Complete(context.Background(), "gpt", []Message{tt.msg}, nil); err != nil || sent != tt.want {
			t.Errorf("%+v: sent %s, %v; want %s", tt.msg, sent, err, tt.want)
		}
	}
}

// Tool calls come in pieces too: by their index, or without one, where a
// piece with an ID starts the next call.
func TestCompleteStreamsToolCalls(t *testing.T) {
	want := []ToolCall{
		{ID: "a", Type: "function", Function: FunctionCall{Name: "read", Arguments: `{"path": "/x"}`}},
		{ID: "b", Type: "function", Function: FunctionCall{Name: "list", Arguments: `{}`}},
	}
	for name, h := range map[string]http.HandlerFunc{
		"by index": sse(
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"a","type":"function","function":{"name":"read"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\": "}},{"index":1,"id":"b","function":{"name":"list","arguments":"{"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":1,"type":"function"}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"/x\"}"}},{"index":1,"function":{"arguments":"}"}}]}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`),
		"without index": sse(
			`{"choices":[{"delta":{"tool_calls":[{"id":"a","type":"function","function":{"name":"read","arguments":"{\"path\": "}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"function":{"arguments":"\"/x\"}"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"id":"b","function":{"name":"list","arguments":"{}"}}]}}]}`,
			"[DONE]"),
	} {
		c := newServer(t, h)
		if got, err := c.Complete(context.Background(), "gpt", nil, nil); err != nil || !slices.Equal(got.ToolCalls, want) {
			t.Errorf("%s without a sink: got %+v, %v", name, got, err)
		}
		var written []int
		ctx := WithCalling(context.Background(), func(bytes int) { written = append(written, bytes) })
		got, err := c.Complete(ctx, "gpt", nil, nil)
		if err != nil || got.Content != "" || !slices.Equal(got.ToolCalls, want) {
			t.Errorf("%s: got %+v, %v", name, got, err)
		}
		// How much of the calls is written grows with every piece of names
		// and arguments of all of them, and only with such a piece.
		grows := len(written) >= 3
		for i := 1; i < len(written); i++ {
			grows = grows && written[i] > written[i-1]
		}
		if all := len("read") + len(`{"path": "/x"}`) + len("list") + len(`{}`); !grows || written[len(written)-1] != all {
			t.Errorf("%s: the sink was told %v", name, written)
		}
	}
}

// A stream that does not tell a whole answer is an error, not a reply.
func TestCompleteStreamFails(t *testing.T) {
	for name, tt := range map[string]struct {
		h    http.HandlerFunc
		want string
	}{
		"error":      {sse(`{"choices":[{"delta":{"content":"Hal"}}]}`, `{"error":{"message":"overloaded"}}`), "openai: the stream broke off: overloaded"},
		"no JSON":    {sse(`{"choices":`), "openai: the stream has an event that is no JSON"},
		"no choices": {sse(`{"choices":[]}`, "[DONE]"), "openai: response has no choices"},
		"nothing":    {sse(), "openai: response has no choices"},
		"bad index":  {sse(`{"choices":[{"delta":{"tool_calls":[{"index":2,"id":"a"}]}}]}`), "openai: the stream has a tool call with index 2"},
		"too big":    {sse(`{"choices":[{"delta":{"content":"` + strings.Repeat("x", maxEvent) + `"}}]}`), "token too long"},
		"cut off": {func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Content-Length", "1000")
			w.Write([]byte(`data: {"choices":[{"delta":{"content":"Hal"}}]}` + "\n\n"))
		}, "unexpected EOF"},
	} {
		got, err := newServer(t, tt.h).Complete(context.Background(), "gpt", nil, nil)
		if err == nil || !strings.Contains(err.Error(), tt.want) || got.Content != "" {
			t.Errorf("%s: got %+v, %v; want %q", name, got, err, tt.want)
		}
	}
}

// A model may think for long while it sends something; an API that sends
// nothing for the timeout is given up, before and within the stream.
func TestCompleteIsGivenUpWhenNothingComes(t *testing.T) {
	defer func(d time.Duration) { timeout = d }(timeout)
	timeout = 100 * time.Millisecond
	release := make(chan struct{})
	defer close(release)
	slow := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for range 5 { // 250 ms in all, never 100 ms without data
			w.Write([]byte(": thinking\n\n"))
			w.(http.Flusher).Flush()
			time.Sleep(50 * time.Millisecond)
		}
		w.Write([]byte(`data: {"choices":[{"delta":{"content":"done"},"finish_reason":"stop"}]}` + "\n\n"))
	}
	if got, err := newServer(t, slow).Complete(context.Background(), "gpt", nil, nil); err != nil || got.Content != "done" {
		t.Errorf("slow: got %+v, %v", got, err)
	}
	for name, h := range map[string]http.HandlerFunc{
		"silent": func(http.ResponseWriter, *http.Request) { <-release },
		"stuck": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Write([]byte(`data: {"choices":[{"delta":{"content":"Hal"}}]}` + "\n\n"))
			w.(http.Flusher).Flush()
			<-release
		},
	} {
		_, err := newServer(t, h).Complete(context.Background(), "gpt", nil, nil)
		// The error is why it was given up, and nothing of how the request
		// broke off because of it.
		if !errors.Is(err, context.DeadlineExceeded) || err.Error() != "openai: no data from the API for 100ms: context deadline exceeded" {
			t.Errorf("%s: %v", name, err)
		}
	}
	// One who gives up the answer is told so, not that the API was silent.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := newServer(t, slow).Complete(ctx, "gpt", nil, nil); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled: %v", err)
	}
}

// An answer in one piece that is no JSON is an error.
func TestAnswerThatIsNoJSON(t *testing.T) {
	c := newServer(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("<html>")) })
	if _, err := c.Complete(context.Background(), "gpt", nil, nil); err == nil {
		t.Error("completion: want error")
	}
	if _, err := c.Models(context.Background()); err == nil {
		t.Error("models: want error")
	}
}

// The provider is the name of the service the base URL points at.
func TestProvider(t *testing.T) {
	for _, test := range []struct{ url, want string }{
		{"https://igate.service.example.com/v1", "iGate"},
		{"https://api.openai.com/v1", "OpenAI"},
		{"https://openrouter.ai/api/v1", "OpenRouter"},
		{"http://localhost:11434/v1", "localhost"},
		{"http://127.0.0.1:1234/v1", "localhost"},
		{"", ""},
		{"llama.cpp", ""},
		{"http://[::1", ""},
		{"https://ollama.example.com/v1", "ollama"},
	} {
		if got := Provider(test.url); got != test.want {
			t.Errorf("Provider(%q) = %q, want %q", test.url, got, test.want)
		}
	}
}

// The usage of a call comes in the stream after its choice, or in one piece
// with the answer, the cached tokens in the field of OpenAI or of DeepSeek.
func TestCompleteTellsTheUsage(t *testing.T) {
	for name, tc := range map[string]struct {
		h    http.HandlerFunc
		want Usage
	}{
		"stream": {sse(`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":1200,"prompt_tokens_details":{"cached_tokens":1024}}}`, "[DONE]"), Usage{Prompt: 1200, Cached: 1024}},
		"in the last event": {sse(`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":900,"prompt_cache_hit_tokens":640}}`), Usage{Prompt: 900, Cached: 640}},
		"none":              {sse(`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`, "[DONE]"), Usage{}},
		"in one piece": {func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":50}}`))
		}, Usage{Prompt: 50}},
	} {
		got, err := newServer(t, tc.h).Complete(context.Background(), "gpt", nil, nil)
		if err != nil || got.Content != "ok" || got.Usage != tc.want {
			t.Errorf("%s: got %+v, %v; want the usage %+v", name, got, err, tc.want)
		}
	}
}

// redirect sends every request to the server at to, whatever its URL.
type redirect struct{ to string }

func (r redirect) RoundTrip(req *http.Request) (*http.Response, error) {
	u, _ := url.Parse(r.to)
	req.URL.Scheme, req.URL.Host = u.Scheme, u.Host
	return http.DefaultTransport.RoundTrip(req)
}

// The usage is asked for with stream_options, and every API gets the session
// as prompt_cache_key, OpenAI as a gateway; an API that refuses one of them
// is asked without it from then on.
func TestCompleteAsksForUsageAndCache(t *testing.T) {
	var bodies []map[string]any
	var refuse []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		for _, name := range refuse {
			if _, ok := body[name]; ok {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(`{"error":{"message":"Unrecognized request argument supplied: ` + name + `"}}`))
				return
			}
		}
		sse(`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`, "[DONE]")(w, r)
	}))
	defer srv.Close()
	openAI := New(DefaultBaseURL, "sk")
	openAI.http = &http.Client{Transport: redirect{srv.URL}}
	if _, err := openAI.Complete(WithSession(context.Background(), "conv-1"), "gpt", nil, nil); err != nil {
		t.Fatal(err)
	}
	other := New(srv.URL, "sk")
	if _, err := other.Complete(WithSession(context.Background(), "conv-1"), "gpt", nil, nil); err != nil {
		t.Fatal(err)
	}
	for i, b := range bodies {
		if b["prompt_cache_key"] != "conv-1" || b["stream_options"] == nil {
			t.Fatalf("body %d: %v", i, b)
		}
	}
	refuse, bodies = []string{"stream_options", "prompt_cache_key"}, nil
	for range 2 {
		if got, err := other.Complete(context.Background(), "gpt", nil, nil); err != nil || got.Content != "ok" {
			t.Fatalf("refused: %+v, %v", got, err)
		}
	}
	if len(bodies) != 4 || bodies[0]["stream_options"] == nil || bodies[1]["stream_options"] != nil || bodies[1]["prompt_cache_key"] == nil ||
		bodies[2]["prompt_cache_key"] != nil || bodies[3]["stream_options"] != nil || bodies[3]["prompt_cache_key"] != nil {
		t.Fatalf("after the refusals: %v", bodies)
	}
	// An error that names no parameter sent is no refusal of one.
	refuse, bodies = []string{"model"}, nil
	if _, err := other.Complete(context.Background(), "gpt", nil, nil); err == nil || len(bodies) != 1 {
		t.Fatalf("%v, %d requests", err, len(bodies))
	}
}

// A Claude model gets the first system message and the last 3 messages with
// content as cache_control breakpoints; another model gets none, and an API
// that refuses them is asked without them from then on.
func TestCompleteSetsBreakpointsForClaude(t *testing.T) {
	var bodies []string
	refuse := false
	c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		if refuse && strings.Contains(string(b), "cache_control") {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":{"message":"messages.1.content.0.cache_control: Extra inputs are not permitted"}}`))
			return
		}
		sse(`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`, "[DONE]")(w, r)
	})
	call := ToolCall{ID: "c1", Type: "function", Function: FunctionCall{Name: "read", Arguments: "{}"}}
	msgs := []Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "u1"}, {Role: "assistant", Content: "a1"},
		{Role: "user", Content: "u2"}, {Role: "assistant", ToolCalls: []ToolCall{call}}, {Role: "tool", ToolCallID: "c1", Content: "r1"},
		{Role: "user", Content: "note"}}
	before := slices.Clone(msgs)
	for _, model := range []string{"anthropic/claude-sonnet-4.5", "deepseek-v4"} {
		if _, err := c.Complete(context.Background(), model, msgs, nil); err != nil {
			t.Fatal(err)
		}
	}
	part := func(text string) string {
		return `"content":[{"type":"text","text":"` + text + `","cache_control":{"type":"ephemeral"}}]`
	}
	for _, text := range []string{"sys", "u2", "r1", "note"} {
		if !strings.Contains(bodies[0], `{"role":`) || !strings.Contains(bodies[0], part(text)) {
			t.Errorf("no breakpoint at %q: %s", text, bodies[0])
		}
	}
	if strings.Count(bodies[0], "cache_control") != 4 || !strings.Contains(bodies[0], `{"role":"user","content":"u1"}`) ||
		!strings.Contains(bodies[0], `"tool_call_id":"c1"`) {
		t.Errorf("Claude: %s", bodies[0])
	}
	if strings.Contains(bodies[1], "cache_control") {
		t.Errorf("another model: %s", bodies[1])
	}
	if !reflect.DeepEqual(msgs, before) {
		t.Error("the messages were changed")
	}

	refuse, bodies = true, nil
	for range 2 {
		if _, err := c.Complete(context.Background(), "claude-opus-4", msgs, nil); err != nil {
			t.Fatal(err)
		}
	}
	if len(bodies) != 3 || !strings.Contains(bodies[0], "cache_control") || strings.Contains(bodies[1], "cache_control") ||
		strings.Contains(bodies[2], "cache_control") || !strings.Contains(bodies[1], `{"role":"user","content":"note"}`) {
		t.Fatalf("after the refusal: %q", bodies)
	}
}

// A conversation without a system message, or one whose system message is
// empty, has breakpoints at its last messages only.
func TestBreakpoints(t *testing.T) {
	for name, tc := range map[string]struct {
		msgs []Message
		want []bool
	}{
		"no system":    {[]Message{{Role: "user", Content: "a"}, {Role: "user", Content: "b"}}, []bool{true, true}},
		"empty system": {[]Message{{Role: "system"}, {Role: "user", Content: "a"}}, []bool{false, true}},
		"many": {[]Message{{Role: "system", Content: "s"}, {Role: "user", Content: "a"}, {Role: "user", Content: "b"},
			{Role: "user", Content: "c"}, {Role: "user", Content: "d"}}, []bool{true, false, true, true, true}},
		"none": {nil, []bool{}},
	} {
		got, n := breakpoints(wireOf(tc.msgs))
		marks := []bool{}
		count := 0
		for _, m := range got {
			marks = append(marks, m.breakpoint)
			if m.breakpoint {
				count++
			}
		}
		if !slices.Equal(marks, tc.want) || n != count {
			t.Errorf("%s: %v, %d", name, marks, n)
		}
	}
}

// The stream is read until the choice is finished and the usage came, in
// whatever order, and no further.
func TestCompleteReadsTheStreamUntilTheUsage(t *testing.T) {
	for name, h := range map[string]http.HandlerFunc{
		"usage first": sse(`{"choices":[{"delta":{"content":"o"}}],"usage":{"prompt_tokens":5}}`,
			`{"choices":[{"delta":{"content":"k"},"finish_reason":"stop"}]}`, "[DONE]"),
		"then nothing more": sse(`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5}}`,
			"no event of the API"),
	} {
		got, err := newServer(t, h).Complete(context.Background(), "gpt", nil, nil)
		if err != nil || got.Content != "ok" || got.Usage.Prompt != 5 {
			t.Errorf("%s: %+v, %v", name, got, err)
		}
	}
}

// What another kind of provider kept of the thoughts of its model is kept
// with the message as it is, in a field of its own, and read back as that.
func TestReasoningItemsAreKept(t *testing.T) {
	msg := Message{Role: "assistant", Content: "a", Reasoning: `[{"type":"reasoning"}]`, ReasoningField: ReasoningItems}
	b, err := json.Marshal(msg)
	if err != nil || string(b) != `{"role":"assistant","content":"a","reasoning_items":"[{\"type\":\"reasoning\"}]"}` {
		t.Fatalf("written as %s, %v", b, err)
	}
	var back Message
	if err := json.Unmarshal(b, &back); err != nil || back.Reasoning != msg.Reasoning || back.ReasoningField != ReasoningItems {
		t.Fatalf("read back as %+v, %v", back, err)
	}
	// The messages of the caller are not changed by leaving it out.
	msgs := []Message{{Role: "user", Content: "hi"}, msg}
	if got := own(msgs); got[1].Reasoning != "" || got[1].ReasoningField != "" || got[1].Content != "a" || msgs[1].Reasoning == "" {
		t.Fatalf("own = %+v, of %+v", got, msgs)
	}
	plain := []Message{{Role: "assistant", Reasoning: "r", ReasoningField: "reasoning"}}
	if got := own(plain); &got[0] != &plain[0] {
		t.Fatal("messages without foreign thoughts are copied")
	}
}

func TestSessionOf(t *testing.T) {
	if got := SessionOf(context.Background()); got != "" {
		t.Fatalf("a context without a session has %q", got)
	}
	if got := SessionOf(WithSession(context.Background(), "conv-1")); got != "conv-1" {
		t.Fatalf("SessionOf = %q", got)
	}
}

// An access is kept as its endpoint, which is shown, and its key, which is
// a secret, and read back as both.
func TestCredentials(t *testing.T) {
	c := Credentials(" http://localhost:11434/v1/ ", "sk-1")
	if c.Title != "localhost" || c.About != "http://localhost:11434/v1" || string(c.Data) != `{"base_url":"http://localhost:11434/v1"}` || string(c.Secret) != `{"api_key":"sk-1"}` {
		t.Fatalf("kept as %+v", c)
	}
	if got := c.Secrets(); len(got) != 1 || got[0] != "sk-1" {
		t.Fatalf("its secrets: %q", got)
	}
	if url, key, err := Saved(c); err != nil || url != "http://localhost:11434/v1" || key != "sk-1" {
		t.Fatalf("Saved = %q, %q, %v", url, key, err)
	}
	// An access without a key has no secret.
	open := Credentials("http://x/v1", "")
	if open.Secret != nil {
		t.Fatalf("an access without a key keeps %s", open.Secret)
	}
	if url, key, err := Saved(open); err != nil || url != "http://x/v1" || key != "" {
		t.Fatalf("Saved = %q, %q, %v", url, key, err)
	}
	// What is no access of this kind.
	for _, c := range []credentials.Credentials{{}, {Data: []byte(`{"base_url":""}`)}, {Data: []byte(`{`)}, {Data: open.Data, Secret: []byte(`[1]`)}} {
		if _, _, err := Saved(c); err == nil {
			t.Errorf("Saved(%+v) should fail", c)
		}
	}
}

// A stream ends with [DONE]: an event after it is not read.
func TestCompleteStopsAtDone(t *testing.T) {
	c := newServer(t, sse(`{"choices":[{"delta":{"content":"ok"}}]}`, "[DONE]", `{"choices":[{"delta":{"content":" and more"}}]}`))
	if got, err := c.Complete(context.Background(), "gpt", nil, nil); err != nil || got.Content != "ok" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

// An answer that is complete is given, even when the call is ended right
// after it.
func TestCompleteEndedAfterTheAnswer(t *testing.T) {
	c := newServer(t, sse(`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`, "[DONE]"))
	ctx, end := context.WithCancelCause(context.Background())
	defer end(nil)
	ctx = WithTrace(ctx, func(format string, _ ...any) {
		if strings.HasPrefix(format, "llm answered") {
			end(errors.New("ended"))
		}
	})
	if got, err := c.Complete(ctx, "gpt", nil, nil); err != nil || got.Content != "ok" {
		t.Fatalf("got %+v, %v", got, err)
	}
}

// The pictures of the results of tools go in a message of the user right
// after the last of them, as the API takes a result only as text; a message
// of the user has its own pictures as parts. Before a breakpoint they are
// parts as well.
func TestCompleteSendsImages(t *testing.T) {
	var bodies []string
	c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		sse(`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`, "[DONE]")(w, r)
	})
	png := Image{Type: "image/png", Data: []byte("p")}
	jpeg := Image{Type: "image/jpeg", Data: []byte("j")}
	calls := []ToolCall{{ID: "c1", Type: "function", Function: FunctionCall{Name: "read", Arguments: "{}"}},
		{ID: "c2", Type: "function", Function: FunctionCall{Name: "read", Arguments: "{}"}}}
	msgs := []Message{{Role: "user", Content: "look", Images: []Image{jpeg}}, {Role: "assistant", ToolCalls: calls},
		{Role: "tool", ToolCallID: "c1", Content: "r1", Images: []Image{png}}, {Role: "tool", ToolCallID: "c2", Content: "r2", Images: []Image{jpeg}},
		{Role: "user", Content: "note"}}
	before := slices.Clone(msgs)
	for _, model := range []string{"gpt-5", "claude-sonnet-4.5"} {
		if _, err := c.Complete(context.Background(), model, msgs, nil); err != nil {
			t.Fatal(err)
		}
	}
	var sent struct{ Messages []json.RawMessage }
	json.Unmarshal([]byte(bodies[0]), &sent)
	want := []string{
		`{"role":"user","content":[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,ag=="}}]}`,
		`{"role":"tool","content":"r1","tool_call_id":"c1"}`,
		`{"role":"tool","content":"r2","tool_call_id":"c2"}`,
		`{"role":"user","content":[{"type":"text","text":"[The pictures the tools above returned.]"},` +
			`{"type":"image_url","image_url":{"url":"data:image/png;base64,cA=="}},{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,ag=="}}]}`,
		`{"role":"user","content":"note"}`,
	}
	if len(sent.Messages) != 6 {
		t.Fatalf("sent %s", bodies[0])
	}
	for i, j := range []int{0, 2, 3, 4, 5} {
		if got := string(sent.Messages[j]); got != want[i] {
			t.Errorf("message %d:\n%s\nwant\n%s", j, got, want[i])
		}
	}
	if !strings.Contains(bodies[1], `"text":"[The pictures the tools above returned.]","cache_control":{"type":"ephemeral"}},{"type":"image_url"`) {
		t.Errorf("a breakpoint with pictures: %s", bodies[1])
	}
	if !reflect.DeepEqual(msgs, before) {
		t.Error("the messages were changed")
	}
}

// The pictures of results at the end of a conversation follow them all the
// same, and a conversation without them stays as it is.
func TestShown(t *testing.T) {
	png := Image{Type: "image/png", Data: []byte("p")}
	msgs := []Message{{Role: "user", Content: "look", Images: []Image{png}}, {Role: "tool", Content: "r", Images: []Image{png}}}
	got := shown(msgs)
	if len(got) != 3 || got[1].Images != nil || got[2].Role != "user" || len(got[2].Images) != 1 || len(got[0].Images) != 1 {
		t.Fatalf("shown %+v", got)
	}
	plain := []Message{{Role: "user", Content: "look"}}
	if got := shown(plain); &got[0] != &plain[0] {
		t.Fatal("a conversation without pictures should stay as it is")
	}
}

// A message is written without its pictures where it is kept, and read
// back as it was, but for them.
func TestImagesAreNotKept(t *testing.T) {
	msgs := []Message{{Role: "user", Content: "look", Images: []Image{{Type: "image/png", Data: []byte("p")}}}}
	b, err := json.Marshal(msgs)
	if err != nil || string(b) != `[{"role":"user","content":"look"}]` {
		t.Fatalf("kept %s, %v", b, err)
	}
	var back []Message
	if err := json.Unmarshal(b, &back); err != nil || len(back) != 1 || back[0].Content != "look" {
		t.Fatalf("read back %+v, %v", back, err)
	}
}
