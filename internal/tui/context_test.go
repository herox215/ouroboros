package tui

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"ouroboros/plugin"
)

// The chat shows how long the context of the conversation is, from the first
// call of the LLM on; a new conversation has none yet.
func TestChatShowsTheContext(t *testing.T) {
	m, b := newModel(t)
	if view := ansi.Strip(m.View().Content); strings.Contains(view, "context") {
		t.Fatalf("before the first call:\n%s", view)
	}
	b.contextSize = 46285
	b.says = []string{"I will ask beta"}
	b.steps = []Step{{Text: "asking beta"}, {Text: "asking beta", Done: true}}
	answered(t, m, say(t, m, "ask beta"))
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "alpha  · context 45.2 KiB ≈ 11.6k tokens") || strings.Contains(view, "cached") {
		t.Fatalf("after the answer:\n%s", view)
	}
	// Once the API told what it had cached, the chat shows it too: of the
	// last call, and of the session.
	b.cache = plugin.SessionCache{Tokens: 1000, Cached: 920, AllTokens: 3000, AllCached: 2520}
	answered(t, m, say(t, m, "again"))
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "alpha  · context 45.2 KiB ≈ 11.6k tokens  · cached 92%, session 84%") {
		t.Fatalf("with the cache:\n%s", view)
	}
	run(m, "/new")
	if m.chat.context != 0 || m.chat.cache.Tokens != 0 || strings.Contains(ansi.Strip(m.View().Content), "context") {
		t.Fatalf("a new conversation: %d", m.chat.context)
	}
	for n, want := range map[int]string{1: "0.0 KiB ≈ 0 tokens", 3999: "3.9 KiB ≈ 999 tokens", 4000: "3.9 KiB ≈ 1.0k tokens", 409600: "400.0 KiB ≈ 102.4k tokens"} {
		if got := contextSize(n); got != want {
			t.Errorf("%d: %q; want %q", n, got, want)
		}
	}
}

// /context shows what the LLM is sent, message by message, and scrolls
// through it; esc goes back to the chat.
func TestContextScreenShowsTheContext(t *testing.T) {
	m, b := newModel(t)
	b.contextSize = 4000
	b.cache = plugin.SessionCache{Tokens: 1000, Cached: 500, AllTokens: 1000, AllCached: 500}
	answered(t, m, say(t, m, "ping"))
	b.contents = []Message{{"system", "You are alpha."}, {"tool run", "Run JavaScript"}, {"user", "ping"}, {"assistant", "→ run {}"},
		{"tool", strings.Repeat("line\n", 60)}, {"assistant", "pong"}}
	cmd := run(m, "/context")
	if m.screen != screenContext || m.input.Value() != "" {
		t.Fatal("/context should open the context screen")
	}
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "…") || !strings.Contains(view, "Context  3.9 KiB ≈ 1.0k tokens at the last call, cached 50%, session 50%") {
		t.Fatalf("while the context is on its way:\n%s", view)
	}
	m.Update(cmd())
	view := ansi.Strip(m.View().Content)
	for _, want := range []string{"system  14 bytes", "You are alpha.", "tool run  14 bytes", "user  4 bytes", "assistant  10 bytes", "→ run {}", "0%"} {
		if !strings.Contains(view, want) {
			t.Fatalf("the context should show %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "pong") {
		t.Fatalf("the end is below the screen:\n%s", view)
	}
	for _, k := range []string{"down", "j", "pgdown", "end"} {
		scroll(t, m, press(m, k)) // each key glides on its own
	}
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "pong") || !strings.Contains(view, "100%") {
		t.Fatalf("the end should be in sight:\n%s", view)
	}
	for _, k := range []string{"up", "k", "pgup", "home"} {
		scroll(t, m, press(m, k)) // each key glides on its own
	}
	if m.contents.YOffset() != 0 {
		t.Fatalf("home should go to the top: %d", m.contents.YOffset())
	}
	turn(t, m, tea.MouseWheelDown)
	if m.contents.YOffset() == 0 || m.history.YOffset() != 0 {
		t.Fatal("the wheel should scroll the context, not the chat")
	}
	m.Update(tea.WindowSizeMsg{Width: 60, Height: 20})
	if m.contents.Width() != 56 || m.contents.Height() != 14 {
		t.Fatalf("the context should fit the window: %dx%d", m.contents.Width(), m.contents.Height())
	}
	press(m, "pgdown")
	if m.glide == nil {
		t.Fatal("a page key should start a glide of the context")
	}
	press(m, "esc")
	if m.glide != nil || m.screen != screenChat {
		t.Fatal("esc should end the scroll of the context and go back to the chat")
	}
}

// A context the backend cannot give in full shows why, with what there is;
// an empty one says so, and one of a conversation that was left is dropped.
func TestContextScreenWithoutAContext(t *testing.T) {
	m, b := newModel(t)
	b.fail = map[string]error{"contents": errors.New("the node is not set up")}
	m.Update(run(m, "/context")())
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "✗ the node is not set up") || !strings.Contains(view, "Nothing yet.") || strings.Contains(view, "at the last call") {
		t.Fatalf("the error should be shown:\n%s", view)
	}
	press(m, "esc")
	b.fail = nil
	b.contents = []Message{{"user", "of another conversation"}}
	cmd := run(m, "/context")
	m.newConversation()
	m.Update(cmd())
	if strings.Contains(ansi.Strip(m.View().Content), "of another conversation") {
		t.Fatal("the context of a conversation that was left should be dropped")
	}
}

// Every answer has its one line of calls, also while it is on its way, which
// counts its calls as far as they have come; an answer without calls has
// none.
func TestChatSumsUpTheCallsOfAnAnswer(t *testing.T) {
	m, b := newModel(t)
	b.steps = []Step{{Text: "asking beta"}, {Text: "asking beta ✓ 1s", Done: true}, {Text: "asking beta"}, {Text: "asking beta ✗ offline", Done: true}}
	answered(t, m, say(t, m, "ask beta twice"))
	b.steps = nil
	answered(t, m, say(t, m, "thanks"))
	gate := make(chan struct{})
	b.ask = func(_ context.Context, step func(Step), _ func(string)) (string, error) {
		step(Step{Text: "reading a.go"})
		<-gate
		return "", nil
	}
	cmd := say(t, m, "read a.go")
	drive(t, m, cmd, func() bool { return m.chat.doing == "reading a.go" }) // the call is announced
	view := trimmedLines(m)
	if !strings.Contains(view, "│   ↳ Used 2 tools, 1 failed\n") || strings.Count(view, "↳") != 2 ||
		!strings.Contains(view, "│   ↳ Used 1 tool\n") {
		t.Fatalf("the answer before is summed up, the one on its way too:\n%s", view)
	}
	close(gate)
	drive(t, m, nil, func() bool { return !m.chat.pending })
	if view := trimmedLines(m); !strings.Contains(view, "│   ↳ Used 1 tool\n") || strings.Contains(view, "↳ ·") {
		t.Fatalf("the answer is in:\n%s", view)
	}
}

// The line of an answer goes below its last round, also when the answer is
// the first entry of the conversation; a message without calls has none, and
// the answer on its way has as far as it has come.
func TestSummaries(t *testing.T) {
	read := []call{{text: "reading a ✓ 1ms", done: true}}
	both := append(slices.Clone(read), read...)
	for name, tc := range map[string]struct {
		c    conversation
		want map[int]string
	}{
		"first entry": {conversation{entries: []entry{{calls: read}, {text: "done"}}}, map[int]string{0: summary(read)}},
		"two rounds":  {conversation{entries: []entry{{calls: read}, {calls: read}}}, map[int]string{0: "", 1: summary(both)}},
		"no calls":    {conversation{entries: []entry{{mine: true, text: "hi"}, {text: "hello"}}}, map[int]string{}},
		"on its way":  {conversation{entries: []entry{{calls: read}, {calls: read}}, pending: true}, map[int]string{0: "", 1: summary(both)}},
	} {
		if got := tc.c.summaries(); !maps.Equal(got, tc.want) {
			t.Errorf("%s: %v, want %v", name, got, tc.want)
		}
	}
}
