package assistant

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"ouroboros/internal/agent"
	"ouroboros/internal/config"
	"ouroboros/internal/identity"
	"ouroboros/internal/openai"
)

// scriptLLM answers a user message "!TOOL ARGS" with a call to TOOL and the
// text saying, every other message with "said: TEXT". A message "wait" makes
// it wait until its context ends. It fails with failing, if set.
type scriptLLM struct {
	failing error
	saying  string
	waiting chan struct{} // closed once it waits
}

func (l *scriptLLM) Complete(ctx context.Context, _ string, msgs []openai.Message, tools []openai.Tool) (openai.Message, error) {
	last := msgs[len(msgs)-1]
	if l.failing != nil {
		return openai.Message{}, l.failing
	}
	if last.Role == "user" && last.Content == "wait" {
		close(l.waiting)
		<-ctx.Done()
		return openai.Message{}, ctx.Err()
	}
	if rest, ok := strings.CutPrefix(last.Content, "!"); ok && last.Role == "user" {
		name, args, _ := strings.Cut(rest, " ")
		if name != toolRun && len(tools) == 1 && tools[0].Function.Name == toolRun {
			name, args = toolRun, scriptOf(name, args)
		}
		call := openai.ToolCall{ID: "1", Type: "function", Function: openai.FunctionCall{Name: name, Arguments: args}}
		return openai.Message{Content: l.saying, ToolCalls: []openai.ToolCall{call}}, nil
	}
	return openai.Message{Content: "said: " + last.Content}, nil
}

// scriptOf is the arguments of run for a script that calls the tool name
// with args and gives back what the LLM would get of the call itself.
func scriptOf(name, args string) string {
	b, _ := json.Marshal(runArgs{Script: fmt.Sprintf(`try { tools[%q](%s) } catch (e) { "error: " + e.message }`, name, args)})
	return string(b)
}

// logged is a log that keeps its lines.
type logged struct {
	mu    sync.Mutex
	lines []string
}

func (l *logged) log(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

// count is how many lines were logged since the last take.
func (l *logged) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.lines)
}

// take returns the lines logged since the last call, with every duration
// written as D.
func (l *logged) take() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	lines := l.lines
	l.lines = nil
	for i, line := range lines {
		lines[i] = sent.ReplaceAllString(duration.ReplaceAllString(line, "${1}D"), "sent N")
	}
	return lines
}

var (
	duration = regexp.MustCompile(`((?:took|after) )[0-9.]+(?:ms|s|µs|ns|m)`)
	sent     = regexp.MustCompile(`sent [0-9]+\.[0-9] KiB`)
)

// recorded is an assistant that logs to the returned log, with the LLM llm
// and the tools of the test.
func recorded(t *testing.T, cfg config.Config, llm agent.LLM) (*Assistant, *logged) {
	t.Helper()
	log := &logged{}
	tools := []agent.Tool{
		agent.NewTool("echo", "", nil, func(_ context.Context, args struct {
			Text string `json:"text"`
		}) (string, error) {
			return args.Text, nil
		}),
		agent.NewTool("fail", "", nil, func(_ context.Context, args struct {
			Why string `json:"why"`
		}) (string, error) {
			return "", errors.New(args.Why)
		}),
		agent.NewTool("block", "", nil, func(ctx context.Context, _ struct{}) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		}),
		agent.NewTool("write", "", nil, func(context.Context, struct{}) (string, error) { return "wrote", nil }),
		agent.NewTool("edit", "", nil, func(context.Context, struct{}) (string, error) { return "edited", nil }),
	}
	a := New(func() (config.Config, error) { return cfg, nil }, Builder{
		LLM:      func(string) (agent.LLM, error) { return llm, nil },
		Accesses: &accesses{secrets: []string{accessToken, accessToken + "-longer"}},
		Tools:    func() []agent.Tool { return tools },
	}, log.log)
	return a, log
}

func ownerConfig() config.Config {
	return config.Config{Access: "http://llm", APIKey: "sk-secret-123", Model: "m", Name: "me", PrivateKey: identity.Generate().Private()}
}

func equal(t *testing.T, what string, got, want []string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("%s:\n%s\nwant:\n%s", what, strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// The log has the message of the owner, every call of the LLM, every tool
// call and the answer, each with how long it took, and of a tool call that
// failed its error.
func TestAskIsLogged(t *testing.T) {
	llm := &scriptLLM{}
	a, log := recorded(t, ownerConfig(), llm)
	ctx := context.Background()

	if got, err := a.Ask(ctx, "c1", "hello\nthere", nil); err != nil || got != "said: hello\nthere" {
		t.Fatalf("got %q, %v", got, err)
	}
	equal(t, "a plain answer", log.take(), []string{
		`chat c1: message from the owner: "hello\nthere"`,
		`chat c1: llm call 1 took D: text`,
		`chat c1: answered after D (llm calls: 1, tool calls: 0, sent N): "said: hello\nthere"`,
	})

	if _, err := a.Ask(ctx, "c1", `!echo {"text":"hi"}`, nil); err != nil {
		t.Fatal(err)
	}
	equal(t, "a tool call", log.take(), []string{
		`chat c1: message from the owner: "!echo {\"text\":\"hi\"}"`,
		`chat c1: llm call 1 took D: tool calls: 1`,
		`chat c1: tool echo {"text":"hi"} took D: ok (2 bytes)`,
		`chat c1: tool run ` + scriptOf("echo", `{"text":"hi"}`) + ` took D: ok (2 bytes)`,
		`chat c1: llm call 2 took D: text`,
		`chat c1: answered after D (llm calls: 2, tool calls: 2, sent N): "said: hi"`,
	})

	if _, err := a.Ask(ctx, "c2", `!fail {"why":"disk\nfull"}`, nil); err != nil {
		t.Fatal(err)
	}
	equal(t, "a tool call that failed", log.take(), []string{
		`chat c2: message from the owner: "!fail {\"why\":\"disk\\nfull\"}"`,
		`chat c2: llm call 1 took D: tool calls: 1`,
		`chat c2: tool fail {"why":"disk\nfull"} took D: error "disk\nfull"`,
		`chat c2: tool run ` + scriptOf("fail", `{"why":"disk\nfull"}`) + ` took D: ok (16 bytes)`,
		`chat c2: llm call 2 took D: text`,
		`chat c2: answered after D (llm calls: 2, tool calls: 2, sent N): "said: error: disk\nfull"`,
	})

	llm.failing = errTest
	if _, err := a.Ask(ctx, "c2", "hi", nil); err != errTest {
		t.Fatalf("err %v", err)
	}
	equal(t, "an answer that failed", log.take(), []string{
		`chat c2: message from the owner: "hi"`,
		`chat c2: llm call 1 took D: failed "test error"`,
		`chat c2: failed after D (llm calls: 1, tool calls: 0, sent N): "test error"`,
	})

	// A configuration that cannot be used is no answer: nothing to log.
	b, log := recorded(t, config.Config{}, llm)
	if _, err := b.Ask(ctx, "c3", "hi", nil); !errors.Is(err, config.ErrIncomplete) || len(log.take()) != 0 {
		t.Fatalf("not set up: %v", err)
	}
}

// What the LLM says along with calls of tools is logged with its call,
// shortened and without secrets; when it says nothing, the line has only the
// number of calls (see TestAskIsLogged). The text of a call that ends in text
// is the answer and is logged once, as such.
func TestWhatTheLLMSaysIsLogged(t *testing.T) {
	cfg := ownerConfig()
	llm := &scriptLLM{saying: "I will look\nfirst, with " + cfg.APIKey + " " + strings.Repeat("a", maxLogged)}
	a, log := recorded(t, cfg, llm)
	if _, err := a.Ask(context.Background(), "c1", `!echo {"text":"hi"}`, nil); err != nil {
		t.Fatal(err)
	}
	head := "I will look\nfirst, with [redacted] " // what is left of the text before the a's
	said := strconv.Quote(head+strings.Repeat("a", maxLogged-len(head))) + "… (+" + strconv.Itoa(len(head)) + " bytes)"
	equal(t, "said along with a tool call", log.take(), []string{
		`chat c1: message from the owner: "!echo {\"text\":\"hi\"}"`,
		`chat c1: llm call 1 took D: tool calls: 1, said ` + said,
		`chat c1: tool echo {"text":"hi"} took D: ok (2 bytes)`,
		`chat c1: tool run ` + scriptOf("echo", `{"text":"hi"}`) + ` took D: ok (2 bytes)`,
		`chat c1: llm call 2 took D: text`,
		`chat c1: answered after D (llm calls: 2, tool calls: 2, sent N): "said: hi"`,
	})
}

// Run tells say what the LLM says along with calls of tools, and nothing of
// the answer, which is what Run returns.
func TestRunSaysWhatComesWithToolCalls(t *testing.T) {
	a, _ := recorded(t, ownerConfig(), &scriptLLM{saying: "I will look"})
	var said []string
	got, err := a.Run(context.Background(), "c1", `!echo {"text":"hi"}`, nil, func(s string) { said = append(said, s) })
	if err != nil || got != "said: hi" || strings.Join(said, "|") != "I will look" {
		t.Fatalf("got %q, %v, said %q", got, err, said)
	}
}

// The tools a script calls are logged like the others, before the script.
func TestCallsOfAScriptAreLogged(t *testing.T) {
	a, log := recorded(t, ownerConfig(), &scriptLLM{})
	script, _ := json.Marshal(map[string]string{"script": `echo({text: "a"}); try { fail({why: "no"}) } catch (e) {} return "x"`})
	if _, err := a.Ask(context.Background(), "c1", "!run "+string(script), nil); err != nil {
		t.Fatal(err)
	}
	equal(t, "a script", log.take(), []string{
		`chat c1: message from the owner: ` + strconv.Quote("!run "+string(script)),
		`chat c1: llm call 1 took D: tool calls: 1`,
		`chat c1: tool echo {"text":"a"} took D: ok (1 bytes)`,
		`chat c1: tool fail {"why":"no"} took D: error "no"`,
		`chat c1: tool run ` + string(script) + ` took D: ok (1 bytes)`,
		`chat c1: llm call 2 took D: text`,
		`chat c1: answered after D (llm calls: 2, tool calls: 3, sent N): "said: x"`,
	})
}

// The owner stops an answer while a tool runs, by a context that ends or by
// Stop: the tool and the answer are logged as canceled, with how long they
// ran, and nothing after that.
func TestCanceledToolIsLogged(t *testing.T) {
	a, log := recorded(t, ownerConfig(), &scriptLLM{})
	want := []string{
		`chat c1: message from the owner: "!block {}"`,
		`chat c1: llm call 1 took D: tool calls: 1`,
		`chat c1: tool block {} took D: canceled`,
		`chat c1: tool run ` + scriptOf("block", "{}") + ` took D: canceled`,
		`chat c1: canceled after D (llm calls: 1, tool calls: 0, sent N)`,
	}
	for name, stop := range map[string]func(cancel context.CancelFunc){
		"context": func(cancel context.CancelFunc) { cancel() },
		"Stop":    func(context.CancelFunc) { a.Stop() },
	} {
		ctx, cancel := context.WithCancel(context.Background())
		started := make(chan struct{})
		done := make(chan error)
		go func() {
			_, err := a.Ask(ctx, "c1", "!block {}", func(s Step) {
				if !s.Done && s.Tool == "block" {
					close(started)
				}
			})
			done <- err
		}()
		<-started
		stop(cancel)
		if name == "context" {
			// The log says so at once, before the answer has returned.
			for log.count() < len(want) {
				time.Sleep(time.Millisecond)
			}
		}
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("%s: %v", name, err)
		}
		equal(t, name, log.take(), want)
		cancel()
	}
	a.Stop() // with nothing on its way
}

// An answer stopped while the LLM is asked says that the LLM was waited for.
func TestCanceledCallOfTheLLMIsLogged(t *testing.T) {
	llm := &scriptLLM{waiting: make(chan struct{})}
	a, log := recorded(t, ownerConfig(), llm)
	done := make(chan error)
	go func() {
		_, err := a.Ask(context.Background(), "c1", "wait", nil)
		done <- err
	}()
	<-llm.waiting
	a.Stop()
	equal(t, "stopped", log.take(), []string{
		`chat c1: message from the owner: "wait"`,
		`chat c1: llm call 1 took D: canceled`,
		`chat c1: canceled after D (llm calls: 1, tool calls: 0, sent N)`,
	})
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if lines := log.take(); len(lines) != 0 {
		t.Fatalf("after the last line: %q", lines)
	}
}

// What a tool says it did after its answer was stopped is not logged: the
// last line is that the answer was canceled.
func TestNothingAfterTheEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var lines []string
	r := newRecord(ctx, func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) }, "c", nil, "hi", func() {})
	r.step(agent.Step{Tool: "a", Args: json.RawMessage(`{}`)})
	r.call(agent.Call{})
	r.step(agent.Step{Tool: "b", Args: json.RawMessage(`{}`)})
	cancel()
	r.step(agent.Step{Tool: "b", Args: json.RawMessage(`{}`), Done: true})
	r.call(agent.Call{Done: true})
	r.end("late", nil)
	r.cancel()
	r.steered("late")
	r.trace("llm request: %s", "late")
	for i, line := range lines {
		lines[i] = sent.ReplaceAllString(duration.ReplaceAllString(line, "${1}D"), "sent N")
	}
	equal(t, "after the end", lines, []string{
		`chat c: message from the owner: "hi"`,
		`chat c: llm call 1 took D: canceled`,
		`chat c: tool b {} took D: canceled`,
		`chat c: tool a {} took D: canceled`,
		`chat c: canceled after D (llm calls: 1, tool calls: 0, sent N)`,
	})
}

// What the API says a call cost is logged with the call, and summed up at
// the end of the answer.
func TestTokensAreLogged(t *testing.T) {
	var lines []string
	r := newRecord(context.Background(), func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) }, "c", nil, "hi", func() {})
	r.call(agent.Call{Sent: 2048})
	r.call(agent.Call{Done: true, ToolCalls: 1, Tokens: 12345, Cached: 10240})
	r.call(agent.Call{Sent: 1024})
	r.call(agent.Call{Done: true, Tokens: 1000, Cached: 999, Thought: 2048})
	r.end("ok", nil)
	for i, line := range lines {
		lines[i] = duration.ReplaceAllString(line, "${1}D")
	}
	equal(t, "tokens", lines, []string{
		`chat c: message from the owner: "hi"`,
		`chat c: llm call 1 took D: tool calls: 1 [12.3k tokens, 10.2k cached (82%)]`,
		`chat c: llm call 2 took D: text [1.0k tokens, 999 cached (99%), thought 2.0 KiB]`,
		`chat c: answered after D (llm calls: 2, tool calls: 0, sent 3.0 KiB, 13.3k tokens, 11.2k cached (84%)): "ok"`,
	})
}

// Of a tool call the log takes the arguments, and of those of write and edit
// the size of the content; of the result only its size. A text is cut after
// maxLogged bytes, where a character begins, and says how much is left out.
func TestHowMuchTextIsLogged(t *testing.T) {
	r := &record{}
	for _, tt := range []struct{ tool, args, want string }{
		{"write", `{"path":"~/a","content":"secret plan"}`, `{"content":"(11 bytes)","path":"~/a"}`},
		{"write", `{"path":"~/a","content":7}`, `{"content":"(1 bytes)","path":"~/a"}`},
		{"write", `{"path":"~/a"}`, `{"path":"~/a"}`},
		{"edit", `{"path":"~/a","old":"x","new":"yz"}`, `{"new":"(2 bytes)","old":"(1 bytes)","path":"~/a"}`},
		{"edit", `not json`, `(8 bytes)`},
		{"edit", `null`, `(4 bytes)`},
		{"read", `{"path":"~/a\nb"}`, `{"path":"~/a\nb"}`},
	} {
		if got := r.args(tt.tool, json.RawMessage(tt.args)); got != tt.want {
			t.Errorf("%s %s: %s, want %s", tt.tool, tt.args, got, tt.want)
		}
	}

	long := strings.Repeat("a", maxLogged-1) + "é" + "b"
	if got := r.text(long); got != `"`+strings.Repeat("a", maxLogged-1)+`"… (+3 bytes)` {
		t.Errorf("cut inside a character: %s", got)
	}
	if got := r.text(long[1 : maxLogged+1]); got != `"`+strings.Repeat("a", maxLogged-2)+"é\"" {
		t.Errorf("exactly at the limit: %s", got)
	}
	if got := r.plain(strings.Repeat("x\n", maxLogged)); got != strings.Repeat(`x\n`, maxLogged/2)+"… (+300 bytes)" {
		t.Errorf("plain: %s", got)
	}
	if got := r.text("a\r\nb"); got != `"a\r\nb"` {
		t.Errorf("one line: %s", got)
	}
}

// accessToken is what an access of the node signs in with, in these tests.
const accessToken = "tok-of-an-access-456"

// Neither the key of the API nor the key of the node is in the log, in any of
// the ways it is written, also not as part of an argument, an error, what the
// LLM says along with a tool call or an answer.
func TestNoSecretIsLogged(t *testing.T) {
	cfg := ownerConfig()
	key, _ := base64.StdEncoding.DecodeString(cfg.PrivateKey)
	seed := key[:32]
	leaks := []string{
		accessToken, accessToken + "-longer", cfg.APIKey, cfg.PrivateKey, base64.RawStdEncoding.EncodeToString(key), base64.URLEncoding.EncodeToString(key),
		base64.RawURLEncoding.EncodeToString(key), hex.EncodeToString(key), strings.ToUpper(hex.EncodeToString(key)),
		base64.StdEncoding.EncodeToString(seed), hex.EncodeToString(seed),
	}
	llm := &scriptLLM{}
	a, log := recorded(t, cfg, llm)
	var all []string
	for _, leak := range leaks {
		llm.saying = "using " + leak
		args, _ := json.Marshal(map[string]string{"text": "key " + leak, "why": "key " + leak})
		for _, msg := range []string{"tell " + leak, "!echo " + string(args), "!fail " + string(args)} {
			if _, err := a.Ask(context.Background(), "c", msg, nil); err != nil {
				t.Fatal(err)
			}
		}
		llm.failing = errors.New("refused " + leak)
		a.Ask(context.Background(), "c", "hi", nil)
		llm.failing = nil
		all = append(all, log.take()...)
	}
	text := strings.Join(all, "\n")
	for _, leak := range leaks {
		if strings.Contains(text, leak) {
			t.Fatalf("%s is in the log:\n%s", leak, text)
		}
	}
	// What is redacted is still in the log, as such.
	if strings.Count(text, redacted) < 9*len(leaks) || !strings.Contains(text, `tool echo {"text":"key [redacted]","why":"key [redacted]"}`) {
		t.Fatalf("log:\n%s", text)
	}

	// A key of the node that cannot be read is redacted as it is written.
	if got := longestFirst(secrets(config.Config{APIKey: "k", PrivateKey: "not base64!"})); strings.Join(got, ",") != "not base64!,k" {
		t.Fatalf("secrets %q", got)
	}
	if got := longestFirst(secrets(config.Config{})); len(got) != 0 {
		t.Fatalf("no secrets: %q", got)
	}
	// The longest first, and each once: none is left in part.
	if got := longestFirst([]string{"b", "abc", "", "ab", "a", "abc"}); strings.Join(got, ",") != "abc,ab,a,b" {
		t.Fatalf("longest first: %q", got)
	}
}

// A tool call that ended is no longer under way: canceling names only the
// calls that are, and of two of the same tool the one that ended.
func TestOnlyCallsUnderWayAreCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var lines []string
	r := newRecord(ctx, func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) }, "c", nil, "hi", func() {})
	step := func(tool, args string, done bool) {
		r.step(agent.Step{Tool: tool, Args: json.RawMessage(args), Done: done})
	}
	step("run", `{"n":1}`, false)
	step("echo", `{"n":1}`, false)
	step("echo", `{"n":2}`, false)
	step("echo", `{"n":2}`, false)
	step("read", `{"n":3}`, false)
	step("echo", `{"n":2}`, true) // the last one of the two that are alike
	step("echo", `{"n":9}`, true) // never started
	step("read", `{"n":3}`, true) // the last that started
	step("run", `{"n":1}`, true)  // the first that started
	cancel()
	r.end("", context.Canceled)
	var canceled []string
	for _, line := range lines {
		if strings.HasSuffix(line, ": canceled") {
			canceled = append(canceled, duration.ReplaceAllString(line, "${1}D"))
		}
	}
	equal(t, "under way", canceled, []string{
		`chat c: tool echo {"n":2} took D: canceled`,
		`chat c: tool echo {"n":1} took D: canceled`,
	})
}

// The context of a conversation is as long as what its last call of the LLM
// sent; one without a call has none.
func TestContext(t *testing.T) {
	a, _ := recorded(t, ownerConfig(), &scriptLLM{})
	if a.Context("c1") != 0 {
		t.Fatal("before a call")
	}
	if _, err := a.Ask(context.Background(), "c1", `!echo {"text":"hi"}`, nil); err != nil {
		t.Fatal(err)
	}
	first := a.Context("c1")
	if _, err := a.Ask(context.Background(), "c1", "more", nil); err != nil {
		t.Fatal(err)
	}
	if first < 1000 || a.Context("c1") <= first || a.Context("c2") != 0 {
		t.Fatalf("%d, then %d", first, a.Context("c1"))
	}
}

// usageLLM answers with what the API would say of the calls: of call n, 1000n
// tokens, of which cached[n-1] were cached; the calls beyond cached say
// nothing.
type usageLLM struct {
	cached []int
	calls  int
}

func (l *usageLLM) Complete(context.Context, string, []openai.Message, []openai.Tool) (openai.Message, error) {
	l.calls++
	if l.calls > len(l.cached) {
		return openai.Message{Content: "ok"}, nil
	}
	return openai.Message{Content: "ok", Usage: openai.Usage{Prompt: 1000 * l.calls, Cached: l.cached[l.calls-1]}}, nil
}

// The cache of a conversation is what the API said of its last call and of
// all its calls; a call it said nothing of does not count, and a conversation
// without a call has none.
func TestCache(t *testing.T) {
	a, _ := recorded(t, ownerConfig(), &usageLLM{cached: []int{0, 1500}})
	for _, text := range []string{"one", "two", "three"} {
		if _, err := a.Ask(context.Background(), "c1", text, nil); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := a.Cache("c1"), (Cache{Tokens: 2000, Cached: 1500, AllTokens: 3000, AllCached: 1500, Metrics: CallMetrics{Calls: 3, UnknownCalls: 3}}); got != want || a.Cache("c2") != (Cache{}) {
		t.Fatalf("%+v, want %+v; c2 %+v", got, want, a.Cache("c2"))
	}
}

// The contents of a conversation are what its next call of the LLM is sent:
// the system prompt, the tools and the conversation so far. Without an agent
// for the configuration there is the conversation alone.
func TestContents(t *testing.T) {
	a, _ := recorded(t, ownerConfig(), &scriptLLM{})
	if _, err := a.Ask(context.Background(), "c1", `!echo {"text":"hi"}`, nil); err != nil {
		t.Fatal(err)
	}
	c, err := a.Contents("c1")
	if err != nil || !strings.Contains(c.System, `"me"`) || len(c.Tools) != 1 || c.Tools[0].Function.Name != toolRun ||
		len(c.Messages) < 3 || c.Messages[0].Content != `!echo {"text":"hi"}` || len(c.Messages[1].ToolCalls) != 1 {
		t.Fatalf("%+v, %v", c, err)
	}
	b, _ := recorded(t, config.Config{}, &scriptLLM{})
	if err := b.Resume("c1", a.Conversation("c1")); err != nil {
		t.Fatal(err)
	}
	if err := b.Resume("c2", []byte("{")); err == nil {
		t.Fatal("what is no conversation cannot be resumed")
	}
	if c, err := b.Contents("c1"); !errors.Is(err, config.ErrIncomplete) || c.System != "" || c.Tools != nil || len(c.Messages) < 3 {
		t.Fatalf("%+v, %v", c, err)
	}
}

// In debug mode the log has what every call of the LLM sent and got, without
// secrets; off, it has none of it, from the next call on.
func TestDebugIsLogged(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Echo", strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":9}}`))
	}))
	defer srv.Close()
	cfg := ownerConfig()
	log := &logged{}
	debug := true
	a := New(func() (config.Config, error) { return cfg, nil }, Builder{
		LLM:      func(string) (agent.LLM, error) { return openai.New(srv.URL, accessToken), nil },
		Accesses: &accesses{secrets: []string{accessToken}},
		Tools:    func() []agent.Tool { return nil },
	}, log.log).WithDebug(func() bool { return debug })

	if _, err := a.Ask(context.Background(), "c1", "hi", nil); err != nil {
		t.Fatal(err)
	}
	lines := strings.Join(log.take(), "\n")
	for _, want := range []string{"chat c1: debug llm request: ", "chat c1: debug llm prefix: first request",
		"chat c1: debug llm response: HTTP 200", "X-Echo: [redacted]", `chat c1: debug llm answered: `, `usage {"prompt_tokens":9}`} {
		if !strings.Contains(lines, want) {
			t.Errorf("no %q in:\n%s", want, lines)
		}
	}
	if strings.Contains(lines, accessToken) {
		t.Errorf("the key in:\n%s", lines)
	}

	debug = false
	a.Ask(context.Background(), "c1", "hi", nil)
	if lines := strings.Join(log.take(), "\n"); strings.Contains(lines, "debug") {
		t.Errorf("debug off:\n%s", lines)
	}
}

// What the owner says while the answer is on its way is logged when the
// answer takes it, and the agent goes on with it.
func TestSteeringIsLogged(t *testing.T) {
	a, log := recorded(t, ownerConfig(), &scriptLLM{})
	queue := []string{"and this"}
	ctx := agent.WithSteering(context.Background(), func() []string {
		said := queue
		queue = nil
		return said
	})
	if got, err := a.Ask(ctx, "c1", `!echo {"text":"hi"}`, nil); err != nil || got != "said: and this" {
		t.Fatalf("got %q, %v", got, err)
	}
	equal(t, "steered", log.take(), []string{
		`chat c1: message from the owner: "!echo {\"text\":\"hi\"}"`,
		`chat c1: llm call 1 took D: tool calls: 1`,
		`chat c1: tool echo {"text":"hi"} took D: ok (2 bytes)`,
		`chat c1: tool run ` + scriptOf("echo", `{"text":"hi"}`) + ` took D: ok (2 bytes)`,
		`chat c1: message from the owner on the way: "and this"`,
		`chat c1: llm call 2 took D: text`,
		`chat c1: answered after D (llm calls: 2, tool calls: 2, sent N): "said: and this"`,
	})
}
