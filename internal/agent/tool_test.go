package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

var errTest = errors.New("test error")

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

// A tool with settings runs with those of its entry in the configuration, and
// with none when it is listed by its name alone.
func TestConfigurable(t *testing.T) {
	type settings struct {
		Greeting string `json:"greeting"`
		Times    int    `json:"times"`
	}
	tool := Configurable(Tool{Name: "greet", Description: "Greets.", Schema: "{}"}, func(s settings) (RunFunc, error) {
		if s.Times < 0 {
			return nil, errTest
		}
		return func(_ context.Context, args json.RawMessage) (string, error) {
			return fmt.Sprint(s.Greeting, s.Times, string(args)), nil
		}, nil
	})
	if got, err := tool.Run(context.Background(), json.RawMessage(`1`)); err != nil || got != "01" {
		t.Fatalf("without settings: %q, %v", got, err)
	}
	configured, err := tool.Configure(json.RawMessage(`{"greeting": "hi", "times": 2}`))
	if err != nil || configured.Name != "greet" || configured.Description != "Greets." || configured.Schema != "{}" || configured.Configure == nil {
		t.Fatalf("configured: %+v, %v", configured, err)
	}
	if got, err := configured.Run(context.Background(), json.RawMessage(`1`)); err != nil || got != "hi21" {
		t.Fatalf("with settings: %q, %v", got, err)
	}
	// The tool itself stays as it was: it is configured once per kind of conversation.
	if got, _ := tool.Run(context.Background(), json.RawMessage(`1`)); got != "01" {
		t.Fatalf("the tool was changed by configuring it: %q", got)
	}
	if again, err := tool.Configure(json.RawMessage(`{}`)); err != nil {
		t.Fatalf("no settings: %v", err)
	} else if got, _ := again.Run(context.Background(), json.RawMessage(`1`)); got != "01" {
		t.Fatalf("no settings: %q", got)
	}

	for settings, want := range map[string]string{
		`{"greting": "hi"}`: `settings of the tool greet: json: unknown field "greting"`,
		`{"times": "2"}`:    "settings of the tool greet: json: cannot unmarshal string into Go struct field settings.times of type int",
		`{"times": -1}`:     "settings of the tool greet: test error",
		`[]`:                "settings of the tool greet: json: cannot unmarshal array into Go value of type agent.settings",
	} {
		if got, err := tool.Configure(json.RawMessage(settings)); err == nil || err.Error() != want || got.Name != "" {
			t.Errorf("%s: %+v, %v", settings, got, err)
		}
	}
	if _, err := tool.Configure(json.RawMessage(`{"times": -1}`)); !errors.Is(err, errTest) {
		t.Errorf("the error of the tool is lost: %v", err)
	}
}
