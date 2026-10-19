package assistant

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ouroboros/internal/agent"
	"ouroboros/internal/config"
	"ouroboros/internal/openai"
	"ouroboros/internal/suggestion"
)

// reviewLLM calls the tool fail as often as failing says, and then answers;
// asked for a review it answers reply and keeps what it was sent.
type reviewLLM struct {
	failing int
	reply   string

	mu       sync.Mutex
	reviewed []openai.Message
	effort   string
	left     time.Duration // of the time the review may take, when it was asked
}

func (l *reviewLLM) Complete(ctx context.Context, _ string, msgs []openai.Message, tools []openai.Tool) (openai.Message, error) {
	if msgs[0].Content == reviewPrompt {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.reviewed, l.effort = msgs, openai.EffortOf(ctx)
		if deadline, ok := ctx.Deadline(); ok {
			l.left = time.Until(deadline)
		}
		return openai.Message{Content: l.reply}, nil
	}
	called := 0
	for _, m := range msgs {
		called += len(m.ToolCalls)
	}
	if called >= l.failing {
		return openai.Message{Content: "done"}, nil
	}
	name, args := "fail", fmt.Sprintf(`{"why":"no %d"}`, called) // the same call again is not run

	return openai.Message{ToolCalls: []openai.ToolCall{{ID: "1", Type: "function", Function: openai.FunctionCall{Name: name, Arguments: args}}}}, nil
}

// suggested runs an answer with llm and returns what its review proposed,
// "" if nothing came within a second, and the log. What it suggests is kept
// in store, if not nil.
func suggested(t *testing.T, llm *reviewLLM, store ...*suggestion.Store) (string, []string) {
	t.Helper()
	a, log := recorded(t, ownerConfig(), llm)
	if len(store) > 0 {
		a.builder = a.builder.WithSuggestions(store[0])
	}
	got := make(chan string, 1)
	ctx := WithReview(context.Background(), func(text string) { got <- text })
	if reply, err := a.Ask(ctx, "c", "go", nil); err != nil || reply != "done" {
		t.Fatalf("answer: %q, %v", reply, err)
	}
	select {
	case text := <-got:
		return text, log.take()
	case <-time.After(time.Second):
		return "", log.take()
	}
}

// An answer with signals is reviewed once it is given: the LLM reads the log
// of the answer, its signals and the tools of the agent, and what it proposes
// goes to the owner and into the log.
func TestAnswerWithSignalsIsReviewed(t *testing.T) {
	llm := &reviewLLM{failing: failedTools, reply: "CHANGE: Let read say which files are near.\n"}
	store := suggestion.Open(filepath.Join(t.TempDir(), "suggestions.json"))
	text, lines := suggested(t, llm, store)
	if text != "Let read say which files are near." {
		t.Fatalf("suggested %q:\n%s", text, strings.Join(lines, "\n"))
	}
	asked := llm.reviewed[1].Content
	for _, want := range []string{`message from the owner: "go"`, "tool fail", "- failed tools: 3 tool calls failed", "<tools>\necho: ", "<withheld>\n\n</withheld>"} {
		if !strings.Contains(asked, want) {
			t.Errorf("the review is not sent %q:\n%s", want, asked)
		}
	}
	if strings.Contains(asked, "tool run") {
		t.Errorf("the review contains an obsolete run call:\n%s", asked)
	}
	if llm.effort != reviewEffort {
		t.Errorf("effort %q", llm.effort)
	}
	if llm.left <= reviewTime/2 || llm.left > reviewTime {
		t.Errorf("the review had %s left, of %s", llm.left, reviewTime)
	}
	if last := lines[len(lines)-1]; last != `chat c: review: "Let read say which files are near."` {
		t.Errorf("last line of the log: %s", last)
	}
	kept, err := store.List()
	if err != nil || len(kept) != 1 || kept[0].Text != text || kept[0].Session != "c" || kept[0].Signals[0] != "3 tool calls failed" {
		t.Errorf("kept %+v, %v", kept, err)
	}
}

// What answers NONE proposes nothing, and an answer without signals, or one
// that is not to be reviewed, is not.
func TestNothingToSuggest(t *testing.T) {
	llm := &reviewLLM{failing: failedTools, reply: "**NONE.** The build takes long."}
	if text, _ := suggested(t, llm); text != "" {
		t.Fatalf("NONE suggested %q", text)
	}
	llm = &reviewLLM{failing: 1, reply: "CHANGE: it."}
	if text, _ := suggested(t, llm); text != "" || llm.reviewed != nil {
		t.Fatalf("an answer without signals suggested %q", text)
	}
	llm = &reviewLLM{failing: failedTools, reply: "CHANGE: it."}
	a, _ := recorded(t, ownerConfig(), llm)
	if _, err := a.Ask(context.Background(), "c", "go", nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	llm.mu.Lock()
	defer llm.mu.Unlock()
	if llm.reviewed != nil {
		t.Fatal("reviewed without WithReview")
	}
}

// The task of a suggestion the owner took says what was measured, if
// anything, and its answer is not reviewed again.
func TestSuggestedTask(t *testing.T) {
	got := SuggestedTask("add grep", nil)
	if strings.Contains(got, "measured") || !strings.HasPrefix(got, suggestedIntro+"\n\nadd grep\n\nThe review saw only the log") {
		t.Fatalf("without signals: %q", got)
	}
	if got := SuggestedTask("add grep", []string{"a", "b"}); !strings.Contains(got, "What was measured of that answer: a; b.") {
		t.Fatalf("with signals: %q", got)
	}
	llm := &reviewLLM{failing: failedTools, reply: "CHANGE: again."}
	a, _ := recorded(t, ownerConfig(), llm)
	ctx := WithReview(context.Background(), func(string) {})
	if _, err := a.Ask(ctx, "c", SuggestedTask("add grep", nil), nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	llm.mu.Lock()
	defer llm.mu.Unlock()
	if llm.reviewed != nil {
		t.Fatal("the answer to a suggestion taken was reviewed")
	}
}

// A review is told the tools the owner allows the agent, and the names of
// those of ouro the owner withholds.
func TestReviewTools(t *testing.T) {
	cfg := ownerConfig()
	cfg.Tools = &config.Tools{H2A: []config.Entry{{Tool: "echo"}}}
	a, _ := recorded(t, cfg, &reviewLLM{})
	tools := a.reviewTools(cfg)
	if strings.Join(tools.allowed, "|") != "echo: " {
		t.Errorf("allowed %q", tools.allowed)
	}
	if strings.Join(tools.withheld, ", ") != "fail, block, write, edit, user_question, delegate" {
		t.Errorf("withheld %q", tools.withheld)
	}
}

func TestFirstSentence(t *testing.T) {
	if got := firstSentence("Read a file.  Lines are\nnumbered. More."); got != "Read a file." {
		t.Errorf("got %q", got)
	}
	if got := firstSentence("v1.2 of it"); got != "v1.2 of it" {
		t.Errorf("got %q", got)
	}
	if got := firstSentence(". And then"); got != "." {
		t.Errorf("got %q", got)
	}
}

// Only what starts with CHANGE: is proposed: NONE is nothing, and so is a
// model that says nothing is to change in other words.
func TestSuggestion(t *testing.T) {
	for reply, want := range map[string]string{
		"":                                 "",
		"NONE":                             "",
		"Kein Änderungsvorschlag: es lief": "",
		"CHANGE: Add\n\n  grep.":           "Add grep.",
		"**CHANGE:** add grep.":            "add grep.",
		"CHANGE: " + strings.Repeat("ä", maxSuggested): strings.Repeat("ä", maxSuggested/2) + "…",
		"CHANGE: " + strings.Repeat("a", maxSuggested): strings.Repeat("a", maxSuggested),
	} {
		if got := proposed(reply); got != want {
			t.Errorf("proposed(%.20q) = %.20q, want %.20q", reply, got, want)
		}
	}
}

// The log a review reads keeps the message of the owner and as much of the
// end as fits.
func TestBounded(t *testing.T) {
	lines := []string{"first", "aaaa", "bbbb", "cccc"}
	if got := bounded(lines, 100); strings.Join(got, "|") != "first|aaaa|bbbb|cccc" {
		t.Errorf("all fit: %q", got)
	}
	if got := bounded(lines, 12); strings.Join(got, "|") != "first|… 2 lines left out …|cccc" {
		t.Errorf("cut: %q", got)
	}
	if got := bounded(lines, 13); strings.Join(got, "|") != "first|… 1 line left out …|bbbb|cccc" {
		t.Errorf("one cut: %q", got)
	}
	if got := bounded(nil, 10); got != nil {
		t.Errorf("none: %q", got)
	}
}

// A review that cannot be made is in the log, and suggests nothing: when the
// configuration cannot be read, the access cannot be opened or the LLM fails.
func TestReviewFails(t *testing.T) {
	tools := toolsOf{}
	a := New(func() (config.Config, error) { return config.Config{}, errors.New("no config") }, Builder{}, discard)
	if _, err := a.review(context.Background(), nil, nil, tools); err == nil {
		t.Error("reviewed without a configuration")
	}
	opened := 0 // the agent opens the access first, the review again
	a = New(func() (config.Config, error) { return ownerConfig(), nil }, Builder{
		LLM: func(string) (agent.LLM, error) {
			if opened++; opened > 1 {
				return nil, errors.New("no access")
			}
			return &titleLLM{}, nil
		},
		Tools: func() []agent.Tool { return nil },
	}, discard)
	if _, err := a.review(context.Background(), nil, nil, tools); err == nil {
		t.Error("reviewed without an access")
	}
	b, log := recorded(t, ownerConfig(), &titleLLM{err: errors.New("down")})
	got := ""
	b.reviewed("c", []Signal{{SignalSlowTool, "bash took 42s"}}, []string{"x"}, tools, func(text string) { got = text })
	if lines := log.take(); got != "" || len(lines) != 1 || lines[0] != "chat c: review failed: down" {
		t.Fatalf("suggested %q, log %q", got, lines)
	}
}

// What cannot be kept is in the log, and suggested all the same.
func TestSuggestionNotKept(t *testing.T) {
	path := filepath.Join(t.TempDir(), "suggestions.json")
	os.WriteFile(path, []byte("nope"), 0o600)
	a, log := recorded(t, ownerConfig(), &titleLLM{reply: "CHANGE: add grep."})
	a.builder = a.builder.WithSuggestions(suggestion.Open(path))
	got := ""
	a.reviewed("c", nil, nil, toolsOf{}, func(text string) { got = text })
	lines := log.take()
	if got != "add grep." || len(lines) != 2 || !strings.HasPrefix(lines[1], "chat c: the suggestion is not kept: ") {
		t.Fatalf("suggested %q, log %q", got, lines)
	}
}
