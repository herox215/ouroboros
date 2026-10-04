package a2a

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	"ouroboros/internal/identity"
)

const maxBodyBytes = 1 << 20

// Responder answers text within the conversation contextID.
type Responder func(ctx context.Context, contextID, text string) (string, error)

// Server serves the agent card and the authenticated JSON-RPC endpoint.
// Only trusted peers may send messages; everyone else becomes a pending
// connection request that a human has to approve.
type Server struct {
	Card     func() AgentCard // asked for every request, so that the card can change
	Identity *identity.Identity
	Trust    Trust
	Respond  Responder
	// Methods returns the methods added to the built-in ones, read anew for
	// every request. May be nil.
	Methods func() []Method
}

// Caller is who sent a request.
type Caller struct {
	PublicKey string // authenticated by the request signature
	Addr      string // where to reach the caller again
}

// Method is a JSON-RPC method added to the server. It is only called for
// peers the owner is connected to. Handle decodes params itself and returns
// the Message to answer with (nil for none) or an *Error with one of the Code
// constants.
//
// To add a method: contribute a Method from a plugin with kernel.Add. A name
// that is built in (MethodSend, MethodAccept, MethodReject) is never handed
// to an added method: what decides about trust cannot be replaced.
type Method struct {
	Name   string
	Handle func(ctx context.Context, from Caller, params json.RawMessage) (*Message, *Error)
}

// method handles the JSON-RPC method it is registered for.
type method func(ctx context.Context, from Caller, params json.RawMessage) (*Message, *Error)

// Handler returns the HTTP handler of the server.
func (s *Server) Handler() http.Handler {
	verify := newVerifier()
	// The built-in methods: JSON-RPC method name to handler.
	builtin := map[string]method{
		MethodSend:   s.send,
		MethodAccept: s.accept,
		MethodReject: s.reject,
	}
	return handler(s.Card,
		func(r *http.Request, body []byte) (Caller, error) {
			publicKey, err := verify.verify(r, body)
			return Caller{publicKey, callbackAddr(r, r.Header.Get(HeaderPort))}, err
		},
		func(name string) (method, bool) {
			if run, ok := builtin[name]; ok {
				return run, true
			}
			return s.added(name)
		})
}

// added returns the added method called name, limited to connected peers.
func (s *Server) added(name string) (method, bool) {
	if s.Methods == nil {
		return nil, false
	}
	for _, m := range s.Methods() {
		if m.Name != name {
			continue
		}
		return func(ctx context.Context, from Caller, params json.RawMessage) (*Message, *Error) {
			if trusted, _ := s.Trust.State(from.PublicKey); !trusted {
				return nil, &Error{CodeUnauthorized, "not connected"}
			}
			return m.Handle(ctx, from, params)
		}, true
	}
	return nil, false
}

// NewHandler serves the agent card and the JSON-RPC endpoint without
// authentication: every peer is trusted. Used for tests and raw A2A clients.
func NewHandler(card AgentCard, respond Responder) http.Handler {
	return handler(func() AgentCard { return card }, nil, func(name string) (method, bool) {
		return answer(respond), name == MethodSend
	})
}

// handler serves the agent card and dispatches JSON-RPC requests to methods.
// Every request goes the same way: read and parse the body, authenticate the
// caller (unless authenticate is nil), look up the method, answer with its
// result or its error.
func handler(card func() AgentCard, authenticate func(*http.Request, []byte) (Caller, error), lookup func(name string) (method, bool)) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+CardPath, func(w http.ResponseWriter, r *http.Request) {
		c := card()
		c.URL = "http://" + r.Host + "/"
		writeJSON(w, c)
	})
	mux.HandleFunc("POST /", func(w http.ResponseWriter, r *http.Request) {
		res := response{JSONRPC: "2.0"}
		defer func() { writeJSON(w, res) }()

		var req request
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
		if err != nil || json.Unmarshal(body, &req) != nil {
			res.Error = &Error{CodeParseError, "parse error"}
			return
		}
		res.ID = req.ID
		var from Caller
		if authenticate != nil {
			if from, err = authenticate(r, body); err != nil {
				res.Error = &Error{CodeUnauthorized, err.Error()}
				return
			}
		}
		run, ok := lookup(req.Method)
		if !ok {
			res.Error = &Error{CodeMethodNotFound, "method not found"}
			return
		}
		res.Result, res.Error = run(r.Context(), from, req.Params)
	})
	return mux
}

// send answers a message of a trusted peer. Anyone else leaves a connection
// request instead.
func (s *Server) send(ctx context.Context, from Caller, params json.RawMessage) (*Message, *Error) {
	var p sendParams
	if json.Unmarshal(params, &p) != nil || p.Message.Text() == "" {
		return nil, errNoText
	}
	if p.Hops > MaxHops {
		return nil, &Error{CodeTooManyHops, "too many agents have passed this message on"}
	}
	trusted, blocked := s.Trust.State(from.PublicKey)
	if s.Identity != nil && from.PublicKey == s.Identity.Public() {
		trusted = true
	}
	switch {
	case trusted:
		// The Responder learns who asks: what it does for a peer is limited.
		return reply(WithOrigin(ctx, Origin{PublicKey: from.PublicKey, Hops: p.Hops}), s.Respond, p.Message)
	case blocked:
		return nil, &Error{CodeBlocked, "connection refused"}
	}
	s.Trust.AddIncoming(from.PublicKey, p.Peer.Name, p.Peer.Model, from.Addr)
	return nil, &Error{CodePending, "connection request is waiting for approval"}
}

// accept notes that a peer this node asked to connect has agreed.
func (s *Server) accept(_ context.Context, from Caller, params json.RawMessage) (*Message, *Error) {
	var p contactParams
	if json.Unmarshal(params, &p) != nil {
		return nil, &Error{CodeInvalidParams, "invalid params"}
	}
	if s.Trust.HadOutgoing(from.PublicKey) {
		s.Trust.AddTrusted(from.PublicKey, p.Peer.Name, p.Peer.Model)
	}
	return nil, nil
}

// reject notes that a peer this node asked to connect has refused.
func (s *Server) reject(_ context.Context, from Caller, _ json.RawMessage) (*Message, *Error) {
	if s.Trust.HadOutgoing(from.PublicKey) {
		s.Trust.RemoveOutgoing(from.PublicKey)
	}
	return nil, nil
}

var errNoText = &Error{CodeInvalidParams, "message needs a text part"}

// answer is MethodSend without a trust check: respond answers the message.
func answer(respond Responder) method {
	return func(ctx context.Context, _ Caller, params json.RawMessage) (*Message, *Error) {
		var p sendParams
		if json.Unmarshal(params, &p) != nil || p.Message.Text() == "" {
			return nil, errNoText
		}
		return reply(ctx, respond, p.Message)
	}
}

// reply lets respond answer msg within its conversation.
func reply(ctx context.Context, respond Responder, msg Message) (*Message, *Error) {
	contextID := msg.ContextID
	if contextID == "" {
		contextID = NewID()
	}
	text, err := respond(ctx, contextID, msg.Text())
	if err != nil {
		return nil, &Error{CodeInternalError, err.Error()}
	}
	answer := NewMessage("agent", contextID, text)
	return &answer, nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
