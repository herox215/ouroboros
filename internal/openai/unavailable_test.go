package openai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// An API cannot answer for now when its limit is reached, its service fails
// or it is not reached, or when its provider says so: not when what was asked
// is wrong, nor when the call was stopped.
func TestIsUnavailable(t *testing.T) {
	limit := errors.New("the limit of the plan is reached")
	marked := Unavailable(limit)
	if !errors.Is(marked, limit) || marked.Error() != limit.Error() {
		t.Fatalf("a marked error is the error: %v", marked)
	}
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	_, unreached := New(srv.URL, "").Complete(context.Background(), "m", nil, nil)
	stopped, stop := context.WithCancel(context.Background())
	stop()
	_, canceled := New(srv.URL, "").Complete(stopped, "m", nil, nil)
	if unreached == nil || canceled == nil {
		t.Fatal("a server that is closed answers")
	}
	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"marked":                  {marked, true},
		"marked, wrapped":         {fmt.Errorf("chatgpt: %w", marked), true},
		"too many requests":       {&APIError{Status: 429}, true},
		"payment required":        {&APIError{Status: 402}, true},
		"the service fails":       {fmt.Errorf("call: %w", &APIError{Status: 500}), true},
		"the gateway fails":       {&APIError{Status: 503}, true},
		"not reached":             {unreached, true},
		"a request that is wrong": {&APIError{Status: 400}, false},
		"a key that is refused":   {&APIError{Status: 401}, false},
		"no such model":           {&APIError{Status: 404}, false},
		"just below the service":  {&APIError{Status: 499}, false},
		"stopped":                 {canceled, false},
		"stopped, though marked":  {Unavailable(context.Canceled), false},
		"anything else":           {errors.New("openai: response has no choices"), false},
		"nothing":                 {nil, false},
	} {
		if got := IsUnavailable(tc.err); got != tc.want {
			t.Errorf("%s: %v is unavailable: %v", name, tc.err, got)
		}
	}
}
