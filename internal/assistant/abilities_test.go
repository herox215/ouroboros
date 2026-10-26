package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"ouroboros/internal/agent"
	"ouroboros/internal/config"
	"ouroboros/internal/openai"
)

type abilityTestLLM func([]openai.Message, []openai.Tool) openai.Message

func (f abilityTestLLM) Complete(_ context.Context, _ string, msgs []openai.Message, tools []openai.Tool) (openai.Message, error) {
	return f(msgs, tools), nil
}

func TestAbilityAllowlistSettingsAndPermissions(t *testing.T) {
	hidden := agent.Configurable(agent.Tool{Name: "ability_fixture", Schema: `{"type":"object"}`}, func(s struct {
		Prefix string `json:"prefix"`
	}) (agent.RunFunc, error) { return func(context.Context, json.RawMessage) (string, error) { return s.Prefix + "result", nil }, nil })
	loader := agent.NewTool("ability_load", "load", nil, func(ctx context.Context, _ struct{}) (string, error) { return agent.LoadAbility(ctx, "fixture") })
	loader.OwnerOnly = true
	loader.AbilityCatalog = func() []agent.Ability { return []agent.Ability{{Name: "fixture", Tools: []agent.Tool{hidden}}} }
	all := []agent.Tool{loader}
	for _, of := range []audience{forPeers, forOnePeer} {
		if _, err := pick(all, []config.Entry{{Tool: "ability_fixture"}}, of); err == nil {
			t.Fatal("peer allowed hidden owner tool")
		}
		if got, err := pick(all, []config.Entry{{Tool: config.AllTools}}, of); err != nil || len(got) != 0 {
			t.Fatalf("peer tools: %+v %v", got, err)
		}
	}
	for _, mode := range []string{"configured", "withheld", "permission denied"} {
		restricted := mode == "withheld"
		denied := mode == "permission denied"
		t.Run(mode, func(t *testing.T) {
			entries := []config.Entry{{Tool: "ability_load"}}
			if !restricted {
				entries = append(entries, config.Entry{Tool: "ability_fixture", Settings: json.RawMessage(`{"prefix":"configured "}`)})
			}
			picked, err := pick(all, entries, forOwner)
			if err != nil {
				t.Fatal(err)
			}
			if len(forHelpers(picked)) != 0 {
				t.Fatal("helpers received ability tools")
			}
			round := 0
			llm := abilityTestLLM(func(msgs []openai.Message, specs []openai.Tool) openai.Message {
				round++
				switch round {
				case 1:
					for _, s := range specs {
						if s.Function.Name == "ability_fixture" {
							t.Fatal("eager tool")
						}
					}
					return openai.Message{ToolCalls: []openai.ToolCall{{ID: "load", Function: openai.FunctionCall{Name: "ability_load", Arguments: `{}`}}}}
				case 2:
					if restricted {
						if !strings.Contains(msgs[len(msgs)-1].Content, "not allowed") {
							t.Fatalf("load bypass: %+v", msgs)
						}
						return openai.Message{Content: "done"}
					}
					return openai.Message{ToolCalls: []openai.ToolCall{{ID: "fixture", Function: openai.FunctionCall{Name: "ability_fixture", Arguments: `{}`}}}}
				case 3:
					if denied {
						if !strings.Contains(msgs[len(msgs)-1].Content, "needs the leave of the owner") {
							t.Fatalf("permission bypass: %+v", msgs[len(msgs)-1])
						}
					} else if msgs[len(msgs)-1].Content != "configured result" {
						t.Fatalf("settings not applied: %+v", msgs[len(msgs)-1])
					}
				}
				return openai.Message{Content: "done"}
			})
			cfg := config.Config{}
			if denied {
				cfg.Tools = &config.Tools{Ask: &[]config.Rule{{Tool: "ability_fixture", Match: ".*"}}}
			}
			guarded := (Builder{}).permitted(cfg, picked)
			a := agent.New(llm, "model", "system", guarded...)
			if _, err := a.Respond(t.Context(), "one", "test"); err != nil {
				t.Fatal(err)
			}
		})
	}
}
