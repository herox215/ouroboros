package assistant

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"ouroboros/internal/agent"
	"ouroboros/internal/config"
	"ouroboros/internal/openai/openaitest"
)

// The yolo mode turns off the rules of the owner that would ask them before
// a tool call: the call runs and nobody is asked.
func TestYoloStopsPermission(t *testing.T) {
	cfg := config.Config{Tools: &config.Tools{Ask: &[]config.Rule{{Match: ".*"}}}}
	// run calls a tool that a rule matches, as the owner answers it with
	// answer, and reports how often the owner was asked and the tool ran.
	run := func(yolo bool, answer string) (asked, runs int, err error) {
		tool := agent.Tool{Name: ToolBash, Run: func(context.Context, json.RawMessage) (string, error) {
			runs++
			return "done", nil
		}}
		var questions []Question
		guarded := (Builder{}).permitted(cfg, []agent.Tool{tool}, yolo)[0]
		_, err = guarded.Run(answering(answer, &questions), json.RawMessage(`{"command": "rm -rf build"}`))
		return len(questions), runs, err
	}
	// Without yolo the owner is asked, and their no stops the call.
	if asked, runs, err := run(false, AllowNot); asked != 1 || runs != 0 || err == nil {
		t.Fatalf("without yolo: %d asked, %d runs, %v", asked, runs, err)
	}
	// With yolo the call runs at once, and the owner is not asked at all.
	if asked, runs, err := run(true, AllowNot); asked != 0 || runs != 1 || err != nil {
		t.Fatalf("with yolo: %d asked, %d runs, %v", asked, runs, err)
	}
}

// The yolo mode takes nothing else away: the agent still asks the owner a
// question, and still proposes a change of the configuration that waits for
// their answer.
func TestYoloKeepsQuestions(t *testing.T) {
	cfg := config.Config{Access: openaitest.Key, Model: "gpt-4o", Name: "me", Tools: &config.Tools{H2A: config.Named(config.AllTools)}}
	// With a node, the agent also has the tools of the configuration, of
	// which one waits for the owner to allow a change.
	b := Builder{LLM: overHTTP, Tools: func() []agent.Tool {
		return PeerTools(nil, func() string { return "" })
	}}.WithConfig(func() (config.Config, error) { return cfg, nil }, nil)
	names := func(bot *agent.Agent) []string {
		_, specs := bot.Prompt()
		var out []string
		for _, s := range specs {
			out = append(out, s.Function.Name)
		}
		return out
	}

	plain, err := b.forOwner(cfg, false)
	if err != nil {
		t.Fatal(err)
	}
	yolo, err := b.forOwner(cfg, true)
	if err != nil {
		t.Fatal(err)
	}
	got := names(yolo)
	for _, name := range []string{ToolUserQuestion, toolProposeConfig, ToolDelegate} {
		if !slices.Contains(got, name) {
			t.Errorf("yolo mode takes %s away: %v", name, got)
		}
	}
	prompt, _ := yolo.Prompt()
	if !strings.Contains(prompt, yoloHint) || !strings.Contains(prompt, questionHint) {
		t.Errorf("the prompt of the yolo mode:\n%s", prompt)
	}
	if prompt, _ := plain.Prompt(); strings.Contains(prompt, yoloHint) || !strings.Contains(prompt, questionHint) {
		t.Errorf("the prompt without yolo:\n%s", prompt)
	}
}

// The mode holds from the next agent on: current asks the switch anew, and
// builds the agent anew when it changed, so that the rules are off while it
// is on and back after.
func TestYoloRebuildsTheAgent(t *testing.T) {
	cfg := config.Config{Access: openaitest.Key, Model: "gpt-4o", Name: "me", Tools: &config.Tools{H2A: config.Named(config.AllTools)}}
	yolo := false
	b := Builder{LLM: overHTTP, Tools: func() []agent.Tool {
		return PeerTools(nil, func() string { return "" })
	}}.WithConfig(func() (config.Config, error) { return cfg, nil }, nil)
	a := New(func() (config.Config, error) { return cfg, nil }, b, func(string, ...any) {}).WithYolo(func() bool { return yolo })

	prompt := func() string {
		bot, _, err := a.current(false, "")
		if err != nil {
			t.Fatal(err)
		}
		text, _ := bot.Prompt()
		return text
	}
	if plain := prompt(); strings.Contains(plain, yoloHint) {
		t.Fatalf("the prompt without yolo names it:\n%s", plain)
	}
	yolo = true
	if on := prompt(); !strings.Contains(on, yoloHint) {
		t.Fatalf("the prompt with yolo does not name it:\n%s", on)
	}
	yolo = false
	if plain := prompt(); strings.Contains(plain, yoloHint) {
		t.Fatalf("the prompt after yolo still names it:\n%s", plain)
	}
}
