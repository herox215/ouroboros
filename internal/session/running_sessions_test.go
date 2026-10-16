package session

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRunningSessions(t *testing.T) {
	s := &Service{
		sessions: map[string]*session{
			"idle":        {title: "idle secret", events: []Event{{Text: "idle body"}}},
			"answer":      {title: "Zulu", cancel: func() {}, events: []Event{{Text: "not the named title"}}},
			"task":        {title: "Alpha", tasks: []*task{{}}},
			"wait":        {title: "Beta", asked: make(chan queued)},
			"all":         {title: "Alpha", cancel: func() {}, tasks: []*task{{}}, asked: make(chan queued)},
			"fallback":    {cancel: func() {}, events: []Event{{Text: "First message"}, {Text: "private later body"}}},
			"unnamed":     {asked: make(chan queued)},
			"empty first": {tasks: []*task{{}}, events: []Event{{}, {Text: "not a title"}}},
		},
		// A deliberately unusable archive catches accidental archive access.
		archive: &archive{},
		jobs:    map[string]running{"peer/conversation": {jobs: []Job{{Peer: "peer", Title: "peer secret"}}}},
	}
	want := []string{"Alpha", "Alpha", "Beta", "First message", "Unbenannte Sitzung", "Unbenannte Sitzung", "Zulu"}
	for range 20 {
		got, err := s.RunningSessions(t.Context())
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("titles = %q, %v; want %q", got, err, want)
		}
	}
	// Sampling again reflects work that ended without retaining stale titles.
	s.sessions["task"].tasks = nil
	s.sessions["wait"].asked = nil
	got, err := s.RunningSessions(t.Context())
	want = []string{"Alpha", "First message", "Unbenannte Sitzung", "Unbenannte Sitzung", "Zulu"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("after completion: %q, %v; want %q", got, err, want)
	}
}

func TestRunningSessionsBoundedTitles(t *testing.T) {
	prefix := strings.Repeat("x", maxTitle-1)
	for _, text := range []string{"short", strings.Repeat("ä", maxTitle/2), prefix + "ä", strings.Repeat("界", maxTitle)} {
		for _, named := range []bool{false, true} {
			se := &session{cancel: func() {}, events: []Event{{Text: text}}}
			if named {
				se.title = text
				se.events[0].Text = "not the title"
			}
			s := &Service{sessions: map[string]*session{"owner": se}}
			list, err := s.List(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			got, err := s.RunningSessions(t.Context())
			if err != nil || len(got) != 1 || got[0] != list[0].Title || len(got[0]) > maxTitle || !utf8.ValidString(got[0]) {
				t.Fatalf("named=%v: titles %q, %v; List %q", named, got, err, list[0].Title)
			}
		}
	}
}

func TestRunningSessionsCanceled(t *testing.T) {
	s := &Service{sessions: map[string]*session{"owner": {title: "secret", cancel: func() {}}}}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	// A canceled call must return even when the session mutex is held.
	s.mu.Lock()
	defer s.mu.Unlock()
	got, err := s.RunningSessions(ctx)
	if !errors.Is(err, context.Canceled) || len(got) != 0 {
		t.Fatalf("canceled: %q, %v", got, err)
	}
}

func TestRunningSessionsEmpty(t *testing.T) {
	s := &Service{}
	got, err := s.RunningSessions(t.Context())
	if err != nil || len(got) != 0 {
		t.Fatalf("empty: %q, %v", got, err)
	}
}
