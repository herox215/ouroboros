package tui

import (
	"ouroboros/internal/assistant"
	"ouroboros/plugin"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
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
		{assistant.CallMetrics{Calls: 1, UnknownCalls: 1, Via: "mid of Ollama"}, "answered by the fallback mid of Ollama · TPS unknown · cost unknown"},
	} {
		if got := callMetrics(plugin.SessionCache{Metrics: tc.m}); got != tc.want {
			t.Fatalf("%q, want %q", got, tc.want)
		}
	}
	m := New(&fakeBackend{status: Status{Configured: true, Running: true, Name: "alpha"}}, nil, nil)
	m.status = Status{Configured: true, Running: true, Name: "alpha"}
	m.chat.entries = []entry{{text: "hello"}}
	m.chat.cache.Metrics = assistant.CallMetrics{Calls: 1, TPS: 42, InPlan: true}
	m.width, m.height = 160, 40
	if got := m.footerView(); !strings.Contains(got, "42.0 TPS") || !strings.Contains(got, "included in plan") {
		t.Fatalf("%s", got)
	}
	if got := m.context.view(m); !strings.Contains(got, "42.0 TPS · cost included in plan") {
		t.Fatalf("%s", got)
	}
}

// TPS stays in the chat metrics, not duplicated beside the model in the header.
func TestHeaderOmitsTPS(t *testing.T) {
	m, b := newModel(t)
	b.status.Running, b.status.Model, b.status.Effort = true, "flash", "high"
	m.Update(m.refreshStatus())
	if head := ansi.Strip(m.headerView()); strings.Contains(head, "TPS") {
		t.Fatalf("before a call: %s", head)
	}
	b.cache = plugin.SessionCache{Metrics: assistant.CallMetrics{Calls: 1, Output: 327, TPS: 104.52}}
	answered(t, m, say(t, m, "hi"))
	if head := ansi.Strip(m.headerView()); strings.Contains(head, "TPS") || !strings.HasSuffix(head, " · flash · effort high") {
		t.Fatalf("after a call: %s", head)
	}
	drive(t, m, run(m, "/new"), func() bool { return true })
	if head := ansi.Strip(m.headerView()); strings.Contains(head, "TPS") {
		t.Fatalf("a new session: %s", head)
	}
}

// Where the API took less of the effort than was chosen, or something else,
// the header says what it took at the last call: once that call was made
// with the effort chosen now.
func TestHeaderShowsTheEffortTaken(t *testing.T) {
	m, b := newModel(t)
	b.status.Running, b.status.Model, b.status.Effort = true, "flash", "max"
	m.Update(m.refreshStatus())
	b.cache = plugin.SessionCache{Metrics: assistant.CallMetrics{Calls: 1, Effort: "max", Took: "high", Fell: true}}
	answered(t, m, say(t, m, "hi"))
	if head := ansi.Strip(m.headerView()); !strings.HasSuffix(head, " · flash · effort max → high") {
		t.Fatalf("taken as less: %s", head)
	}
	m.chat.cache.Metrics.Took = ""
	if head := ansi.Strip(m.headerView()); !strings.HasSuffix(head, "· effort max → not taken") {
		t.Fatalf("not taken: %s", head)
	}
	// Chosen anew, it says nothing until a call was made with it.
	m.chat.effort, m.chat.efforted = "low", true
	if head := ansi.Strip(m.headerView()); !strings.HasSuffix(head, "· effort low") {
		t.Fatalf("chosen anew: %s", head)
	}
	// Taken as it was sent, it says nothing more.
	m.chat.effort = "max"
	m.chat.cache.Metrics.Took, m.chat.cache.Metrics.Fell = "xhigh", false
	if head := ansi.Strip(m.headerView()); !strings.HasSuffix(head, "· effort max") {
		t.Fatalf("taken: %s", head)
	}
}
