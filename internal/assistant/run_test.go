package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ouroboros/internal/agent"
	"ouroboros/internal/config"
	"ouroboros/internal/diagnosis"
	"ouroboros/internal/openai"
	"ouroboros/internal/openai/openaitest"
	"ouroboros/internal/script"
)

// scripted is a node whose agents have run and two tools to call from it:
// who says whom the conversation is with, as far as the hops, and note takes
// the setting "prefix".
func scripted(t *testing.T) (config.Config, Builder) {
	t.Helper()
	who := agent.NewTool("who", "", nil, func(ctx context.Context, _ struct{}) (string, error) {
		return fmt.Sprintf("%s in %s", ctx.Value(asker{}), agent.ContextID(ctx)), nil
	})
	type settings struct {
		Prefix string `json:"prefix"`
	}
	type noteArgs struct {
		Text string `json:"text"`
	}
	note := agent.Configurable(agent.Tool{Name: "note", Schema: `{"type":"object"}`}, func(s settings) (agent.RunFunc, error) {
		return func(_ context.Context, raw json.RawMessage) (string, error) {
			var args noteArgs
			if json.Unmarshal(raw, &args) != nil || args.Text == "" {
				return "", errTest
			}
			return s.Prefix + args.Text, nil
		}, nil
	})
	cfg := config.Config{Access: openaitest.NewServerWithKey(t, ""), Model: "gpt-4o", Name: "me"}
	return cfg, Builder{LLM: overHTTP, Tools: func() []agent.Tool { return []agent.Tool{who, note} }}
}

// asker is the key under which a test says who asks.
type asker struct{}

// script is the message that makes the fake LLM run source.
func runs(source string) string {
	args, _ := json.Marshal(runArgs{Script: source})
	return "!" + toolRun + " " + string(args)
}

// result is what the fake LLM says after a tool ran: its result.
func result(t *testing.T, got string, err error) string {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	_, last, ok := strings.Cut(got, " last=")
	if !ok {
		t.Fatalf("no tool ran: %q", got)
	}
	return last
}

// The owner's agent is offered run and nothing else. A script calls its
// tools, under the conversation the script was asked for in, and the owner is
// shown every call the script makes.
func TestRunForTheOwner(t *testing.T) {
	cfg, b := scripted(t)
	a := New(func() (config.Config, error) { return cfg, nil }, b, discard)
	ctx := context.WithValue(context.Background(), asker{}, "the owner")
	var steps []string
	observe := func(s Step) { steps = append(steps, fmt.Sprintf("%s %s %v", s.Tool, s.Args, s.Done)) }

	got, err := a.Ask(ctx, "chat", runs(`return [who(), note({text: "a"}), tools.note({text: "b"}), typeof run, typeof tools.run, Object.keys(tools)]`), observe)
	if got := result(t, got, err); got != `["the owner in chat","a","b","undefined","undefined",["who","note"]]` {
		t.Fatalf("got %s", got)
	}
	call := `run {"script":"return [who(), note({text: \"a\"}), tools.note({text: \"b\"}), typeof run, typeof tools.run, Object.keys(tools)]"}`
	want := []string{call + " false", "who {} false", "who {} true", `note {"text":"a"} false`, `note {"text":"a"} true`,
		`note {"text":"b"} false`, `note {"text":"b"} true`, call + " true"}
	if strings.Join(steps, "\n") != strings.Join(want, "\n") {
		t.Fatalf("steps:\n%s\nwant:\n%s", strings.Join(steps, "\n"), strings.Join(want, "\n"))
	}
	// A tool on its own is no tool of the LLM: the fake one calls it in a script.
	steps = nil
	if got, err := a.Ask(ctx, "chat2", `!who {}`, observe); err != nil || !strings.HasSuffix(got, " last=the owner in chat2") || !strings.HasPrefix(steps[0], "run ") {
		t.Fatalf("a tool besides run: %q, %v, %q", got, err, steps)
	}
	if got, err := a.Ask(ctx, "chat2", "?system", nil); err != nil || !strings.Contains(got, scriptHint) {
		t.Fatalf("the prompt: %q, %v", got, err)
	}

	// A tool that fails throws; the error of a script is one of the tool run.
	got, err = a.Ask(ctx, "chat", runs(`try { note({}) } catch (e) { return "caught " + e.message }`), nil)
	if got := result(t, got, err); got != "caught test error" {
		t.Fatalf("caught: %s", got)
	}
	got, err = a.Ask(ctx, "chat", runs("who()\nnote({})"), nil)
	if got := result(t, got, err); got != "error: the script failed: Error: test error\n\tat script:2:5" {
		t.Fatalf("not caught: %q", got)
	}
	for args, want := range map[string]string{`{}`: `error: run needs "script"`, `{"script": ""}`: `error: run needs "script"`, `{"script": 1}`: "error: run: invalid arguments"} {
		got, err = a.Ask(ctx, "chat", "!"+toolRun+" "+args, nil)
		if got := result(t, got, err); got != want {
			t.Fatalf("%s: %q", args, got)
		}
	}

	// The tools of a script are the ones the owner left the agent, with their settings.
	cfg.Tools = &config.Tools{H2A: []config.Entry{{Tool: "note", Settings: json.RawMessage(`{"prefix": "> "}`)}}}
	got, err = a.Ask(ctx, "chat", runs(`return [note({text: "a"}), typeof who, Object.keys(tools)]`), nil)
	if got := result(t, got, err); got != `["> a","undefined",["note"]]` {
		t.Fatalf("with settings: %s", got)
	}
	// Without any, a script only computes.
	cfg.Tools = &config.Tools{H2A: []config.Entry{}}
	got, err = a.Ask(ctx, "chat", runs(`return [typeof note, Object.keys(tools), 6 * 7]`), nil)
	if got := result(t, got, err); got != `["undefined",[],42]` {
		t.Fatalf("no tools: %s", got)
	}
	// run is no tool to list.
	cfg.Tools = &config.Tools{H2A: config.Named(toolRun, "note")}
	if _, err := a.Ask(ctx, "chat", "hi", nil); err == nil || err.Error() != "tools.h2a: run is no tool to list: the agent always has it, and every tool listed here is a function of its scripts" {
		t.Fatalf("run listed: %v", err)
	}
}

// A peer runs scripts only when a2a gives it a tool, and a script has the
// tools of a2a and no others; explain_error is among them.
func TestRunForAPeer(t *testing.T) {
	cfg, b := scripted(t)
	ask := func(text string) string {
		t.Helper()
		bot, err := b.ForPeers(cfg)
		if err != nil {
			t.Fatal(err)
		}
		got, err := bot.Respond(context.WithValue(from("luna"), asker{}, "luna"), "c", text)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	b.Failures = diagnosis.Open(filepath.Join(t.TempDir(), "failures.json"))
	if got := ask(runs(`return 1`)); !strings.Contains(got, " turns=1 ") {
		t.Fatalf("by default: %q", got)
	}
	if got := ask("?system"); strings.Contains(got, toolRun) || strings.Contains(got, toolExplainError) {
		t.Fatalf("the prompt without tools: %q", got)
	}
	cfg.Tools = &config.Tools{A2A: []config.Entry{{Tool: "note", Settings: json.RawMessage(`{"prefix": "peer: "}`)}}}
	if got := ask(runs(`return [note({text: "a"}), typeof who, Object.keys(tools)]`)); !strings.HasSuffix(got, ` last=["peer: a","undefined",["note","explain_error"]]`) {
		t.Fatalf("listed: %q", got)
	}
	if got := ask("?system"); !strings.HasSuffix(got, " "+strings.TrimSpace(scriptHint)) {
		t.Fatalf("the prompt: %q", got)
	}
	cfg.Tools = &config.Tools{A2A: config.Named("who")}
	if got := ask(runs(`return who()`)); !strings.HasSuffix(got, " last=luna in c") {
		t.Fatalf("the conversation of a script: %q", got)
	}
	// The script looks up the failures of the peer it runs for, and of nobody else.
	if got := ask(runs(`return explain_error({id: "AAAA2222"})`)); !strings.Contains(got, ` last=No failed answer with the error ID AAAA2222`) {
		t.Fatalf("with failures: %q", got)
	}
	cfg.Tools = &config.Tools{A2A: config.Named("*")}
	if got := ask(runs(`return Object.keys(tools)`)); !strings.HasSuffix(got, ` last=["who","note","explain_error"]`) {
		t.Fatalf("every tool: %q", got)
	}
	cfg.Tools = &config.Tools{A2A: config.Named(toolRun)}
	if _, err := b.ForPeers(cfg); err == nil || !strings.HasPrefix(err.Error(), "tools.a2a: run is no tool to list") {
		t.Fatalf("run listed: %v", err)
	}
}

// What the LLM is told of run: every tool as a function, with its parameters,
// and the limits.
func TestRunDescription(t *testing.T) {
	tools := []agent.Tool{
		agent.NewTool("greet", "Greets a peer.", []agent.Param{{Name: "peer", Description: "who"}, {Name: "how", Optional: true}},
			func(context.Context, struct{}) (string, error) { return "", nil }),
		{Name: "delete", Description: "Deletes.", Schema: `{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`},
		{Name: "list_peers", Schema: `{"type":"object","properties":{}}`},
		{Name: "odd", Schema: `{"properties":{"value":{"description":"anything"}}}`},
		{Name: "broken", Schema: `not json`},
	}
	run := withRun(tools, script.NewSessions())
	if run.Name != "run" || run.Schema != `{"type":"object","properties":{"script":{"type":"string","description":"JavaScript"}},"required":["script"]}` {
		t.Fatalf("got %s: %s", run.Name, run.Schema)
	}
	want := "Everything you can do is a function here:\n" +
		"- greet({how?: string, peer: string}): Greets a peer.\n    peer: who\n" +
		"- tools.delete({path: string}): Deletes.\n" +
		"- list_peers()\n" +
		"- odd({value?: any})\n    value: anything\n" +
		"- broken()\n"
	if !strings.Contains(run.Description, want) {
		t.Fatalf("got:\n%s", run.Description)
	}
	if want := "Limits: 2m0s, 64 calls, 64 KiB of source; 16 KiB of print and 16 KiB of the value come back, " +
		"4 KiB each once the results of an answer have more than 64 KiB. What is left out is kept in printed and returned. "; !strings.Contains(run.Description, want) {
		t.Fatalf("the limits are not what the tool says: %s", run.Description)
	}
	if script.MaxCalls != 64 || script.MaxSource != 64<<10 || script.MaxResult != 16<<10 || script.MaxPrint != 16<<10 ||
		script.Tight != 4<<10 || agent.Budget != 64<<10 {
		t.Fatal("the limits changed: the README names them")
	}
	if out, err := run.Run(context.Background(), json.RawMessage(`{"script": "Object.keys(tools)"}`)); err != nil || out != `["greet","delete","list_peers","odd","broken"]` {
		t.Fatalf("run: %q, %v", out, err)
	}
	if none := withRun(nil, script.NewSessions()); !strings.Contains(none.Description, "a function here, and there is none: a script can only compute. ") {
		t.Fatalf("without tools:\n%s", none.Description)
	}
}

// The scripts of a conversation build on each other; another conversation
// starts with nothing, and so does a peer in a conversation of the same name.
func TestRunKeepsPerConversation(t *testing.T) {
	cfg, b := scripted(t)
	a := New(func() (config.Config, error) { return cfg, nil }, b, discard)
	ask := func(chat, source string) string {
		t.Helper()
		got, err := a.Ask(context.Background(), chat, runs(source), nil)
		return result(t, got, err)
	}
	if got := ask("one", `let found = note({text: "a"}); print("found it"); found.length`); got != "found it\n1\n[kept for the next script: found (string of 1 bytes)]" {
		t.Fatalf("first: %q", got)
	}
	if got := ask("one", `let found = found + note({text: "b"}); found`); got != "ab\n[kept for the next script: found (string of 2 bytes)]" {
		t.Fatalf("second: %q", got)
	}
	if got := ask("two", `typeof found`); got != "undefined" {
		t.Fatalf("another conversation: %q", got)
	}
	// An agent built anew for a changed configuration goes on with them.
	cfg.Model = "gpt-4o-mini"
	if got := ask("one", `found`); !strings.HasPrefix(got, "ab\n") {
		t.Fatalf("after a change: %q", got)
	}

	cfg.Tools = &config.Tools{A2A: config.Named("note")}
	p := b.Peers(func() (config.Config, error) { return cfg, nil }, discard)
	respond := func(source string) string {
		t.Helper()
		got, err := p.Respond(from("luna"), "one", runs(source))
		return result(t, got, err)
	}
	respond(`var mine = 1`)
	if got := respond(`[typeof found, mine]`); got != "[\"undefined\",1]\n[kept for the next script: mine (number)]" {
		t.Fatalf("a peer: %q", got)
	}
}

// scriptsLLM runs the scripts one after the other with run, and then answers.
type scriptsLLM []string

func (l *scriptsLLM) Complete(_ context.Context, _ string, msgs []openai.Message, _ []openai.Tool) (openai.Message, error) {
	if len(*l) == 0 {
		return openai.Message{Content: "done"}, nil
	}
	args, _ := json.Marshal(runArgs{Script: (*l)[0]})
	*l = (*l)[1:]
	return openai.Message{ToolCalls: []openai.ToolCall{{ID: "c", Function: openai.FunctionCall{Name: toolRun, Arguments: string(args)}}}}, nil
}

// Once the results of an answer are beyond the budget, a script gives back
// less of what it prints.
func TestRunGivesBackLessBeyondTheBudget(t *testing.T) {
	// 4 times 16000 and 1536 bytes: Budget exactly, which is not beyond it.
	var scripts scriptsLLM
	for i, n := range []int{16000, 16000, 16000, 16000, 1536, 16000, 16000} {
		scripts = append(scripts, fmt.Sprintf(`print("%c".repeat(%d))`, 'a'+i, n)) // none repeats another
	}
	a := agent.New(&scripts, "m", "sys", withRun(nil, script.NewSessions()))
	var printed []int
	if _, err := a.Run(context.Background(), "c1", "go", agent.Observer{Step: func(s agent.Step) {
		if s.Done {
			letter := string(rune('a' + len(printed)))
			printed = append(printed, len(s.Result)-len(strings.TrimLeft(s.Result, letter)))
		}
	}}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(printed, []int{16000, 16000, 16000, 16000, 1536, 16000, script.Tight}) {
		t.Fatalf("printed %v", printed)
	}
}

// A front end is told what the LLM says along with the tools it asks for,
// once for every call that asks for some: not what it answers with in the
// end, and nothing of a call that failed.
func TestRunTellsWhatTheLLMSaysAlongWithTools(t *testing.T) {
	llm := &scriptLLM{saying: "let me look"}
	a, _ := recorded(t, ownerConfig(), llm)
	var said []string
	say := func(text string) { said = append(said, text) }
	if _, err := a.Run(context.Background(), "c", `!echo {"text":"x"}`, nil, say); err != nil {
		t.Fatal(err)
	}
	if len(said) != 1 || said[0] != "let me look" {
		t.Fatalf("with a call of a tool: %q", said)
	}
	// An answer in text alone is the answer: it is not said on the way.
	if _, err := a.Run(context.Background(), "c", "hello", nil, say); err != nil || len(said) != 1 {
		t.Fatalf("with an answer alone: %q, %v", said, err)
	}
	llm.failing = errTest
	if _, err := a.Run(context.Background(), "c", `!echo {"text":"x"}`, nil, say); err == nil || len(said) != 1 {
		t.Fatalf("with a call that failed: %q, %v", said, err)
	}
}

// The first line of a text ends at its first line break, also when that is
// where the text begins.
func TestFirstLine(t *testing.T) {
	for in, want := range map[string]string{"one\ntwo": "one", "\nrest": "", "": "", "plain": "plain"} {
		if got := firstLine(in); got != want {
			t.Errorf("firstLine(%q) = %q, want %q", in, got, want)
		}
	}
}
