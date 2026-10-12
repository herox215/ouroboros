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

type goOnKey struct{}

// WithGoOn returns ctx in which a tool can ask for the conversation to go on
// by itself once ouro started anew: goOn is called, see AskGoOn.
func WithGoOn(ctx context.Context, goOn func()) context.Context {
	return context.WithValue(ctx, goOnKey{}, goOn)
}

// AskGoOn asks whoever holds the conversation of ctx to go on with it by
// itself once ouro started anew from the build AskRestart asked for, as if
// the owner said to go on. It reports false if nobody does that.
func AskGoOn(ctx context.Context) bool {
	goOn, ok := ctx.Value(goOnKey{}).(func())
	if ok {
		goOn()
	}
	return ok
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
