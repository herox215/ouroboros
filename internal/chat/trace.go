package chat

import "context"

// A trace is told what a call of the LLM sends and gets, beyond what the
// caller sees of it: for the debug mode of a node, to find out why a call
// costs what it does.
type trace func(format string, args ...any)

type traceKey struct{}

// WithTrace returns a context whose calls of the LLM tell log, line by line,
// what they send and get: the request in short, how far it begins like the
// last request of its session (what a provider can have cached of it), the
// headers of the response and the usage as the API sent it.
func WithTrace(ctx context.Context, log func(format string, args ...any)) context.Context {
	return context.WithValue(ctx, traceKey{}, trace(log))
}

// traceOf returns the trace of ctx, nil if it has none.
func traceOf(ctx context.Context) trace {
	t, _ := ctx.Value(traceKey{}).(trace)
	return t
}

// TraceOf returns what a call of the LLM under ctx tells its trace with, nil
// if it has none: see WithTrace. It is for an adapter that tells the same
// lines of its own requests.
func TraceOf(ctx context.Context) func(format string, args ...any) {
	if t := traceOf(ctx); t != nil {
		return t
	}
	return nil
}

// thoughtKey marks the sink a stream tells what its model thinks.
type thoughtKey struct{}

// WithThought returns a context whose calls of the LLM tell sink every piece
// of what the model thinks before it writes, as the stream brings it: for a
// front end that shows the thinking as it happens. A provider that keeps its
// thinking to itself, as the responses API of OpenAI does, tells nothing.
func WithThought(ctx context.Context, sink func(text string)) context.Context {
	return context.WithValue(ctx, thoughtKey{}, sink)
}

// ThoughtOf returns the sink of ctx for what the model thinks, nil if it has
// none: see WithThought. It is for an adapter that reads its own stream.
func ThoughtOf(ctx context.Context) func(text string) {
	sink, _ := ctx.Value(thoughtKey{}).(func(text string))
	return sink
}

// answerKey marks the sink a stream tells what its model writes.
type answerKey struct{}

// WithAnswering returns a context whose calls of the LLM tell sink every piece
// of the answer it writes, as the stream brings it: for a front end that shows
// the answer while it is on its way. The whole of it comes with the message
// all the same, so a front end shows the pieces only until then.
func WithAnswering(ctx context.Context, sink func(text string)) context.Context {
	return context.WithValue(ctx, answerKey{}, sink)
}

// AnsweringOf returns the sink of ctx for what the model writes, nil if it has
// none.
func AnsweringOf(ctx context.Context) func(text string) {
	sink, _ := ctx.Value(answerKey{}).(func(text string))
	return sink
}

// callingKey marks the sink a stream tells how much of calls of tools its
// model has written.
type callingKey struct{}

// WithCalling returns a context whose calls of the LLM tell sink how many
// bytes of calls of tools the model has written so far in the call, as the
// stream brings them: for a front end that shows that the model writes while
// none of it is text, as when it writes a script of some KiB. A stream that
// brings a call in one piece tells it once.
func WithCalling(ctx context.Context, sink func(bytes int)) context.Context {
	return context.WithValue(ctx, callingKey{}, sink)
}

// CallingOf returns the sink of ctx for how much of calls of tools the model
// has written, nil if it has none.
func CallingOf(ctx context.Context) func(bytes int) {
	sink, _ := ctx.Value(callingKey{}).(func(bytes int))
	return sink
}
