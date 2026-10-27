package openai

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"ouroboros/internal/chat"
	"syscall"
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

// A call broke off on its way when its connection ended before the answer
// was in, or a gateway or the service was overloaded for a moment: not when
// it was stopped, waited too long or was refused for what it asked.
func TestIsTransient(t *testing.T) {
	post := func(err error) error { return &url.Error{Op: "Post", URL: "http://llm/v1/chat/completions", Err: err} }
	for name, tc := range map[string]struct {
		err  error
		want bool
	}{
		"closed":                  {post(io.EOF), true},
		"closed in the middle":    {fmt.Errorf("stream: %w", io.ErrUnexpectedEOF), true},
		"reset":                   {post(syscall.ECONNRESET), true},
		"aborted":                 {post(syscall.ECONNABORTED), true},
		"broken pipe":             {post(syscall.EPIPE), true},
		"bad gateway":             {&APIError{Status: 502}, true},
		"unavailable":             {fmt.Errorf("call: %w", &APIError{Status: 503}), true},
		"gateway timeout":         {&APIError{Status: 504}, true},
		"overloaded":              {&APIError{Status: 529}, true},
		"the service fails":       {&APIError{Status: 500}, false},
		"too many requests":       {&APIError{Status: 429}, false},
		"a request that is wrong": {&APIError{Status: 400}, false},
		"stopped":                 {post(context.Canceled), false},
		"waited too long":         {post(context.DeadlineExceeded), false},
		"stopped as it closed":    {errors.Join(context.Canceled, io.EOF), false},
		"waited, then closed":     {errors.Join(context.DeadlineExceeded, io.ErrUnexpectedEOF), false},
		"anything else":           {errors.New("openai: response has no choices"), false},
		"nothing":                 {nil, false},
	} {
		if got := IsTransient(tc.err); got != tc.want {
			t.Errorf("%s: %v is transient: %v", name, tc.err, got)
		}
	}
}

// The adapter maps its HTTP status onto the shared categories.
func TestAPIErrorCategory(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   chat.Category
	}{
		{401, chat.Unauthorized},
		{403, chat.Unauthorized},
		{429, chat.Unavailable | chat.RateLimited},
		{402, chat.Unavailable | chat.Rejected},
		{400, chat.Rejected},
		{500, chat.Unavailable | chat.ServiceFailure},
		{502, chat.Unavailable | chat.ServiceFailure | chat.Transient},
		{503, chat.Unavailable | chat.ServiceFailure | chat.Transient},
		{504, chat.Unavailable | chat.ServiceFailure | chat.Transient},
		{529, chat.Unavailable | chat.ServiceFailure | chat.Transient},
	} {
		if got := (&APIError{Status: tc.status}).Category(); got != tc.want {
			t.Errorf("HTTP %d is %b, want %b", tc.status, got, tc.want)
		}
	}
}
