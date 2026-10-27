package diagnosis

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ouroboros/internal/agent"
	"ouroboros/internal/chat"
	"ouroboros/internal/openai"
)

// What an error tells about its environment: none of it may be kept or
// explained to a peer.
var secrets = []string{"10.0.0.5", "11434", "chat/completions", "sk-live-123", "hunter2", "/home/alice", "tok_abc"}

// timeout is a network error that says it is a timeout.
type timeout struct{}

func (timeout) Error() string   { return "i/o timeout talking to 10.0.0.5" }
func (timeout) Timeout() bool   { return true }
func (timeout) Temporary() bool { return true }

func TestClassify(t *testing.T) {
	refused := &url.Error{Op: "Post", URL: "http://10.0.0.5:11434/v1/chat/completions?key=sk-live-123",
		Err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}}
	tests := []struct {
		err  error
		want Cause
	}{
		{refused, CauseLLMUnreachable},
		{&net.DNSError{Err: "no such host", Name: "llm.internal"}, CauseLLMUnreachable},
		{&url.Error{Op: "Post", URL: "http://10.0.0.5:11434", Err: timeout{}}, CauseTimeout},
		{&url.Error{Op: "Post", URL: "http://10.0.0.5:11434", Err: context.Canceled}, CauseCancelled},
		{fmt.Errorf("answering: %w", context.DeadlineExceeded), CauseTimeout},
		{&openai.APIError{Status: 401, Message: "Incorrect API key provided: sk-live-123"}, CauseLLMAuth},
		{&openai.APIError{Status: 403, Message: "organisation hunter2 is suspended"}, CauseLLMAuth},
		{&openai.APIError{Status: 429, Message: "quota of tok_abc exceeded"}, CauseLLMRateLimited},
		{chat.Mark(errors.New("plan limit reached: tok_abc"), chat.Unavailable|chat.RateLimited), CauseLLMRateLimited},
		{&openai.APIError{Status: 400, Message: "context length exceeded"}, CauseLLMRejected},
		{&openai.APIError{Status: 404, Message: "model gpt-secret not found"}, CauseLLMRejected},
		{&openai.APIError{Status: 503, Message: "upstream 10.0.0.5 is down"}, CauseLLMFailed},
		{&openai.APIError{Status: 500, Message: "internal error"}, CauseLLMFailed},
		{&openai.APIError{Status: 499, Message: "client closed request"}, CauseLLMRejected},
		{fmt.Errorf("wrapped: %w", agent.ErrTooManyToolCalls), CauseTooManyToolCalls},
		{errors.New("open /home/alice/.config/ouroboros/config.json: permission denied"), CauseInternal},
	}
	for _, tt := range tests {
		got := Classify(tt.err)
		if got != tt.want {
			t.Errorf("Classify(%v) = %q, want %q", tt.err, got, tt.want)
		}
		// Whatever the error says, the explanation says none of it.
		for _, secret := range secrets {
			if strings.Contains(got.Explain(), secret) || strings.Contains(string(got), secret) {
				t.Errorf("%q leaks %q", got, secret)
			}
		}
	}
}

func TestExplain(t *testing.T) {
	seen := map[string]bool{}
	for cause, text := range explanations {
		if text == "" || cause.Explain() != text || seen[text] {
			t.Errorf("%s: explanation %q", cause, cause.Explain())
		}
		seen[text] = true
	}
	// A category from a newer or older version is explained as unknown.
	if got := Cause("flux_capacitor").Explain(); got != CauseInternal.Explain() {
		t.Errorf("unknown cause: %q", got)
	}
}

func TestRecordAndLookup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "failures.json")
	s := Open(path)
	if _, ok, err := s.Lookup("alice", "AAAA"); ok || err != nil {
		t.Fatalf("an empty list: %v, %v", ok, err)
	}
	at := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	want := Entry{ID: "AAAA", Peer: "alice", ContextID: "ctx", MessageID: "msg", At: at, Cause: CauseLLMAuth}
	if err := s.Record(want); err != nil {
		t.Fatal(err)
	}
	if err := s.Record(Entry{ID: "BBBB", Peer: "bob", At: at, Cause: CauseTimeout}); err != nil {
		t.Fatal(err)
	}

	// Another store on the same file finds it: the agent is not the server.
	got, ok, err := Open(path).Lookup("alice", "AAAA")
	if err != nil || !ok || got != want {
		t.Fatalf("got %+v, %v, %v", got, ok, err)
	}
	// An ID opens only what happened to the peer that asks.
	for _, q := range [][2]string{{"bob", "AAAA"}, {"alice", "BBBB"}, {"alice", "CCCC"}, {"", "AAAA"}, {"alice", ""}} {
		if e, ok, err := s.Lookup(q[0], q[1]); ok || err != nil || e != (Entry{}) {
			t.Errorf("Lookup(%q, %q) = %+v, %v, %v", q[0], q[1], e, ok, err)
		}
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Errorf("perm = %o, want 600", info.Mode().Perm())
	}
}

// The list is bounded in the number of its entries and in their size.
func TestStoreIsBounded(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "failures.json"))
	for i := range MaxEntries + 3 {
		if err := s.Record(Entry{ID: fmt.Sprint(i), Peer: "alice"}); err != nil {
			t.Fatal(err)
		}
	}
	var kept int
	s.access(func(entries []Entry) ([]Entry, bool) {
		kept = len(entries)
		return nil, false
	})
	if kept != MaxEntries {
		t.Fatalf("%d entries kept, want %d", kept, MaxEntries)
	}
	if _, ok, _ := s.Lookup("alice", "2"); ok {
		t.Error("the oldest entries should have made room")
	}
	if _, ok, _ := s.Lookup("alice", "3"); !ok {
		t.Error("an entry within the bound was dropped")
	}

	// A peer names its conversation and its message as it likes.
	long := strings.Repeat("x", 10*maxIDBytes)
	s.Record(Entry{ID: "LONG", Peer: "alice", ContextID: long, MessageID: long})
	if e, _, _ := s.Lookup("alice", "LONG"); len(e.ContextID) != maxIDBytes || len(e.MessageID) != maxIDBytes {
		t.Fatalf("kept %d and %d bytes of the IDs", len(e.ContextID), len(e.MessageID))
	}
}

func TestBrokenStore(t *testing.T) {
	check := func(name string, s *Store) {
		t.Helper()
		if err := s.Record(Entry{ID: "AAAA", Peer: "alice"}); err == nil {
			t.Errorf("%s: Record should fail", name)
		}
		if _, ok, err := s.Lookup("alice", "AAAA"); ok || err == nil {
			t.Errorf("%s: Lookup = %v, %v", name, ok, err)
		}
	}
	check("missing directory", Open(filepath.Join(t.TempDir(), "missing", "failures.json")))

	garbage := filepath.Join(t.TempDir(), "failures.json")
	os.WriteFile(garbage, []byte("nope"), 0o600)
	check("garbage", Open(garbage))

	dir := filepath.Join(t.TempDir(), "failures.json")
	os.Mkdir(dir, 0o700)
	check("directory", Open(dir))

	// A time JSON cannot hold: the entry is not written, the list stays.
	s := Open(filepath.Join(t.TempDir(), "failures.json"))
	s.Record(Entry{ID: "AAAA", Peer: "alice"})
	if err := s.Record(Entry{ID: "BBBB", Peer: "alice", At: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)}); err == nil {
		t.Error("an entry that cannot be encoded should fail")
	}
	if _, ok, err := s.Lookup("alice", "AAAA"); !ok || err != nil {
		t.Errorf("a failed Record damaged the list: %v, %v", ok, err)
	}
}
