package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"ouroboros/internal/agent"
	"ouroboros/internal/config"
	"ouroboros/internal/diagnosis"
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
	cfg := config.Config{BaseURL: openaitest.NewServerWithKey(t, ""), Model: "gpt-4o", Name: "me"}
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

// The owner's agent runs a script that calls its other tools, under the
// conversation the script was asked for in, and the owner is shown every call
// the script makes like one the LLM made itself.
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

	// A tool that fails throws; the error of a script is one of the tool run.
	got, err = a.Ask(ctx, "chat", runs(`try { note({}) } catch (e) { return "caught " + e.message }`), nil)
	if got := result(t, got, err); got != "caught test error" {
		t.Fatalf("caught: %s", got)
	}
	got, err = a.Ask(ctx, "chat", runs("who()\nnote({})"), nil)
	if got := result(t, got, err); got != "error: the script failed: Error: test error\n\tat script:2:5\n\tat script:3:3" {
		t.Fatalf("not caught: %q", got)
	}
	for _, args := range []string{`{}`, `{"script": ""}`, `{"script": 1}`} {
		got, err = a.Ask(ctx, "chat", "!"+toolRun+" "+args, nil)
		if got := result(t, got, err); got != `error: run needs "script"` {
			t.Fatalf("%s: %q", args, got)
		}
	}

	// The tools of a script are the ones the owner left the agent, with their settings.
	cfg.Tools = &config.Tools{H2A: []config.Entry{{Tool: toolRun}, {Tool: "note", Settings: json.RawMessage(`{"prefix": "> "}`)}}}
	got, err = a.Ask(ctx, "chat", runs(`return [note({text: "a"}), typeof who, Object.keys(tools)]`), nil)
	if got := result(t, got, err); got != `["> a","undefined",["note"]]` {
		t.Fatalf("with settings: %s", got)
	}
	cfg.Tools = &config.Tools{H2A: config.Named(toolRun)}
	got, err = a.Ask(ctx, "chat", runs(`return [typeof note, Object.keys(tools)]`), nil)
	if got := result(t, got, err); got != `["undefined",[]]` {
		t.Fatalf("run alone: %s", got)
	}
	// Without run in the list there is no run.
	cfg.Tools = &config.Tools{H2A: config.Named("who", "note")}
	if got, err := a.Ask(ctx, "chat2", runs(`return 1`), nil); err != nil || !strings.HasSuffix(got, " turns=1 last="+runs(`return 1`)) {
		t.Fatalf("without run: %q, %v", got, err)
	}
}

// For a peer run is off unless it is listed, and a script has the tools of
// a2a and no others; explain_error is among them as it is among the tools of
// the agent.
func TestRunForAPeer(t *testing.T) {
	cfg, b := scripted(t)
	ask := func(source string) string {
		t.Helper()
		bot, err := b.ForPeers(cfg)
		if err != nil {
			t.Fatal(err)
		}
		got, err := bot.Respond(context.WithValue(from("luna"), asker{}, "luna"), "c", runs(source))
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := ask(`return who()`); !strings.Contains(got, " turns=1 ") {
		t.Fatalf("by default: %q", got)
	}
	cfg.Tools = &config.Tools{A2A: config.Named("who", "note")}
	if got := ask(`return who()`); !strings.Contains(got, " turns=1 ") {
		t.Fatalf("not listed: %q", got)
	}
	cfg.Tools = &config.Tools{A2A: []config.Entry{{Tool: toolRun}, {Tool: "note", Settings: json.RawMessage(`{"prefix": "peer: "}`)}}}
	if got := ask(`return [note({text: "a"}), typeof who, Object.keys(tools)]`); !strings.HasSuffix(got, ` last=["peer: a","undefined",["note"]]`) {
		t.Fatalf("listed: %q", got)
	}
	cfg.Tools = &config.Tools{A2A: config.Named(toolRun, "who")}
	if got := ask(`return who()`); !strings.HasSuffix(got, " last=luna in c") {
		t.Fatalf("the conversation of a script: %q", got)
	}

	// With the failures of the node, the script looks them up for the peer
	// it runs for, and for nobody else.
	b.Failures = diagnosis.Open(filepath.Join(t.TempDir(), "failures.json"))
	if got := ask(`return [Object.keys(tools), explain_error({id: "AAAA2222"})]`); !strings.Contains(got, ` last=[["who","explain_error"],"No failed answer with the error ID AAAA2222`) {
		t.Fatalf("with failures: %q", got)
	}
	cfg.Tools = &config.Tools{A2A: config.Named("*")}
	if got := ask(`return Object.keys(tools)`); !strings.HasSuffix(got, ` last=["who","note","explain_error"]`) {
		t.Fatalf("every tool: %q", got)
	}
}

// What the LLM is told about run, and what run is before an agent has it.
func TestRunSlot(t *testing.T) {
	slot := runSlot()
	if slot.Name != "run" || slot.Schema != `{"type":"object","properties":{"script":{"type":"string","description":"the body of a JavaScript function"}},"required":["script"]}` {
		t.Fatalf("got %s: %s", slot.Name, slot.Schema)
	}
	if want := "A script may take 1m0s, call 64 tools, have 64 KiB and return 64 KiB. "; !strings.Contains(slot.Description, want) {
		t.Fatalf("the limits are not what the tool says: %s", slot.Description)
	}
	if script.MaxCalls != 64 || script.MaxSource != 64<<10 || script.MaxResult != 64<<10 {
		t.Fatal("the limits changed: the README names them")
	}
	if got, err := slot.Run(context.Background(), json.RawMessage(`{"script": "return 1"}`)); err == nil || got != "" || err.Error() != "run has no tools to run a script with" {
		t.Fatalf("run without an agent: %q, %v", got, err)
	}
	// Tools without run stay as they are.
	tools := []agent.Tool{{Name: "a"}, {Name: "b"}}
	if got := withRun(tools); len(got) != 2 || &got[0] != &tools[0] {
		t.Fatalf("got %+v", got)
	}
	// The list that is handed in is not changed.
	tools = []agent.Tool{{Name: "a"}, slot, {Name: "b"}}
	got := withRun(tools)
	if names(got) != "a,run,b" || got[1].Description != slot.Description || got[1].Schema != slot.Schema {
		t.Fatalf("got %+v", got)
	}
	if out, err := tools[1].Run(context.Background(), json.RawMessage(`{"script": "return 1"}`)); err == nil || out != "" {
		t.Fatalf("the slot that was handed in runs: %q, %v", out, err)
	}
	if out, err := got[1].Run(context.Background(), json.RawMessage(`{"script": "return Object.keys(tools)"}`)); err != nil || out != `["a","b"]` {
		t.Fatalf("run: %q, %v", out, err)
	}
}
