package assistant

import "testing"

func TestRestoreStatistics(t *testing.T) {
	a, _ := recorded(t, ownerConfig(), &scriptLLM{})
	want := Cache{Tokens: 1000, Cached: 900, AllTokens: 3000, AllCached: 2500, Metrics: CallMetrics{Calls: 3, Output: 120}}
	a.RestoreStatistics("continued", 4321, want)
	if a.Cache("continued") != want || a.Context("continued") != 4321 {
		t.Fatal("statistics not restored")
	}
	if a.Cache("other") != (Cache{}) || a.Context("other") != 0 {
		t.Fatal("statistics leaked into another conversation")
	}
	a.sizing.Lock()
	a.caches["continued"] = a.caches["continued"].add(500, 400)
	a.sizing.Unlock()
	got := a.Cache("continued")
	if got.AllTokens != 3500 || got.AllCached != 2900 || got.Tokens != 500 || got.Cached != 400 {
		t.Fatalf("totals did not continue: %+v", got)
	}
}
