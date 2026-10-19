package agent

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
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
	// Options are the settings Configure takes, for the owner to set them
	// one by one: see OptionsOf. Configurable fills them in.
	Options []Option
	// OwnerOnly says that the tool is for the owner of the node alone: the
	// agent that answers peers never gets it, and listing it for peers is an
	// error.
	OwnerOnly bool
	// Trusted says that the tool reaches beyond what a tool for files keeps
	// to, like running commands or building ouro: a peer gets it only when
	// the list of the tools of that very peer names it, never with every
	// tool and never in the list of all peers.
	Trusted bool
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
	t.Options = OptionsOf[S]()
	return t
}

// Option is a setting a tool takes, as the owner sets it.
type Option struct {
	Name  string // its key in the JSON object of the settings
	Kind  OptionKind
	About string // what it does
	// Unset is what holds while it is not set, like "everything the tools
	// for files may write".
	Unset string
}

// OptionKind is what kind of value an Option takes.
type OptionKind int

// The kinds of an Option.
const (
	OptionText   OptionKind = iota // a string
	OptionNumber                   // a number
	OptionSwitch                   // true or false
	OptionList                     // a list of strings
)

// OptionsOf are the options of the settings S, a struct with a JSON field
// for every setting, in the order of its fields. The tag about says what a
// field does and unset what holds while it is not set:
//
//	Allow *[]string `json:"allow" about:"the paths it may go to" unset:"everything"`
func OptionsOf[S any]() []Option {
	t := reflect.TypeFor[S]()
	if t.Kind() != reflect.Struct {
		return nil
	}
	var out []Option
	for f := range t.Fields() {
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if !f.IsExported() || name == "-" {
			continue
		}
		if name == "" {
			name = f.Name
		}
		o := Option{Name: name, About: f.Tag.Get("about"), Unset: f.Tag.Get("unset")}
		ft := f.Type
		if ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		switch {
		case ft.Kind() == reflect.Bool:
			o.Kind = OptionSwitch
		case numeric(ft):
			o.Kind = OptionNumber
		case (ft.Kind() == reflect.Slice || ft.Kind() == reflect.Array) && ft.Elem().Kind() == reflect.String:
			o.Kind = OptionList
		case ft.Kind() == reflect.String:
			o.Kind = OptionText
		default:
			continue // what the owner cannot set one by one: the JSON of the settings sets it
		}
		out = append(out, o)
	}
	return out
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
			if err := unknown(name, raw, params, reflect.TypeOf(args)); err != nil {
				return "", err
			}
			return run(ctx, args)
		},
	}
}

// unknown fails for an argument raw has that is neither one of params nor a
// field of t: a model that writes "cwd" for "dir", or misspells a name, is
// told so, rather than the call doing something else than it meant. A tool
// of no params takes whatever it is given.
func unknown(name string, raw json.RawMessage, params []Param, t reflect.Type) error {
	if len(params) == 0 {
		return nil
	}
	var names []string
	for _, p := range params {
		names = append(names, p.Name)
	}
	return unknownOf(name, raw, names, t)
}

// DecodeAll is Decode into v, a pointer to a struct, and fails for an
// argument that is no field of it, as NewTool does for one that is no param.
func DecodeAll(name string, raw json.RawMessage, v any) error {
	if err := Decode(name, raw, v); err != nil {
		return err
	}
	return unknownOf(name, raw, nil, reflect.TypeOf(v))
}

// unknownOf fails for an argument raw has that is neither named in names
// nor a field of the struct t points to, and says which it takes: those of
// names, or else the fields.
func unknownOf(name string, raw json.RawMessage, names []string, t reflect.Type) error {
	var given map[string]json.RawMessage
	if json.Unmarshal(raw, &given) != nil {
		return nil
	}
	known := map[string]bool{}
	for _, n := range names {
		known[n] = true
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	var fields []string
	if t.Kind() == reflect.Struct {
		for f := range t.Fields() {
			tag, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if tag == "-" {
				continue
			}
			known[cmp.Or(tag, f.Name)] = true
			fields = append(fields, cmp.Or(tag, f.Name))
		}
	}
	var wrong []string
	for key := range given {
		if !known[key] {
			wrong = append(wrong, strconv.Quote(key))
		}
	}
	if len(wrong) == 0 || len(known) == 0 {
		return nil
	}
	slices.Sort(wrong)
	if names == nil {
		names = fields
	}
	return fmt.Errorf("%s: invalid arguments: it has no %s; it takes %s", name, strings.Join(wrong, ", "), strings.Join(names, ", "))
}

// Decode decodes the arguments raw of a call of the tool name into v. A
// value the LLM wrote as a string is taken as the value it holds: a number,
// like "offset": "295"; true or false, like "background": "True"; and an
// array or an object in JSON, like "options": "[\"a\", \"b\"]". Models do
// so even for direct tool calls. What cannot be decoded is an
// InvalidArguments error.
func Decode(name string, raw json.RawMessage, v any) error {
	for range maxQuoted {
		err := json.Unmarshal(raw, v)
		var typed *json.UnmarshalTypeError
		if err == nil || !errors.As(err, &typed) || typed.Value != "string" || !quotable(typed.Type) {
			if err != nil {
				return InvalidArguments(name, err)
			}
			return nil
		}
		unquoted, ok := unquote(raw, typed.Field, typed.Type)
		if !ok {
			return InvalidArguments(name, err)
		}
		raw = unquoted
	}
	return InvalidArguments(name, errors.New("too many values as strings"))
}

// maxQuoted is how many values written as strings Decode takes in one call.
const maxQuoted = 16

// quotable reports whether a value of t is taken from a string that holds
// it, see Decode.
func quotable(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Bool, reflect.Slice, reflect.Array, reflect.Map, reflect.Struct:
		return true
	}
	return numeric(t)
}

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
// the value of t it holds, if it holds one.
func unquote(raw json.RawMessage, field string, t reflect.Type) (json.RawMessage, bool) {
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
	value, ok := held(strings.TrimSpace(text), t)
	if !ok {
		return nil, false
	}
	object[last] = value
	b, err := json.Marshal(root)
	return b, err == nil
}

// held is the value of t that text holds, if it holds one: a number, true
// or false in any case, or an array or an object in JSON.
func held(text string, t reflect.Type) (any, bool) {
	switch t.Kind() {
	case reflect.Bool:
		switch strings.ToLower(text) {
		case "true":
			return true, true
		case "false":
			return false, true
		}
		return nil, false
	case reflect.Slice, reflect.Array:
		var list []any
		return json.RawMessage(text), json.Unmarshal([]byte(text), &list) == nil && list != nil
	case reflect.Map, reflect.Struct:
		var object map[string]any
		return json.RawMessage(text), json.Unmarshal([]byte(text), &object) == nil && object != nil
	}
	if _, err := strconv.ParseFloat(text, 64); err != nil {
		return nil, false
	}
	return json.Number(text), true
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
			detail = "they are one object of named values, not " + jsonValue(typed.Value)
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
