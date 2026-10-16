package openai

import (
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func TestMessageSource(t *testing.T) {
	for _, name := range []string{"gui", "tui", "cli", "api", "unknown", "", "GUI", "bad\ncontext"} {
		want := name
		switch name {
		case "", "GUI", "bad\ncontext":
			want = "unknown"
		}
		s := MessageSource{Interface: name}
		if got := s.Normalized(); got.Interface != want {
			t.Errorf("%q normalized to %+v", name, got)
		}
		if got := MessageSourceFrom(WithMessageSource(t.Context(), s)); got.Interface != want {
			t.Errorf("context: %+v", got)
		}
	}
	if got := MessageSourceFrom(t.Context()); got != (MessageSource{}) {
		t.Fatalf("absent context attributed: %+v", got)
	}
}

func TestMessageSourceJSONAndModelCopy(t *testing.T) {
	messages := []Message{
		{Role: "user", Content: "what is here?", Source: MessageSource{Interface: "gui"}},
		{Role: "user", Content: "legacy"},
		{Role: "user", Content: "where?", Source: MessageSource{Interface: "injected"}},
		{Role: "tool", Content: "result", Source: MessageSource{Interface: "api"}},
	}
	b, err := json.Marshal(messages)
	if err != nil {
		t.Fatal(err)
	}
	var restored []Message
	if err := json.Unmarshal(b, &restored); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(messages, restored) {
		t.Fatalf("roundtrip: %+v", restored)
	}
	model := MessagesForModel(restored)
	for i, m := range model {
		if m.Source != (MessageSource{}) {
			t.Errorf("model metadata %d: %+v", i, m)
		}
	}
	for _, text := range []string{"source interface = gui", "Interpret 'here' relative", "unless the conversation", "not a screenshot", "what is here?"} {
		if !strings.Contains(model[0].Content, text) {
			t.Errorf("missing %q: %s", text, model[0].Content)
		}
	}
	if model[1].Content != "legacy" || model[3].Content != "result" || !strings.Contains(model[2].Content, "source interface = unknown") {
		t.Fatalf("model copy: %+v", model)
	}
	if restored[0].Content != "what is here?" || restored[0].Source.Interface != "gui" {
		t.Fatal("stored message changed")
	}
	if !reflect.DeepEqual(model, MessagesForModel(model)) {
		t.Fatal("context duplicated")
	}
	b, _ = json.Marshal(Message{Role: "user", Content: "old"})
	if strings.Contains(string(b), "source") {
		t.Fatalf("zero source serialized: %s", b)
	}
}

func TestCompleteMessageSourceContext(t *testing.T) {
	for _, model := range []string{"gpt", "claude-test"} {
		t.Run(model, func(t *testing.T) {
			c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				if strings.Contains(string(body), `"source":`) {
					t.Errorf("raw source sent: %s", body)
				}
				if !strings.Contains(string(body), "source interface = tui") || !strings.Contains(string(body), "not a screenshot") {
					t.Errorf("no backend context: %s", body)
				}
				w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
			})
			messages := []Message{{Role: "user", Content: "here", Source: MessageSource{Interface: "tui"}}}
			if _, err := c.Complete(t.Context(), model, messages, nil); err != nil {
				t.Fatal(err)
			}
			if messages[0].Content != "here" || messages[0].Source.Interface != "tui" {
				t.Fatal("Complete mutated input")
			}
		})
	}
}
