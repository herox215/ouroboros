package session

import "context"

type restartKey struct{}

// WithRestart returns ctx in which a tool can ask for ouro to start anew
// from a new build: restart is told the binary to start, see AskRestart. The
// Service hands such a ctx to the answers of a node that can take its own
// place.
func WithRestart(ctx context.Context, restart func(exe string)) context.Context {
	return context.WithValue(ctx, restartKey{}, restart)
}

// AskRestart asks whoever holds the conversation of ctx to start exe in place of
// the running ouro once the answer is given, and to go on with the
// conversation there. It reports false if nobody does that, like a peer.
func AskRestart(ctx context.Context, exe string) bool {
	restart, ok := ctx.Value(restartKey{}).(func(string))
	if ok {
		restart(exe)
	}
	return ok
}
