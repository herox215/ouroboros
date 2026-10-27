package openai

import (
	"encoding/base64"

	"ouroboros/internal/chat"
)

// wireMessage is the request form of a message: the fields an
// OpenAI-compatible API takes, in the order it takes them. A chat.Message is
// never marshaled into a request directly.
type wireMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
	// ToolCalls are the calls an assistant message wants run.
	ToolCalls  []chat.ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	// Reasoning goes into the field the provider it came from used.
	ReasoningContent string `json:"reasoning_content,omitempty"`
	Reasoning        string `json:"reasoning,omitempty"`
	ReasoningText    string `json:"reasoning_text,omitempty"`
	ReasoningItems   string `json:"reasoning_items,omitempty"`

	images     []chat.Image
	breakpoint bool
}

// wirePart is one part of a message content: text, or a picture as a data URL.
type wirePart struct {
	Type         string            `json:"type"`
	Text         *string           `json:"text,omitempty"`
	ImageURL     *wireURL          `json:"image_url,omitempty"`
	CacheControl map[string]string `json:"cache_control,omitempty"`
}

type wireURL struct {
	URL string `json:"url"`
}

// wireReply is a message of a non-streamed answer.
type wireReply struct {
	Role             string          `json:"role"`
	Content          string          `json:"content"`
	ToolCalls        []chat.ToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string          `json:"tool_call_id,omitempty"`
	ReasoningContent string          `json:"reasoning_content,omitempty"`
	Reasoning        string          `json:"reasoning,omitempty"`
	ReasoningText    string          `json:"reasoning_text,omitempty"`
	ReasoningItems   string          `json:"reasoning_items,omitempty"`
}

// message returns the reply as the message the agent keeps.
func (w wireReply) message() chat.Message {
	m := chat.Message{Role: w.Role, Content: w.Content, ToolCalls: w.ToolCalls, ToolCallID: w.ToolCallID}
	switch {
	case w.ReasoningContent != "":
		m.Reasoning, m.ReasoningField = w.ReasoningContent, "reasoning_content"
	case w.Reasoning != "":
		m.Reasoning, m.ReasoningField = w.Reasoning, "reasoning"
	case w.ReasoningText != "":
		m.Reasoning, m.ReasoningField = w.ReasoningText, "reasoning_text"
	case w.ReasoningItems != "":
		m.Reasoning, m.ReasoningField = w.ReasoningItems, chat.ReasoningItems
	}
	return m
}

// wireOf converts messages to the request form, without yet deciding on the
// content parts: see parts.
func wireOf(msgs []chat.Message) []wireMessage {
	out := make([]wireMessage, len(msgs))
	for i, m := range msgs {
		w := wireMessage{Role: m.Role, Content: m.Content, ToolCalls: m.ToolCalls, ToolCallID: m.ToolCallID, images: m.Images}
		switch m.ReasoningField {
		case "reasoning_content":
			w.ReasoningContent = m.Reasoning
		case "reasoning_text":
			w.ReasoningText = m.Reasoning
		case chat.ReasoningItems:
			w.ReasoningItems = m.Reasoning
		default:
			w.Reasoning = m.Reasoning
		}
		out[i] = w
	}
	return out
}

// wireMessages returns the messages as a request sends them, with the cache
// breakpoints if cache is true, and how many there are.
func wireMessages(msgs []chat.Message, cache bool) ([]wireMessage, int) {
	out := wireOf(msgs)
	marked := 0
	if cache {
		out, marked = breakpoints(out)
	}
	for i := range out {
		out[i].parts()
	}
	return out, marked
}

// parts writes the content of w as parts when it is a cache breakpoint or
// carries pictures, as the APIs take them.
func (w *wireMessage) parts() {
	if !w.breakpoint && len(w.images) == 0 {
		return
	}
	text := w.Content.(string)
	part := wirePart{Type: "text", Text: &text}
	if w.breakpoint {
		part.CacheControl = map[string]string{"type": "ephemeral"}
	}
	parts := []wirePart{part}
	for _, image := range w.images {
		parts = append(parts, wirePart{Type: "image_url", ImageURL: &wireURL{imageURL(image)}})
	}
	w.Content = parts
}

// imageURL is a picture as a data URL, which the APIs take in place of a link.
func imageURL(i chat.Image) string {
	return "data:" + i.Type + ";base64," + base64.StdEncoding.EncodeToString(i.Data)
}
