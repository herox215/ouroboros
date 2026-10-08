package tui

import (
	"ouroboros/internal/assistant"
	"ouroboros/plugin"
	"strings"
	"testing"
)

func TestCallMetricsDisplay(t *testing.T) {
	for _, tc := range []struct {
		m    assistant.CallMetrics
		want string
	}{
		{assistant.CallMetrics{}, ""},
		{assistant.CallMetrics{Calls: 1, TPS: 25, InPlan: true}, "25.0 TPS · cost included in plan"},
		{assistant.CallMetrics{Calls: 1}, "TPS unknown · cost unknown"},
		{assistant.CallMetrics{Calls: 2, TPS: 10, CostKnown: true, CostUSD: 0.01, AllCostUSD: 0.02, ChargedCalls: 1, UnknownCalls: 1}, "10.0 TPS · cost $0.0100 · session $0.0200 (partial)"},
		{assistant.CallMetrics{Calls: 1, CostKnown: true, ChargedCalls: 1}, "TPS unknown · cost $0.0000 · session $0.0000"},
		{assistant.CallMetrics{Calls: 2, InPlan: true, AllCostUSD: 0.5, ChargedCalls: 1, PlanCalls: 1}, "TPS unknown · cost included in plan · session $0.5000 + plan"},
		{assistant.CallMetrics{Calls: 2, InPlan: true, UnknownCalls: 1, PlanCalls: 1}, "TPS unknown · cost included in plan · session partial"},
		{assistant.CallMetrics{Calls: 1, UnknownCalls: 1}, "TPS unknown · cost unknown"},
	} {
		if got := callMetrics(plugin.SessionCache{Metrics: tc.m}); got != tc.want {
			t.Fatalf("%q, want %q", got, tc.want)
		}
	}
	m := New(&fakeBackend{status: Status{Configured: true, Running: true, Name: "alpha"}}, nil, nil)
	m.status = Status{Configured: true, Running: true, Name: "alpha"}
	m.chat.cache.Metrics = assistant.CallMetrics{Calls: 1, TPS: 42, InPlan: true}
	m.width, m.height = 160, 40
	if got := m.chatView(); !strings.Contains(got, "42.0 TPS") || !strings.Contains(got, "included in plan") {
		t.Fatalf("%s", got)
	}
	if got := m.contextView(); !strings.Contains(got, "42.0 TPS · cost included in plan") {
		t.Fatalf("%s", got)
	}
}
