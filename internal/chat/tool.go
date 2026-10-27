package chat

import "encoding/json"

// ToolCall is the model's request to run a tool.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

// FunctionCall names a function and its arguments as a JSON object.
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Tool describes a function the model may call.
type Tool struct {
	Type     string   `json:"type"`
	Function Function `json:"function"`
}

// Function is the signature of a tool; Parameters is a JSON schema.
type Function struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// NewTool returns a function tool taking the arguments described by the JSON schema.
func NewTool(name, description, schema string) Tool {
	return Tool{Type: "function", Function: Function{name, description, json.RawMessage(schema)}}
}
