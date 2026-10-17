// Package presentation gives locally presented messages stable, opaque identities.
package presentation

import (
	"context"
	"crypto/rand"
	"sync"
)

type traceKey struct{}

type trace struct {
	mu       sync.Mutex
	current  string
	terminal string
}

// New starts a fresh presentation trace, even if ctx already has one.
func New(ctx context.Context) context.Context {
	return context.WithValue(ctx, traceKey{}, &trace{terminal: NewID()})
}

// Ensure preserves an existing trace or starts one if ctx has none.
func Ensure(ctx context.Context) context.Context {
	if ctx.Value(traceKey{}) != nil {
		return ctx
	}
	return New(ctx)
}

// NewID returns a unique opaque identity, independent of message text.
func NewID() string { return rand.Text() }

// Next returns a fresh identity and makes it current in ctx's trace.
// Call New or Ensure first to retain the identity in the context.
func Next(ctx context.Context) string {
	id := NewID()
	if t, _ := ctx.Value(traceKey{}).(*trace); t != nil {
		t.mu.Lock()
		t.current = id
		t.mu.Unlock()
	}
	return id
}

// Current returns the last identity from Next, or empty if there is none.
func Current(ctx context.Context) string {
	if t, _ := ctx.Value(traceKey{}).(*trace); t != nil {
		t.mu.Lock()
		defer t.mu.Unlock()
		return t.current
	}
	return ""
}

// Terminal returns the trace's fixed terminal identity, or empty without a trace.
func Terminal(ctx context.Context) string {
	if t, _ := ctx.Value(traceKey{}).(*trace); t != nil {
		return t.terminal
	}
	return ""
}
