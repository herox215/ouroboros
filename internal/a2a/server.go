package a2a

import (
	"context"
	"crypto/rand"
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
// connection request that a human has to approve. Requests have to be signed
// for this node, and everything it answers is signed with Identity.
type Server struct {
	Card     func() AgentCard // asked for every request, so that the card can change
	Identity *identity.Identity
	Trust    Trust
	Respond  Responder
	// Methods returns the methods added to the built-in ones, read anew for
	// every request. May be nil.
	Methods func() []Method
	// Log records what peers are not told, such as why the Responder failed.
	// It is called like log.Printf. May be nil.
	Log func(format string, args ...any)
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

// Handler returns the HTTP handler of the server. It serves the agent card
// and dispatches JSON-RPC requests to methods. Every request goes the same
// way: read and parse the body, authenticate the caller, look up the method,
// answer with its result or its error.
func (s *Server) Handler() http.Handler {
	verify := newVerifier(s.Identity.Public())
	// The built-in methods: JSON-RPC method name to handler.
	builtin := map[string]method{
		MethodSend:   s.send,
		MethodAccept: s.accept,
		MethodReject: s.reject,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+CardPath, func(w http.ResponseWriter, r *http.Request) {
		c := s.Card()
		c.URL = "http://" + r.Host + "/"
		// The card is public, but signed: who asks with a nonce learns that
		// the node holding the key answered just now.
		s.write(w, c, func(body []byte) []byte { return cardMessage(r.Header.Get(HeaderNonce), body) })
	})
	mux.HandleFunc("POST /", func(w http.ResponseWriter, r *http.Request) {
		res := response{JSONRPC: "2.0"}
		// Every response is signed for the request it answers, errors too.
		defer func() {
			s.write(w, res, func(body []byte) []byte {
				return responseMessage(r.Header.Get(HeaderNonce), r.Header.Get(HeaderSig), body)
			})
		}()

		var req request
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
		if err != nil || json.Unmarshal(body, &req) != nil {
			res.Error = &Error{CodeParseError, "parse error"}
			return
		}
		res.ID = req.ID
		publicKey, err := verify.verify(r, body, req.Method)
		if err != nil {
			res.Error = &Error{CodeUnauthorized, err.Error()}
			return
		}
		run, ok := builtin[req.Method]
		if !ok {
			if run, ok = s.added(req.Method); !ok {
				res.Error = &Error{CodeMethodNotFound, "method not found"}
				return
			}
		}
		from := Caller{publicKey, callbackAddr(r, r.Header.Get(HeaderPort))}
		res.Result, res.Error = run(r.Context(), from, req.Params)
	})
	return mux
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
	if from.PublicKey == s.Identity.Public() {
		trusted = true
	}
	switch {
	case trusted:
		// The Responder learns who asks: what it does for a peer is limited.
		return s.reply(WithOrigin(ctx, Origin{PublicKey: from.PublicKey, Hops: p.Hops}), from, p.Message)
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

// reply lets the Responder answer msg within its conversation.
//
// Why the Responder failed is nothing a peer gets to read: the error of an
// LLM or a tool names endpoints and whatever else its text contains. The peer
// is told that it failed, with an ID; the cause goes to the log under that ID.
// Only the owner, who asks with the key of this node, reads the cause itself.
func (s *Server) reply(ctx context.Context, from Caller, msg Message) (*Message, *Error) {
	contextID := msg.ContextID
	if contextID == "" {
		contextID = NewID()
	}
	text, err := s.Respond(ctx, contextID, msg.Text())
	if err != nil {
		id := rand.Text()[:8]
		if s.Log != nil {
			s.Log("error %s answering %s (%s): %v", id, from.Addr, from.PublicKey, err)
		}
		if from.PublicKey == s.Identity.Public() {
			return nil, &Error{CodeInternalError, err.Error()}
		}
		return nil, &Error{CodeInternalError, "the agent could not answer (error " + id + ")"}
	}
	answer := NewMessage("agent", contextID, text)
	return &answer, nil
}

// write answers with v as JSON, signed by this node. message returns the
// string the signature covers for the body that is sent.
func (s *Server) write(w http.ResponseWriter, v any, message func(body []byte) []byte) {
	// Cannot fail: cards and responses consist of strings and numbers.
	body, _ := json.Marshal(v)
	signAnswer(w.Header(), s.Identity, message(body))
	w.Header().Set("Content-Type", "application/json")
	w.Write(body)
}
