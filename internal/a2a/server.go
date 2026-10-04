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
	// Channels returns the channels peers can open, read anew for every
	// request. May be nil.
	Channels func() []ChannelHandler
	// Log records what peers are not told, such as why the Responder failed
	// or why the contacts could not be read or changed.
	// It is called like log.Printf. May be nil.
	Log func(format string, args ...any)
	// Failed is told about every answer to a peer that failed, so that it
	// can be explained to the peer when it asks about it. May be nil.
	Failed func(Failure)

	strangers limiter    // limits the connection requests per address
	channels  ChannelSet // the channels peers have open, and their limits
}

// Failure is an answer to a peer that the Responder failed to give.
type Failure struct {
	ID        string // what the peer was told in place of the cause
	Peer      string // public key of the peer that asked
	ContextID string // conversation of the message
	MessageID string // the message that was not answered
	Err       error  // the cause, which is not for the peer to read
}

// Caller is who sent a request.
type Caller struct {
	PublicKey string // authenticated by the request signature
	Addr      string // where to reach the caller again
	host      string // IP the request came from
}

// Method is a JSON-RPC method added to the server. It is only called for
// peers the owner is connected to, and like every request only for one that
// was signed by the caller for this node and this method: Handle never runs
// for anyone else. It decodes params itself and returns the Message to answer
// with (nil for none) or an *Error with one of the Code constants.
//
// To add a method: contribute a Method from a plugin with plugin.Add; a peer
// runs it with Call of the network (peers.Network, Client.Call). A name
// that is built in (see Builtin) is never handed to an added method: what
// decides about trust cannot be replaced.
type Method struct {
	Name   string
	Handle func(ctx context.Context, from Caller, params json.RawMessage) (*Message, *Error)
}

// Builtin reports whether name is a method every node has. Such a method is
// never an added one: it is neither replaced by a Method nor run with
// Client.Call, and no channel is opened under its name.
func Builtin(name string) bool {
	return name == MethodSend || name == MethodRequest || name == MethodAccept || name == MethodReject || name == MethodOpen
}

// method handles the JSON-RPC method it is registered for.
type method func(ctx context.Context, from Caller, params json.RawMessage) (*Message, *Error)

// Handler returns the HTTP handler of the server. It serves the agent card
// and dispatches JSON-RPC requests to methods. Every request goes the same
// way: read and parse the body, authenticate the caller, look up the method,
// answer with its result or its error. Only MethodOpen ends differently: once
// the caller is authenticated, the connection becomes a channel (see open).
func (s *Server) Handler() http.Handler {
	verify := newVerifier(s.Identity.Public())
	// The built-in methods: JSON-RPC method name to handler. Keep Builtin in
	// step with them and MethodOpen.
	builtin := map[string]method{
		MethodSend:    s.send,
		MethodRequest: s.request,
		MethodAccept:  s.accept,
		MethodReject:  s.reject,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+CardPath, func(w http.ResponseWriter, r *http.Request) {
		c := s.Card()
		c.URL = "https://" + r.Host + "/"
		writeJSON(w, c)
	})
	mux.HandleFunc("POST /", func(w http.ResponseWriter, r *http.Request) {
		res := response{JSONRPC: "2.0"}
		opened := false // the connection is a channel now: there is nothing to answer
		defer func() {
			if !opened {
				writeJSON(w, res)
			}
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
		from := Caller{publicKey, callbackAddr(r, r.Header.Get(HeaderPort)), remoteHost(r)}
		if req.Method == MethodOpen {
			res.Error = s.open(w, from, req.Params)
			opened = res.Error == nil
			return
		}
		run, ok := builtin[req.Method]
		if !ok {
			if run, ok = s.added(req.Method); !ok {
				res.Error = &Error{CodeMethodNotFound, "method not found"}
				return
			}
		}
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

// open turns the connection of a request into the channel its params name
// and runs the handler of that channel on it, until the handler returns or
// the channel is closed. It is the only method that takes the connection
// away from HTTP: the caller is answered with "101 Switching Protocols" and
// then with frames, see Channel.
//
// What comes before is the same as for an added method: the request was
// signed by from for this node, and only a peer the owner is connected to
// gets a channel. A peer the owner refused gets none, whatever else the
// contacts say about it: it is what the channels of a peer are closed for,
// see ChannelSet. A caller that is refused is answered like any other, with
// the error that is returned, and no code of a plugin has run.
func (s *Server) open(w http.ResponseWriter, from Caller, params json.RawMessage) *Error {
	var p channelParams
	if json.Unmarshal(params, &p) != nil || p.Name == "" {
		return &Error{CodeInvalidParams, "invalid params"}
	}
	handler, ok := s.channel(p.Name)
	if !ok {
		return &Error{CodeMethodNotFound, "channel not found"}
	}
	trusted, blocked, err := s.Trust.State(from.PublicKey)
	if err != nil {
		return s.trustFailed(from, err)
	}
	if !trusted || blocked {
		return &Error{CodeUnauthorized, "not connected"}
	}
	if err := s.channels.reserve(from.PublicKey, s.Trust, true); err != nil {
		return err
	}
	conn, buf, err := http.NewResponseController(w).Hijack()
	if err != nil {
		s.channels.release(from.PublicKey)
		s.logf("no channel %s for %s (%s): %v", p.Name, from.Addr, from.PublicKey, err)
		return &Error{CodeInternalError, "internal error"}
	}
	// The time a request may take does not apply to a channel. If the caller
	// is gone already, the channel finds out when it is used.
	conn.SetDeadline(time.Time{})
	io.WriteString(conn, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: "+ChannelProtocol+"\r\n\r\n")

	// What the caller sent after the request is in buf already.
	ch := newChannel(conn, buf.Reader, from.PublicKey, s.channels.idleTimeout(), s.channels.closeWait())
	s.channels.add(ch)
	defer ch.Close()
	handler.Handle(ch.Context(), ch)
	return nil
}

// channel returns the channel handler called name.
func (s *Server) channel(name string) (ChannelHandler, bool) {
	if s.Channels == nil || Builtin(name) {
		return ChannelHandler{}, false
	}
	for _, h := range s.Channels() {
		if h.Name == name {
			return h, true
		}
	}
	return ChannelHandler{}, false
}

// Close closes every channel peers have open to the server, and from then on
// no further one is opened. Shutting down the HTTP server does not: a channel
// is no request that ends by itself.
func (s *Server) Close() { s.channels.Close() }

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
	if err := s.admit(from, p.Peer); err != nil {
		return nil, err
	}
	// The Responder learns who asks: what it does for a peer is limited.
	return s.reply(WithOrigin(ctx, Origin{PublicKey: from.PublicKey, Hops: p.Hops}), from, p.Message)
}

// request answers a node that asks to connect: nothing if the owner is
// connected to it already, and what send answers a stranger otherwise. It is
// how a node asks without a message, which a stranger would get to read.
func (s *Server) request(_ context.Context, from Caller, params json.RawMessage) (*Message, *Error) {
	var p contactParams
	if json.Unmarshal(params, &p) != nil {
		return nil, &Error{CodeInvalidParams, "invalid params"}
	}
	return nil, s.admit(from, p.Peer)
}

// admit lets a peer the owner is connected to through: nil. Anyone else is
// told why not, and a stranger, who calls itself peer, leaves a connection
// request on the way.
func (s *Server) admit(from Caller, peer Peer) *Error {
	// Contacts that cannot be read trust nobody but the node itself.
	trusted, blocked, err := s.Trust.State(from.PublicKey)
	if from.PublicKey == s.Identity.Public() {
		trusted, err = true, nil
	}
	switch {
	case err != nil:
		return s.trustFailed(from, err)
	case trusted:
		return nil
	case blocked:
		return &Error{CodeBlocked, "connection refused"}
	}
	// A key costs nothing to make up, so a stranger is limited by its address.
	if !s.strangers.allow(from.host, time.Now()) {
		return &Error{CodeTooManyRequests, "too many connection requests from your address, try again later"}
	}
	// A request that was not recorded is not pending: nobody would decide on it.
	if err := s.Trust.AddIncoming(from.PublicKey, peer.Name, peer.Model, from.Addr); err != nil {
		return s.trustFailed(from, err)
	}
	return &Error{CodePending, "connection request is waiting for approval"}
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
// is told that it failed, with an ID; the cause goes to the log under that ID
// and to Failed, which keeps it for the peer to ask about later. The answer
// promises no explanation: whoever would give it may be what failed.
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
		if s.Failed != nil {
			s.Failed(Failure{ID: id, Peer: from.PublicKey, ContextID: contextID, MessageID: msg.MessageID, Err: err})
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
