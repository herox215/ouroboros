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
	Card     AgentCard
	Identity *identity.Identity
	Trust    Trust
	Respond  Responder
}

// NewHandler serves the agent card and the JSON-RPC endpoint without
// authentication: every peer is trusted. Used for tests and raw A2A clients.
func NewHandler(card AgentCard, respond Responder) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == CardPath {
			writeCard(w, r, card)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
		if err != nil {
			writeJSON(w, response{JSONRPC: "2.0", Error: &Error{CodeParseError, "parse error"}})
			return
		}
		var req request
		if json.Unmarshal(body, &req) != nil {
			writeJSON(w, response{JSONRPC: "2.0", Error: &Error{CodeParseError, "parse error"}})
			return
		}
		writeJSON(w, dispatch(r.Context(), req, respond))
	})
}

// Handler returns the HTTP handler of the server.
func (s *Server) Handler() http.Handler {
	verify := newVerifier()
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+CardPath, func(w http.ResponseWriter, r *http.Request) {
		writeCard(w, r, s.Card)
	})
	mux.HandleFunc("POST /", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
		if err != nil {
			writeJSON(w, response{JSONRPC: "2.0", Error: &Error{CodeParseError, "parse error"}})
			return
		}
		var req request
		if json.Unmarshal(body, &req) != nil {
			writeJSON(w, response{JSONRPC: "2.0", Error: &Error{CodeParseError, "parse error"}})
			return
		}
		publicKey, err := verify.verify(r, body)
		if err != nil {
			writeJSON(w, response{JSONRPC: "2.0", ID: req.ID, Error: &Error{CodeUnauthorized, err.Error()}})
			return
		}
		writeJSON(w, s.dispatch(r, publicKey, req))
	})
	return mux
}

func (s *Server) dispatch(r *http.Request, publicKey string, req request) response {
	res := response{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case MethodSend:
		text := req.Params.Message.Text()
		if text == "" {
			res.Error = &Error{CodeInvalidParams, "message needs a text part"}
			return res
		}
		trusted, blocked := s.Trust.State(publicKey)
		if s.Identity != nil && publicKey == s.Identity.Public() {
			trusted = true
		}
		if !trusted {
			if blocked {
				res.Error = &Error{CodeBlocked, "connection refused"}
				return res
			}
			s.Trust.AddIncoming(publicKey, req.Params.Peer.Name, req.Params.Peer.Model, callbackAddr(r, r.Header.Get(HeaderPort)))
			res.Error = &Error{CodePending, "connection request is waiting for approval"}
			return res
		}
		contextID := req.Params.Message.ContextID
		if contextID == "" {
			contextID = NewID()
		}
		answer, err := s.Respond(r.Context(), contextID, text)
		if err != nil {
			res.Error = &Error{CodeInternalError, err.Error()}
			return res
		}
		reply := NewMessage("agent", contextID, answer)
		res.Result = &reply
	case MethodAccept:
		if s.Trust.HadOutgoing(publicKey) {
			s.Trust.AddTrusted(publicKey, req.Params.Peer.Name, req.Params.Peer.Model)
		}
	case MethodReject:
		if s.Trust.HadOutgoing(publicKey) {
			s.Trust.RemoveOutgoing(publicKey)
		}
	default:
		res.Error = &Error{CodeMethodNotFound, "method not found"}
	}
	return res
}

// dispatch answers a request from an already trusted peer (raw handler).
func dispatch(ctx context.Context, req request, respond Responder) response {
	res := response{JSONRPC: "2.0", ID: req.ID}
	if req.Method != MethodSend {
		res.Error = &Error{CodeMethodNotFound, "method not found"}
		return res
	}
	text := req.Params.Message.Text()
	if text == "" {
		res.Error = &Error{CodeInvalidParams, "message needs a text part"}
		return res
	}
	contextID := req.Params.Message.ContextID
	if contextID == "" {
		contextID = NewID()
	}
	answer, err := respond(ctx, contextID, text)
	if err != nil {
		res.Error = &Error{CodeInternalError, err.Error()}
		return res
	}
	reply := NewMessage("agent", contextID, answer)
	res.Result = &reply
	return res
}

func writeCard(w http.ResponseWriter, r *http.Request, card AgentCard) {
	card.URL = "http://" + r.Host + "/"
	writeJSON(w, card)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
