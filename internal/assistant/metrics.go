package assistant

import (
	"math"
	"ouroboros/internal/agent"
)

// CallMetrics describes the last model call and totals since this node started.
// TPS includes request latency, but never time spent executing tools.
// A missing charge is not a zero charge. Partial totals are explicitly marked.
type CallMetrics struct {
	Output       int     `json:"output,omitempty"`
	TPS          float64 `json:"tps,omitempty"`
	CostUSD      float64 `json:"cost_usd,omitempty"`
	CostKnown    bool    `json:"cost_known,omitempty"`
	InPlan       bool    `json:"in_plan,omitempty"`
	AllCostUSD   float64 `json:"all_cost_usd,omitempty"`
	ChargedCalls int     `json:"charged_calls,omitempty"`
	UnknownCalls int     `json:"unknown_calls,omitempty"`
	PlanCalls    int     `json:"plan_calls,omitempty"`
	Calls        int     `json:"calls,omitempty"`
	// Via is what answered the last call instead of the access and the
	// model of the agent, "" if those did: see config.Config.Fallbacks.
	Via string `json:"via,omitempty"`
}

func (c Cache) withCall(call agent.Call) Cache {
	if call.Tokens > 0 {
		c = c.add(call.Tokens, call.Cached)
	}
	m := c.Metrics
	m.Calls++
	m.Output, m.TPS = call.Output, 0
	if call.Output > 0 && call.Took > 0 {
		m.TPS = float64(call.Output) / call.Took.Seconds()
	}
	m.InPlan, m.Via = call.InPlan, call.Via
	m.CostKnown, m.CostUSD = false, 0
	switch {
	case call.InPlan:
		m.PlanCalls++
	case call.CostUSD != nil && *call.CostUSD >= 0 && !math.IsNaN(*call.CostUSD) && !math.IsInf(*call.CostUSD, 0):
		m.CostKnown, m.CostUSD = true, *call.CostUSD
		m.AllCostUSD += m.CostUSD
		m.ChargedCalls++
	default:
		m.UnknownCalls++
	}
	c.Metrics = m
	return c
}
