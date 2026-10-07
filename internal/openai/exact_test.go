package openai

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

// The lines of a trace say sizes in KiB and shares in percent: the numbers
// are what a reader compares, so they are checked as they are printed.
func TestTraceNumbers(t *testing.T) {
	c := newServer(t, sse(`{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`, "[DONE]"))
	ctx, lines := traced()
	long := strings.Repeat("x", 2048-len(`{"role":"user","content":""}`))
	tool := NewTool("read", strings.Repeat("d", 1024), `{"type":"object"}`)
	if _, err := c.Complete(ctx, "m", []Message{{Role: "user", Content: long}}, []Tool{tool}); err != nil {
		t.Fatal(err)
	}
	has(t, *lines, "1 messages (2.0 KiB), 1 tools (1.1 KiB, hash ")

	if got := percent(1, 3); got != "33.3%" {
		t.Errorf("percent(1, 3) = %q", got)
	}
	if got := percent(3, 4); got != "75.0%" {
		t.Errorf("percent(3, 4) = %q", got)
	}
	if got := percent(0, 0); got != "100%" {
		t.Errorf("percent(0, 0) = %q", got)
	}
}

// What is told of a message from the byte it differs at is at most
// maxTraced bytes of it, with how many are left out.
func TestTraceAround(t *testing.T) {
	exact := []byte("ab" + strings.Repeat("c", maxTraced))
	if got, want := around(exact, 2), fmt.Sprintf("%d bytes long, from there %q", len(exact), strings.Repeat("c", maxTraced)); got != want {
		t.Errorf("a rest of exactly maxTraced bytes:\n got %s\nwant %s", got, want)
	}
	longer := []byte("ab" + strings.Repeat("c", maxTraced) + "ddd")
	want := fmt.Sprintf("%d bytes long, from there %q… (+3 bytes)", len(longer), strings.Repeat("c", maxTraced))
	if got := around(longer, 2); got != want {
		t.Errorf("a longer rest:\n got %s\nwant %s", got, want)
	}
}

// Where a request breaks with the one before is told with the share of the
// bytes before the break, in the message that changed too; a message that
// only goes on where it ended before breaks at its end.
func TestCompareWhereItBreaks(t *testing.T) {
	var s Sent
	s.Compare("s", nil, [][]byte{[]byte("aaaa"), []byte("bbbbbbbb")})
	got := s.Compare("s", nil, [][]byte{[]byte("aaaa"), []byte("bbbbXXXX")})
	if !strings.Contains(got, "breaks at message 1 of 2") || !strings.Contains(got, "at byte 4 of it") || !strings.Contains(got, "before it 66.7% of the bytes are the same") {
		t.Errorf("a message that changed: %s", got)
	}
	// One message is the beginning of the other: it breaks where the
	// shorter one ends.
	s.Compare("p", nil, [][]byte{[]byte("abc")})
	if got := s.Compare("p", nil, [][]byte{[]byte("abcd")}); !strings.Contains(got, "at byte 3 of it") || !strings.Contains(got, `it was 3 bytes long, from there ""`) || !strings.Contains(got, `it is 4 bytes long, from there "d"`) {
		t.Errorf("a message that goes on: %s", got)
	}
	if got := s.Compare("p", nil, [][]byte{[]byte("ab")}); !strings.Contains(got, "at byte 2 of it") {
		t.Errorf("a message that was cut: %s", got)
	}
}

// The last requests of maxSessions sessions are kept. A session that is
// known is compared also when that many are kept; a new one beyond them
// makes room by forgetting them all.
func TestCompareKeepsManySessions(t *testing.T) {
	var s Sent
	item := [][]byte{[]byte("a")}
	for i := range maxSessions {
		if got := s.Compare(fmt.Sprint("s", i), nil, item); got != "first request of the session in the trace" {
			t.Fatalf("session %d: %s", i, got)
		}
	}
	// All of them are known, the first and the last.
	for _, session := range []string{"s0", fmt.Sprint("s", maxSessions-1), "s0"} {
		if got := s.Compare(session, nil, item); !strings.Contains(got, "all 1 messages of the last request are kept") {
			t.Fatalf("%s with %d sessions kept: %s", session, maxSessions, got)
		}
	}
	if len(s.last) != maxSessions {
		t.Fatalf("%d sessions kept", len(s.last))
	}
	// One more: the others are forgotten, and it is kept.
	if got := s.Compare("new", nil, item); got != "first request of the session in the trace" {
		t.Fatalf("a new session: %s", got)
	}
	if got := s.Compare("s0", nil, item); got != "first request of the session in the trace" || len(s.last) != 2 {
		t.Fatalf("a session that was forgotten: %s, %d kept", got, len(s.last))
	}
	if got := s.Compare("new", nil, item); !strings.Contains(got, "all 1 messages") {
		t.Fatalf("the new session, asked again: %s", got)
	}
}

// The usage of an answer is the last one the API sent: an event without
// one, or with null in its place, leaves it as it is.
func TestAnsweredKeepsTheUsage(t *testing.T) {
	var a answered
	a.read([]byte(`{"usage":{"prompt_tokens":7}}`))
	a.read([]byte(`{"id":"x"}`))
	if string(a.Usage) != `{"prompt_tokens":7}` {
		t.Fatalf("after an event without a usage: %s", a.Usage)
	}
	a.read([]byte(`{"usage":null}`))
	if string(a.Usage) != `{"prompt_tokens":7}` {
		t.Fatalf("after an event with null as usage: %s", a.Usage)
	}
	a.read([]byte(`{"usage":{"prompt_tokens":9}}`))
	if string(a.Usage) != `{"prompt_tokens":9}` {
		t.Fatalf("after another usage: %s", a.Usage)
	}
}

// An API that refuses a parameter it was not sent is not asked again: the
// request failed for another reason. A model that is no Claude model is sent
// no cache_control.
func TestRefusalOfWhatWasNotSent(t *testing.T) {
	var requests atomic.Int32
	c := newServer(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"message":"cache_control is not allowed here"}}`))
	})
	_, err := c.Complete(context.Background(), "gpt", []Message{{Role: "system", Content: "s"}, {Role: "user", Content: "hi"}}, nil)
	if err == nil || requests.Load() != 1 || c.refused[cacheControl].Load() {
		t.Fatalf("got %v after %d requests, refused %v", err, requests.Load(), c.refused[cacheControl].Load())
	}
}

// What follows the end of a stream is not part of the answer.
func TestStreamEndsAtDone(t *testing.T) {
	c := newServer(t, sse(`{"choices":[{"delta":{"content":"ok"}}]}`, "[DONE]", `{"choices":[{"delta":{"content":" and more"},"finish_reason":"stop"}]}`))
	got, err := c.Complete(context.Background(), "gpt", nil, nil)
	if err != nil || got.Content != "ok" {
		t.Fatalf("got %+v, %v", got, err)
	}
}
