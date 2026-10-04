// Package a2a implements the subset of the Agent2Agent (A2A) protocol v0.3
// that Ouroboros needs: the agent card and the JSON-RPC method message/send.
package a2a

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	ProtocolVersion = "0.3.0"
	CardPath        = "/.well-known/agent-card.json"
	MethodSend      = "message/send"
	// MethodAccept and MethodReject are sent back to a node that asked to
	// connect, once the human on the other side has decided.
	MethodAccept = "contact/accept"
	MethodReject = "contact/reject"
)

// JSON-RPC error codes.
const (
	CodeParseError     = -32700
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
	CodeUnauthorized   = -32001 // missing or bad request signature
	CodePending        = -32002 // waiting for the peer to approve the request
	CodeBlocked        = -32003 // the peer refused to connect
	CodeTooManyHops    = -32004 // the message was passed on by too many agents
)

// MaxHops is how many agents may pass a message on before it is refused.
// It ends a question that goes round in a circle of nodes.
const MaxHops = 3

// Peer is the identifying information a node sends about itself.
type Peer struct {
	Name  string `json:"name,omitempty"`
	Model string `json:"model,omitempty"`
}

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

// code reports whether err is a JSON-RPC error with the given code.
func code(err error, want int) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == want
}

// IsPending reports whether err means the peer is waiting for a human decision.
func IsPending(err error) bool { return code(err, CodePending) }

// ErrTooManyHops is returned instead of sending a message that MaxHops agents
// have passed on already.
var ErrTooManyHops = errors.New("a2a: too many agents have passed this message on")

// IsBlocked reports whether err means the peer refused to connect.
func IsBlocked(err error) bool { return code(err, CodeBlocked) }

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"` // decoded by the method they are for
}

// sendParams are the parameters of MethodSend.
type sendParams struct {
	Message Message `json:"message"`
	Peer    Peer    `json:"peer"`
	// Hops counts the agents that passed the message on: 0 when the owner of
	// the sending node asked for it.
	Hops int `json:"hops,omitempty"`
}

// contactParams are the parameters of MethodAccept and MethodReject.
type contactParams struct {
	Peer Peer `json:"peer"`
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
