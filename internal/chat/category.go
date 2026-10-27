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
	// Timeout: the call waited too long. It counts as Unavailable too, as it
	// did before the categories were shared, but not as Transient.
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
			category &^= Transient
		}
		return category
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
		if transientIO(err) {
			category |= Transient
		}
		return category
	}
	if transientIO(err) {
		return Transient
	}
	return 0
}

// transientIO reports whether err is a connection that ended before the
// answer was in.
func transientIO(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNABORTED) || errors.Is(err, syscall.EPIPE)
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

// UnavailableError marks err as the error of a call the API cannot answer for
// now, whatever it is asked: for an adapter whose errors do not tell by
// themselves. The cause stays reachable with errors.Unwrap.
type UnavailableError struct{ Err error }

func (e *UnavailableError) Error() string      { return e.Err.Error() }
func (e *UnavailableError) Unwrap() error      { return e.Err }
func (e *UnavailableError) Category() Category { return Unavailable }

// NewUnavailable wraps err as an UnavailableError; nil stays nil.
func NewUnavailable(err error) error {
	if err == nil {
		return nil
	}
	return &UnavailableError{Err: err}
}

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
