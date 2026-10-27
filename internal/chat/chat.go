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
	Reasoning      string `json:"reasoning,omitempty"`
	ReasoningField string `json:"reasoning_field,omitempty"`
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

// URL is the picture as a data URL, which the APIs take in place of a link.
func (i Image) URL() string {
	return "data:" + i.Type + ";base64," + base64.StdEncoding.EncodeToString(i.Data)
}
