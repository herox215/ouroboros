package agent

import (
	"context"
	"encoding/json"
	"testing"
)

func TestNewTool(t *testing.T) {
	type args struct {
		Peer string `json:"peer"`
		Note string `json:"note"`
	}
	tool := NewTool("greet", "Greets a peer.",
		[]Param{{Name: "peer", Description: "who"}, {Name: "note", Optional: true}},
		func(_ context.Context, a args) (string, error) { return a.Peer + "/" + a.Note, nil })

	want := `{"type":"object","properties":{"note":{"type":"string"},"peer":{"type":"string","description":"who"}},"required":["peer"]}`
	if tool.Name != "greet" || tool.Schema != want {
		t.Fatalf("schema %s", tool.Schema)
	}
	if got, err := tool.Run(context.Background(), json.RawMessage(`{"peer":"beta","note":"hi"}`)); err != nil || got != "beta/hi" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := tool.Run(context.Background(), json.RawMessage(`{"peer":1}`)); err == nil {
		t.Fatal("arguments of the wrong type should fail")
	}
	if none := NewTool("now", "", nil, func(context.Context, struct{}) (string, error) { return "", nil }); none.Schema != `{"type":"object","properties":{}}` {
		t.Fatalf("schema without params: %s", none.Schema)
	}
}
