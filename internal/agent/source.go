package agent

import (
	"context"
	"errors"

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

type interruptKey struct{}

// WithInterrupt returns ctx under which a message that comes for the answer
// while the LLM writes stops that call of the LLM: what it wrote is dropped,
// and the LLM is called anew with the message, see WithMessageSteering. One
// is sent on interrupt for each such message; one that comes while a tool
// runs waits for its results instead.
func WithInterrupt(ctx context.Context, interrupt <-chan struct{}) context.Context {
	return context.WithValue(ctx, interruptKey{}, interrupt)
}

// Interrupt is what tells the answer of ctx of a message for it, see
// WithInterrupt: nil without.
func Interrupt(ctx context.Context) <-chan struct{} {
	interrupt, _ := ctx.Value(interruptKey{}).(<-chan struct{})
	return interrupt
}

// interrupting returns the context the LLM is called under in ctx: one that
// ends once a message comes for the answer, see WithInterrupt. stop ends what
// waits for it, and interrupted tells whether a message ended it.
func interrupting(ctx context.Context) (call context.Context, stop func(), interrupted func() bool) {
	interrupt := Interrupt(ctx)
	if interrupt == nil {
		return ctx, func() {}, func() bool { return false }
	}
	// A message that came while a tool ran was taken after its results.
	for drained := false; !drained; {
		select {
		case <-interrupt:
		default:
			drained = true
		}
	}
	call, cancel := context.WithCancelCause(ctx)
	done := make(chan struct{})
	go func() {
		select {
		case <-interrupt:
			cancel(errInterrupted)
		case <-done:
		}
	}()
	return call, func() { close(done); cancel(nil) }, func() bool { return context.Cause(call) == errInterrupted }
}

// errInterrupted ends a call of the LLM that a message for the answer came
// during, see WithInterrupt.
var errInterrupted = errors.New("a message came for the answer")

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
