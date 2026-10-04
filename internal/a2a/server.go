package a2a

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"ouroboros/internal/identity"
)

const maxBodyBytes = 1 << 20

// Responder answers text within the conversation contextID.
type Responder func(ctx context.Context, contextID, text string) (string, error)

// Server serves the agent card and the authenticated JSON-RPC endpoint.
// Only trusted peers may send messages; everyone else becomes a pending
// connection request that a human has to approve, as long as its address does
// not send too many of them (see limiter). Requests have to be signed
// for this node. Serve the Handler over TLS with TLSConfig(Identity): that is
// how peers know that it is this node that answers, and what encrypts it.
type Server struct {
	Card     func() AgentCard // asked for every request, so that the card can change
	Identity *identity.Identity
	Trust    Trust
	Respond  Responder
	// Methods returns the methods added to the built-in ones, read anew for
	// every request. May be nil.
	Methods func() []Method
	// Log records what peers are not told, such as why the Responder failed
	// or why the contacts could not be read or changed.
	// It is called like log.Printf. May be nil.
	Log func(format string, args ...any)

	strangers limiter // limits the connection requests per address
}

// Caller is who sent a request.
type Caller struct {
	PublicKey string // authenticated by the request signature
	Addr      string // where to reach the caller again
	host      string // IP the request came from
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
		c.URL = "https://" + r.Host + "/"
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
		from := Caller{publicKey, callbackAddr(r, r.Header.Get(HeaderPort)), remoteHost(r)}
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
			trusted, _, err := s.Trust.State(from.PublicKey)
			if err != nil {
				return nil, s.trustFailed(from, err)
			}
			if !trusted {
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
	// Contacts that cannot be read trust nobody but the node itself.
	trusted, blocked, err := s.Trust.State(from.PublicKey)
	if from.PublicKey == s.Identity.Public() {
		trusted, err = true, nil
	}
	switch {
	case err != nil:
		return nil, s.trustFailed(from, err)
	case trusted:
		// The Responder learns who asks: what it does for a peer is limited.
		return s.reply(WithOrigin(ctx, Origin{PublicKey: from.PublicKey, Hops: p.Hops}), from, p.Message)
	case blocked:
		return nil, &Error{CodeBlocked, "connection refused"}
	}
	// A key costs nothing to make up, so a stranger is limited by its address.
	if !s.strangers.allow(from.host, time.Now()) {
		return nil, &Error{CodeTooManyRequests, "too many connection requests from your address, try again later"}
	}
	// A request that was not recorded is not pending: nobody would decide on it.
	if err := s.Trust.AddIncoming(from.PublicKey, p.Peer.Name, p.Peer.Model, from.Addr); err != nil {
		return nil, s.trustFailed(from, err)
	}
	return nil, &Error{CodePending, "connection request is waiting for approval"}
}

// accept notes that a peer this node asked to connect has agreed.
func (s *Server) accept(_ context.Context, from Caller, params json.RawMessage) (*Message, *Error) {
	var p contactParams
	if json.Unmarshal(params, &p) != nil {
		return nil, &Error{CodeInvalidParams, "invalid params"}
	}
	asked, err := s.Trust.HadOutgoing(from.PublicKey)
	if err == nil && asked {
		err = s.Trust.AddTrusted(from.PublicKey, p.Peer.Name, p.Peer.Model)
	}
	if err != nil {
		return nil, s.trustFailed(from, err)
	}
	return nil, nil
}

// reject notes that a peer this node asked to connect has refused.
func (s *Server) reject(_ context.Context, from Caller, _ json.RawMessage) (*Message, *Error) {
	asked, err := s.Trust.HadOutgoing(from.PublicKey)
	if err == nil && asked {
		err = s.Trust.RemoveOutgoing(from.PublicKey)
	}
	if err != nil {
		return nil, s.trustFailed(from, err)
	}
	return nil, nil
}

// trustFailed is the answer to a peer when the contacts could not be read or
// changed. The peer is only told that it failed; why goes to the log.
func (s *Server) trustFailed(from Caller, err error) *Error {
	s.logf("contacts failed for %s (%s): %v", from.Addr, from.PublicKey, err)
	return &Error{CodeInternalError, "internal error"}
}

// logf records what peers are not told, if there is a log.
func (s *Server) logf(format string, args ...any) {
	if s.Log != nil {
		s.Log(format, args...)
	}
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
		s.logf("error %s answering %s (%s): %v", id, from.Addr, from.PublicKey, err)
		if from.PublicKey == s.Identity.Public() {
			return nil, &Error{CodeInternalError, err.Error()}
		}
		return nil, &Error{CodeInternalError, "the agent could not answer (error " + id + ")"}
	}
	answer := NewMessage("agent", contextID, text)
	return &answer, nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
