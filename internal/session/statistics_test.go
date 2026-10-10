package session

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"ouroboros/internal/assistant"
	"ouroboros/internal/history"
	"ouroboros/internal/openai"
)

type statisticsAgent struct{ fakeAgent }

func (a *statisticsAgent) RestoreStatistics(_ string, size int, cache assistant.Cache) {
	a.size, a.cache = size, cache
}

func TestArchivedStatisticsSurviveRestart(t *testing.T) {
	dir := t.TempDir()
	arch := history.Open(dir)
	want := assistant.Cache{Tokens: 1000, Cached: 900, AllTokens: 3000, AllCached: 2500, Metrics: assistant.CallMetrics{Calls: 3, Output: 120}}
	raw, _ := json.Marshal(want)
	who := history.Who{Session: "s1"}
	if err := arch.Append(who, []openai.Message{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "hello"}}, nil, history.Statistics{Context: 4321, Cache: raw}); err != nil {
		t.Fatal(err)
	}
	if err := arch.SetTitle(history.Owner("s1"), "a title"); err != nil {
		t.Fatal(err)
	}
	a := &statisticsAgent{}
	s := New(a)
	t.Cleanup(s.Close)
	s.Archive(history.Open(dir), t.Logf, func(string) string { return "" })
	past, _, err := s.Watch(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range past {
		if e.Cache != want || e.Context != 4321 {
			t.Fatalf("lost statistics: %+v", e)
		}
	}
	if a.cache != want || a.size != 4321 {
		t.Fatalf("agent usage not restored: %+v", a)
	}
	// Subsequent events use the restored values, not zeroes.
	s.mu.Lock()
	se := s.sessions["s1"]
	s.emit(se, Event{Kind: Title, Text: "new title", At: time.Now()})
	last := se.events[len(se.events)-1]
	s.mu.Unlock()
	if last.Cache != want || last.Context != 4321 {
		t.Fatalf("next event lost usage: %+v", last)
	}
}
