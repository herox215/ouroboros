package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
			if err := json.Unmarshal(raw, &args); err != nil {
				return "", fmt.Errorf("%s: invalid arguments", name)
			}
			return run(ctx, args)
		},
	}
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
