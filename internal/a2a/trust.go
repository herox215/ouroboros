package a2a

// Trust is the policy that decides who may talk to this node and records the
// connection requests that wait for a human decision. contacts.Store implements it.
type Trust interface {
	// State reports whether publicKey is trusted and/or blocked.
	State(publicKey string) (trusted, blocked bool)
	// HadOutgoing reports whether this node asked publicKey to connect.
	HadOutgoing(publicKey string) bool
	// AddIncoming records a pending request from publicKey.
	AddIncoming(publicKey, name, model, addr string)
	// AddTrusted marks publicKey as trusted, e.g. after it accepted our request.
	AddTrusted(publicKey, name, model string)
	// RemoveOutgoing drops a pending outgoing request.
	RemoveOutgoing(publicKey string)
}
