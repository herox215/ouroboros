package assistant

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"ouroboros/internal/agent"
	"ouroboros/internal/config"
	"ouroboros/internal/memory"
	"ouroboros/internal/openai"
)

// archiveLLM answers every call with reply, and counts the calls.
type archiveLLM struct {
	mu    sync.Mutex
	asked int
	reply string
}

func (l *archiveLLM) Complete(_ context.Context, _ string, _ []openai.Message, _ []openai.Tool) (openai.Message, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.asked++
	return openai.Message{Content: l.reply}, nil
}

func (l *archiveLLM) calls() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.asked
}

// archivistAssistant is an assistant whose archivist walks the stores of
// dirs, with the interval of cfg.
func archivistAssistant(t *testing.T, cfg config.Config, llm agent.LLM, dirs ...string) *Assistant {
	t.Helper()
	b := Builder{
		LLM:              func(string) (agent.LLM, error) { return llm, nil },
		Tools:            func() []agent.Tool { return nil },
		ContextProviders: func() []agent.ContextProvider { return nil },
		AnswerHooks:      func() []agent.AnswerHook { return nil },
	}
	a := New(func() (config.Config, error) { return cfg, nil }, b, nil)
	profiles := make([]MemoryProfile, len(dirs))
	for i, dir := range dirs {
		dir := dir
		profiles[i] = MemoryProfile{Dir: dir, Store: func() (*memory.Store, error) { return memory.Open(dir) }}
	}
	a.builder = a.builder.WithMemoryProfiles(func() []MemoryProfile { return profiles })
	return a
}

func archiveConfig(interval string) config.Config {
	return config.Config{Access: "access", Model: "model", Name: "node", MemoryInterval: &interval}
}

// A pass with nothing to examine makes no model call and writes nothing.
func TestArchivistIsIdleWhenThereIsNothingToDo(t *testing.T) {
	dir := t.TempDir()
	llm := &archiveLLM{reply: "[]"}
	a := archivistAssistant(t, archiveConfig("1h"), llm, dir)
	a.archive(time.Now())
	if n := llm.calls(); n != 0 {
		t.Fatalf("a model was asked %d times without a candidate", n)
	}
	store, err := memory.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if metas, err := store.Metas(context.Background(), "archivist/"); err != nil || len(metas) != 0 {
		t.Fatalf("a pass with nothing to do wrote: %+v, %v", metas, err)
	}
}

// A pass merges two memories that say the same. A pass right after it makes
// no model call and writes nothing, although the interval passed.
func TestArchivistMergesAndThenSleeps(t *testing.T) {
	dir := t.TempDir()
	seed, err := memory.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer seed.Close()
	ctx := context.Background()
	a, _, err := seed.Remember(ctx, "", "minerva-zugang", "Zugang zu minerva über SSH als root.", "session", false, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := seed.Remember(ctx, "", "minerva-ssh-access", "Zugang zu minerva per SSH als root.", "session", false, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	llm := &archiveLLM{reply: fmt.Sprintf(
		`[{"op":"merge","ids":[%q,%q],"key":"minerva-ssh-access","content":"Zugang zu minerva per SSH als root.","reason":"both say the same"}]`,
		a.ID, b.ID)}
	cfg := archiveConfig("1ns")
	assistant := archivistAssistant(t, cfg, llm, dir)
	now := time.Now()
	assistant.archive(now)
	if n := llm.calls(); n != 1 {
		t.Fatalf("the model was asked %d times, want 1", n)
	}
	current, err := seed.Current(ctx, 10)
	if err != nil || len(current) != 1 {
		t.Fatalf("duplicates are not merged: %+v, %v", current, err)
	}
	changes, err := seed.Changes(ctx, 10)
	if err != nil || len(changes) != 1 || changes[0].Action != "merge" || changes[0].Reason == "" {
		t.Fatalf("the merge is not in the log with its reason: %+v, %v", changes, err)
	}
	assistant.archive(now.Add(time.Hour))
	if n := llm.calls(); n != 1 {
		t.Fatalf("a second pass asked the model again (%d calls)", n)
	}
	changes, err = seed.Changes(ctx, 10)
	if err != nil || len(changes) != 1 {
		t.Fatalf("a second pass changed something: %+v, %v", changes, err)
	}
}

// The archivist walks the stores of every profile of the node.
func TestArchivistCoversEveryProfile(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	for _, dir := range []string{first, second} {
		store, err := memory.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.Remember(context.Background(), "", "key", "a memory of this profile", "session", false, time.Time{}); err != nil {
			t.Fatal(err)
		}
		store.Close()
	}
	llm := &archiveLLM{reply: "[]"}
	a := archivistAssistant(t, archiveConfig("1h"), llm, first, second)
	a.archive(time.Now())
	if n := llm.calls(); n != 2 {
		t.Fatalf("the model was asked %d times, want once per profile", n)
	}
	for _, dir := range []string{first, second} {
		store, err := memory.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		metas, err := store.Metas(context.Background(), archivistSeen)
		store.Close()
		if err != nil || len(metas) == 0 {
			t.Fatalf("profile %s was not examined: %+v, %v", dir, metas, err)
		}
	}
}

// Off is off: no model call, even with candidates.
func TestArchivistCanBeTurnedOff(t *testing.T) {
	dir := t.TempDir()
	store, err := memory.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Remember(context.Background(), "", "key", "a memory", "session", false, time.Time{}); err != nil {
		t.Fatal(err)
	}
	store.Close()
	llm := &archiveLLM{reply: "[]"}
	a := archivistAssistant(t, archiveConfig("off"), llm, dir)
	a.archive(time.Now())
	if n := llm.calls(); n != 0 {
		t.Fatalf("the archivist ran while off (%d calls)", n)
	}
}

// A cancelled pass changes nothing and asks no model.
func TestCancelledArchivistChangesNothing(t *testing.T) {
	dir := t.TempDir()
	store, err := memory.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, _, err := store.Remember(context.Background(), "", "key", "a memory", "session", false, time.Time{}); err != nil {
		t.Fatal(err)
	}
	llm := &archiveLLM{reply: "[]"}
	a := archivistAssistant(t, archiveConfig("1h"), llm, dir)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.archiveStore(ctx, dir, store, archiveConfig("1h"), time.Time{}); err == nil {
		t.Fatal("a cancelled pass reported no error")
	}
	if n := llm.calls(); n != 0 {
		t.Fatalf("a cancelled pass asked the model %d times", n)
	}
}
