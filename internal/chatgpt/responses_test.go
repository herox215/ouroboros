package chatgpt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"ouroboros/internal/agent"
	"ouroboros/internal/chatgpt/chatgpttest"
	"ouroboros/internal/llm"
	"ouroboros/internal/openai"
)

// model signs in at a fake and returns the model of the access.
func model(t *testing.T) (*chatgpttest.Server, agent.LLM) {
	t.Helper()
	srv, o := fake(t)
	c := signIn(t, srv, o)
	return srv, Provider(o).Model(llm.Unsaved(&c))
}

var tools = []openai.Tool{openai.NewTool("run", "Run a script.", `{"type":"object","properties":{"script":{"type":"string"}}}`)}

// The agents talk to the Responses API with the token of the access: the
// request is streamed and not stored, the system prompt goes as the
// instructions, and the answer comes back as a message of the agents.
func TestComplete(t *testing.T) {
	srv, m := model(t)
	ctx := openai.WithSession(context.Background(), "conv-1")
	msgs := []openai.Message{{Role: "system", Content: "You are alpha."}, {Role: "user", Content: "hi"}}
	got, err := m.Complete(ctx, "gpt-6.1-sol", msgs, tools)
	if err != nil {
		t.Fatal(err)
	}
	if got.Role != "assistant" || got.Content != "[gpt-6.1-sol] items=1 last=hi" || got.ToolCalls != nil || got.Reasoning != "" || got.Usage != (openai.Usage{Prompt: 100, Cached: 80, InPlan: true}) {
		t.Fatalf("got %+v", got)
	}
	seen := srv.Seen()
	body, _ := json.Marshal(seen.Requests[0])
	want := `{"input":[{"content":"hi","role":"user"}],"instructions":"You are alpha.","model":"gpt-6.1-sol","prompt_cache_key":"conv-1","store":false,"stream":true,` +
		`"tools":[{"description":"What the agent of this Ouroboros node can do.","name":"ouro","tools":[{"description":"Run a script.","name":"run",` +
		`"parameters":{"properties":{"script":{"type":"string"}},"type":"object"},"type":"function"}],"type":"namespace"}]}`
	if string(body) != want {
		t.Fatalf("sent\n%s\nwant\n%s", body, want)
	}
	if seen.Bearers[0] != "at-2" {
		t.Fatalf("asked with %v", seen.Bearers)
	}
	// The conversation goes along as a header too: that is what makes the
	// API take its requests where it cached the conversation.
	if !reflect.DeepEqual(seen.Sessions, []string{"conv-1"}) {
		t.Fatalf("the session of the request: %q", seen.Sessions)
	}
	// Without tools and without a session, neither is sent.
	if _, err := m.Complete(context.Background(), "m", msgs, nil); err != nil {
		t.Fatal(err)
	}
	if body := srv.Seen().Requests[1]; body["tools"] != nil || body["prompt_cache_key"] != nil || srv.Seen().Sessions[1] != "" {
		t.Fatalf("sent %v, session %q", body, srv.Seen().Sessions)
	}
}

// A call of a tool comes back as one, and goes back with its result in the
// items the API knows: the conversation of the agents is translated both
// ways, and what the model thought goes back as the API sent it.
func TestCompleteWithToolsAndThoughts(t *testing.T) {
	srv, m := model(t)
	srv.Set(func(s *chatgpttest.State) { s.Reasoning = "gAAAA-encrypted" })
	ctx := context.Background()
	history := []openai.Message{{Role: "system", Content: "You are alpha."}, {Role: "user", Content: `!run {"script":"1+1"}`}}
	call, err := m.Complete(ctx, "m", history, tools)
	if err != nil {
		t.Fatal(err)
	}
	wantCall := []openai.ToolCall{{ID: "call-1", Type: "function", Function: openai.FunctionCall{Name: "run", Arguments: `{"script":"1+1"}`}}}
	if call.Content != "" || !reflect.DeepEqual(call.ToolCalls, wantCall) || call.ReasoningField != openai.ReasoningItems ||
		call.Reasoning != `[{"encrypted_content":"gAAAA-encrypted","id":"rs_1","summary":[],"type":"reasoning"}]` {
		t.Fatalf("got %+v", call)
	}
	// The conversation goes on: the call, its result, a note of the node
	// and the thoughts of another provider, which are not sent.
	history = append(history, call,
		openai.Message{Role: "tool", ToolCallID: "call-1", Content: "2"},
		openai.Message{Role: "system", Content: "A note of a plugin."},
		openai.Message{Role: "assistant", Content: "It is 2.", Reasoning: "thoughts of another model", ReasoningField: "reasoning_content"},
		openai.Message{Role: "assistant", Content: "", Reasoning: "not JSON", ReasoningField: openai.ReasoningItems},
		openai.Message{Role: "user", Content: "thanks"})
	srv.Set(func(s *chatgpttest.State) { s.Reasoning = "" })
	got, err := m.Complete(ctx, "m", history, tools)
	if err != nil || got.Content != "[m] items=7 last=thanks" || got.Reasoning != "" || got.ReasoningField != "" {
		t.Fatalf("got %+v, %v", got, err)
	}
	input, _ := json.Marshal(srv.Seen().Requests[1]["input"])
	want := `[{"content":"!run {\"script\":\"1+1\"}","role":"user"},` +
		`{"encrypted_content":"gAAAA-encrypted","id":"rs_1","summary":[],"type":"reasoning"},` +
		`{"arguments":"{\"script\":\"1+1\"}","call_id":"call-1","name":"run","namespace":"ouro","type":"function_call"},` +
		`{"call_id":"call-1","output":"2","type":"function_call_output"},` +
		`{"content":"A note of a plugin.","role":"developer"},` +
		`{"content":"It is 2.","role":"assistant"},` +
		`{"content":"thanks","role":"user"}]`
	if string(input) != want {
		t.Fatalf("sent\n%s\nwant\n%s", input, want)
	}
	if strings.Contains(string(input), "thoughts of another model") {
		t.Fatal("the thoughts of another provider were sent")
	}
	// A result that is empty is an output all the same.
	if _, input := translate([]openai.Message{{Role: "tool", ToolCallID: "c"}}); string(input[0]) != `{"type":"function_call_output","call_id":"c","output":""}` {
		t.Fatalf("an empty result: %s", input[0])
	}
}

// The key of the cache is left out where the API does not take it, from the
// first refusal on.
func TestCompleteWithoutTheCacheKey(t *testing.T) {
	srv, m := model(t)
	srv.Set(func(s *chatgpttest.State) { s.NoCacheKey = true })
	ctx := openai.WithSession(context.Background(), "conv-1")
	for range 2 {
		if got, err := m.Complete(ctx, "m", []openai.Message{{Role: "user", Content: "hi"}}, nil); err != nil || got.Content != "[m] items=1 last=hi" {
			t.Fatalf("got %+v, %v", got, err)
		}
	}
	requests := srv.Seen().Requests
	if len(requests) != 3 || requests[0]["prompt_cache_key"] != "conv-1" || requests[1]["prompt_cache_key"] != nil || requests[2]["prompt_cache_key"] != nil {
		t.Fatalf("sent %v", requests)
	}
}

// A limit that is reached stops the answer and says where to look: nothing
// is tried again, nothing is switched to, and no time is made up for when
// it is over.
func TestCompleteAtTheLimit(t *testing.T) {
	srv, m := model(t)
	ask := func() error {
		_, err := m.Complete(context.Background(), "m", []openai.Message{{Role: "user", Content: "hi"}}, nil)
		return err
	}
	limit := `{"error":{"code":"subscription_sharing_usage_limit_exceeded","message":"limit"}}`
	srv.Set(func(s *chatgpttest.State) { s.Responses = []chatgpttest.Response{{Status: 429, Body: limit}} })
	err := ask()
	if !errors.Is(err, ErrLimit) || !openai.IsUnavailable(err) || !strings.Contains(err.Error(), UsagePage) || strings.Contains(err.Error(), "try again after") || len(srv.Seen().Requests) != 1 {
		t.Fatalf("at the limit: %v, %d requests", err, len(srv.Seen().Requests))
	}
	// When it is over is told as far as the API says.
	srv.Set(func(s *chatgpttest.State) {
		s.Responses = []chatgpttest.Response{{Status: 429, Body: limit, Header: map[string]string{"Retry-After": "3600"}}}
	})
	if err := ask(); !errors.Is(err, ErrLimit) || !openai.IsUnavailable(err) || !strings.Contains(err.Error(), "the API says to try again after 3600") {
		t.Fatalf("at the limit, with a time: %v", err)
	}
	// The limit may come after the stream began.
	srv.Set(func(s *chatgpttest.State) {
		s.Responses = []chatgpttest.Response{{Events: []string{`{"type":"response.created"}`,
			`{"type":"response.failed","response":{"error":{"code":"subscription_sharing_usage_limit_exceeded","message":"limit"}}}`}}}
	})
	if err := ask(); !errors.Is(err, ErrLimit) {
		t.Fatalf("at the limit, in the stream: %v", err)
	}
}

// What the API fails with is told so that the owner knows what to do.
func TestCompleteFails(t *testing.T) {
	srv, m := model(t)
	ask := func() error {
		_, err := m.Complete(context.Background(), "m", []openai.Message{{Role: "user", Content: "hi"}}, nil)
		return err
	}
	events := func(e ...string) []chatgpttest.Response { return []chatgpttest.Response{{Events: e}} }
	for name, tt := range map[string]struct {
		responses []chatgpttest.Response
		want      string
	}{
		"not eligible": {[]chatgpttest.Response{{Status: 403, Body: `{"error":{"code":"subscription_sharing_user_not_eligible","message":"workspace policy"}}`}},
			"using the ChatGPT plan is not available for this account or workspace: workspace policy"},
		"not eligible, no reason": {[]chatgpttest.Response{{Status: 403, Body: `{"error":{"code":"subscription_sharing_user_not_eligible"}}`}}, "the API did not say why"},
		"invalid user":            {[]chatgpttest.Response{{Status: 401, Body: `{"error":{"code":"subscription_sharing_invalid_user"}}`}, {Status: 401}}, ErrSignedOut.Error()},
		"unsupported":             {[]chatgpttest.Response{{Status: 400, Body: `{"error":{"code":"subscription_sharing_unsupported_capability","message":"no","param":"tools"}}`}}, "chatgpt: HTTP 400 subscription_sharing_unsupported_capability no"},
		"a detail":                {[]chatgpttest.Response{{Status: 403, Body: `{"detail":"region"}`}}, "chatgpt: HTTP 403  region"},
		"no body":                 {[]chatgpttest.Response{{Status: 500}}, "chatgpt: HTTP 500"},
		"an empty detail":         {[]chatgpttest.Response{{Status: 500, Body: `{"detail":""}`}}, "chatgpt: HTTP 500"},
		"failed":                  {events(`{"type":"response.failed","response":{"error":{"code":"server_error","message":"boom"}}}`), "chatgpt: HTTP 200 server_error boom"},
		"failed, no reason":       {events(`{"type":"response.failed","response":{}}`), "the response failed, the API did not say why"},
		"incomplete":              {events(`{"type":"response.incomplete","response":{"incomplete_details":{"reason":"max_output_tokens"}}}`), "the response is incomplete: max_output_tokens"},
		"incomplete, no reason":   {events(`{"type":"response.incomplete","response":{}}`), "the response is incomplete: the API did not say why"},
		"an error event":          {events(`{"type":"error","code":"rate_limited","message":"slow down"}`), "chatgpt: HTTP 200 rate_limited slow down"},
		"ended early":             {events(`{"type":"response.created"}`), "the stream ended before the response was complete"},
		"no JSON":                 {events(`{`), "the stream has an event that is no JSON"},
		"an odd item":             {events(`{"type":"response.output_item.done","item":7}`), "the stream has an item that cannot be read"},
		"too long an event":       {events(`{"type":"` + strings.Repeat("x", maxEvent) + `"}`), "token too long"},
	} {
		srv.Set(func(s *chatgpttest.State) { s.Responses = tt.responses })
		if err := ask(); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// An API that cannot take the request right now is asked again a little
// later, a few times.
func TestCompleteTriesAgainLater(t *testing.T) {
	srv, m := model(t)
	ask := func(ctx context.Context) (string, error) {
		got, err := m.Complete(ctx, "m", []openai.Message{{Role: "user", Content: "hi"}}, nil)
		return got.Content, err
	}
	down := chatgpttest.Response{Status: 503, Body: `{"detail":"routing is unavailable"}`}
	busy := chatgpttest.Response{Events: []string{`{"type":"response.failed","response":{"error":{"code":"subscription_sharing_usage_unavailable","message":"later"}}}`}}
	srv.Set(func(s *chatgpttest.State) { s.Responses = []chatgpttest.Response{down, busy} })
	if got, err := ask(context.Background()); err != nil || got != "[m] items=1 last=hi" || len(srv.Seen().Requests) != 3 {
		t.Fatalf("after two tries: %q, %v, %d requests", got, err, len(srv.Seen().Requests))
	}
	// It stays down: told after the last try, with what the API said.
	srv.Set(func(s *chatgpttest.State) { s.Responses = []chatgpttest.Response{down, down, down, down} })
	if _, err := ask(context.Background()); err == nil || !openai.IsUnavailable(err) || !strings.Contains(err.Error(), "cannot be used right now, try again later") || !strings.Contains(err.Error(), "routing is unavailable") {
		t.Fatalf("down for good: %v", err)
	}
	if n := len(srv.Seen().Requests); n != 6 {
		t.Fatalf("%d requests", n)
	}
	// A key of the cache that is refused on the way does not count as a
	// try: the API is still asked as often as it would have been.
	srv.Set(func(s *chatgpttest.State) { s.NoCacheKey, s.Responses = true, []chatgpttest.Response{down, down} })
	if got, err := m.Complete(openai.WithSession(context.Background(), "conv-9"), "m", []openai.Message{{Role: "user", Content: "hi"}}, nil); err != nil || got.Content != "[m] items=1 last=hi" {
		t.Fatalf("with the key refused before two tries: %+v, %v", got, err)
	}
	srv.Set(func(s *chatgpttest.State) { s.NoCacheKey = false })
	// Given up while it waits to try again.
	old := backoff
	t.Cleanup(func() { backoff = old })
	backoff = []time.Duration{time.Hour}
	srv.Set(func(s *chatgpttest.State) { s.Responses = []chatgpttest.Response{down} })
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := ask(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("given up: %v", err)
	}
}

// An API that sends nothing is given up on, and so is an access that is
// not there.
func TestCompleteGivesUp(t *testing.T) {
	srv, o := fake(t)
	c := signIn(t, srv, o)
	old := idle
	t.Cleanup(func() { idle = old })
	idle = 50 * time.Millisecond
	srv.Set(func(s *chatgpttest.State) { s.Slow = 300 * time.Millisecond })
	srv.Expire() // the token is renewed first, which takes long here
	m := Provider(o).Model(llm.Unsaved(&c))
	if _, err := m.Complete(context.Background(), "m", nil, nil); !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "no data from the API for 50ms") {
		t.Fatalf("an API that sends nothing: %v", err)
	}
	idle = old
	if _, err := Provider(o).Model(failing{}).Complete(context.Background(), "m", nil, nil); err == nil || err.Error() != "gone" {
		t.Fatalf("an access that is gone: %v", err)
	}
	o.API = "http://bad url"
	if _, err := Provider(o).Model(llm.Unsaved(&c)).Complete(context.Background(), "m", nil, nil); err == nil {
		t.Fatal("an API that has no address answered")
	}
}

func TestAPIError(t *testing.T) {
	if got := (&apiError{Status: 418}).Error(); got != "chatgpt: HTTP 418" {
		t.Fatalf("got %q", got)
	}
	// A thought goes back without how far it got.
	if got := string(thought([]byte(`{"type":"reasoning","id":"rs_1","status":"completed","encrypted_content":"x"}`))); got != `{"encrypted_content":"x","id":"rs_1","type":"reasoning"}` {
		t.Fatalf("thought %s", got)
	}
}

// In the debug mode of the node, the calls through this provider tell what
// they send and get like those to an OpenAI-compatible API: the request in
// short, whether the key of the cache went along, how far the request begins
// like the one before, the headers of the response and the usage as the API
// sent it. That is how to find out why a call was not cached.
func TestCompleteIsTraced(t *testing.T) {
	srv, o := fake(t)
	c := signIn(t, srv, o)
	m := Provider(o).Model(llm.Unsaved(&c))
	var lines []string
	ctx := openai.WithTrace(openai.WithSession(context.Background(), "conv-1"), func(format string, args ...any) {
		lines = append(lines, fmt.Sprintf(format, args...))
	})
	take := func() string {
		text := strings.Join(lines, "\n")
		lines = nil
		return text
	}
	msgs := []openai.Message{{Role: "system", Content: "You are alpha."}, {Role: "user", Content: "hi"}}
	if _, err := m.Complete(ctx, "m", msgs, tools); err != nil {
		t.Fatal(err)
	}
	got := take()
	for _, want := range []string{
		"llm request: " + srv.API + "/responses, model m, session conv-1, instructions 0.0 KiB, 1 items (0.0 KiB), 1 tools (0.2 KiB, hash ",
		"prompt_cache_key true, session_id header true",
		"llm prefix: first request of the session in the trace",
		"llm response: HTTP 200 after ", "Content-Type: text/event-stream",
		`llm answered: id "", model "", events 3, usage {"input_tokens":100,"input_tokens_details":{"cached_tokens":80}}`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("no %q in:\n%s", want, got)
		}
	}
	// The next request of the conversation begins like the one before.
	msgs = append(msgs, openai.Message{Role: "assistant", Content: "ho"}, openai.Message{Role: "user", Content: "again"})
	if _, err := m.Complete(ctx, "m", msgs, tools); err != nil {
		t.Fatal(err)
	}
	if got := take(); !strings.Contains(got, "llm prefix: all 2 messages of the last request are kept, 2 are new") {
		t.Errorf("the prefix:\n%s", got)
	}
	// Other instructions are a request that begins differently, and so are
	// other tools.
	msgs[0].Content = "You are beta."
	if _, err := m.Complete(ctx, "m", msgs, nil); err != nil {
		t.Fatal(err)
	}
	if got := take(); !strings.Contains(got, "the tools changed") || !strings.Contains(got, `breaks at message 0 of 4 (role "system")`) || !strings.Contains(got, "0 tools (0.0 KiB") {
		t.Errorf("a prefix that changed:\n%s", got)
	}

	// The key of the cache that the API refuses is said to be left out, and
	// every request after it says that it went without.
	srv.Set(func(s *chatgpttest.State) { s.NoCacheKey = true })
	if _, err := m.Complete(ctx, "m", msgs, nil); err != nil {
		t.Fatal(err)
	}
	if got := take(); !strings.Contains(got, "llm request: the API refused prompt_cache_key, asked again without it") ||
		!strings.Contains(got, "llm response: failed: chatgpt: HTTP 400 subscription_sharing_unsupported_capability") {
		t.Errorf("a key that is refused:\n%s", got)
	}
	if _, err := m.Complete(ctx, "m", msgs, nil); err != nil {
		t.Fatal(err)
	}
	if got := take(); !strings.Contains(got, "prompt_cache_key false") {
		t.Errorf("after the key was refused:\n%s", got)
	}
	// A usage the API did not send, and an API that is not reached.
	srv.Set(func(s *chatgpttest.State) {
		s.Responses = []chatgpttest.Response{{Events: []string{`{"type":"response.completed","response":{"id":"resp_1","model":"m-1"}}`}}}
	})
	if msg, err := m.Complete(ctx, "m", msgs, nil); err != nil || msg.Usage != (openai.Usage{InPlan: true}) {
		t.Fatalf("without a usage: %+v, %v", msg, err)
	}
	if got := take(); !strings.Contains(got, `llm answered: id "resp_1", model "m-1", events 1, usage none`) {
		t.Errorf("without a usage:\n%s", got)
	}
	o.API = "http://127.0.0.1:1"
	if _, err := Provider(o).Model(llm.Unsaved(&c)).Complete(ctx, "m", msgs, nil); err == nil {
		t.Fatal("want error")
	}
	if got := take(); !strings.Contains(got, "llm response: none after ") {
		t.Errorf("an API that is not reached:\n%s", got)
	}
	// No token of the access is in what is told.
	_, tok, _ := read(c)
	for _, secret := range []string{tok.Access, tok.Refresh, tok.ID} {
		if strings.Contains(got, secret) {
			t.Errorf("%q is told to the trace", secret)
		}
	}
}

// The pictures of a result of a tool, and those of a message of the user,
// are parts after its text.
func TestTranslateImages(t *testing.T) {
	png := openai.Image{Type: "image/png", Data: []byte("p")}
	_, input := translate([]openai.Message{
		{Role: "user", Content: "look", Images: []openai.Image{png}},
		{Role: "tool", ToolCallID: "c", Content: "a picture", Images: []openai.Image{png}},
	})
	want := []string{
		`{"role":"user","content":[{"type":"input_text","text":"look"},{"type":"input_image","image_url":"data:image/png;base64,cA=="}]}`,
		`{"type":"function_call_output","call_id":"c","output":[{"type":"input_text","text":"a picture"},{"type":"input_image","image_url":"data:image/png;base64,cA=="}]}`,
	}
	for i := range want {
		if string(input[i]) != want[i] {
			t.Errorf("item %d:\n%s\nwant\n%s", i, input[i], want[i])
		}
	}
}

// The effort goes to the API as reasoning.effort, as it is named; "max",
// which some models know, and else as "xhigh". One the model does not know
// is asked again with the next that is near, and at last without; what it
// took is what it is asked with from then on. Without an effort none is
// sent.
func TestCompleteSendsTheEffort(t *testing.T) {
	srv, m := model(t)
	var traced []string
	var took openai.Took
	ask := func(effort string) []string {
		t.Helper()
		before := len(srv.Seen().Requests)
		ctx := openai.WithTrace(openai.WithEffort(context.Background(), effort), func(format string, args ...any) {
			if line := fmt.Sprintf(format, args...); strings.Contains(line, "refused the reasoning effort") {
				traced = append(traced, line)
			}
		})
		msg, err := m.Complete(ctx, "m", []openai.Message{{Role: "user", Content: "hi"}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		took = msg.Effort
		var sent []string
		for _, body := range srv.Seen().Requests[before:] {
			reasoning, _ := body["reasoning"].(map[string]any)
			effort, ok := reasoning["effort"].(string)
			if !ok {
				effort = "-"
			}
			sent = append(sent, effort)
		}
		return sent
	}
	// The message says what the API took of the effort.
	for _, tt := range []struct {
		effort  string
		efforts []string // that the API takes, nil for all
		want    []string
		took    string
	}{
		{"", nil, []string{"-"}, ""},
		{"medium", nil, []string{"medium"}, "medium"},
		{"max", nil, []string{"max"}, "max"},
		{"none", []string{"low", "high"}, []string{"none", "minimal", "low"}, "low"},
		{"none", []string{"low", "high"}, []string{"low"}, "low"}, // what it took the last time
		{"max", []string{"low"}, []string{"max", "xhigh", "high", "-"}, ""},
	} {
		srv.Set(func(s *chatgpttest.State) { s.Efforts = tt.efforts })
		if got := ask(tt.effort); !slices.Equal(got, tt.want) {
			t.Errorf("%q: sent %q, want %q", tt.effort, got, tt.want)
		}
		if took != (openai.Took{Asked: tt.effort, Value: tt.took, Told: true}) {
			t.Errorf("%q: took %+v", tt.effort, took)
		}
	}
	// Every refusal is traced.
	if len(traced) != 5 || traced[0] != `llm request: the API refused the reasoning effort "none", asked again with "minimal"` {
		t.Fatalf("traced %q", traced)
	}
}
