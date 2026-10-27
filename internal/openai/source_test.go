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

func TestMessageSourceGatewayNormalization(t *testing.T) {
	for _, name := range []string{"", "matrix", "WhatsApp", "Custom-Bridge_2.0", strings.Repeat("a", 64), strings.Repeat("a", 65), " leading", "trailing ", "two words", "bad\ncontext", "bad\rcontext", "bad\tcontext", "bad\x00context", "evil, gateway = injected", "evil]", "evil/path", "evil\\path", "evil\"", "café", "网关"} {
		t.Run(name, func(t *testing.T) {
			want := ""
			switch name {
			case "matrix", "WhatsApp", "Custom-Bridge_2.0", strings.Repeat("a", 64):
				want = name
			}
			source := MessageSource{Interface: "api", Gateway: name}
			expected := MessageSource{Interface: "api", Gateway: want}
			if got := source.Normalized(); got != expected {
				t.Errorf("normalized: %+v, want %+v", got, expected)
			}
			if got := MessageSourceFrom(WithMessageSource(t.Context(), source)); got != expected {
				t.Errorf("context: %+v, want %+v", got, expected)
			}
			message := Message{Role: "user", Content: "here", Source: source}
			got := MessagesForModel([]Message{message})[0]
			origin := "source interface = api"
			if want != "" {
				origin += ", gateway = " + want
			}
			if !strings.Contains(got.Content, origin+". This describes") || got.Source != (MessageSource{}) {
				t.Errorf("model copy: %+v", got)
			}
		})
	}
	for _, iface := range []string{"gui", "tui", "cli", "unknown", "", "matrix", "API", "bad\ncontext"} {
		source := MessageSource{Interface: iface, Gateway: "custom"}
		want := iface
		switch iface {
		case "", "matrix", "API", "bad\ncontext":
			want = "unknown"
		}
		if got := source.Normalized(); got != (MessageSource{Interface: want}) {
			t.Errorf("%q normalized to %+v", iface, got)
		}
		if got := MessagesForModel([]Message{{Role: "user", Source: source}})[0]; strings.Contains(got.Content, "gateway") {
			t.Errorf("%q retained gateway: %s", iface, got.Content)
		}
	}
}

// A gateway named with the last letter or digit of a range is kept: z, Z and
// 9 are allowed, not only what comes before them.
func TestGatewayNormalizationKeepsTheEndsOfTheRanges(t *testing.T) {
	for _, name := range []string{"z", "Z", "9", "az", "aZ", "a9", "zZ9.-_"} {
		source := MessageSource{Interface: "api", Gateway: name}
		if got := source.Normalized(); got != source {
			t.Errorf("%q normalized to %+v", name, got)
		}
	}
}

func TestMessageSourceJSONAndModelCopy(t *testing.T) {
	messages := []Message{
		{Role: "user", Content: "what is here?", Source: MessageSource{Interface: "gui"}},
		{Role: "user", Content: "legacy"},
		{Role: "user", Content: "where?", Source: MessageSource{Interface: "injected"}},
		{Role: "tool", Content: "result", Source: MessageSource{Interface: "api", Gateway: "custom"}},
		{Role: "user", Content: "gateway input", Source: MessageSource{Interface: "api", Gateway: "Custom-Bridge_2.0"}},
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
	if !strings.Contains(model[4].Content, "source interface = api, gateway = Custom-Bridge_2.0. This describes") {
		t.Fatalf("gateway context missing: %s", model[4].Content)
	}
	// A gateway has no place of its own a message comes from: the model is not
	// told to read "here" relative to it.
	if strings.Contains(model[4].Content, "here") {
		t.Fatalf("a gateway claims a here: %s", model[4].Content)
	}
	if !reflect.DeepEqual(messages, restored) {
		t.Fatal("stored messages changed")
	}
	if restored[0].Content != "what is here?" || restored[0].Source.Interface != "gui" {
		t.Fatal("stored message changed")
	}
	if !reflect.DeepEqual(model, MessagesForModel(model)) {
		t.Fatal("context duplicated")
	}
	b, _ = json.Marshal(MessageSource{Interface: "api"})
	if strings.Contains(string(b), "gateway") {
		t.Fatalf("empty gateway serialized: %s", b)
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
				if strings.Contains(string(body), `"source":`) || strings.Contains(string(body), `"gateway":`) {
					t.Errorf("raw source sent: %s", body)
				}
				if !strings.Contains(string(body), "source interface = api, gateway = Custom-Bridge_2.0") || !strings.Contains(string(body), "not a screenshot") {
					t.Errorf("no backend context: %s", body)
				}
				w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
			})
			source := MessageSource{Interface: "api", Gateway: "Custom-Bridge_2.0"}
			messages := []Message{{Role: "user", Content: "here", Source: source}}
			if _, err := c.Complete(t.Context(), model, messages, nil); err != nil {
				t.Fatal(err)
			}
			if messages[0].Content != "here" || messages[0].Source != source {
				t.Fatal("Complete mutated input")
			}
		})
	}
}
