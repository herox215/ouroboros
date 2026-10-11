package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// Tool is something the agent can do while it answers.
type Tool struct {
	Name        string
	Description string
	Schema      string // JSON schema of the arguments
	// Run executes the tool. Its result, or its error, is handed to the LLM.
	Run func(ctx context.Context, args json.RawMessage) (string, error)
	// Configure returns the tool as it runs with settings: the JSON object
	// the owner wrote next to its name in the configuration, "{}" for a tool
	// that is listed by its name alone. It is called when an agent is built,
	// once per kind of conversation, and fails for settings the tool does not
	// take. Nil for a tool without settings; see Configurable.
	Configure func(settings json.RawMessage) (Tool, error)
	// OwnerOnly says that the tool is for the owner of the node alone: the
	// agent that answers peers never gets it, and listing it for peers is an
	// error.
	OwnerOnly bool
	// Functions are the names of the tools the tool calls inside, as run
	// calls them in a script: the LLM that calls one of them as a tool of
	// its own is told to call it there.
	Functions []string
	// Forward returns the arguments of the tool for a call of the function
	// name with args, which the LLM made as if it was a tool of its own: the
	// call then runs inside the tool, and the LLM is told to call it there.
	// Nil for a tool that only says where the function is.
	Forward func(name string, args json.RawMessage) (json.RawMessage, error)
}

// RunFunc is what runs a tool.
type RunFunc = func(ctx context.Context, args json.RawMessage) (string, error)

// Configurable returns t as a tool the owner can give settings in the
// configuration. S is a struct with a JSON field for every setting the tool
// takes: a setting it does not have, or one of the wrong type, is an error
// when the agent is built. with returns what runs the tool with the settings
// it is handed, or an error for settings it cannot run with. It has to take
// the zero S, which is what a tool listed by its name alone gets.
func Configurable[S any](t Tool, with func(settings S) (RunFunc, error)) Tool {
	t.Configure = func(raw json.RawMessage) (Tool, error) {
		var settings S
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&settings); err != nil {
			return Tool{}, fmt.Errorf("settings of the tool %s: %w", t.Name, err)
		}
		run, err := with(settings)
		if err != nil {
			return Tool{}, fmt.Errorf("settings of the tool %s: %w", t.Name, err)
		}
		configured := t
		configured.Run = run
		return configured, nil
	}
	var none S
	t.Run, _ = with(none)
	return t
}

// Param is a text argument of a tool. Only text is supported: a tool that
// needs numbers or lists sets Tool.Schema and decodes its arguments itself.
type Param struct {
	Name        string
	Description string
	Optional    bool
}

// NewTool returns a tool that takes params as its arguments and hands them to
// run decoded into T, whose JSON field names are the names of the params.
// A tool without arguments passes nil params and uses struct{} as T.
//
//	type greetArgs struct {
//		Peer string `json:"peer"`
//	}
//
//	agent.NewTool("greet", "Greets a peer and returns the greeting.",
//		[]agent.Param{{Name: "peer", Description: "name of the peer"}},
//		func(ctx context.Context, args greetArgs) (string, error) {
//			return "hello " + args.Peer, nil
//		})
func NewTool[T any](name, description string, params []Param, run func(ctx context.Context, args T) (string, error)) Tool {
	return Tool{
		Name:        name,
		Description: description,
		Schema:      schema(params),
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args T
			if err := Decode(name, raw, &args); err != nil {
				return "", err
			}
			return run(ctx, args)
		},
	}
}

// Decode decodes the arguments raw of a call of the tool name into v. A
// number the LLM wrote as a string, like "offset": "295", is taken as the
// number: models do so, mostly when they call a tool they were not offered.
// What cannot be decoded is an InvalidArguments error.
func Decode(name string, raw json.RawMessage, v any) error {
	for range maxQuoted {
		err := json.Unmarshal(raw, v)
		var typed *json.UnmarshalTypeError
		if err == nil || !errors.As(err, &typed) || typed.Value != "string" || !numeric(typed.Type) {
			if err != nil {
				return InvalidArguments(name, err)
			}
			return nil
		}
		unquoted, ok := unquote(raw, typed.Field)
		if !ok {
			return InvalidArguments(name, err)
		}
		raw = unquoted
	}
	return InvalidArguments(name, errors.New("too many numbers as strings"))
}

// maxQuoted is how many numbers written as strings Decode takes in one call.
const maxQuoted = 16

// numeric reports whether t is a number in JSON.
func numeric(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return true
	}
	return false
}

// unquote returns raw with the string at the dotted path field written as
// the number it holds, if it holds one.
func unquote(raw json.RawMessage, field string) (json.RawMessage, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var root any
	if field == "" {
		return nil, false
	}
	_ = dec.Decode(&root) // JSON that Unmarshal read up to a type that did not fit
	keys := strings.Split(field, ".")
	object, _ := root.(map[string]any)
	for _, key := range keys[:len(keys)-1] {
		object, _ = object[key].(map[string]any)
	}
	last := keys[len(keys)-1]
	text, ok := object[last].(string)
	if !ok {
		return nil, false
	}
	if _, err := strconv.ParseFloat(strings.TrimSpace(text), 64); err != nil {
		return nil, false
	}
	object[last] = json.Number(strings.TrimSpace(text))
	b, err := json.Marshal(root)
	return b, err == nil
}

// InvalidArguments is the error of the tool name for arguments that err
// says could not be decoded: it says what is wrong with them, so that the
// LLM can call the tool again without guessing.
//
//	read: invalid arguments: "offset" is a number, not a string
func InvalidArguments(name string, err error) error {
	detail := "they are a JSON object"
	var typed *json.UnmarshalTypeError
	if errors.As(err, &typed) {
		if typed.Field == "" {
			detail = "they are an object, not " + jsonValue(typed.Value)
		} else {
			detail = fmt.Sprintf("%q is %s, not %s", typed.Field, jsonType(typed.Type), jsonValue(typed.Value))
		}
	}
	return fmt.Errorf("%s: invalid arguments: %s", name, detail)
}

// jsonType is what a value of t is in JSON. The decoder names the type a
// pointer points to, never the pointer.
func jsonType(t reflect.Type) string {
	switch t.Kind() {
	case reflect.String:
		return "a string"
	case reflect.Bool:
		return "true or false"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return "a whole number"
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "a whole number of at least 0"
	case reflect.Float32, reflect.Float64:
		return "a number"
	case reflect.Slice, reflect.Array:
		return "an array"
	default:
		return "an object"
	}
}

// jsonValue is how the error of a decoder names a value it was given.
func jsonValue(value string) string {
	switch value {
	case "array", "object":
		return "an " + value
	case "bool":
		return "true or false"
	case "string", "number":
		return "a " + value
	}
	return value // like "number 1.5": the number itself
}

// schema is the JSON schema of an object with the text properties params.
func schema(params []Param) string {
	type property struct {
		Type        string `json:"type"`
		Description string `json:"description,omitempty"`
	}
	object := struct {
		Type       string              `json:"type"`
		Properties map[string]property `json:"properties"`
		Required   []string            `json:"required,omitempty"`
	}{Type: "object", Properties: map[string]property{}}
	for _, p := range params {
		object.Properties[p.Name] = property{"string", p.Description}
		if !p.Optional {
			object.Required = append(object.Required, p.Name)
		}
	}
	b, _ := json.Marshal(object) // strings only: cannot fail
	return string(b)
}
