package assistant

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"ouroboros/internal/memory"
	"ouroboros/internal/workdir"
)

// memories is a store of memories in a directory of the test.
func memories(t *testing.T) *memory.Store {
	t.Helper()
	s, err := memory.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// opened returns store as a Builder is to get it.
func opened(store *memory.Store) func() (*memory.Store, error) {
	return func() (*memory.Store, error) { return store, nil }
}

// Only lines that start with REMEMBER:, a scope there is, a key and a
// memory are remembered, at most maxLessons of them, and none longer than
// maxLearned; a CHANGE beside them is proposed all the same.
func TestLessons(t *testing.T) {
	reply := "CHANGE: Add\n  grep.\n" +
		"- **REMEMBER:** project build: ouro is built with make build.\n" +
		"REMEMBER: everywhere x: y\n" +
		"REMEMBER: global : no key\n" +
		"REMEMBER: global no colon\n" +
		"REMEMBER: global " + strings.Repeat("k", memory.MaxKey+1) + ": too long a key\n" +
		"REMEMBER: global long: " + strings.Repeat("x", maxLearned+1) + "\n" +
		"REMEMBER: core style: short answers,\n  without headings.\n" +
		"REMEMBER: global editor: vim\n" +
		"REMEMBER: global shell: zsh\n"
	want := []lesson{{"project", "build", "ouro is built with make build.", 0}, {"core", "style", "short answers, without headings.", 0}, {"global", "editor", "vim", 0}}
	if got := lessons(reply); !slices.Equal(got, want) {
		t.Errorf("lessons:\n%+v\nwant\n%+v", got, want)
	}
	if got := proposed(reply); got != "Add grep." {
		t.Errorf("proposed %q", got)
	}
	if got := lessons("NONE"); got != nil {
		t.Errorf("NONE remembers %+v", got)
	}
}

// What a review remembers is kept at once, in the scope it names, and the
// session is told; a CHANGE beside it is suggested as before.
func TestReviewRemembers(t *testing.T) {
	store := memories(t)
	llm := &reviewLLM{failing: failedTools, reply: "CHANGE: Let read say which files are near.\n" +
		"REMEMBER: project build: ouro is built with make build.\n" +
		"REMEMBER: core style: short answers."}
	a, log := recorded(t, ownerConfig(), llm)
	a.builder = a.builder.WithMemories(opened(store))
	got := make(chan Remembered, 2)
	suggested := make(chan string, 1)
	ctx := WithRemembered(workdir.With(context.Background(), "/p"), func(r Remembered) { got <- r })
	ctx = WithReview(ctx, func(text string) { suggested <- text })
	if _, err := a.Ask(ctx, "c", "go", nil); err != nil {
		t.Fatal(err)
	}
	var kept []Remembered
	for range 2 {
		select {
		case r := <-got:
			kept = append(kept, r)
		case <-time.After(time.Second):
			t.Fatalf("remembered %+v:\n%s", kept, strings.Join(log.take(), "\n"))
		}
	}
	if <-suggested != "Let read say which files are near." {
		t.Error("the change is not suggested beside")
	}
	if m := kept[0].Memory; m.Project != "/p" || m.Key != "build" || m.Source != "c" || kept[0].Was.ID != "" {
		t.Errorf("project memory: %+v", kept[0])
	}
	if m := kept[1].Memory; m.Project != "" || !m.Core || m.Content != "short answers." {
		t.Errorf("core memory: %+v", kept[1])
	}
	if !strings.Contains(llm.reviewed[0].Content, "REMEMBER:") {
		t.Error("the review is not asked what to remember")
	}
}

// A memory of the project of an answer without one is global, a core one
// that does not fit too, and one that cannot be kept is in the log only.
// Without a store or WithRemembered nothing is kept or told.
func TestLearn(t *testing.T) {
	store := memories(t)
	a, log := recorded(t, ownerConfig(), &titleLLM{})
	var told []Remembered
	to := review{remembered: func(r Remembered) { told = append(told, r) }}
	logf := func(format string, args ...any) { a.log("chat c: "+format, args...) }

	a.learn(context.Background(), "c", lesson{"project", "build", "make build", 0}, to, logf)
	if _, err := store.Search(context.Background(), "", "global", "build", 10); err != nil || len(told) != 0 {
		t.Fatalf("kept without a store: %+v", told)
	}
	a.builder = a.builder.WithMemories(opened(store))
	a.learn(context.Background(), "c", lesson{"project", "build", "make build", 0}, to, logf)
	if len(told) != 1 || told[0].Memory.Project != "" {
		t.Fatalf("a memory of no project: %+v", told)
	}
	big, err := store.CreateCore(context.Background(), "big", strings.Repeat("x", memory.MaxCore), "owner")
	if err != nil {
		t.Fatal(err)
	}
	a.learn(context.Background(), "c", lesson{"core", "style", "short answers", 0}, to, logf)
	if len(told) != 2 || told[1].Memory.Core || told[1].Memory.Key != "style" {
		t.Fatalf("a core memory that does not fit: %+v", told)
	}
	// A core memory that the review corrects is not kept as a global one,
	// which would take it out of every prompt, and it is not cut either.
	a.learn(context.Background(), "c", lesson{"core", "big", strings.Repeat("y", memory.MaxCore+100), 0}, to, logf)
	if len(told) != 2 {
		t.Fatalf("a full core memory was kept anyway: %+v", told)
	}
	if e, err := store.Read(context.Background(), "", big.ID); err != nil || e.Content != strings.Repeat("x", memory.MaxCore) {
		t.Fatalf("the core memory changed: %+v, %v", e, err)
	}
	a.learn(context.Background(), "c", lesson{"global", "", "no key", 0}, to, logf)
	b, blog := recorded(t, ownerConfig(), &titleLLM{reply: "done"})
	b.builder = b.builder.WithMemories(func() (*memory.Store, error) { return nil, errors.New("no store") })
	b.learn(context.Background(), "c", lesson{"global", "editor", "vim", 0}, to, func(format string, args ...any) { b.log("chat c: "+format, args...) })
	if _, err := b.Ask(context.Background(), "c", "hi", nil); err != nil {
		t.Fatal(err)
	}
	if lines := blog.take(); !slices.Contains(lines, `chat c: the memory "editor" is not kept: no store`) || !slices.Contains(lines, "chat c: the core memories cannot be read: no store") {
		t.Fatalf("without a store:\n%s", strings.Join(lines, "\n"))
	}
	a.learn(context.Background(), "c", lesson{"global", "editor", "vim", 0}, review{}, logf)
	lines := log.take()
	if len(told) != 2 || !slices.Contains(lines, `chat c: the core memories are full: "style" is kept as a global memory`) ||
		!slices.Contains(lines, `chat c: the core memories are full: "big" stays as it is, merge or shorten them first`) ||
		!slices.ContainsFunc(lines, func(l string) bool { return strings.HasPrefix(l, `chat c: the memory "" is not kept: `) }) ||
		!slices.Contains(lines, `chat c: review: remembered "editor"`) {
		t.Fatalf("told %+v, log:\n%s", told, strings.Join(lines, "\n"))
	}
}

// The core memories are in the prompt of every answer to the owner, the
// oldest first; without any, or a store, the prompt is as it was, and core
// memories that cannot be read are in the log.
func TestCoreInPrompt(t *testing.T) {
	llm := &titleLLM{reply: "done"}
	a, log := recorded(t, ownerConfig(), llm)
	if _, err := a.Ask(context.Background(), "c", "hi", nil); err != nil {
		t.Fatal(err)
	}
	plain := llm.sent[0].Content
	store := memories(t)
	a.builder = a.builder.WithMemories(opened(store))
	if _, err := a.Ask(context.Background(), "c", "hi", nil); err != nil || llm.sent[0].Content != plain {
		t.Fatalf("a prompt without core memories changed: %v", err)
	}
	for _, kv := range [][2]string{{"name", "Lu"}, {"style", "short answers"}} {
		if _, err := store.CreateCore(context.Background(), kv[0], kv[1], "owner"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.Ask(context.Background(), "c", "hi", nil); err != nil {
		t.Fatal(err)
	}
	if want := plain + "\n\n" + coreIntro + "\n- name: Lu\n- style: short answers"; llm.sent[0].Content != want {
		t.Fatalf("prompt:\n%s\nwant\n%s", llm.sent[0].Content, want)
	}
	store.Close()
	log.take()
	if _, err := a.Ask(context.Background(), "c", "hi", nil); err != nil || llm.sent[0].Content != plain {
		t.Fatalf("a prompt with core memories that cannot be read: %v", err)
	}
	if !slices.ContainsFunc(log.take(), func(l string) bool { return strings.HasPrefix(l, "chat c: the core memories cannot be read: ") }) {
		t.Error("not in the log")
	}
}
