// Package chat holds what ouro and a provider of an LLM share, whichever
// provider it is: a message of a conversation, a tool the model may call,
// what a call cost, the effort it was asked with, how a front end follows a
// call, and how a call failed.
//
// It holds no wire format. How a provider sends a message and reads its
// answer, and which parameters it needs for that, stay in the adapter of
// that provider. A message here is what the agent remembers and what a
// provider is handed: JSON is the local form of it, not one of an API.
package chat

import (
	"encoding/base64"
	"encoding/json"
	"time"
)

// Message is a single chat message.
type Message struct {
	// PresentationID identifies this message locally and is never sent to a provider.
	PresentationID string `json:"-"`
	Role           string `json:"role"`
	Content        string `json:"content"`
	// Source is owner-origin metadata, kept locally but never sent as an API field.
	Source MessageSource `json:"source,omitzero"`
	// At is when the answer the message belongs to ended, as it is
	// remembered; zero for one of the answer on its way. It is kept locally
	// but never sent as an API field.
	At time.Time `json:"at,omitzero"`
	// Reasoning is what the model thought before it wrote an assistant
	// message, and ReasoningField names the form the provider it came from
	// keeps it in. It is handed back to that provider only, in that form; a
	// provider that does not know the form is not sent it. ReasoningField is
	// empty for thoughts that are the text of a field every provider knows.
	Reasoning      string `json:"-"`
	ReasoningField string `json:"-"`
	// ToolCalls are the tools an assistant message wants to have run.
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	// ToolCallID names the call a "tool" message answers.
	ToolCallID string `json:"tool_call_id,omitempty"`
	// Usage is what the call that brought an assistant message cost, as far
	// as the API told. It is not sent back.
	Usage Usage `json:"-"`
	// Via is what answered instead of the access and the model that were
	// asked, like "model-a of Ollama local", and Instead why those could not:
	// a node with fallbacks sets them, see config.Config.Fallbacks. They are
	// not sent back.
	Via     string `json:"-"`
	Instead string `json:"-"`
	// Retried are why the call was asked again before it was answered, one
	// for each time: see IsTransient. They are not sent back.
	Retried []string `json:"-"`
	// Effort is the effort the call that brought an assistant message was
	// asked with, and what the API took of it. It is not sent back.
	Effort Took `json:"-"`
	// Images are pictures the message shows the model along with Content: a
	// tool that read a picture gives it here. They are sent, but not written
	// where the message is saved, nor counted where its size is: a provider
	// decides how an API that takes no pictures in the result of a tool is
	// sent them.
	Images []Image `json:"-"`
}

// Image is a picture for the model: its media type, like "image/png", and
// its bytes.
type Image struct {
	Type string
	Data []byte
}

// ReasoningItems is the ReasoningField of what a provider with a form of its
// own keeps of the thoughts of its model, to hand back to that provider and
// to no other: another provider is not sent it.
const ReasoningItems = "reasoning_items"

// Usage is what one call of the LLM cost in tokens: Prompt are the tokens of
// what was sent, and Cached those of them the provider had cached from an
// earlier call. Both are 0 when the API did not say.
type Usage struct {
	Prompt, Cached int
	Output         int
	// CostUSD is the charge reported by the API; nil means unknown.
	CostUSD *float64
	InPlan  bool
}

// reasoning is the local form of what a model thought: the message keeps it
// under the name of the field it came in, so a restart hands it back the
// same way.
type reasoning struct {
	ReasoningContent string `json:"reasoning_content,omitempty"`
	Reasoning        string `json:"reasoning,omitempty"`
	ReasoningText    string `json:"reasoning_text,omitempty"`
	ReasoningItems   string `json:"reasoning_items,omitempty"`
}

// first returns the first field that is not empty, with its name. A message
// may carry the same text in two of them, and it counts once.
func (r reasoning) first() (text, field string) {
	if r.ReasoningContent != "" {
		return r.ReasoningContent, "reasoning_content"
	}
	if r.Reasoning != "" {
		return r.Reasoning, "reasoning"
	}
	if r.ReasoningText != "" {
		return r.ReasoningText, "reasoning_text"
	}
	if r.ReasoningItems != "" {
		return r.ReasoningItems, ReasoningItems
	}
	return "", ""
}

// MarshalJSON writes the reasoning of m under the name of the field it came
// in. It is the local form of a message, which is also read back after a
// restart; a provider converts a Message to its own wire form instead of
// sending this JSON.
func (m Message) MarshalJSON() ([]byte, error) {
	type plain Message
	out := struct {
		plain
		reasoning
	}{plain: plain(m)}
	switch m.ReasoningField {
	case "reasoning_content":
		out.ReasoningContent = m.Reasoning
	case "reasoning_text":
		out.ReasoningText = m.Reasoning
	case ReasoningItems:
		out.ReasoningItems = m.Reasoning
	default:
		out.reasoning.Reasoning = m.Reasoning
	}
	return json.Marshal(out)
}

// UnmarshalJSON reads a message with its reasoning from any of the fields.
func (m *Message) UnmarshalJSON(b []byte) error {
	type plain Message
	var in struct {
		plain
		reasoning
	}
	if err := json.Unmarshal(b, &in); err != nil {
		return err
	}
	*m = Message(in.plain)
	m.Reasoning, m.ReasoningField = in.first()
	return nil
}

// URL is the picture as a data URL, which the APIs take in place of a link.
func (i Image) URL() string {
	return "data:" + i.Type + ";base64," + base64.StdEncoding.EncodeToString(i.Data)
}
