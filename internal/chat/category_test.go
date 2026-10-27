package chat

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"syscall"
	"testing"
)

// The categories keep the decisions of the adapter: fallbacks switch on
// Unavailable, retries on Transient, and diagnosis reads the rest.
func TestCategory(t *testing.T) {
	reset := func(err error) error { return &net.OpError{Op: "read", Err: err} }
	for name, tc := range map[string]struct {
		err  error
		want Category
	}{
		"canceled":          {context.Canceled, Canceled},
		"canceled wins":     {fmt.Errorf("call: %w", context.Canceled), Canceled},
		"canceled, marked":  {NewUnavailable(context.Canceled), Canceled},
		"deadline":          {context.DeadlineExceeded, Timeout | Unavailable},
		"categorized":       {&UnavailableError{Err: errors.New("no")}, Unavailable},
		"wrapped category":  {fmt.Errorf("call: %w", &UnavailableError{Err: errors.New("no")}), Unavailable},
		"too many requests": {statusError(StatusCategory(http.StatusTooManyRequests)), Unavailable | RateLimited},
		"payment required":  {statusError(StatusCategory(http.StatusPaymentRequired)), Unavailable | Rejected},
		"auth":              {statusError(StatusCategory(http.StatusUnauthorized)), Unauthorized},
		"forbidden":         {statusError(StatusCategory(http.StatusForbidden)), Unauthorized},
		"bad request":       {statusError(StatusCategory(http.StatusBadRequest)), Rejected},
		"service fails":     {statusError(StatusCategory(http.StatusInternalServerError)), Unavailable | ServiceFailure},
		"bad gateway":       {statusError(StatusCategory(http.StatusBadGateway)), Unavailable | ServiceFailure | Transient},
		"unavailable":       {statusError(StatusCategory(http.StatusServiceUnavailable)), Unavailable | ServiceFailure | Transient},
		"gateway timeout":   {statusError(StatusCategory(http.StatusGatewayTimeout)), Unavailable | ServiceFailure | Transient},
		"overloaded":        {statusError(StatusCategory(529)), Unavailable | ServiceFailure | Transient},
		"eof":               {io.EOF, Transient},
		"unexpected eof":    {io.ErrUnexpectedEOF, Transient},
		"reset":             {reset(syscall.ECONNRESET), Unavailable | Transient},
		"network":           {reset(errors.New("unreachable")), Unavailable},
		"nothing":           {nil, 0},
		"anything else":     {errors.New("no choices"), 0},
	} {
		if got := Of(tc.err); got != tc.want {
			t.Errorf("%s: %v is %b, want %b", name, tc.err, got, tc.want)
		}
		if got, want := IsUnavailable(tc.err), tc.want&Unavailable != 0; got != want {
			t.Errorf("%s: IsUnavailable = %v, want %v", name, got, want)
		}
		if got, want := IsTransient(tc.err), tc.want&Transient != 0; got != want {
			t.Errorf("%s: IsTransient = %v, want %v", name, got, want)
		}
	}
}

// A marked error keeps its cause and its text.
func TestUnavailableKeepsCause(t *testing.T) {
	cause := errors.New("quota")
	err := NewUnavailable(fmt.Errorf("chatgpt: %w", cause))
	if !errors.Is(err, cause) || err.Error() != "chatgpt: quota" {
		t.Fatalf("marked error lost its cause: %v", err)
	}
	if !IsUnavailable(err) || IsTransient(err) {
		t.Fatalf("marked error: unavailable %v, transient %v", IsUnavailable(err), IsTransient(err))
	}
}

// statusError is an adapter error with an HTTP status.
type statusError Category

func (e statusError) Error() string      { return fmt.Sprint("HTTP ", int(e)) }
func (e statusError) Category() Category { return Category(e) }
