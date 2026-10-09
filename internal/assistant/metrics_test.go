package assistant

import (
	"math"
	"ouroboros/internal/agent"
	"ouroboros/internal/openai"
	"testing"
	"time"
)

func TestCallMetrics(t *testing.T) {
	price := 0.0123
	c := Cache{}.withCall(agent.Call{Tokens: 100, Cached: 80, Output: 50, Took: 2 * time.Second, CostUSD: &price})
	if c.Metrics.TPS != 25 || c.Metrics.CostUSD != price || c.Metrics.AllCostUSD != price || !c.Metrics.CostKnown {
		t.Fatalf("%+v", c)
	}
	c = c.withCall(agent.Call{})
	if c.Metrics.TPS != 0 || c.Metrics.CostKnown || c.Metrics.UnknownCalls != 1 || c.Metrics.AllCostUSD != price {
		t.Fatalf("%+v", c)
	}
	c = c.withCall(agent.Call{InPlan: true, Output: 10})
	if !c.Metrics.InPlan || c.Metrics.PlanCalls != 1 || c.Metrics.Calls != 3 || c.Metrics.TPS != 0 {
		t.Fatalf("%+v", c)
	}
	if c = c.withCall(agent.Call{Via: "mid of B"}); c.Metrics.Via != "mid of B" {
		t.Fatalf("%+v", c)
	}
	if c = c.withCall(agent.Call{InPlan: true}); c.Metrics.Via != "" || c.Metrics.Calls != 5 {
		t.Fatalf("%+v", c)
	}
	zero := 0.0
	c = c.withCall(agent.Call{CostUSD: &zero})
	if !c.Metrics.CostKnown || c.Metrics.InPlan || c.Metrics.ChargedCalls != 2 || c.Metrics.PlanCalls != 2 {
		t.Fatalf("%+v", c)
	}
	for _, invalid := range []float64{-1, math.NaN(), math.Inf(1)} {
		got := Cache{}.withCall(agent.Call{CostUSD: &invalid})
		if got.Metrics.CostKnown || got.Metrics.UnknownCalls != 1 {
			t.Fatalf("%+v", got)
		}
	}

	// What the API took of the effort is of the last call.
	e := Cache{}.withCall(agent.Call{Effort: openai.Took{Asked: "max", Value: "high", Told: true}})
	if e.Metrics.Effort != "max" || e.Metrics.Took != "high" || !e.Metrics.Fell {
		t.Fatalf("%+v", e)
	}
	if e = e.withCall(agent.Call{Effort: openai.Took{Asked: "max", Value: "max", Told: true}}); e.Metrics.Took != "max" || e.Metrics.Fell {
		t.Fatalf("%+v", e)
	}
}
