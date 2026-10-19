package history

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"ouroboros/internal/openai"
)

func TestPresentationIDRoundtrip(t *testing.T) {
	for _, test := range []struct {
		name    string
		err     error
		outcome string
	}{
		{"answered", nil, Answered},
		{"failed", errors.New("private failure"), Failed},
		{"canceled", context.Canceled, Canceled},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, withStatistics := range []bool{false, true} {
				a := at(t)
				messages := []openai.Message{
					{Role: "user", Content: "question"},
					{Role: "assistant", Content: "partial", PresentationID: "message-id"},
				}
				var err error
				if withStatistics {
					err = a.Append(owner, messages, test.err, Statistics{Context: 42, PresentationID: "end-id"})
				} else {
					err = a.AppendWithPresentationID(owner, messages, test.err, "end-id")
				}
				if err != nil {
					t.Fatal(err)
				}
				if err := a.Close(); err != nil {
					t.Fatal(err)
				}
				reopened := Open(a.dir)
				defer reopened.Close()
				got, err := reopened.Records(Owner(owner.Session))
				if err != nil || len(got) != 3 {
					t.Fatalf("roundtrip: %+v, %v", got, err)
				}
				if got[0].PresentationID != "" || got[1].PresentationID != "message-id" || got[2].PresentationID != "end-id" {
					t.Fatalf("presentation IDs: %+v", got)
				}
				end := got[2]
				if end.Role != "end" || end.Outcome != test.outcome {
					t.Fatalf("end: %+v", end)
				}
				if withStatistics {
					if end.Statistics == nil || end.Statistics.Context != 42 || end.Statistics.PresentationID != "" {
						t.Fatalf("statistics: %+v", end.Statistics)
					}
				} else if end.Statistics != nil {
					t.Fatalf("invented statistics: %+v", end.Statistics)
				}
				b, err := json.Marshal(end.Statistics)
				if err != nil || strings.Contains(string(b), "end-id") || strings.Contains(string(b), "presentation") {
					t.Fatalf("statistics JSON: %s, %v", b, err)
				}
			}
		})
	}
}

func TestPresentationIDJSONCompatibility(t *testing.T) {
	var legacy Record
	if err := json.Unmarshal([]byte(`{"role":"end","outcome":"failed"}`), &legacy); err != nil {
		t.Fatal(err)
	}
	if legacy.PresentationID != "" {
		t.Fatalf("legacy ID: %+v", legacy)
	}
	b, err := json.Marshal(legacy)
	if err != nil || strings.Contains(string(b), "presentation_id") {
		t.Fatalf("absent ID: %s, %v", b, err)
	}
	b, err = json.Marshal(Statistics{Context: 42, PresentationID: "end-id"})
	if err != nil || string(b) != `{"context":42}` {
		t.Fatalf("statistics JSON: %s, %v", b, err)
	}
	a := at(t)
	if err := a.Append(owner, question, nil); err != nil {
		t.Fatal(err)
	}
	got := all(t, a, owner)
	for _, r := range got {
		if r.PresentationID != "" || r.Statistics != nil {
			t.Fatalf("invented metadata: %+v", r)
		}
	}
}
