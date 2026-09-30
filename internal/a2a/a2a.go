// Package a2a implements the subset of the Agent2Agent (A2A) protocol v0.3
// that Ouroboros needs: the agent card and the JSON-RPC method message/send.
package a2a

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	ProtocolVersion = "0.3.0"
	CardPath        = "/.well-known/agent-card.json"
	MethodSend      = "message/send"
)

// JSON-RPC error codes.
const (
	CodeParseError     = -32700
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
)

// AgentCard describes an agent and how to reach it.
type AgentCard struct {
	ProtocolVersion    string       `json:"protocolVersion"`
	Name               string       `json:"name"`
	Description        string       `json:"description"`
	URL                string       `json:"url"`
	PreferredTransport string       `json:"preferredTransport"`
	Version            string       `json:"version"`
	Capabilities       Capabilities `json:"capabilities"`
	DefaultInputModes  []string     `json:"defaultInputModes"`
	DefaultOutputModes []string     `json:"defaultOutputModes"`
	Skills             []Skill      `json:"skills"`
}

type Capabilities struct {
	Streaming         bool `json:"streaming"`
	PushNotifications bool `json:"pushNotifications"`
}

type Skill struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
}

// Message is a single turn in a conversation.
type Message struct {
	Kind      string `json:"kind"`
	Role      string `json:"role"`
	MessageID string `json:"messageId"`
	ContextID string `json:"contextId,omitempty"`
	Parts     []Part `json:"parts"`
}

// Part is a piece of message content. Only text parts are supported.
type Part struct {
	Kind string `json:"kind"`
	Text string `json:"text,omitempty"`
}

// NewMessage returns a text message from role.
func NewMessage(role, contextID, text string) Message {
	return Message{
		Kind:      "message",
		Role:      role,
		MessageID: NewID(),
		ContextID: contextID,
		Parts:     []Part{{Kind: "text", Text: text}},
	}
}

// Text joins all text parts of m.
func (m Message) Text() string {
	var texts []string
	for _, p := range m.Parts {
		if p.Kind == "text" && p.Text != "" {
			texts = append(texts, p.Text)
		}
	}
	return strings.Join(texts, "\n")
}

// Error is a JSON-RPC error.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string {
	return fmt.Sprintf("a2a: %s (%d)", e.Message, e.Code)
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  struct {
		Message Message `json:"message"`
	} `json:"params"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  *Message        `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// NewID returns a random UUIDv4.
func NewID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
