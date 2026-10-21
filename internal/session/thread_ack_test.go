package session

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ouroboros/internal/agent"
	"ouroboros/internal/assistant"
	"ouroboros/internal/config"
	"ouroboros/internal/openai"
	"ouroboros/internal/openai/openaitest"
)

const handoffAck = "I'll look into the login problem."
const handoffFinding = "The login problem was caused by an expired token."

// Script only the model: routing, the post-tool turn, background execution,
// result delivery and owner-visible events all use the real assistant/service.
type handoffAckLLM struct {
	release  <-chan struct{}
	started  chan struct{}
	postTool atomic.Int32
	results  atomic.Int32
}

func (l *handoffAckLLM) Complete(ctx context.Context, _ string, msgs []openai.Message, tools []openai.Tool) (openai.Message, error) {
	reply := func(text string) (openai.Message, error) {
		return openai.Message{Role: "assistant", Content: text}, nil
	}
	if strings.HasPrefix(msgs[0].Content, openaitest.TitlePrompt) {
		return reply(openaitest.Untitled)
	}
	last := msgs[len(msgs)-1]
	if slices.ContainsFunc(tools, func(tool openai.Tool) bool { return tool.Function.Name == assistant.ToolSessionNew }) {
		switch {
		case last.Role == "tool":
			if last.ToolCallID != "handoff" || strings.HasPrefix(last.Content, "error:") {
				return openai.Message{}, fmt.Errorf("unexpected handoff result: %+v", last)
			}
			l.postTool.Add(1)
			return reply(handoffAck)
		case strings.HasPrefix(last.Content, backgroundHead):
			if !strings.Contains(last.Content, "expired token") {
				return openai.Message{}, fmt.Errorf("missing complete background finding: %q", last.Content)
			}
			l.results.Add(1)
			return reply(handoffFinding)
		default:
			return openai.Message{Role: "assistant", ToolCalls: []openai.ToolCall{{
				ID: "handoff", Type: "function", Function: openai.FunctionCall{
					Name: assistant.ToolSessionNew, Arguments: `{"text":"inspect the login problem"}`,
				},
			}}}, nil
		}
	}
	select {
	case l.started <- struct{}{}:
	default:
	}
	select {
	case <-l.release:
		return reply("Raw finding: expired token")
	case <-ctx.Done():
		return openai.Message{}, ctx.Err()
	}
}

// Ordinary work must get one acknowledgement before the delayed outcome, and
// one separately composed result afterwards. An unconditional tool End skips
// the scripted post-tool completion and makes this test fail before release.
func TestThreadAcknowledgesBeforeDelayedResult(t *testing.T) {
	release := make(chan struct{})
	llm := &handoffAckLLM{release: release, started: make(chan struct{}, 1)}
	cfg := config.Config{Access: "scripted", Model: "test", Name: "me"}
	a := assistant.New(func() (config.Config, error) { return cfg, nil }, assistant.Builder{
		LLM:   func(string) (agent.LLM, error) { return llm, nil },
		Tools: func() []agent.Tool { return nil },
	}, func(string, ...any) {})
	t.Cleanup(a.Stop)
	s := New(a)
	t.Cleanup(s.Close)
	_, live, err := s.Watch(context.Background(), ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	ask(t, s, "Please investigate the login problem.")
	before := until(t, live, Answer)
	if got := before[len(before)-1].Text; got != handoffAck {
		t.Fatalf("before releasing work, got %q, want acknowledgement", got)
	}
	select {
	case <-llm.started:
	case <-time.After(5 * time.Second):
		t.Fatal("background work did not start")
	}
	if llm.postTool.Load() != 1 || llm.results.Load() != 0 {
		t.Fatalf("before release: post-tool turns=%d, results=%d", llm.postTool.Load(), llm.results.Load())
	}
	close(release)
	events := append(before, working(t, live)...)
	var answers []string
	for _, e := range events {
		if e.Kind == Answer {
			answers = append(answers, e.Text)
		}
		if e.Kind == Failed {
			t.Fatalf("unexpected failure: %+v", e)
		}
		if e.Kind == Message && strings.Contains(e.Text, "Raw finding") {
			t.Fatalf("internal outcome shown as owner message: %+v", e)
		}
	}
	if !slices.Equal(answers, []string{handoffAck, handoffFinding}) {
		t.Fatalf("owner-visible answers: %q", answers)
	}
	if llm.postTool.Load() != 1 || llm.results.Load() != 1 {
		t.Fatalf("final turns: post-tool=%d, results=%d", llm.postTool.Load(), llm.results.Load())
	}
}
