package openai

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPresentationIDIsLocalOnly(t *testing.T) {
	for _, m := range []Message{
		{Role: "assistant", Content: "hello", PresentationID: "local-presentation-id"},
		{Role: "assistant", Content: "hello", PresentationID: "local-presentation-id", breakpoint: true},
		{Role: "user", Content: "hello", PresentationID: "local-presentation-id", sent: true, Images: []Image{{}}},
	} {
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), m.PresentationID) || strings.Contains(strings.ToLower(string(b)), "presentation") {
			t.Fatalf("presentation identity leaked: %s", b)
		}
	}
	var m Message
	if err := json.Unmarshal([]byte(`{"role":"assistant","content":"hello","PresentationID":"provider-id","presentation_id":"provider-id"}`), &m); err != nil {
		t.Fatal(err)
	}
	if m.PresentationID != "" {
		t.Fatalf("provider supplied a local identity: %q", m.PresentationID)
	}
}
