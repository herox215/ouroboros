package agent

import "context"

// WithoutInputMessages lets a child agent use its own input instead of inheriting
// the owner's message envelope. It leaves the parent's context unchanged.
func WithoutInputMessages(ctx context.Context) context.Context {
	return context.WithValue(ctx, inputMessagesKey{}, struct{}{})
}
