package assistant

import (
	"context"
	"errors"
	"io"
	"net/url"
	"strings"
	"syscall"
	"testing"
	"time"

	"ouroboros/internal/openai"
)

// answers is an LLM that fails with each of errs in turn, and then answers.
type answers struct {
	errs  []error
	calls int
}

func (a *answers) Complete(context.Context, string, []openai.Message, []openai.Tool) (openai.Message, error) {
	a.calls++
	if a.calls <= len(a.errs) {
		return openai.Message{}, a.errs[a.calls-1]
	}
	return openai.Message{Content: "hello"}, nil
}

// noWait waits for nothing, unless ctx ended.
func noWait(ctx context.Context, _ time.Duration) error { return ctx.Err() }

// A call that broke off on its way is asked again, twice at most; the
// answer says why. Any other error is not.
func TestRetrying(t *testing.T) {
	eof := &url.Error{Op: "Post", URL: "http://llm/v1/chat/completions", Err: io.EOF}
	ctx := context.Background()

	llm := &answers{errs: []error{eof}}
	msg, err := retrying{llm, noWait}.Complete(ctx, "m", nil, nil)
	if err != nil || msg.Content != "hello" || llm.calls != 2 || len(msg.Retried) != 1 || !strings.HasSuffix(msg.Retried[0], ": EOF") {
		t.Fatalf("once: %+v, %v, %d calls", msg, err, llm.calls)
	}

	reset := &url.Error{Op: "Post", URL: "x", Err: syscall.ECONNRESET}
	overloaded := &openai.APIError{Status: 503, Message: "overloaded"}
	llm = &answers{errs: []error{reset, overloaded, eof}}
	_, err = retrying{llm, noWait}.Complete(ctx, "m", nil, nil)
	if !errors.Is(err, io.EOF) || llm.calls != 3 || !strings.HasSuffix(err.Error(), "(asked 3 times, as it broke off)") {
		t.Fatalf("never answered: %v, %d calls", err, llm.calls)
	}

	for _, e := range []error{
		&openai.APIError{Status: 400, Message: "context too long"},
		&openai.APIError{Status: 401, Message: "bad key"},
		&openai.APIError{Status: 429, Message: "quota"},
		context.Canceled,
		&url.Error{Op: "Post", URL: "x", Err: context.DeadlineExceeded},
		errors.New("openai: the stream has an event that is no JSON"),
	} {
		llm = &answers{errs: []error{e}}
		if _, err := (retrying{llm, noWait}).Complete(ctx, "m", nil, nil); err != e || llm.calls != 1 {
			t.Errorf("%v: %v, %d calls", e, err, llm.calls)
		}
	}

	// An answer that is stopped meanwhile is not asked again.
	stopped, cancel := context.WithCancel(ctx)
	cancel()
	llm = &answers{errs: []error{eof}}
	if _, err := (retrying{llm, noWait}).Complete(stopped, "m", nil, nil); err != eof || llm.calls != 1 {
		t.Errorf("stopped: %v, %d calls", err, llm.calls)
	}
}
