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

// The error of arguments that cannot be decoded says what is wrong with
// them.
func TestInvalidArguments(t *testing.T) {
	type args struct {
		S  string            `json:"s"`
		B  bool              `json:"b"`
		I  int               `json:"i"`
		U  uint              `json:"u"`
		F  float64           `json:"f"`
		L  []string          `json:"l"`
		M  map[string]string `json:"m"`
		P  *int              `json:"p"`
		In struct {
			X int `json:"x"`
		} `json:"in"`
	}
	for raw, want := range map[string]string{
		`{"s": 1}`:           `"s" is a string, not a number`,
		`{"s": true}`:        `"s" is a string, not true or false`,
		`{"b": "yes"}`:       `"b" is true or false, not a string`,
		`{"i": 1.5}`:         `"i" is a whole number, not number 1.5`,
		`{"u": -1}`:          `"u" is a whole number of at least 0, not number -1`,
		`{"f": [1]}`:         `"f" is a number, not an array`,
		`{"l": {}}`:          `"l" is an array, not an object`,
		`{"m": []}`:          `"m" is an object, not an array`,
		`{"p": "1"}`:         `"p" is a whole number, not a string`,
		`{"in": {"x": "1"}}`: `"in.x" is a whole number, not a string`,
		`[]`:                 "they are an object, not an array",
		`"x"`:                "they are an object, not a string",
		`{`:                  "they are a JSON object",
	} {
		var a args
		err := json.Unmarshal([]byte(raw), &a)
		if got := InvalidArguments("t", err).Error(); got != "t: invalid arguments: "+want {
			t.Errorf("%s: %q, want %q", raw, got, want)
		}
	}
}

// A number written as a string is taken as the number, also within an
// object; a string that is no number, or no object to find it in, is an
// error.
func TestDecodeTakesNumbersAsStrings(t *testing.T) {
	type args struct {
		Path   string  `json:"path"`
		Offset int     `json:"offset"`
		Limit  uint    `json:"limit"`
		Scale  float64 `json:"scale"`
		In     struct {
			X int `json:"x"`
		} `json:"in"`
	}
	var got args
	if err := Decode("read", json.RawMessage(`{"path": "a", "offset": "295", "limit": " 90 ", "scale": "1.5", "in": {"x": "-2"}}`), &got); err != nil ||
		got.Path != "a" || got.Offset != 295 || got.Limit != 90 || got.Scale != 1.5 || got.In.X != -2 {
		t.Fatalf("got %+v, %v", got, err)
	}
	for raw, want := range map[string]string{
		`{"offset": "one"}`: `read: invalid arguments: "offset" is a whole number, not a string`,
		`{"offset": "1.5"}`: `read: invalid arguments: "offset" is a whole number, not number 1.5`,
		`{"limit": "-1"}`:   `read: invalid arguments: "limit" is a whole number of at least 0, not number -1`,
		`{"path": 1}`:       `read: invalid arguments: "path" is a string, not a number`,
		`[1]`:               `read: invalid arguments: they are an object, not an array`,
		`{"offset": "1"`:    `read: invalid arguments: they are a JSON object`,
	} {
		var a args
		if err := Decode("read", json.RawMessage(raw), &a); err == nil || err.Error() != want {
			t.Errorf("%s: %v, want %q", raw, err, want)
		}
	}
	var flag struct {
		On bool `json:"on"`
	}
	if err := Decode("t", json.RawMessage(`{"on": "true"}`), &flag); err == nil || err.Error() != `t: invalid arguments: "on" is true or false, not a string` {
		t.Errorf("true as a string: %v", err)
	}
	var number int
	if err := Decode("t", json.RawMessage(`"5"`), &number); err == nil {
		t.Error("a number as a string instead of the arguments: no error")
	}
	// A string that cannot be found again, as in an array, stays an error.
	var list struct {
		N []int `json:"n"`
	}
	if err := Decode("t", json.RawMessage(`{"n": ["1"]}`), &list); err == nil {
		t.Error("a number as a string in an array: no error")
	}
	// More numbers as strings than Decode takes.
	var many map[string]int
	raw := "{"
	for i := range maxQuoted + 1 {
		raw += fmt.Sprintf(`"k%02d": "%d",`, i, i)
	}
	raw = raw[:len(raw)-1] + "}"
	if err := Decode("t", json.RawMessage(raw), &many); err == nil {
		t.Error("too many numbers as strings: no error")
	}
}
