package history

import (
	"encoding/json"
	"strings"
	"testing"

	"ouroboros/internal/openai"
)

func TestMessageSourceRoundtrip(t *testing.T) {
	a := at(t)
	messages := []openai.Message{
		{Role: "user", Content: "here", Source: openai.MessageSource{Interface: "gui"}},
		{Role: "user", Content: "legacy"},
		{Role: "assistant", Content: "done"},
	}
	if err := a.Append(owner, messages, nil); err != nil {
		t.Fatal(err)
	}
	got := all(t, Open(a.dir), owner)
	if len(got) != 4 || got[0].Source != messages[0].Source || got[0].Content != "here" {
		t.Fatalf("roundtrip: %+v", got)
	}
	for _, r := range got[1:] {
		if r.Source != (openai.MessageSource{}) {
			t.Fatalf("source invented: %+v", r)
		}
		b, err := json.Marshal(r)
		if err != nil || strings.Contains(string(b), "source") {
			t.Fatalf("absent source: %s, %v", b, err)
		}
	}
	var old Record
	if err := json.Unmarshal([]byte(`{"role":"user","content":"old"}`), &old); err != nil {
		t.Fatal(err)
	}
	if old.Source.Normalized().Interface != "unknown" {
		t.Fatalf("legacy semantics: %+v", old)
	}
}
