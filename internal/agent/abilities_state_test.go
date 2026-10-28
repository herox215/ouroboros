package agent

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

func TestAbilityInspectOnlyAndIdempotentLoad(t *testing.T) {
	var count atomic.Int32
	tools := abilityFixture(t, &count)
	a := New(nil, "model", "system", tools...)
	copy, ctx, runtime := a.abilityAnswer(t.Context(), "one")
	if _, err := LoadAbility(ctx, "fixture"); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAbility(ctx, "fixture"); err != nil {
		t.Fatal(err)
	}
	runtime.activate()
	if len(copy.specs) != 3 {
		t.Fatalf("duplicate pending activation: %d tools", len(copy.specs))
	}
	if _, err := LoadAbility(ctx, "fixture"); err != nil {
		t.Fatal(err)
	}
	runtime.activate()
	if len(copy.specs) != 3 {
		t.Fatal("duplicate loaded activation")
	}
	inspectOnly := New(nil, "model", "system", tools[1])
	_, ctx, _ = inspectOnly.abilityAnswer(t.Context(), "inspect")
	if _, err := InspectAbility(ctx, "fixture"); err != nil {
		t.Fatal(err)
	}
}

// A conversation remembers the abilities it loaded. Once the node no longer
// has one, or withholds one of its tools, the next answer grants nothing of
// it, and the prompt stays as it is.
func TestAbilityRememberedButGoneGrantsNothing(t *testing.T) {
	var count atomic.Int32
	tools := abilityFixture(t, &count)
	a := New(nil, "model", "system", tools[:2]...) // the tool secret is withheld
	a.memory.conversations["one"] = &conversation{abilities: []string{"removed", "fixture"}}
	copy, _, runtime := a.abilityAnswer(t.Context(), "one")
	runtime.activate()
	if len(copy.specs) != 2 || len(runtime.loaded) != 0 || copy.system.Content != "system" {
		t.Fatalf("granted: %d tools, loaded %v, system %q", len(copy.specs), runtime.loaded, copy.system.Content)
	}
}

// Only an ability of the catalog loads, and only within a conversation of
// the owner it is inspected.
func TestAbilityUnknownOrOutsideConversation(t *testing.T) {
	var count atomic.Int32
	a := New(nil, "model", "system", abilityFixture(t, &count)...)
	_, ctx, _ := a.abilityAnswer(t.Context(), "one")
	if _, err := LoadAbility(ctx, "missing"); err == nil || !strings.Contains(err.Error(), "unknown ability") {
		t.Fatalf("load: %v", err)
	}
	if _, err := InspectAbility(t.Context(), "fixture"); err == nil {
		t.Fatal("inspected outside a conversation")
	}
}

// Names are unique among all tools once a catalog is among them, and a
// configured tool of an ability stays the tool it was.
func TestAbilityExpansionRefusals(t *testing.T) {
	var count atomic.Int32
	catalog := abilityFixture(t, &count)[0].catalog
	loader := Tool{Name: "loader", AbilityCatalog: func() []Ability { return catalog }}
	if _, err := ExpandAbilities([]Tool{{Name: "same"}, {Name: "same"}, loader}); err == nil || !strings.Contains(err.Error(), `duplicate tool "same"`) {
		t.Fatalf("duplicate: %v", err)
	}
	refused := errors.New("refused")
	for _, tc := range []struct {
		name      string
		configure func(json.RawMessage) (Tool, error)
		want      string
	}{
		{"refused", func(json.RawMessage) (Tool, error) { return Tool{}, refused }, "refused"},
		{"renamed", func(json.RawMessage) (Tool, error) { return Tool{Name: "other"}, nil }, `changed name "secret"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			own := slices.Clone(catalog)
			own[0].Tools = slices.Clone(own[0].Tools)
			own[0].Tools[0].Configure = tc.configure
			expanded, err := ExpandAbilities([]Tool{{Name: "loader", AbilityCatalog: func() []Ability { return own }}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := expanded[1].Configure(json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("configure: %v", err)
			}
		})
	}
}
