package openai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"/x\"}"}},{"index":1,"function":{"arguments":"}"}}]}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`),
		"without index": sse(
			`{"choices":[{"delta":{"tool_calls":[{"id":"a","type":"function","function":{"name":"read","arguments":"{\"path\": "}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"function":{"arguments":"\"/x\"}"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"id":"b","function":{"name":"list","arguments":"{}"}}]}}]}`,
			"[DONE]"),
	} {
		got, err := newServer(t, h).Complete(context.Background(), "gpt", nil, nil)
		if err != nil || got.Content != "" || !slices.Equal(got.ToolCalls, want) {
			t.Errorf("%s: got %+v, %v", name, got, err)
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
		if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "no data from the API for 100ms") {
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
			`{"choices":[],"usage":{"prompt_tokens":1200,"prompt_tokens_details":{"cached_tokens":1024}}}`, "[DONE]"), Usage{1200, 1024}},
		"in the last event": {sse(`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":900,"prompt_cache_hit_tokens":640}}`), Usage{900, 640}},
		"none":              {sse(`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`, "[DONE]"), Usage{}},
		"in one piece": {func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":50}}`))
		}, Usage{50, 0}},
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

// The usage is asked for with stream_options, and OpenAI gets the session as
// prompt_cache_key; an API that refuses stream_options is asked without them
// from then on.
func TestCompleteAsksForUsageAndCache(t *testing.T) {
	var bodies []map[string]any
	refuse := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		if _, ok := body["stream_options"]; ok && refuse {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":{"message":"Unrecognized request argument supplied: stream_options"}}`))
			return
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
	if bodies[0]["prompt_cache_key"] != "conv-1" || bodies[0]["stream_options"] == nil ||
		bodies[1]["prompt_cache_key"] != nil || bodies[1]["stream_options"] == nil {
		t.Fatalf("bodies %v", bodies)
	}
	refuse, bodies = true, nil
	for range 2 {
		if got, err := other.Complete(context.Background(), "gpt", nil, nil); err != nil || got.Content != "ok" {
			t.Fatalf("refused: %+v, %v", got, err)
		}
	}
	if len(bodies) != 3 || bodies[0]["stream_options"] == nil || bodies[1]["stream_options"] != nil || bodies[2]["stream_options"] != nil {
		t.Fatalf("after the refusal: %v", bodies)
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
