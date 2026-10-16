package agent

import (
	"context"
	"encoding/json"

	"ouroboros/internal/openai"
)

// OwnerReply keeps the owner's exact answer separate from its origin.
// Source is absent for callers that use the legacy WithOwner callback.
type OwnerReply struct {
	Text   string               `json:"answer"`
	Source openai.MessageSource `json:"source,omitzero"`
}

// WithOwnerReply is WithOwner with per-reply source metadata.
func WithOwnerReply(ctx context.Context, ask func(context.Context, json.RawMessage) (OwnerReply, error)) context.Context {
	return context.WithValue(ctx, ownerKey{}, ask)
}

// AskOwnerReply asks the owner without mixing backend context into their answer.
func AskOwnerReply(ctx context.Context, question json.RawMessage) (OwnerReply, error) {
	ask, ok := ctx.Value(ownerKey{}).(func(context.Context, json.RawMessage) (OwnerReply, error))
	if !ok || ask == nil {
		return OwnerReply{}, ErrNoOwner
	}
	return ask(ctx, question)
}
