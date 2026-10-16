package agent

import (
	"context"

	"ouroboros/internal/openai"
)

// WithInputMessages replaces Run's text input with distinct messages, including
// an explicitly empty slice. A copy is taken so the caller can reuse its input.
func WithInputMessages(ctx context.Context, messages []openai.Message) context.Context {
	return context.WithValue(ctx, inputMessagesKey{}, copyMessages(messages))
}

// WithMessageSteering is WithSteering with per-message metadata. When present,
// it takes precedence over the older string callback, even if it returns none.
func WithMessageSteering(ctx context.Context, steer func() []openai.Message) context.Context {
	return context.WithValue(ctx, messageSteeringKey{}, steer)
}

func steeredMessages(ctx context.Context) []openai.Message {
	return SteeredMessages(ctx)
}

// SteeredMessages consumes pending steering with its per-message metadata,
// falling back to legacy text steering attributed to the context source.
func SteeredMessages(ctx context.Context) []openai.Message {
	if steer, ok := ctx.Value(messageSteeringKey{}).(func() []openai.Message); ok && steer != nil {
		return copyMessages(steer())
	}
	var messages []openai.Message
	for _, text := range Steered(ctx) {
		messages = append(messages, openai.Message{Role: "user", Content: text, Source: openai.MessageSourceFrom(ctx)})
	}
	return messages
}
