package chat

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"syscall"
)

// Category is what an error of a call of the LLM means to the decisions of a
// node, as a bit set: fallbacks switch on Unavailable, retries on Transient,
// and diagnosis explains the rest. A category may hold several bits when an
// error means several things, like an overloaded gateway, which is both
// unavailable and transient.
type Category uint16

const (
	// Unavailable: the API cannot answer for now, whatever it is asked. A
	// node with fallbacks goes on with another access.
	Unavailable Category = 1 << iota
	// Transient: a call broke off on its way and asked again is likely
	// answered.
	Transient
	// RateLimited: a rate limit or quota is reached (HTTP 429).
	RateLimited
	// ServiceFailure: the service failed on its side (HTTP 5xx).
	ServiceFailure
	// Rejected: the request itself was refused, like a context that is too
	// long or a key that is not allowed the model. It is not tried again as
	// it is, and no fallback helps. HTTP 402 carries it too, because its
	// quota is out for now.
	Rejected
	// Unauthorized: the credentials were refused (HTTP 401 or 403).
	Unauthorized
	// Canceled: the call was stopped by its caller.
	Canceled
	// Timeout: the call waited too long, and it counts as Unavailable too:
	// an adapter that watches its own stream for a stall gives up with
	// context.DeadlineExceeded while the caller's context is still live,
	// and that call goes on with a fallback. A caller that gives up is
	// told by its context, which the fallback checks itself, not by this
	// bit.
	Timeout
)

// Categorized is an error that says what it means to the decisions of a node.
// Adapters implement it for their errors; errors.As finds it through wrapping.
type Categorized interface {
	Category() Category
}

// Of returns what err means, 0 if it says nothing. A canceled call takes
// precedence over everything, as it did before: an error marked Unavailable
// that carries context.Canceled is Canceled only. A deadline is Timeout and
// Unavailable. Network errors are Unavailable, and a timeout among them is
// Timeout too. EOF, an unexpected EOF, a reset, aborted or broken connection
// are Transient; the reset ones are also Unavailable, being network errors.
// A marked error gets the bits of the connection it carries: a marked call
// that ended in a reset is Transient as well.
func Of(err error) Category {
	if err == nil {
		return 0
	}
	if errors.Is(err, context.Canceled) {
		return Canceled
	}
	var categorized Categorized
	if errors.As(err, &categorized) {
		category := categorized.Category()
		if errors.Is(err, context.DeadlineExceeded) {
			category |= Timeout | Unavailable
			category &^= Transient
		}
		return category | endedOnItsWay(err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return Timeout | Unavailable
	}
	var network net.Error
	if errors.As(err, &network) {
		category := Unavailable
		if network.Timeout() {
			category |= Timeout
		}
		return category | endedOnItsWay(err)
	}
	return endedOnItsWay(err)
}

// endedOnItsWay is what err says as a connection that ended before the answer
// was in: Transient, and Unavailable too for a reset, aborted or broken one,
// which is a network error.
func endedOnItsWay(err error) Category {
	switch {
	case errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNABORTED) || errors.Is(err, syscall.EPIPE):
		return Unavailable | Transient
	case errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF):
		return Transient
	}
	return 0
}

// IsUnavailable reports whether err says that the API cannot answer for now,
// whatever it is asked: a limit or a quota is reached (HTTP 429 or 402), the
// service fails (5xx), it is not reached at all, or its provider marked the
// error with Unavailable. A node with fallbacks goes on with another access
// then. An error of what was asked is none, like a key that is refused or a
// context that is too long, nor is a call that was stopped.
func IsUnavailable(err error) bool { return Of(err)&Unavailable != 0 }

// IsTransient reports whether err says that a call broke off on its way, and
// asked again it is likely answered: the connection was closed or reset
// before the answer was in, or a gateway or the service is overloaded for a
// moment (HTTP 502, 503, 504 or 529). A call that was stopped, that waited
// too long, or that was refused for what it asked, is none.
func IsTransient(err error) bool { return Of(err)&Transient != 0 }

// MarkedError marks err as meaning meaning, for an adapter whose errors do
// not tell by themselves: a plan limit is Unavailable and RateLimited, for
// instance. The cause stays reachable with errors.Unwrap, and the bits the
// connection itself tells are added by Of.
type MarkedError struct {
	Err     error
	Meaning Category
}

func (e *MarkedError) Error() string      { return e.Err.Error() }
func (e *MarkedError) Unwrap() error      { return e.Err }
func (e *MarkedError) Category() Category { return e.Meaning }

// Mark returns err marked as meaning meaning; nil stays nil.
func Mark(err error, meaning Category) error {
	if err == nil {
		return nil
	}
	return &MarkedError{Err: err, Meaning: meaning}
}

// NewUnavailable marks err as an error of a call the API cannot answer for
// now, whatever it is asked; nil stays nil. It is Mark with Unavailable.
func NewUnavailable(err error) error { return Mark(err, Unavailable) }

// StatusCategory maps an HTTP status of a failed call to its category: 401
// and 403 Unauthorized; 429 Unavailable and RateLimited; 402 Unavailable and
// Rejected; 502, 503, 504 and 529 Unavailable, ServiceFailure and Transient;
// other 5xx Unavailable and ServiceFailure; other 4xx Rejected. Anything else
// is Rejected, as a non-2xx answer of the API.
func StatusCategory(status int) Category {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return Unauthorized
	case status == http.StatusTooManyRequests:
		return Unavailable | RateLimited
	case status == http.StatusPaymentRequired:
		return Unavailable | Rejected
	case status == http.StatusBadGateway || status == http.StatusServiceUnavailable ||
		status == http.StatusGatewayTimeout || status == 529:
		return Unavailable | ServiceFailure | Transient
	case status >= http.StatusInternalServerError:
		return Unavailable | ServiceFailure
	}
	return Rejected
}
