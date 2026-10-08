package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestOutputAndReportedCost(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"stream": sse(`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`, `{"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":25,"cost":0.0123}}`, "[DONE]"),
		"one piece": func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":100,"completion_tokens":25,"cost":0.0123}}`))
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := newServer(t, handler).Complete(context.Background(), "m", nil, nil)
			if err != nil || got.Usage.Output != 25 || got.Usage.CostUSD == nil || *got.Usage.CostUSD != 0.0123 {
				t.Fatalf("%+v %v", got.Usage, err)
			}
		})
	}
	for _, tc := range []struct {
		body  string
		known bool
	}{
		{`{"completion_tokens":10}`, false},
		{`{"completion_tokens":10,"cost":null}`, false},
		{`{"completion_tokens":10,"cost":0}`, true},
	} {
		var used usage
		if err := json.Unmarshal([]byte(tc.body), &used); err != nil {
			t.Fatal(err)
		}
		if got := used.read(); (got.CostUSD != nil) != tc.known {
			t.Fatalf("%+v", got)
		}
	}
}
