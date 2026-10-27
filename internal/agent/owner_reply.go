package agent

import (
	"context"
	"encoding/json"

	"ouroboros/internal/chat"
	"ouroboros/internal/presentation"
)

// OwnerReply keeps the owner's exact answer separate from its origin.
// Source is absent for callers that use the legacy WithOwner callback.
// Images are transient and excluded from JSON; AskOwnerReply shows them to
// the model with the calling tool result, including when Text is empty.
type OwnerReply struct {
	HasAttachments bool               `json:"-"` // Generic files are never approval evidence.
	Images         []chat.Image       `json:"-"` // Live model input only; never approval evidence.
	Text           string             `json:"answer"`
	Source         chat.MessageSource `json:"source,omitzero"`
}

// WithOwnerReply is WithOwner with per-reply source metadata.
func WithOwnerReply(ctx context.Context, ask func(context.Context, json.RawMessage) (OwnerReply, error)) context.Context {
	return context.WithValue(ctx, ownerKey{}, ask)
}

// sayKey keeps where a tool says something to the owner, see Say.
type sayKey struct{}

// WithSaying returns a context under which a tool can say text to the owner
// of the conversation while it runs, see Say: say shows it to them, as the
// occurrence id, which the conversation keeps for it.
func WithSaying(ctx context.Context, say func(id, text string)) context.Context {
	return context.WithValue(ctx, sayKey{}, say)
}

// SaidHead begins what the agent remembers of what a tool said to the owner
// on its way to the answer, see Say: the text follows it.
const SaidHead = "[Not from the owner: you said to them on your way, as they read it:]\n\n"

// Say says text to the owner of the conversation of ctx, as the agent says
// something on its way to the answer: like that it goes on with what they
// answered. The answer keeps it, see Keep, as the occurrence it was shown
// as. It reports false where nobody hears it.
func Say(ctx context.Context, text string) bool {
	say, ok := ctx.Value(sayKey{}).(func(string, string))
	if !ok || say == nil || text == "" {
		return false
	}
	id := presentation.NewID()
	say(id, text)
	keep(ctx, chat.Message{Role: "user", Content: SaidHead + text, PresentationID: id})
	return true
}

// Keep makes the answer of ctx remember text, which a tool tells the agent
// along with its result, like what the owner was asked before it ran: as a
// message of its own after the results of the round, which is archived with
// the answer. It reports false outside of an answer.
func Keep(ctx context.Context, text string) bool {
	return keep(ctx, chat.Message{Role: "user", Content: text})
}

func keep(ctx context.Context, m chat.Message) bool {
	a := AnswerOf(ctx)
	if a == nil {
		return false
	}
	a.keptMu.Lock()
	defer a.keptMu.Unlock()
	a.kept = append(a.kept, m)
	return true
}

// takeKept returns what the tools of the round asked the answer to keep,
// see Keep, once.
func (a *Answer) takeKept() []chat.Message {
	a.keptMu.Lock()
	defer a.keptMu.Unlock()
	kept := a.kept
	a.kept = nil
	return kept
}

// AskOwnerReply asks the owner without mixing backend context into their answer.
func AskOwnerReply(ctx context.Context, question json.RawMessage) (OwnerReply, error) {
	ask, ok := ctx.Value(ownerKey{}).(func(context.Context, json.RawMessage) (OwnerReply, error))
	if !ok || ask == nil {
		return OwnerReply{}, ErrNoOwner
	}
	reply, err := ask(ctx, question)
	if err == nil {
		Show(ctx, reply.Images...)
	}
	return reply, err
}
