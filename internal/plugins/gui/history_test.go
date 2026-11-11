package gui

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"ouroboros/plugin"
)

func historyAt(hour int) time.Time {
	return time.Date(2020, 1, 1, hour, 0, 0, 0, time.UTC)
}

func TestHistoryNewestRelativeAndBoundary(t *testing.T) {
	s := &fakeSessions{past: []plugin.SessionEvent{
		{Kind: plugin.SessionMessage, Text: "old", At: historyAt(0)},
		{Kind: plugin.SessionAnswer, Text: "old answer", At: historyAt(1)},
		{Kind: plugin.SessionMessage, Text: "boundary", At: historyAt(7)},
		{Kind: plugin.SessionAnswer, Text: "newest", At: historyAt(10)},
		// Watch-time metadata must not turn an idle thread into an empty page.
		{Kind: plugin.SessionDirectory, Text: "/project", At: time.Now()},
	}}
	w := testWindow(t, s)
	got, err := w.Open()
	if err != nil {
		t.Fatal(err)
	}
	if got.Before != 2 || !got.HasMore || len(got.Events) != 3 || got.Events[0].Text != "boundary" {
		t.Fatalf("initial page: %+v", got)
	}
	wire, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(wire, &fields); err != nil || string(fields["before"]) != "2" || string(fields["has_more"]) != "true" {
		t.Fatalf("wire: %s, %v", wire, err)
	}
}

func TestHistoryKeepsWholeTurn(t *testing.T) {
	s := &fakeSessions{past: []plugin.SessionEvent{
		{Kind: plugin.SessionMessage, Text: "old", At: historyAt(0)},
		{Kind: plugin.SessionAnswer, Text: "old answer", At: historyAt(1)},
		{Kind: plugin.SessionMessage, Text: "long turn", At: historyAt(2)},
		{Kind: plugin.SessionStep, Tool: "bash", At: historyAt(8)},
		{Kind: plugin.SessionAnswer, Text: "done", At: historyAt(10)},
	}}
	w := testWindow(t, s)
	got, err := w.Open()
	if err != nil || got.Before != 2 || len(got.Events) != 3 || got.Events[0].Text != "long turn" {
		t.Fatalf("split turn: %+v, %v", got, err)
	}
}

func TestHistoryCoverageAndArchiveUntouched(t *testing.T) {
	var past []plugin.SessionEvent
	for hour := 0; hour <= 20; hour++ {
		past = append(past,
			plugin.SessionEvent{Kind: plugin.SessionMessage, Text: fmt.Sprintf("message %d", hour), At: historyAt(hour)},
			plugin.SessionEvent{Kind: plugin.SessionThought, Text: "hidden", At: historyAt(hour)},
			plugin.SessionEvent{Kind: plugin.SessionAnswer, Text: fmt.Sprintf("answer %d", hour), At: historyAt(hour)},
		)
	}
	original := append([]plugin.SessionEvent(nil), past...)
	s := &fakeSessions{past: past}
	w := testWindow(t, s)
	page, err := w.Open()
	if err != nil {
		t.Fatal(err)
	}
	var all []Event
	for {
		all = append(page.Events, all...)
		if !page.HasMore {
			break
		}
		previous := page.Before
		page, err = w.History(previous)
		if err != nil || page.Before >= previous {
			t.Fatalf("no progress: %+v, %v", page, err)
		}
	}
	var want []Event
	for _, e := range original {
		if e.Kind != plugin.SessionThought {
			want = append(want, w.event(e))
		}
	}
	if !reflect.DeepEqual(all, want) {
		t.Fatalf("coverage/order/duplicates: got %d events, want %d", len(all), len(want))
	}
	if !reflect.DeepEqual(s.past, original) || len(s.watches) != 1 {
		t.Fatal("paging changed the archive or opened another watch")
	}
	// The raw cache is owned by the window, not the service's returned slice.
	s.past[0].Text = "mutated by service"
	first, err := w.History(3)
	if err != nil || first.Events[0].Text != "message 0" {
		t.Fatalf("cache aliased service: %+v, %v", first, err)
	}
}

func TestHistoryInitialStateIsCompact(t *testing.T) {
	s := &fakeSessions{past: []plugin.SessionEvent{
		{Kind: plugin.SessionTitle, Text: "obsolete", At: historyAt(0)},
		{Kind: plugin.SessionTitle, Text: "current", At: historyAt(0)},
		{Kind: plugin.SessionQueued, Text: "consumed", At: historyAt(0)},
		{Kind: plugin.SessionMessage, Text: "old", At: historyAt(0)},
		{Kind: plugin.SessionAnswer, Text: "old answer", At: historyAt(1)},
		{Kind: plugin.SessionQueued, Text: "consumed by recent", At: historyAt(1)},
		{Kind: plugin.SessionQueued, Text: "pending", At: historyAt(1)},
		{Kind: plugin.SessionQueued, Text: "withdrawn", At: historyAt(1)},
		{Kind: plugin.SessionUnqueued, At: historyAt(1)},
		{Kind: plugin.SessionWorking, N: 2, Own: true, At: historyAt(1)},
		{Kind: plugin.SessionAsked, Args: json.RawMessage(`{"question":"Still open?","options":["yes","no"],"recommended":"yes"}`), At: historyAt(1)},
		{Kind: plugin.SessionMessage, Text: "recent", At: historyAt(10)},
		{Kind: plugin.SessionAnswer, Text: "done", At: historyAt(11)},
	}}
	w := testWindow(t, s)
	got, err := w.Open()
	if err != nil {
		t.Fatal(err)
	}
	if got.Before != 11 || len(got.Events) != 7 {
		t.Fatalf("state page: %+v", got)
	}
	want := []plugin.SessionKind{plugin.SessionTitle, plugin.SessionQueued, plugin.SessionQueued, plugin.SessionWorking, plugin.SessionAsked, plugin.SessionMessage, plugin.SessionAnswer}
	for i, kind := range want {
		if got.Events[i].Kind != kind {
			t.Fatalf("event %d: %+v", i, got.Events[i])
		}
	}
	if got.Events[0].Text != "current" || got.Events[2].Text != "pending" || got.Events[3].N != 2 || !got.Events[3].Own || got.Events[4].Text != "Still open?" {
		t.Fatalf("lost state: %+v", got.Events)
	}
	// State carried ahead of the cutoff (especially a visible open question)
	// must not appear again when its raw range is loaded later.
	all := append([]Event(nil), got.Events...)
	for got.HasMore {
		got, err = w.History(got.Before)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, got.Events...)
	}
	if len(all) != len(s.past) {
		t.Fatalf("state paging coverage: %d events, want %d", len(all), len(s.past))
	}
	for _, raw := range s.past {
		want := w.event(raw)
		count := 0
		for _, event := range all {
			if reflect.DeepEqual(event, want) {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("event %+v appeared %d times", want, count)
		}
	}
}

func TestHistoryCursorAndReconnect(t *testing.T) {
	s := &fakeSessions{past: []plugin.SessionEvent{{Kind: plugin.SessionMessage, At: historyAt(0)}}}
	w := testWindow(t, s)
	if _, err := w.History(0); err == nil {
		t.Fatal("history before Open succeeded")
	}
	if _, err := w.Open(); err != nil {
		t.Fatal(err)
	}
	for _, cursor := range []int{-1, 2} {
		if _, err := w.History(cursor); err == nil {
			t.Fatalf("invalid cursor %d accepted", cursor)
		}
	}
	empty, err := w.History(0)
	if err != nil || empty.Before != 0 || empty.HasMore || len(empty.Events) != 0 {
		t.Fatalf("exhausted: %+v, %v", empty, err)
	}
	// Concurrent paging and reconnects operate on a single coherent cache.
	var group sync.WaitGroup
	for range 8 {
		group.Go(func() {
			if _, err := w.History(1); err != nil {
				t.Error(err)
			}
		})
		group.Go(func() {
			if _, err := w.Open(); err != nil {
				t.Error(err)
			}
		})
	}
	group.Wait()
	s.past = nil
	if _, err := w.Open(); err != nil {
		t.Fatal(err)
	}
	if _, err := w.History(1); err == nil {
		t.Fatal("reconnect retained old cache")
	}
	s.watchErr = errors.New("disconnected")
	if _, err := w.Open(); err == nil {
		t.Fatal("watch error lost")
	}
	if _, err := w.History(0); err == nil {
		t.Fatal("failed reconnect retained usable cache")
	}
	w.Close()
	if _, err := w.History(0); err == nil {
		t.Fatal("history after Close succeeded")
	}
}
