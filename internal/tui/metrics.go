package tui

import (
	"fmt"
	"ouroboros/plugin"
	"strings"
)

func callMetrics(c plugin.SessionCache) string {
	m := c.Metrics
	if m.Calls == 0 {
		return ""
	}
	tps := "TPS unknown"
	if m.TPS > 0 {
		tps = fmt.Sprintf("%.1f TPS", m.TPS)
	}
	cost := "cost unknown"
	switch {
	case m.InPlan:
		cost = "cost included in plan"
	case m.CostKnown:
		cost = fmt.Sprintf("cost $%.4f", m.CostUSD)
	}
	parts := []string{tps, cost}
	if m.Via != "" {
		parts = append([]string{"answered by the fallback " + m.Via}, parts...)
	}
	if m.ChargedCalls > 0 {
		total := fmt.Sprintf("session $%.4f", m.AllCostUSD)
		if m.UnknownCalls > 0 {
			total += " (partial)"
		}
		if m.PlanCalls > 0 {
			total += " + plan"
		}
		parts = append(parts, total)
	} else if m.UnknownCalls > 0 && m.InPlan {
		parts = append(parts, "session partial")
	}
	return strings.Join(parts, " · ")
}
