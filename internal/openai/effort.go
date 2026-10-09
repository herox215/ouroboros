package openai

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sync"
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
func (t Took) Fell() bool { return t.Told && t.Value != steps(t.Asked)[0] }

// Efforts is what a client learned of how its API takes an effort, by model.
// The APIs know different values: one that is refused is asked again with
// the next of steps, and from then on with what it took. Its zero value is
// ready to use.
type Efforts struct {
	mu   sync.Mutex
	took map[[2]string]string // by the model and the effort
}

// steps are the values an API is asked with for an effort, in this order,
// "" for none: the nearest it takes, and none at all if it takes none.
func steps(effort string) []string {
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

// Of returns the values to ask model with for effort, in their order: the
// one it took the last time first.
func (e *Efforts) Of(model, effort string) []string {
	all := steps(effort)
	e.mu.Lock()
	took, ok := e.took[[2]string{model, effort}]
	e.mu.Unlock()
	if !ok {
		return all
	}
	return all[slices.Index(all, took):]
}

// Took remembers that model took value for effort.
func (e *Efforts) Took(model, effort, value string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.took == nil {
		e.took = map[[2]string]string{}
	}
	e.took[[2]string{model, effort}] = value
}

// badRequest reports whether err is the API refusing what it was asked,
// with HTTP 400 or 422. Some APIs do not say why: an effort they do not
// know is refused without a word.
func badRequest(err error) bool {
	var api *APIError
	return errors.As(err, &api) && (api.Status == http.StatusBadRequest || api.Status == http.StatusUnprocessableEntity)
}
