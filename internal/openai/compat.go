package openai

import (
	"context"
	"ouroboros/internal/chat"
)

// Message is an alias kept for old callers.
//
// Deprecated: use chat.Message from internal/chat.
type Message = chat.Message

// Image is an alias kept for old callers.
//
// Deprecated: use chat.Image from internal/chat.
type Image = chat.Image

// Usage is an alias kept for old callers.
//
// Deprecated: use chat.Usage from internal/chat.
type Usage = chat.Usage

// ToolCall is an alias kept for old callers.
//
// Deprecated: use chat.ToolCall from internal/chat.
type ToolCall = chat.ToolCall

// FunctionCall is an alias kept for old callers.
//
// Deprecated: use chat.FunctionCall from internal/chat.
type FunctionCall = chat.FunctionCall

// Tool is an alias kept for old callers.
//
// Deprecated: use chat.Tool from internal/chat.
type Tool = chat.Tool

// Function is an alias kept for old callers.
//
// Deprecated: use chat.Function from internal/chat.
type Function = chat.Function

// MessageSource is an alias kept for old callers.
//
// Deprecated: use chat.MessageSource from internal/chat.
type MessageSource = chat.MessageSource

// GatewayMetadata is an alias kept for old callers.
//
// Deprecated: use chat.GatewayMetadata from internal/chat.
type GatewayMetadata = chat.GatewayMetadata

// Took is an alias kept for old callers.
//
// Deprecated: use chat.Took from internal/chat.
type Took = chat.Took

// ReasoningItems is an alias kept for old callers.
//
// Deprecated: use chat.ReasoningItems from internal/chat.
const ReasoningItems = chat.ReasoningItems

// Deprecated: use chat.NewTool from internal/chat.
func NewTool(name, description, schema string) Tool {
	return chat.NewTool(name, description, schema)
}

// Deprecated: use chat.MessagesForModel from internal/chat.
func MessagesForModel(msgs []Message) []Message { return chat.MessagesForModel(msgs) }

// Deprecated: use chat.NewGatewayMetadata from internal/chat.
func NewGatewayMetadata(fields map[string]any) (GatewayMetadata, error) {
	return chat.NewGatewayMetadata(fields)
}

// Deprecated: use chat.WithMessageSource from internal/chat.
func WithMessageSource(ctx context.Context, source MessageSource) context.Context {
	return chat.WithMessageSource(ctx, source)
}

// Deprecated: use chat.WithoutMessageSource from internal/chat.
func WithoutMessageSource(ctx context.Context) context.Context { return chat.WithoutMessageSource(ctx) }

// Deprecated: use chat.MessageSourceFrom from internal/chat.
func MessageSourceFrom(ctx context.Context) MessageSource { return chat.MessageSourceFrom(ctx) }

// Deprecated: use chat.WithSession from internal/chat.
func WithSession(ctx context.Context, id string) context.Context { return chat.WithSession(ctx, id) }

// Deprecated: use chat.SessionOf from internal/chat.
func SessionOf(ctx context.Context) string { return chat.SessionOf(ctx) }

// Deprecated: use chat.WithEffort from internal/chat.
func WithEffort(ctx context.Context, effort string) context.Context {
	return chat.WithEffort(ctx, effort)
}

// Deprecated: use chat.WithEffortOf from internal/chat.
func WithEffortOf(ctx context.Context, effort func() string) context.Context {
	return chat.WithEffortOf(ctx, effort)
}

// Deprecated: use chat.EffortOf from internal/chat.
func EffortOf(ctx context.Context) string { return chat.EffortOf(ctx) }

// Deprecated: use chat.HasEffort from internal/chat.
func HasEffort(ctx context.Context) bool { return chat.HasEffort(ctx) }

// Deprecated: use chat.WithTrace from internal/chat.
func WithTrace(ctx context.Context, log func(format string, args ...any)) context.Context {
	return chat.WithTrace(ctx, log)
}

// Deprecated: use chat.WithThought from internal/chat.
func WithThought(ctx context.Context, sink func(text string)) context.Context {
	return chat.WithThought(ctx, sink)
}

// Deprecated: use chat.WithAnswering from internal/chat.
func WithAnswering(ctx context.Context, sink func(text string)) context.Context {
	return chat.WithAnswering(ctx, sink)
}

// Deprecated: use chat.WithCalling from internal/chat.
func WithCalling(ctx context.Context, sink func(bytes int)) context.Context {
	return chat.WithCalling(ctx, sink)
}

// Deprecated: use chat.AnsweringOf from internal/chat.
func AnsweringOf(ctx context.Context) func(text string) { return chat.AnsweringOf(ctx) }

// Deprecated: use chat.CallingOf from internal/chat.
func CallingOf(ctx context.Context) func(bytes int) { return chat.CallingOf(ctx) }

// Deprecated: use chat.TraceOf from internal/chat.
func TraceOf(ctx context.Context) func(format string, args ...any) { return chat.TraceOf(ctx) }
