package a2a

import (
	"context"
	"encoding/json"
	"net/http"
)

const maxBodyBytes = 1 << 20

// Responder answers text within the conversation contextID.
type Responder func(ctx context.Context, contextID, text string) (string, error)

// NewHandler serves the agent card and the JSON-RPC endpoint.
// The card's URL is derived from the Host header of each request.
func NewHandler(card AgentCard, respond Responder) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+CardPath, func(w http.ResponseWriter, r *http.Request) {
		c := card
		c.URL = "http://" + r.Host + "/"
		writeJSON(w, c)
	})
	mux.HandleFunc("POST /", func(w http.ResponseWriter, r *http.Request) {
		var req request
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&req); err != nil {
			writeJSON(w, response{JSONRPC: "2.0", Error: &Error{CodeParseError, "parse error"}})
			return
		}
		writeJSON(w, dispatch(r.Context(), req, respond))
	})
	return mux
}

func dispatch(ctx context.Context, req request, respond Responder) response {
	res := response{JSONRPC: "2.0", ID: req.ID}
	if req.Method != MethodSend {
		res.Error = &Error{CodeMethodNotFound, "method not found"}
		return res
	}
	msg := req.Params.Message
	text := msg.Text()
	if text == "" {
		res.Error = &Error{CodeInvalidParams, "message needs a text part"}
		return res
	}
	contextID := msg.ContextID
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

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
