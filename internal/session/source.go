package session

import (
	"context"
	"ouroboros/internal/chat"
)

// MessageSource identifies the interface that sent one message, not a session.
type MessageSource = chat.MessageSource

// WithMessageSource attaches the sending interface to an Ask or Steer call.
func WithMessageSource(ctx context.Context, source MessageSource) context.Context {
	return chat.WithMessageSource(ctx, source)
}
