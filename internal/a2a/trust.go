package a2a

// Trust is the policy that decides who may talk to this node and records the
// connection requests that wait for a human decision. contacts.Store implements it.
//
// Every method can fail, as the contacts are a file: the server then answers
// with CodeInternalError and logs the cause. A read that fails reports nobody
// as trusted.
type Trust interface {
	// State reports whether publicKey is trusted and/or blocked.
	State(publicKey string) (trusted, blocked bool, err error)
	// HadOutgoing reports whether this node asked publicKey to connect.
	HadOutgoing(publicKey string) (bool, error)
	// AddIncoming records a pending request from publicKey.
	AddIncoming(publicKey, name, model, addr string) error
	// AddTrusted marks publicKey as trusted, e.g. after it accepted our request.
	AddTrusted(publicKey, name, model string) error
	// RemoveOutgoing drops a pending outgoing request.
	RemoveOutgoing(publicKey string) error
}
