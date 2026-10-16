package session

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ouroboros/internal/assistant"
	"ouroboros/internal/history"
	"ouroboros/internal/openai"
)

var _ ToolDetails = (*Service)(nil)
var _ ToolDetails = (*Client)(nil)

func TestToolDetails(t *testing.T) {
	ctx := context.Background()
	s, a, _ := archived(t, &fakeAgent{}, t.TempDir(), time.Now())
	callID := "call +&?=1"
	msgs := []openai.Message{
		{Role: "assistant", ToolCalls: []openai.ToolCall{call(callID, "read", "raw arguments")}},
		{Role: "tool", ToolCallID: callID, Content: "error: full output"},
	}
	for _, who := range []history.Who{{Session: "s"}, {Session: "p", Peer: "key"}} {
		if err := a.Append(who, msgs, nil); err != nil {
			t.Fatal(err)
		}
	}
	n := serve(t, Handler(s))
	client := NewClient(n.dir)
	for _, api := range []ToolDetails{s, client} {
		for _, id := range []string{"s", history.Party("key") + "/p"} {
			out, err := api.ToolResult(ctx, id, callID)
			if err != nil || out.Arguments != "raw arguments" || out.Content != "error: full output" || !out.Found {
				t.Fatalf("%s: %+v %v", id, out, err)
			}
		}
		if _, err := api.ToolResult(ctx, "missing", callID); !errors.Is(err, ErrNoSession) {
			t.Fatal(err)
		}
		for _, id := range []string{"", "../s", "owner/s", "peer-invalid/../../s", "peer-invalid/p"} {
			if _, err := api.ToolResult(ctx, id, callID); err == nil {
				t.Fatalf("accepted %q", id)
			}
		}
		for _, call := range []string{"", "nested"} {
			if _, err := api.ToolResult(ctx, "s", call); err == nil || !strings.Contains(err.Error(), "nested script") {
				t.Fatalf("call %q: %v", call, err)
			}
		}
	}
	if len(s.sessions) != 0 {
		t.Fatal("detail request loaded or created a session")
	}
	records, err := a.Records("owner/s")
	if err != nil {
		t.Fatal(err)
	}
	events, _ := replay(records)
	for _, e := range events {
		if e.Kind == Step {
			if e.CallID != callID || e.AsStep().ID != callID {
				t.Fatalf("step: %+v", e)
			}
			b, err := json.Marshal(e)
			if err != nil || !strings.Contains(string(b), "call_id") || strings.Contains(string(b), `"content"`) {
				t.Fatalf("event: %s %v", b, err)
			}
		}
	}
}

func TestToolDetailsDoesNotCreateArchive(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "absent")
	s, _, _ := archived(t, &fakeAgent{}, dir, time.Now())
	if _, err := s.ToolResult(context.Background(), "missing", "call"); !errors.Is(err, ErrNoSession) {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("archive created: %v", err)
	}
}

func TestLiveToolCallID(t *testing.T) {
	a := &fakeAgent{run: func(_ context.Context, _ string, observe func(assistant.Step), _ func(string)) (string, error) {
		observe(assistant.Step{ID: "live-call", Tool: "read"})
		observe(assistant.Step{ID: "live-call", Tool: "read", Done: true})
		return "done", nil
	}}
	s := service(t, a)
	asked(t, s, "live", "go")
	past, _, err := s.Watch(context.Background(), "live")
	if err != nil {
		t.Fatal(err)
	}
	steps := 0
	for _, e := range past {
		if e.Kind == Step {
			steps++
			if e.CallID != "live-call" {
				t.Fatalf("step: %+v", e)
			}
		}
	}
	if steps != 2 {
		t.Fatalf("steps: %d", steps)
	}
}
