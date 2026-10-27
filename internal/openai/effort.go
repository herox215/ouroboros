package openai

import (
	"errors"
	"net/http"
	"slices"
	"sync"

	"ouroboros/internal/chat"
)

// Efforts is what a client learned of how its API takes an effort, by model.
// The APIs know different values: one that is refused is asked again with
// the next of steps, and from then on with what it took. Its zero value is
// ready to use.
type Efforts struct {
	mu   sync.Mutex
	took map[[2]string]string // by the model and the effort
}

// Of returns the values to ask model with for effort, in their order: the
// one it took the last time first.
func (e *Efforts) Of(model, effort string) []string {
	all := chat.Steps(effort)
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
