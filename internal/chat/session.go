package chat

import "context"

type sessionKey struct{}

// WithSession returns a context whose requests are sent with the stable
// session ID id, letting providers route and cache per conversation.
func WithSession(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, sessionKey{}, id)
}

// SessionOf is the session WithSession gave ctx, "" if none: another kind of
// provider sends it its own way.
func SessionOf(ctx context.Context) string {
	session, _ := ctx.Value(sessionKey{}).(string)
	return session
}
