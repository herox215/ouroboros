package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
)

// The effort goes to the API as reasoning_effort, as it is named; "max",
// which some APIs know, and else as "xhigh". An API that refuses it, without
// saying why, is asked again with the next that is near, and at last
// without; what it took is what it is asked with from then on. Without an
// effort none is sent.
func TestCompleteSendsTheEffort(t *testing.T) {
	var sent []string
	refuses := map[string]bool{}
	c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var in struct {
			Effort *string `json:"reasoning_effort"`
		}
		json.Unmarshal(b, &in)
		effort := "-"
		if in.Effort != nil {
			effort = *in.Effort
		}
		sent = append(sent, effort)
		if refuses[effort] {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"model":"m"}`))
			return
		}
		sse(`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`, "[DONE]")(w, r)
	})
	var lines []string
	var took Took
	ask := func(effort string) error {
		lines = nil
		ctx := WithTrace(WithEffort(context.Background(), effort), func(format string, args ...any) {
			if strings.Contains(format, "reasoning_effort") {
				lines = append(lines, format)
			}
		})
		msg, err := c.Complete(ctx, "m", []Message{{Role: "user", Content: "hi"}}, nil)
		took = msg.Effort
		return err
	}
	// The message says what the API took of the effort: where it is not
	// what the effort is sent as, it fell.
	for _, tt := range []struct {
		effort  string
		refuses []string
		want    []string
		took    Took
	}{
		{"", nil, []string{"-"}, Took{Asked: "", Value: "", Told: true}},
		{"high", nil, []string{"high"}, Took{Asked: "high", Value: "high", Told: true}},
		{"max", nil, []string{"max"}, Took{Asked: "max", Value: "max", Told: true}},
		{"xhigh", []string{"xhigh"}, []string{"xhigh", "high"}, Took{Asked: "xhigh", Value: "high", Told: true}},
		{"minimal", []string{"minimal"}, []string{"minimal", "low"}, Took{Asked: "minimal", Value: "low", Told: true}},
		{"none", []string{"none", "minimal"}, []string{"none", "minimal", "low"}, Took{Asked: "none", Value: "low", Told: true}},
		{"none", []string{"none", "minimal"}, []string{"low"}, Took{Asked: "none", Value: "low", Told: true}}, // what it took the last time
		{"low", []string{"low"}, []string{"low", "-"}, Took{Asked: "low", Value: "", Told: true}},
	} {
		sent, refuses = nil, map[string]bool{}
		for _, e := range tt.refuses {
			refuses[e] = true
		}
		if err := ask(tt.effort); err != nil || !slices.Equal(sent, tt.want) {
			t.Errorf("%q: sent %q, %v; want %q", tt.effort, sent, err, tt.want)
		}
		if took != tt.took || took.Fell() != (len(tt.refuses) > 0) {
			t.Errorf("%q: took %+v, fell %v", tt.effort, took, took.Fell())
		}
		// The trace tells what was sent first, and every refusal.
		if want := min(len(tt.want), 1+len(tt.refuses)); tt.effort != "" && len(lines) != want {
			t.Errorf("%q: traced %q", tt.effort, lines)
		}
	}

	// An API that refuses whatever it is asked fails with its error.
	sent, refuses = nil, map[string]bool{"max": true, "xhigh": true, "high": true, "-": true}
	if err := ask("max"); !badRequest(err) || !slices.Equal(sent, []string{"max", "xhigh", "high", "-"}) || took.Told || took.Fell() {
		t.Fatalf("sent %q, %v", sent, err)
	}
	// It is asked with all of them again: nothing was taken.
	sent, refuses = nil, map[string]bool{}
	if err := ask("max"); err != nil || !slices.Equal(sent, []string{"max"}) {
		t.Fatalf("sent %q, %v", sent, err)
	}
}

// The effort is kept in the context, and a context without one has "".
func TestEffortOf(t *testing.T) {
	if got := EffortOf(context.Background()); got != "" {
		t.Fatalf("effort %q", got)
	}
	if got := EffortOf(WithEffort(context.Background(), "low")); got != "low" {
		t.Fatalf("effort %q", got)
	}
}

// An effort given as a function is read at every call: one that changes
// holds from the next call on. A context without one has none.
func TestEffortChanges(t *testing.T) {
	ctx := context.Background()
	if EffortOf(ctx) != "" || HasEffort(ctx) {
		t.Fatal("a context without an effort has one")
	}
	if auto := WithEffort(ctx, ""); EffortOf(auto) != "" || !HasEffort(auto) {
		t.Fatal("the effort that leaves it to the model is none")
	}
	effort := "low"
	of := WithEffortOf(ctx, func() string { return effort })
	if EffortOf(of) != "low" || !HasEffort(of) {
		t.Fatalf("effort %q", EffortOf(of))
	}
	effort = "high"
	if EffortOf(of) != "high" {
		t.Fatalf("changed: %q", EffortOf(of))
	}
}

// A bad request is what the API refuses with HTTP 400 or 422, and nothing
// else it fails with.
func TestBadRequest(t *testing.T) {
	for status, want := range map[int]bool{http.StatusBadRequest: true, http.StatusUnprocessableEntity: true, http.StatusUnauthorized: false, http.StatusInternalServerError: false} {
		if got := badRequest(fmt.Errorf("asked: %w", &APIError{Status: status})); got != want {
			t.Errorf("HTTP %d is a bad request: %v", status, got)
		}
	}
	if badRequest(errors.New("no answer")) {
		t.Error("an error that is not of the API is no bad request")
	}
}
