package plugin

import (
	"context"
	"net/http"
	"time"

	"ouroboros/internal/a2a"
	"ouroboros/internal/agent"
	"ouroboros/internal/discovery"
	"ouroboros/internal/llm"
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

// RunFunc is what runs a Tool: it gets the arguments of a call as JSON.
type RunFunc = agent.RunFunc

// Configurable returns t as a tool the owner can give settings in the
// configuration, next to its name: {"tool": "read", "allow": ["~/share"]}. S
// is a struct with a JSON field for every setting the tool takes. A setting
// it does not have, or one of the wrong type, keeps the node from starting.
// with returns what runs the tool with the settings it is handed, once for
// the conversations of the owner and once for those of peers, or an error for
// settings it cannot run with. A tool that is listed by its name alone gets
// the zero S, which with has to take.
func Configurable[S any](t Tool, with func(settings S) (RunFunc, error)) Tool {
	return agent.Configurable(t, with)
}

// Image is a picture a Tool shows the LLM: its media type, like
// "image/png", and its bytes.
type Image = openai.Image

// ShowImage makes the LLM see images along with the result of the Tool that
// ctx is run for, which says in words what they are. It reports false
// outside of a call of a tool, where they would go nowhere. A model that
// takes no pictures fails the answer; the pictures are not kept then.
func ShowImage(ctx context.Context, images ...Image) bool { return agent.Show(ctx, images...) }

// Remark gives the owner a short remark on the call of the Tool that ctx is
// run for, which a front end shows with how the call ended: the size a
// picture was shown in, say. The LLM is not told it. It reports false
// outside of a call of a tool.
func Remark(ctx context.Context, text string) bool { return agent.Remark(ctx, text) }

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

// ContextID returns the conversation a Tool runs for: the ID the agent
// remembers it by, "" outside of an answer. Together with OriginFrom it
// names the conversation for a plugin that keeps something of it; the ID
// alone is local to the agent that answers.
func ContextID(ctx context.Context) string { return agent.ContextID(ctx) }

// Provider is an API the agents can talk to: how the owner signs in to it,
// which models it has and the model the agents answer with. A plugin
// contributes it with Add, under the name of the plugin; the openai plugin
// does for OpenAI-compatible APIs. The node may have several accesses, each
// signed in by one provider, and the owner picks the model of one of them.
//
// A provider gets its own credentials and nothing else of the node: see
// Access. What it returns from Login is kept in a file of its own, apart from
// the configuration, and everything it puts under Credentials.Secret is kept
// out of the log, out of reach of the tools and of every proposal.
type Provider = llm.Provider

type (
	// Access is one access of the node as its provider sees it: its own
	// credentials, to read, and to renew when they ran out.
	Access = llm.Access
	// Credentials are what a provider keeps of an access.
	Credentials = llm.Credentials
	// Dialog is how a provider asks the owner while it signs in, on the
	// terminal and in the terminal UI alike, and Question what it asks.
	Dialog   = llm.Dialog
	Question = llm.Question
	// Models are the models of an access, as a provider lists them.
	Models = llm.Models
	// Model generates a reply for a conversation or asks for tools to be
	// run. Another provider translates to and from the OpenAI message types.
	Model = agent.LLM
	// ChatMessage and ChatTool are what a Model is asked with and answers.
	ChatMessage = openai.Message
	ChatTool    = openai.Tool
)

// What a Model does for the cache of its API is its own business: which
// parameters and headers tell the API that a call goes on from the one before
// differs from API to API. ouro gives it what it needs for that and shows
// what came of it:
//
//   - ContextID(ctx) in Complete is the conversation the call belongs to, the
//     same for every call of it. Send it the way the API wants a conversation
//     named, so that it takes the call where it cached the one before.
//   - ChatMessage.Usage of the answer says how many tokens were sent and how
//     many of them the API had cached. ouro shows it to the owner and logs it.
//   - Trace(ctx), in the debug mode of the node, takes what the call sent and
//     got: the lines that tell why a call was not cached.

// Trace returns what a Model tells the log of the node with while the node
// is in debug mode, nil while it is not: check before putting a line
// together. The lines of the providers of ouro start with "llm request: ",
// "llm prefix: ", "llm response: " and "llm answered: ". No secret of the
// node shows in them: the log replaces every one.
func Trace(ctx context.Context) func(format string, args ...any) { return openai.TraceOf(ctx) }

// Effort is how much the owner wants the model to think before it answers,
// for a Model to send the way its API takes it: one of AllEfforts, like
// "low" or "max", and "" to leave it to the model. Peers are answered with
// "". A model that does not know the effort takes the nearest it knows.
func Effort(ctx context.Context) string { return openai.EffortOf(ctx) }

// Requests keeps the last request of every conversation a Model traced, to
// say with Compare how far the next one begins like it: what the API can
// have cached of it. A Model has one; its zero value is ready to use.
//
//	if trace := plugin.Trace(ctx); trace != nil {
//		trace("llm prefix: %s", m.requests.Compare(plugin.ContextID(ctx), tools, items))
//	}
//
// items are the messages of the request as the API is sent them, each as
// JSON, and tools whatever the request sends of tools.
type Requests = openai.Sent

// TraceResponse tells trace the status and the headers of resp, which came
// after took: where a header says which machine or cache answered, that is
// where to look for it.
func TraceResponse(trace func(format string, args ...any), resp *http.Response, took time.Duration) {
	openai.TraceResponse(trace, resp, took)
}

// ErrKeyRejected is what the Login and the Models of a provider fail with
// when the API refused the key.
var ErrKeyRejected = llm.ErrKeyRejected

// ContextProvider prepares model context when explicitly selected by name.
type ContextProvider = agent.ContextProvider

// AnswerHook contributes current-answer context and observes terminal outcomes.
type AnswerHook = agent.AnswerHook
type Conversation = agent.Conversation
type Processing = agent.Processing
type Preparation = agent.Preparation
type AnswerOutcome = agent.AnswerOutcome
type Diagnostic = agent.Diagnostic
type Inspection = agent.Inspection
