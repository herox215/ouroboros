package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"ouroboros/internal/a2a"
	"ouroboros/internal/openai"
)

type abilityLLM func(context.Context, []openai.Message, []openai.Tool) (openai.Message, error)

func (f abilityLLM) Complete(ctx context.Context, _ string, msgs []openai.Message, tools []openai.Tool) (openai.Message, error) {
	return f(ctx, msgs, tools)
}
func abilityCall(name, args string) openai.Message {
	return openai.Message{ToolCalls: []openai.ToolCall{{ID: name, Function: openai.FunctionCall{Name: name, Arguments: args}}}}
}
func abilityFixture(t *testing.T, count *atomic.Int32) []Tool {
	t.Helper()
	hidden := NewTool("secret", "hidden fixture", nil, func(context.Context, struct{}) (string, error) { count.Add(1); return "fixture result", nil })
	loader := NewTool("ability_load", "load", []Param{{Name: "name"}}, func(ctx context.Context, args struct {
		Name string `json:"name"`
	}) (string, error) {
		return LoadAbility(ctx, args.Name)
	})
	loader.AbilityCatalog = func() []Ability {
		return []Ability{{Name: "fixture", Description: "short catalog", Instructions: "FIXTURE INSTRUCTIONS", Tools: []Tool{hidden}}}
	}
	inspect := NewTool("ability_inspect", "inspect", nil, func(ctx context.Context, _ struct{}) (string, error) { return InspectAbility(ctx, "fixture") })
	inspect.OwnerOnly = true
	tools, err := ExpandAbilities([]Tool{loader, inspect})
	if err != nil {
		t.Fatal(err)
	}
	return tools
}
func hasAbilityTool(tools []openai.Tool, name string) bool {
	for _, t := range tools {
		if t.Function.Name == name {
			return true
		}
	}
	return false
}

func TestAbilityLazyRoundBoundariesAndMemory(t *testing.T) {
	var count atomic.Int32
	tools := abilityFixture(t, &count)
	round := 0
	llm := abilityLLM(func(_ context.Context, msgs []openai.Message, specs []openai.Tool) (openai.Message, error) {
		round++
		loaded := hasAbilityTool(specs, "secret")
		if loaded != (round >= 5) {
			t.Fatalf("round %d: loaded %v", round, loaded)
		}
		switch round {
		case 1:
			return abilityCall("secret", `{}`), nil
		case 2:
			if !strings.Contains(msgs[len(msgs)-1].Content, "unknown tool") {
				t.Fatal("direct hidden call succeeded")
			}
			return abilityCall("ability_inspect", `{}`), nil
		case 3:
			if !strings.Contains(msgs[len(msgs)-1].Content, "FIXTURE INSTRUCTIONS") {
				t.Fatal("inspect missing instructions")
			}
			return abilityCall("secret", `{}`), nil
		case 4:
			m := abilityCall("ability_load", `{"name":"fixture"}`)
			m.ToolCalls = append(m.ToolCalls, abilityCall("secret", `{}`).ToolCalls...)
			return m, nil
		case 5:
			if !strings.Contains(msgs[0].Content, "FIXTURE INSTRUCTIONS") {
				t.Fatal("loaded instructions missing from system")
			}
			if !strings.Contains(msgs[len(msgs)-1].Content, "unknown tool") {
				t.Fatal("same-batch bypass")
			}
			return abilityCall("secret", `{}`), nil
		case 6:
			if msgs[len(msgs)-1].Content != "fixture result" {
				t.Fatalf("result: %+v", msgs[len(msgs)-1])
			}
		}
		return openai.Message{Content: "done"}, nil
	})
	a := New(llm, "model", "system", tools...)
	if _, err := a.Respond(t.Context(), "one", "test"); err != nil {
		t.Fatal(err)
	}
	if count.Load() != 1 {
		t.Fatalf("executions: %d", count.Load())
	}
	if _, err := a.Respond(t.Context(), "one", "continue"); err != nil {
		t.Fatal(err)
	}
	_, specs := a.Prompt()
	if hasAbilityTool(specs, "secret") {
		t.Fatal("template was mutated")
	}
	history := a.memory.Conversation("one")
	a.memory.Restore("restored", history)
	a.llm = abilityLLM(func(_ context.Context, _ []openai.Message, specs []openai.Tool) (openai.Message, error) {
		if hasAbilityTool(specs, "secret") {
			t.Fatal("history restored a grant")
		}
		return openai.Message{Content: "reload first"}, nil
	})
	if _, err := a.Respond(t.Context(), "restored", "continue"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Respond(t.Context(), "two", "continue"); err != nil {
		t.Fatal(err)
	}
}

func TestAbilityConcurrentIsolation(t *testing.T) {
	var count atomic.Int32
	tools := abilityFixture(t, &count)
	var mu sync.Mutex
	rounds := map[string]int{}
	a := New(abilityLLM(func(ctx context.Context, _ []openai.Message, specs []openai.Tool) (openai.Message, error) {
		id := ContextID(ctx)
		mu.Lock()
		rounds[id]++
		round := rounds[id]
		mu.Unlock()
		if id == "load" && round == 1 {
			return abilityCall("ability_load", `{"name":"fixture"}`), nil
		}
		if hasAbilityTool(specs, "secret") != (id == "load") {
			t.Errorf("cross-conversation grant for %s", id)
		}
		return openai.Message{Content: "done"}, nil
	}), "model", "system", tools...)
	var wg sync.WaitGroup
	for _, id := range []string{"load", "other"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := a.Respond(t.Context(), id, "test"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
}

func TestAbilityDeniedToolsAndPeer(t *testing.T) {
	var count atomic.Int32
	tools := abilityFixture(t, &count)
	// Keep the loader but withhold the hidden tool as an explicit allowlist does.
	a := New(abilityLLM(func(_ context.Context, _ []openai.Message, _ []openai.Tool) (openai.Message, error) {
		return openai.Message{Content: "done"}, nil
	}), "model", "system", tools[:2]...)
	_, ctx, _ := a.abilityAnswer(t.Context(), "one")
	if _, err := LoadAbility(ctx, "fixture"); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("load: %v", err)
	}
	if _, err := LoadAbility(a2a.WithOrigin(ctx, a2a.Origin{}), "fixture"); err == nil {
		t.Fatal("peer loaded ability")
	}
	if _, err := LoadAbility(t.Context(), "fixture"); err == nil {
		t.Fatal("load outside conversation")
	}
	if _, err := InspectAbility(ctx, "missing"); err == nil {
		t.Fatal("unknown ability")
	}
}

func TestAbilityNamespaceValidation(t *testing.T) {
	var count atomic.Int32
	base := abilityFixture(t, &count)
	catalog := base[0].catalog
	for _, tc := range []struct {
		name  string
		tools []Tool
	}{
		{"ordinary collision", []Tool{{Name: "secret"}, {Name: "loader", AbilityCatalog: func() []Ability { return catalog }}}},
		{"duplicate ability", []Tool{{Name: "loader", AbilityCatalog: func() []Ability { return append(catalog, catalog...) }}}},
		{"invalid name", []Tool{{Name: "loader", AbilityCatalog: func() []Ability { return []Ability{{Name: "bad name"}} }}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ExpandAbilities(tc.tools); err == nil {
				t.Fatal("accepted invalid catalog")
			}
		})
	}
	// Custom configuration must not discard the private lazy marker.
	catalog[0].Tools[0].Configure = func(json.RawMessage) (Tool, error) {
		return Tool{Name: "secret", Schema: `{}`, Run: func(context.Context, json.RawMessage) (string, error) { return "configured", nil }}, nil
	}
	expanded, err := ExpandAbilities([]Tool{{Name: "loader", AbilityCatalog: func() []Ability { return catalog }}})
	if err != nil {
		t.Fatal(err)
	}
	configured, err := expanded[1].Configure(json.RawMessage(`{}`))
	if err != nil || configured.ability == nil || !configured.OwnerOnly {
		t.Fatalf("configuration lost lazy safety: %+v %v", configured, err)
	}
}
