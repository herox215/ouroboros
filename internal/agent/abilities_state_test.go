package agent

import (
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
