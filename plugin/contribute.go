package plugin

import (
	"context"

	"ouroboros/internal/a2a"
	"ouroboros/internal/agent"
	"ouroboros/internal/discovery"
	"ouroboros/internal/openai"
)

// What a plugin contributes with Add, besides a Command.
type (
	// Tool is something the agents can do while they answer, made with
	// NewTool. Which agent gets it is the owner's choice: a tool is allowed
	// for the owner's agent by default and for peers only when the
	// configuration lists it.
	Tool = agent.Tool
	// Param is a text argument of a Tool.
	Param = agent.Param

	// Method is a JSON-RPC method added to the node, for a plugin on a
	// connected peer to run with Network.Call. Its Handle runs only for a
	// request that the caller signed for this node and this method, and only
	// if the owner of this node is connected to the caller: everyone else is
	// refused before Handle is reached. A name that is built in is never
	// handed to it.
	Method = a2a.Method
	// Caller is the peer that runs a Method, authenticated by its signature.
	Caller = a2a.Caller
	// Message is what a Method answers with and Network.Call returns; Reply
	// makes one, and its Text method reads it.
	Message = a2a.Message
	// Error is the error a Method answers with and Network.Call returns for
	// it, with one of the Code constants.
	Error = a2a.Error

	// PeerSource returns hosts to look for nodes on, in addition to the
	// local network.
	PeerSource = discovery.PeerSource
)

// The codes of an Error.
const (
	CodeInvalidParams = a2a.CodeInvalidParams // the params are not what the method takes
	CodeInternalError = a2a.CodeInternalError // the method failed
	CodeUnauthorized  = a2a.CodeUnauthorized  // the caller is not a connected peer, or not who it says
	CodeMethodMissing = a2a.CodeMethodNotFound
)

// NewTool returns a tool that takes params as its arguments and hands them to
// run decoded into T, whose JSON field names are the names of the params. A
// tool without arguments passes nil params and uses struct{} as T. The LLM
// reads description to decide when to call the tool, and gets what run
// returns: a returned error is handed to it as "error: ...".
func NewTool[T any](name, description string, params []Param, run func(ctx context.Context, args T) (string, error)) Tool {
	return agent.NewTool(name, description, params, run)
}

// Reply returns the message a Method answers text with.
func Reply(text string) *Message {
	m := a2a.NewMessage("agent", "", text)
	return &m
}

// Origin is the peer a conversation comes from.
type Origin = a2a.Origin

// OriginFrom returns the peer the conversation of ctx comes from, and false
// for a conversation of the owner. A Tool may run for either: one that only
// the owner may use asks here.
func OriginFrom(ctx context.Context) (Origin, bool) { return a2a.OriginFrom(ctx) }

// LLM is the service that decides which API the agents talk to: it returns
// the model reached at the endpoint the node is configured for. One plugin
// provides it; the openai plugin does for OpenAI-compatible APIs.
type LLM func(at Endpoint) Model

type (
	// Endpoint is where the LLM of the node is reached.
	Endpoint = agent.Endpoint
	// Model generates a reply for a conversation or asks for tools to be
	// run. Another provider translates to and from the OpenAI message types.
	Model = agent.LLM
	// ChatMessage and ChatTool are what a Model is asked with and answers.
	ChatMessage = openai.Message
	ChatTool    = openai.Tool
)
