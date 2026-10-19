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

type sentKey struct{}

// WithSent returns ctx under which every call of the LLM of an answer is
// sent what shape makes of the conversation, as it is, without the system
// prompt: what is kept of the conversation stays as it is. shape is to keep
// every call of a tool next to its results, and to make the same of the same
// messages, so that what the provider cached of them holds.
func WithSent(ctx context.Context, shape func([]openai.Message) []openai.Message) context.Context {
	return context.WithValue(ctx, sentKey{}, shape)
}

// sent is what the call of the LLM of the answer of ctx is sent of msgs, the
// system prompt first, see WithSent.
func sent(ctx context.Context, msgs []openai.Message) []openai.Message {
	shape, ok := ctx.Value(sentKey{}).(func([]openai.Message) []openai.Message)
	if !ok || shape == nil {
		return msgs
	}
	return append(msgs[:1:1], shape(copyMessages(msgs[1:]))...)
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
