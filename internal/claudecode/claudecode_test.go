package claudecode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"ouroboros/internal/llm"
	"ouroboros/internal/llm/llmtest"
	"ouroboros/internal/openai"
)

// api is a fake Messages API: it answers every request with status and
// stream, and keeps what it was sent.
type api struct {
	*httptest.Server
	mu      sync.Mutex
	status  int
	stream  string
	hang    bool // it sends the headers and then nothing
	cut     bool // it breaks the connection off in the middle of the stream
	bodies  []map[string]json.RawMessage
	headers []http.Header
	queries []string
}

func newAPI(t *testing.T) *api {
	t.Helper()
	a := &api{status: http.StatusOK, stream: answer}
	a.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		json.NewDecoder(r.Body).Decode(&body)
		a.mu.Lock()
		a.bodies = append(a.bodies, body)
		a.headers = append(a.headers, r.Header.Clone())
		a.queries = append(a.queries, r.URL.RawQuery)
		status, stream, hang, cut := a.status, a.stream, a.hang, a.cut
		a.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Request-Id", "req_1")
		if status/100 == 3 {
			w.Header().Set("Location", "/elsewhere")
		}
		w.WriteHeader(status)
		if hang || cut {
			w.(http.Flusher).Flush()
		}
		if hang {
			<-r.Context().Done()
			return
		}
		if cut {
			io.WriteString(w, stream[:len(stream)/2])
			w.(http.Flusher).Flush()
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
			return
		}
		io.WriteString(w, stream)
	}))
	t.Cleanup(a.Close)
	return a
}

func (a *api) set(f func(a *api)) {
	a.mu.Lock()
	defer a.mu.Unlock()
	f(a)
}

func (a *api) body(i int) map[string]json.RawMessage {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.bodies[i]
}

// sse is a stream of the events.
func sse(events ...string) string {
	var b strings.Builder
	for _, e := range events {
		var head struct {
			Type string `json:"type"`
		}
		json.Unmarshal([]byte(e), &head)
		fmt.Fprintf(&b, "event: %s\ndata: %s\n\n", head.Type, e)
	}
	return b.String()
}

// answer is a whole answer: the model thinks, writes and calls a tool.
var answer = sse(
	`{"type":"message_start","message":{"id":"msg_1","model":"claude-test","usage":{"input_tokens":10,"cache_creation_input_tokens":20,"cache_read_input_tokens":300,"output_tokens":1}}}`,
	`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"let me "}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"see"}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"c2lnbmVk"}}`,
	`{"type":"content_block_stop","index":0}`,
	`{"type":"ping"}`,
	`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
	`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Hello, "}}`,
	`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"world."}}`,
	`{"type":"content_block_stop","index":1}`,
	`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_1","name":"read_file","input":{},"caller":{"type":"direct"}}}`,
	`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":""}}`,
	`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"path\": "}}`,
	`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"\"a.txt\"}"}}`,
	`{"type":"content_block_stop","index":2}`,
	`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":42}}`,
	`{"type":"message_stop"}`,
)

// reply is a whole answer of text alone.
var reply = sse(
	`{"type":"message_start","message":{"id":"msg_2","model":"claude-test","usage":{"input_tokens":5}}}`,
	`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
	`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Done."}}`,
	`{"type":"content_block_stop","index":0}`,
	`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`,
	`{"type":"message_stop"}`,
)

// fake is a fake CLI, and the fake API it reaches through the relay.
type fake struct {
	t   *testing.T
	o   Options
	api *api
	log string
	c   llm.Credentials
}

// newFake returns a fake CLI with the variables env, see fakeClaude.
func newFake(t *testing.T, env ...string) *fake {
	t.Helper()
	f := &fake{t: t, api: newAPI(t), log: t.TempDir()}
	f.o = Options{Command: os.Args[0], Dir: filepath.Join(t.TempDir(), "claude-code"), Upstream: f.api.URL + "/",
		Env: append(os.Environ(), append([]string{fakeEnv + "=1", "FAKE_LOG=" + f.log, "ANTHROPIC_API_KEY=sk-ant-not-sent"}, env...)...)}
	f.c = keep(account{Command: os.Args[0], ConfigDir: "/config/of/claude"})
	return f
}

func (f *fake) access() llm.Access { return llm.Unsaved(&f.c) }

func (f *fake) model() *client { return &client{o: f.o, access: f.access()} }

// args are the arguments of the nth run of the CLI, from 1 on.
func (f *fake) args(n int) []string {
	var args []string
	b, _ := os.ReadFile(filepath.Join(f.log, fmt.Sprintf("args-%d.json", n)))
	json.Unmarshal(b, &args)
	return args
}

// env is the environment of the nth run of the CLI.
func (f *fake) env(n int) map[string]string {
	b, _ := os.ReadFile(filepath.Join(f.log, fmt.Sprintf("env-%d.txt", n)))
	return environ(strings.Split(string(b), "\n"))
}

// stdin are the lines the CLI was given, of all runs.
func (f *fake) stdin() []map[string]json.RawMessage {
	b, _ := os.ReadFile(filepath.Join(f.log, "stdin.jsonl"))
	var lines []map[string]json.RawMessage
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var m map[string]json.RawMessage
		json.Unmarshal([]byte(line), &m)
		lines = append(lines, m)
	}
	return lines
}

func value(args []string, flag string) string {
	i := slices.Index(args, flag)
	if i < 0 || i+1 >= len(args) {
		return ""
	}
	return args[i+1]
}

var tools = []openai.Tool{openai.NewTool("read_file", "Reads a file.", `{"type":"object","properties":{"path":{"type":"string"}}}`)}

func TestCompleteTurn(t *testing.T) {
	f := newFake(t)
	var answering []string
	var calling []int
	var traced []string
	ctx := openai.WithAnswering(context.Background(), func(s string) { answering = append(answering, s) })
	ctx = openai.WithCalling(ctx, func(n int) { calling = append(calling, n) })
	ctx = openai.WithEffort(ctx, "high")
	ctx = openai.WithSession(ctx, "session-1")
	ctx = openai.WithTrace(ctx, func(format string, args ...any) { traced = append(traced, fmt.Sprintf(format, args...)) })
	msgs := []openai.Message{{Role: "system", Content: "You are ouro."}, {Role: "system", Content: "Be brief."}, {Role: "user", Content: "Read a.txt"}}
	msg, err := f.model().Complete(ctx, "claude-test", msgs, tools)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Role != "assistant" || msg.Content != "Hello, world." {
		t.Errorf("message %+v", msg)
	}
	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].ID != "toolu_1" || msg.ToolCalls[0].Function.Name != "read_file" || msg.ToolCalls[0].Function.Arguments != `{"path":"a.txt"}` {
		t.Errorf("calls %+v", msg.ToolCalls)
	}
	if msg.ReasoningField != openai.ReasoningItems || !strings.Contains(msg.Reasoning, `"signature":"c2lnbmVk"`) || !strings.Contains(msg.Reasoning, `"thinking":"let me see"`) {
		t.Errorf("reasoning %q in %q", msg.Reasoning, msg.ReasoningField)
	}
	if msg.Usage != (openai.Usage{Prompt: 330, Cached: 300, Output: 42, InPlan: true}) {
		t.Errorf("usage %+v", msg.Usage)
	}
	if msg.Effort != (openai.Took{Asked: "high", Value: "high", Told: true}) {
		t.Errorf("effort %+v", msg.Effort)
	}
	if strings.Join(answering, "") != "Hello, world." || !slices.Equal(calling, []int{9, 17}) {
		t.Errorf("answering %q, calling %v", answering, calling)
	}
	if len(traced) != 2 || !strings.Contains(traced[0], "model claude-test") || !strings.Contains(traced[1], `stop_reason "tool_use"`) {
		t.Errorf("trace %q", traced)
	}

	args := f.args(1)
	if value(args, "--model") != "claude-test" || value(args, "--effort") != "high" || value(args, "--max-turns") != "1" ||
		value(args, "--tools") != "" || value(args, "--permission-mode") != "dontAsk" {
		t.Errorf("args %q", args)
	}
	if id := value(args, "--session-id"); id != sessionID("session-1", "", nil) || len(id) != 36 {
		t.Errorf("session %q", id)
	}
	env := f.env(1)
	if env["ANTHROPIC_API_KEY"] != "" || !strings.HasPrefix(env["ANTHROPIC_BASE_URL"], "http://127.0.0.1:") ||
		env["CLAUDE_CONFIG_DIR"] != "/config/of/claude" || env["CLAUDE_CODE_MAX_RETRIES"] != "0" || env["DISABLE_AUTOUPDATER"] != "1" {
		t.Errorf("env %v", env)
	}
	if wd, _ := os.ReadFile(filepath.Join(f.log, "wd-1.txt")); !strings.HasSuffix(string(wd), "claude-code") {
		t.Errorf("ran in %q", wd)
	}

	body := f.api.body(0)
	var sent []apiTool
	json.Unmarshal(body["tools"], &sent)
	if len(sent) != 1 || sent[0].Name != "read_file" || sent[0].Description != "Reads a file." {
		t.Errorf("tools %s", body["tools"])
	}
	// The message of the user as ouro sent it, the reminder of the CLI left
	// out, and the date at the end.
	want := `[{"content":[{"text":"Read a.txt","type":"text"}],"role":"user"},` +
		`{"content":[{"cache_control":{"type":"ephemeral"},"text":"# Environment\nwhere it runs","type":"text"}],"role":"system"},` +
		`{"content":"Today's date is 2026-10-19.","role":"system"}]`
	if string(body["messages"]) != want {
		t.Errorf("messages\n%s\nwant\n%s", body["messages"], want)
	}
	h := f.api.headers[0]
	if h.Get("Authorization") != "Bearer the-login-of-the-cli" || h.Get("Accept-Encoding") != "identity" || f.api.queries[0] != "beta=true" {
		t.Errorf("headers %v, query %q", h, f.api.queries[0])
	}
	// The system prompt is the system messages before the first other one.
	frames := f.stdin()
	if len(frames) != 1 || string(frames[0]["shouldQuery"]) != "" {
		t.Errorf("stdin %v", frames)
	}
}

func TestCompleteReplays(t *testing.T) {
	f := newFake(t)
	ctx := context.Background()
	msgs := []openai.Message{{Role: "system", Content: "You are ouro."}, {Role: "user", Content: "Read a.txt"}}
	first, err := f.model().Complete(ctx, "claude-test", msgs, tools)
	if err != nil {
		t.Fatal(err)
	}
	f.api.set(func(a *api) { a.stream = reply })
	msgs = append(msgs, first, openai.Message{Role: "tool", ToolCallID: "toolu_1", Content: "the text"},
		openai.Message{Role: "user", Content: "and now?", Images: []openai.Image{{Type: "image/png", Data: []byte("png")}}})
	msg, err := f.model().Complete(ctx, "claude-test", msgs, nil)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Content != "Done." || msg.ToolCalls != nil || msg.Reasoning != "" || msg.Usage.Output != 3 {
		t.Errorf("message %+v", msg)
	}
	if msg.Effort != (openai.Took{Told: true}) || slices.Contains(f.args(2), "--effort") {
		t.Errorf("effort %+v, args %q", msg.Effort, f.args(2))
	}
	if value(f.args(1), "--session-id") != value(f.args(2), "--session-id") {
		t.Errorf("the session changed: %q, %q", value(f.args(1), "--session-id"), value(f.args(2), "--session-id"))
	}
	lines := f.stdin()[1:]
	if len(lines) != 3 || string(lines[0]["shouldQuery"]) != "false" || string(lines[2]["shouldQuery"]) != "" {
		t.Fatalf("stdin %v", lines)
	}
	// The assistant message goes back as the API sent it, thought and all.
	if replayed := string(lines[1]["message"]); !strings.Contains(replayed, `"signature":"c2lnbmVk"`) || !strings.Contains(replayed, `"caller"`) {
		t.Errorf("assistant %s", replayed)
	}
	body := f.api.body(1)
	if _, ok := body["tools"]; ok {
		t.Errorf("tools %s", body["tools"])
	}
	var got []map[string]json.RawMessage
	json.Unmarshal(body["messages"], &got)
	if len(got) != 5 || string(got[1]["content"]) != `[{"text":"# Environment\nwhere it runs","type":"text"}]` {
		t.Fatalf("messages %s", body["messages"])
	}
	if last := string(got[3]["content"]); strings.Contains(last, "system-reminder") || !strings.Contains(last, `"tool_result"`) ||
		!strings.Contains(last, `"media_type":"image/png"`) {
		t.Errorf("newest message of the user %s", last)
	}
	if string(got[4]["content"]) != `[{"cache_control":{"type":"ephemeral"},"text":"Today's date is 2026-10-19.","type":"text"}]` {
		t.Errorf("date %s", got[4]["content"])
	}
}

func TestCompleteRefusesTheSecondRequest(t *testing.T) {
	for _, mode := range []string{"twice", "hello"} {
		f := newFake(t, "FAKE_MODE="+mode)
		f.api.set(func(a *api) { a.stream = reply })
		msg, err := f.model().Complete(context.Background(), "claude-test", []openai.Message{{Role: "user", Content: "hi"}}, nil)
		if err != nil || msg.Content != "Done." {
			t.Errorf("%s: %+v, %v", mode, msg, err)
		}
		if len(f.api.bodies) != 1 {
			t.Errorf("%s: the API got %d requests", mode, len(f.api.bodies))
		}
	}
}

func TestCompleteFailures(t *testing.T) {
	hi := []openai.Message{{Role: "user", Content: "hi"}}
	for _, c := range []struct {
		mode, want string
		is         error
	}{
		{"auth", "not signed in", ErrSignedOut},
		{"limit", "You've hit your limit", ErrLimit},
		{"other", "billing_error: Claude Code did not say more", nil},
		{"fail", "failed (error_during_execution): boom", nil},
		{"failerrs", "one; two", nil},
		{"failnone", "it did not say why", nil},
		{"silent", "ended without a request to the API", nil},
		{"stderr", "(Claude Code said: line 0", nil},
		{"noack", "ended while it was handed the conversation", nil},
		{"badack", "does not take the conversation as it was", nil},
		{"nojson", "is no JSON", nil},
		{"short", "cannot be read", nil},
	} {
		f := newFake(t, "FAKE_MODE="+c.mode)
		msgs := hi
		if c.mode == "noack" || c.mode == "badack" {
			msgs = []openai.Message{{Role: "user", Content: "one"}, {Role: "assistant", Content: "two"}, {Role: "user", Content: "three"}}
		}
		_, err := f.model().Complete(context.Background(), "claude-test", msgs, nil)
		if err == nil || !strings.Contains(err.Error(), c.want) || c.is != nil && !errors.Is(err, c.is) {
			t.Errorf("%s: %v", c.mode, err)
		}
		if c.mode == "limit" && !openai.IsUnavailable(err) {
			t.Errorf("%s: a limit is no reason for a fallback: %v", c.mode, err)
		}
		if c.mode == "stderr" && (!strings.HasSuffix(err.Error(), "…)") || strings.Contains(err.Error(), "line 20")) {
			t.Errorf("%s: %v", c.mode, err)
		}
	}
}

func TestCompleteAPIFailures(t *testing.T) {
	hi := []openai.Message{{Role: "user", Content: "hi"}}
	incomplete := sse(`{"type":"message_start","message":{"id":"msg_3"}}`)
	overloaded := sse(`{"type":"message_start","message":{"id":"msg_3"}}`, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)
	failed := sse(`{"type":"error","error":{"type":"api_error","message":"Internal"}}`)
	for _, c := range []struct {
		name   string
		set    func(a *api)
		want   string
		check  func(error) bool
		closed bool
	}{
		{"401", func(a *api) {
			a.status, a.stream = 401, `{"error":{"type":"authentication_error","message":"invalid token"}}`
		},
			"invalid token", func(err error) bool { return errors.Is(err, ErrSignedOut) }, false},
		{"429", func(a *api) { a.status, a.stream = 429, `{"error":{"type":"rate_limit_error","message":"limit"}}` },
			"usage limit", func(err error) bool { return errors.Is(err, ErrLimit) && openai.IsUnavailable(err) }, false},
		{"429 bare", func(a *api) { a.status, a.stream = 429, `` }, "did not say more", openai.IsUnavailable, false},
		{"403 bare", func(a *api) { a.status, a.stream = 403, `` }, "nothing more", func(err error) bool { return errors.Is(err, ErrSignedOut) }, false},
		{"529", func(a *api) { a.status, a.stream = 529, `{"error":{"type":"overloaded_error","message":"Overloaded"}}` },
			"HTTP 529 overloaded_error: Overloaded", openai.IsTransient, false},
		{"400 text", func(a *api) { a.status, a.stream = 400, "bad request" }, "HTTP 400: bad request", func(err error) bool { return !openai.IsUnavailable(err) }, false},
		{"incomplete", func(a *api) { a.stream = incomplete }, "ended before it was complete", openai.IsTransient, false},
		{"overloaded", func(a *api) { a.stream = overloaded }, "Overloaded", openai.IsTransient, false},
		{"error event", func(a *api) { a.stream = failed }, "HTTP 500 api_error: Internal", openai.IsUnavailable, false},
		{"cut", func(a *api) { a.cut = true }, "broke off", func(error) bool { return true }, false},
		{"redirect", func(a *api) { a.status, a.stream = 307, "" }, "HTTP 307", func(error) bool { return true }, false},
		{"unreachable", func(*api) {}, "connection refused", openai.IsUnavailable, true},
	} {
		f := newFake(t)
		f.api.set(c.set)
		if c.closed {
			f.api.Close()
		}
		_, err := f.model().Complete(context.Background(), "claude-test", hi, nil)
		if err == nil || !strings.Contains(err.Error(), c.want) || !c.check(err) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}

func TestCompleteGivesUpWhenNothingComes(t *testing.T) {
	was := idle
	idle = 300 * time.Millisecond
	t.Cleanup(func() { idle = was })
	hi := []openai.Message{{Role: "user", Content: "hi"}}
	f := newFake(t, "FAKE_MODE=hang")
	if _, err := f.model().Complete(context.Background(), "claude-test", hi, nil); !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "no sign") {
		t.Errorf("a CLI that hangs: %v", err)
	}
	f = newFake(t)
	f.api.set(func(a *api) { a.hang = true })
	if _, err := f.model().Complete(context.Background(), "claude-test", hi, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("an API that hangs: %v", err)
	}
	f = newFake(t, "FAKE_MODE=hang")
	history := []openai.Message{{Role: "user", Content: "one"}, {Role: "assistant", Content: "two"}, {Role: "user", Content: "three"}}
	if _, err := f.model().Complete(context.Background(), "claude-test", history, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a CLI that hangs while it is handed the conversation: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	idle = time.Minute
	if _, err := f.model().Complete(ctx, "claude-test", hi, nil); !errors.Is(err, context.Canceled) {
		t.Errorf("a call that was stopped: %v", err)
	}
}

func TestCompleteCannotStart(t *testing.T) {
	hi := []openai.Message{{Role: "user", Content: "hi"}}
	f := newFake(t)
	bad := &client{o: f.o, access: access{err: errors.New("unreadable")}}
	if _, err := bad.Complete(context.Background(), "m", hi, nil); err == nil || err.Error() != "unreadable" {
		t.Errorf("unreadable access: %v", err)
	}
	if _, err := f.model().Complete(context.Background(), "m", []openai.Message{{Role: "assistant", Content: "x"}}, nil); err == nil || !strings.Contains(err.Error(), "does not end") {
		t.Errorf("no message of the user: %v", err)
	}

	was := listen
	listen = func(string, string) (net.Listener, error) { return nil, errors.New("no ports") }
	_, err := f.model().Complete(context.Background(), "m", hi, nil)
	listen = was
	if err == nil || !strings.Contains(err.Error(), "no ports") {
		t.Errorf("no port: %v", err)
	}

	wasTemp := tempDir
	tempDir = func(string, string) (string, error) { return "", errors.New("disk full") }
	_, err = f.model().Complete(context.Background(), "m", hi, nil)
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Errorf("no directory: %v", err)
	}
	tempDir = func(string, string) (string, error) { return filepath.Join(t.TempDir(), "gone"), nil }
	_, err = f.model().Complete(context.Background(), "m", hi, nil)
	tempDir = wasTemp
	if err == nil || !strings.Contains(err.Error(), "system.md") {
		t.Errorf("no prompt: %v", err)
	}

	script := filepath.Join(t.TempDir(), "claude")
	os.WriteFile(script, []byte("no program"), 0o700)
	f.c = keep(account{Command: script})
	if _, err := f.model().Complete(context.Background(), "m", hi, nil); err == nil || !strings.Contains(err.Error(), "does not start") {
		t.Errorf("a CLI that does not start: %v", err)
	}
}

// access is an access with the credentials c, or one that cannot be read.
type access struct {
	c   llm.Credentials
	err error
}

func (a access) Credentials() (llm.Credentials, error)    { return a.c, a.err }
func (a access) Renew(func(*llm.Credentials) error) error { return nil }

func TestProvider(t *testing.T) {
	p := Provider(Options{})
	if p.Name != Name || p.Title != Title || p.Catalog(nil) != "anthropic" || p.Logout != nil {
		t.Errorf("provider %+v", p)
	}
	if _, ok := p.Model(access{}).(*client); !ok {
		t.Error("the model is no client")
	}
}

func TestKeep(t *testing.T) {
	for _, c := range []struct {
		a     account
		about string
	}{
		{account{Command: "c", Email: "me@example.com", Plan: "max"}, "Claude Max · me@example.com"},
		{account{Command: "c", Plan: "team"}, "Claude Team"},
		{account{Command: "c", Email: "me@example.com"}, "me@example.com"},
	} {
		got := keep(c.a)
		if got.About != c.about || got.Title != "Claude" || len(got.Secret) != 0 {
			t.Errorf("%+v: %+v", c.a, got)
		}
		if back, err := read(got); err != nil || back != c.a {
			t.Errorf("read %+v, %v", back, err)
		}
	}
	if _, err := read(llm.Credentials{Data: []byte(`{}`)}); err == nil {
		t.Error("an access without a command was read")
	}
}

func TestFind(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(home, ".local", "bin")
	os.MkdirAll(bin, 0o700)
	claude := filepath.Join(bin, "claude")
	os.WriteFile(claude, []byte("#!/bin/sh\n"), 0o700)
	other := filepath.Join(home, "other")
	os.WriteFile(other, []byte("#!/bin/sh\n"), 0o700)
	plain := filepath.Join(home, "plain")
	os.WriteFile(plain, []byte(""), 0o600)
	for _, c := range []struct {
		o    Options
		want string
	}{
		{Options{Env: []string{"PATH=", "HOME=" + home}}, claude},
		{Options{Env: []string{"PATH=" + home, "HOME="}, Command: "other"}, other},
		{Options{Env: []string{"PATH=::" + bin}}, claude},
		{Options{Command: other}, other},
		{Options{Command: plain}, ""},
		{Options{Command: home}, ""},
		{Options{Env: []string{"PATH=" + home}, Command: "plain"}, ""},
		{Options{Env: []string{"PATH=" + bin}, Command: "missing"}, ""},
	} {
		got, err := c.o.find()
		if got != c.want || (c.want == "") != errors.Is(err, ErrMissing) {
			t.Errorf("%+v: %q, %v", c.o, got, err)
		}
	}
	// A CLI that moved since the owner signed in is found where it is now.
	o := Options{Env: []string{"PATH=" + bin}}
	c := keep(account{Command: filepath.Join(home, "moved")})
	if acct, err := o.saved(llm.Unsaved(&c)); err != nil || acct.Command != claude {
		t.Errorf("moved: %+v, %v", acct, err)
	}
	if _, err := (Options{Env: []string{"PATH="}, Command: "missing"}).saved(llm.Unsaved(&c)); !errors.Is(err, ErrMissing) {
		t.Errorf("gone: %v", err)
	}
	if _, err := o.saved(access{c: llm.Credentials{Data: []byte(`[]`)}}); err == nil {
		t.Error("unreadable access")
	}
}

func TestEnvironment(t *testing.T) {
	env := childEnv([]string{"PATH=/bin", "ANTHROPIC_BASE_URL=x", "CLAUDECODE=1", "CLAUDE_CONFIG_DIR=/other", "NOEQUALS"}, account{})
	vars := environ(env)
	if vars["PATH"] != "/bin" || vars["ANTHROPIC_BASE_URL"] != "" || vars["CLAUDECODE"] != "" || vars["CLAUDE_CONFIG_DIR"] != "" ||
		vars["USER"] == "" || vars["USER"] != vars["LOGNAME"] || vars["CLAUDE_CODE_TOTAL_TOKENS_REMINDER"] != "off" {
		t.Errorf("env %q", env)
	}
	if vars := environ(childEnv([]string{"USER=me"}, account{})); vars["LOGNAME"] != "" {
		t.Errorf("a user was added to %v", vars)
	}
	if (Options{Env: nil}).env() == nil {
		t.Error("no environment of ouro")
	}
	file := filepath.Join(t.TempDir(), "file")
	os.WriteFile(file, nil, 0o600)
	if dir := (Options{Dir: filepath.Join(file, "sub")}).dir(); dir != "" {
		t.Errorf("dir %q", dir)
	}
}

func TestLogin(t *testing.T) {
	status := filepath.Join(t.TempDir(), "status")
	os.WriteFile(status, []byte(`{"loggedIn":false}`), 0o600)
	f := newFake(t, "FAKE_STATUS="+status, "CLAUDE_CONFIG_DIR=/from/env")
	// The owner signs in while the question is open.
	d := &signing{Dialog: llmtest.Dialog{Answers: map[string][]string{"Claude Code": {""}}}, signIn: func() {
		os.WriteFile(status, []byte(`{"loggedIn":true,"authMethod":"claude.ai","email":"me@example.com","subscriptionType":"max"}`), 0o600)
	}}
	c, err := f.o.login(context.Background(), d, nil)
	if err != nil {
		t.Fatal(err)
	}
	acct, _ := read(c)
	if acct != (account{Command: os.Args[0], ConfigDir: "/from/env", Email: "me@example.com", Plan: "max"}) || c.About != "Claude Max · me@example.com" {
		t.Errorf("signed in %+v as %+v", c, acct)
	}
	if asked := d.Asked(); len(asked) != 1 || !strings.Contains(strings.Join(asked[0].Text, "\n"), "claude auth login") {
		t.Errorf("asked %+v", asked)
	}
	if told := d.Told(); len(told) != 1 || !strings.Contains(told[0], "Agent SDK") {
		t.Errorf("told %q", told)
	}
	if env := f.env(2); env["CLAUDE_CONFIG_DIR"] != "/from/env" {
		t.Errorf("status asked with %v", env)
	}

	// Signed in to again, the directory of the login stays.
	have := keep(account{Command: "old", ConfigDir: "/kept"})
	if c, err := f.o.login(context.Background(), &llmtest.Dialog{}, &have); err != nil || !strings.Contains(string(c.Data), "/kept") {
		t.Errorf("again: %s, %v", c.Data, err)
	}
	unreadable := llm.Credentials{Data: []byte("x")}
	if c, err := f.o.login(context.Background(), &llmtest.Dialog{}, &unreadable); err != nil || !strings.Contains(string(c.Data), "/from/env") {
		t.Errorf("again an unreadable access: %s, %v", c.Data, err)
	}

	// Not installed, it says how to install it, and again until it is.
	missing := Options{Command: "missing", Env: []string{"PATH="}}
	ctx, cancel := context.WithCancel(context.Background())
	dialog := &llmtest.Dialog{Answers: map[string][]string{"Claude Code": {"", ""}}}
	go func() {
		for len(dialog.Asked()) < 3 {
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
	}()
	if _, err := missing.login(ctx, dialog, nil); !errors.Is(err, context.Canceled) {
		t.Errorf("missing: %v", err)
	}
	if asked := dialog.Asked(); !strings.Contains(strings.Join(asked[0].Text, "\n"), InstallHint) || len(dialog.Rejected()) != 2 {
		t.Errorf("asked %+v, rejected %q", asked, dialog.Rejected())
	}

	// A CLI that does not tell its login fails the sign-in.
	broken := newFake(t, "FAKE_STATUS=no json")
	if _, err := broken.o.login(context.Background(), &llmtest.Dialog{}, nil); err == nil || !strings.Contains(err.Error(), "did not tell the login") {
		t.Errorf("broken: %v", err)
	}
}

// signing is a dialog during which the owner signs in: signIn is called
// before the answer to the first question.
type signing struct {
	llmtest.Dialog
	signIn func()
}

func (s *signing) Ask(ctx context.Context, q llm.Question) (string, error) {
	if s.signIn != nil {
		s.signIn()
		s.signIn = nil
	}
	return s.Dialog.Ask(ctx, q)
}

func TestStatusGivesUp(t *testing.T) {
	was := probeTime
	probeTime = 200 * time.Millisecond
	t.Cleanup(func() { probeTime = was })
	script := filepath.Join(t.TempDir(), "claude")
	os.WriteFile(script, []byte("#!/bin/sh\nexec sleep 5\n"), 0o700)
	o := Options{Command: script}
	if _, err := o.status(context.Background(), account{Command: script}); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("status: %v", err)
	}
}

func TestModels(t *testing.T) {
	f := newFake(t, `FAKE_CONTROL={"subtype":"success","request_id":"ID","response":{"models":[`+
		`{"value":"default","resolvedModel":"claude-opus-5-5"},{"value":"opus","resolvedModel":"claude-opus-5-5"},`+
		`{"value":"sonnet","resolvedModel":"claude-sonnet-5-5"},{"value":"claude-haiku-4-5"}]}}`)
	m, err := f.o.models(context.Background(), f.access())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-4-5"}
	if !slices.Equal(m.Chat, want) || !slices.Equal(m.All, want) || m.Default != "claude-opus-5-5" {
		t.Errorf("models %+v", m)
	}
	if env := f.env(1); env["ANTHROPIC_BASE_URL"] != "" {
		t.Errorf("asked through a relay: %v", env)
	}
	if lines := f.stdin(); len(lines) != 1 || string(lines[0]["type"]) != `"control_request"` || !strings.Contains(string(lines[0]["request"]), `"initialize"`) {
		t.Errorf("stdin %v", lines)
	}

	f = newFake(t, `FAKE_CONTROL={"subtype":"success","request_id":"ID","response":{"models":[{"value":"haiku","resolvedModel":"claude-haiku-5-5"}]}}`)
	if m, err := f.o.models(context.Background(), f.access()); err != nil || m.Default != "claude-haiku-5-5" {
		t.Errorf("no default: %+v, %v", m, err)
	}
	f = newFake(t, `FAKE_CONTROL={"subtype":"success","request_id":"ID","response":{"models":[{"value":"default"}]}}`)
	if _, err := f.o.models(context.Background(), f.access()); err == nil || !strings.Contains(err.Error(), "no model") {
		t.Errorf("none: %v", err)
	}
	f = newFake(t, `FAKE_CONTROL={"subtype":"error","request_id":"ID","error":"unknown request"}`)
	if _, err := f.o.models(context.Background(), f.access()); err == nil || !strings.Contains(err.Error(), "refused initialize: unknown request") {
		t.Errorf("refused: %v", err)
	}
	f = newFake(t, `FAKE_CONTROL={"subtype":"error","request_id":"ID"}`)
	if _, err := f.o.models(context.Background(), f.access()); err == nil || !strings.Contains(err.Error(), "did not say why") {
		t.Errorf("refused: %v", err)
	}
	f = newFake(t, `FAKE_CONTROL={"subtype":"success","request_id":"other"}`)
	if _, err := f.o.models(context.Background(), f.access()); err == nil || !strings.Contains(err.Error(), "ended without answering initialize") {
		t.Errorf("no answer: %v", err)
	}
	if _, err := f.o.models(context.Background(), access{err: errors.New("unreadable")}); err == nil {
		t.Error("unreadable access")
	}
	script := filepath.Join(t.TempDir(), "claude")
	os.WriteFile(script, []byte("no program"), 0o700)
	f.c = keep(account{Command: script})
	if _, err := f.o.models(context.Background(), f.access()); err == nil || !strings.Contains(err.Error(), "does not start") {
		t.Errorf("does not start: %v", err)
	}

	was := probeTime
	probeTime = 200 * time.Millisecond
	t.Cleanup(func() { probeTime = was })
	f = newFake(t, "FAKE_MODE=hang")
	if _, err := f.o.models(context.Background(), f.access()); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("hangs: %v", err)
	}
}

func TestUsage(t *testing.T) {
	f := newFake(t, `FAKE_CONTROL={"subtype":"success","request_id":"ID","response":{"rate_limits":{"limits":[`+
		`{"kind":"session","percent":2,"resets_at":"2026-10-19T22:50:00Z"},{"kind":"weekly_all","percent":8,"resets_at":"2026-10-21T08:00:00Z"},`+
		`{"kind":"weekly_scoped","percent":1,"scope":{"model":{"display_name":"Fable"}}},{"kind":"odd","percent":3},{"kind":"no percent"}]}}}`)
	windows, err := f.o.usage(context.Background(), f.access())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, w := range windows {
		names = append(names, fmt.Sprintf("%s %g", w.Name, w.UsedPercent))
	}
	if strings.Join(names, ", ") != "5-hour 2, weekly 8, weekly Fable 1, odd 3" || windows[0].ResetsAt != time.Date(2026, 10, 19, 22, 50, 0, 0, time.UTC) {
		t.Errorf("windows %v", windows)
	}
	if !strings.Contains(string(f.stdin()[0]["request"]), `"skip_behaviors":true`) {
		t.Errorf("stdin %v", f.stdin())
	}
	if _, err := f.o.usage(context.Background(), access{err: errors.New("unreadable")}); err == nil {
		t.Error("unreadable access")
	}
	f = newFake(t, `FAKE_CONTROL={"subtype":"error","request_id":"ID"}`)
	if _, err := f.o.usage(context.Background(), f.access()); err == nil {
		t.Error("refused")
	}
}

func TestParseUsage(t *testing.T) {
	windows, err := parseUsage(json.RawMessage(`{"rate_limits":{"limits":[],"five_hour":{"utilization":2,"resets_at":"2026-10-19T22:50:00Z"},"seven_day":{"utilization":8}}}`))
	if err != nil || len(windows) != 2 || windows[0].Name != "5-hour" || windows[1].Name != "weekly" || windows[1].UsedPercent != 8 {
		t.Errorf("older windows %+v, %v", windows, err)
	}
	for _, body := range []string{`{"rate_limits":null}`, `x`, `{"rate_limits":{"five_hour":{"utilization":null}}}`} {
		if _, err := parseUsage(json.RawMessage(body)); err == nil {
			t.Errorf("%s: no error", body)
		}
	}
}
