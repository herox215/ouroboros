package plugin

import (
	"context"
	"ouroboros/internal/openai"
)

// GatewayMetadata is an immutable, bounded JSON context snapshot, serialized as
// an object in archives and source envelopes.
type GatewayMetadata = openai.GatewayMetadata

// NewGatewayMetadata copies a gateway's descriptive context as JSON. Invalid or
// oversized (over 16 KiB) objects return an error. It confers no permissions.
func NewGatewayMetadata(fields map[string]any) (GatewayMetadata, error) {
	return openai.NewGatewayMetadata(fields)
}

type gatewayMetadataKey struct{}

// WithGatewayMetadata attaches host-supplied context to one Send or Reply,
// including image/file variants. Keep state isolated by authenticated room and
// sender; never include credentials. The adapter always supplies its own origin.
// Snapshots are archived with messages; later updates cannot change old inputs.
func WithGatewayMetadata(ctx context.Context, metadata GatewayMetadata) context.Context {
	return context.WithValue(ctx, gatewayMetadataKey{}, metadata)
}

func (g *GatewayAdapter) messageContext(ctx context.Context) context.Context {
	source := g.source
	source.Metadata, _ = ctx.Value(gatewayMetadataKey{}).(GatewayMetadata)
	return WithMessageSource(ctx, source)
}
