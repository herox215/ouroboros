package chat

import (
	"context"
)

type effortKey struct{}

// WithEffort returns a context whose calls ask the model to think as much as
// effort before it answers: one of config.AllEfforts, like "low" or "max";
// "" leaves it to the model.
func WithEffort(ctx context.Context, effort string) context.Context {
	return WithEffortOf(ctx, func() string { return effort })
}

// WithEffortOf is like WithEffort with the effort that effort returns when a
// call is made: one that changes holds from the next call on.
func WithEffortOf(ctx context.Context, effort func() string) context.Context {
	return context.WithValue(ctx, effortKey{}, effort)
}

// EffortOf is the effort WithEffort or WithEffortOf gave ctx, "" if none:
// another kind of provider sends it its own way.
func EffortOf(ctx context.Context) string {
	if effort, ok := ctx.Value(effortKey{}).(func() string); ok {
		return effort()
	}
	return ""
}

// HasEffort reports whether ctx was given an effort, "" too.
func HasEffort(ctx context.Context) bool {
	_, ok := ctx.Value(effortKey{}).(func() string)
	return ok
}

// Took is the effort a call was asked with and what the API took of it.
type Took struct {
	// Asked is the effort, "" for none; Value is what the API was sent of
	// it in the end, like "xhigh" for "max", "" for nothing; Told says
	// that the provider told them at all.
	Asked, Value string
	Told         bool
}

// Fell reports whether the API took less than was asked, or something else:
// it refused what the effort is sent as, and took one that is near, or none.
func (t Took) Fell() bool { return t.Told && t.Value != Steps(t.Asked)[0] }

// Steps are the values an API is asked with for an effort, in this order,
// "" for none: the nearest it takes, and none at all if it takes none.
func Steps(effort string) []string {
	switch effort {
	case "":
		return []string{""}
	case "none":
		return []string{"none", "minimal", "low", ""}
	case "minimal":
		return []string{"minimal", "low", ""}
	case "xhigh":
		return []string{"xhigh", "high", ""}
	case "max":
		return []string{"max", "xhigh", "high", ""}
	}
	return []string{effort, ""}
}
