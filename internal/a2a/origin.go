package a2a

import "context"

// Origin is the peer a conversation comes from. The server puts it into the
// context of every message it hands to the Responder; a context without one
// belongs to the owner of this node.
//
// Everything a peer could misuse checks it: Client.Send counts the hops, and
// code that acts for the conversation can refuse what only the owner may do.
type Origin struct {
	PublicKey string // of the peer, authenticated by its signature
	Hops      int    // agents that passed the message on before it got here
}

type originKey struct{}

// WithOrigin returns a context for a conversation that comes from a peer.
func WithOrigin(ctx context.Context, o Origin) context.Context {
	return context.WithValue(ctx, originKey{}, o)
}

// OriginFrom returns the peer the conversation of ctx comes from. It reports
// false for a conversation of the owner.
func OriginFrom(ctx context.Context) (Origin, bool) {
	o, ok := ctx.Value(originKey{}).(Origin)
	return o, ok
}
