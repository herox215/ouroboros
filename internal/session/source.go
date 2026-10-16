package session

import (
	"context"
	"ouroboros/internal/openai"
)

// MessageSource identifies the interface that sent one message, not a session.
type MessageSource = openai.MessageSource

// WithMessageSource attaches the sending interface to an Ask or Steer call.
func WithMessageSource(ctx context.Context, source MessageSource) context.Context {
	return openai.WithMessageSource(ctx, source)
}
