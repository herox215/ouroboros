package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"ouroboros/internal/agent"
	"ouroboros/internal/config"
	"ouroboros/internal/memory"
	"ouroboros/internal/openai"
)

// archiveLLM is the model of the archivist: it calls the tools of calls one
// after the other, a call a round, and then ends the pass. It counts its
// calls and keeps what the tools answered.
type archiveLLM struct {
	mu      sync.Mutex
	asked   int
	calls   []archiveCall
	results []string
	block   bool // waits for the end of the pass instead of answering
}

// archiveCall is one call of a tool by the archivist.
type archiveCall struct {
	tool string
	args any
}

func (l *archiveLLM) Complete(ctx context.Context, _ string, msgs []openai.Message, _ []openai.Tool) (openai.Message, error) {
	l.mu.Lock()
	l.asked++
	if l.block {
		l.mu.Unlock()
		<-ctx.Done()
		return openai.Message{}, ctx.Err()
	}
	defer l.mu.Unlock()
	if last := msgs[len(msgs)-1]; last.Role == "tool" {
		l.results = append(l.results, last.Content)
	}
	if len(l.calls) == 0 {
		return openai.Message{Role: "assistant", Content: "Nothing more to do."}, nil
	}
	c := l.calls[0]
	l.calls = l.calls[1:]
	raw, _ := json.Marshal(c.args)
	return openai.Message{Role: "assistant", ToolCalls: []openai.ToolCall{{ID: fmt.Sprint("c", l.asked), Type: "function", Function: openai.FunctionCall{Name: c.tool, Arguments: string(raw)}}}}, nil
}

func (l *archiveLLM) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.asked
}

func (l *archiveLLM) told() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.results...)
}

// archivistAssistant is an assistant whose archivist walks the stores of
// dirs, with the interval of cfg.
func archivistAssistant(t *testing.T, cfg config.Config, llm agent.LLM, dirs ...string) (*Assistant, *logged) {
	t.Helper()
	a, log := recorded(t, cfg, llm)
	profiles := make([]MemoryProfile, len(dirs))
	for i, dir := range dirs {
		profiles[i] = MemoryProfile{Dir: dir, Store: func() (*memory.Store, error) { return memory.Open(dir) }}
	}
	a.builder = a.builder.WithMemoryProfiles(func() []MemoryProfile { return profiles })
	return a, log
}

func archiveConfig(interval string) config.Config {
	cfg := ownerConfig()
	cfg.MemoryInterval = &interval
	return cfg
}

// seeded is the store in dir with the memories of key and content, a pair
// each, and those memories.
func seeded(t *testing.T, dir string, kv ...string) (*memory.Store, []memory.Entry) {
	t.Helper()
	store, err := memory.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	var out []memory.Entry
	for i := 0; i < len(kv); i += 2 {
		e, _, err := store.Remember(context.Background(), "", kv[i], kv[i+1], "session", false, time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, e)
	}
	return store, out
}

// A pass with nothing to examine makes no model call and writes nothing.
func TestArchivistIsIdleWhenThereIsNothingToDo(t *testing.T) {
	dir := t.TempDir()
	llm := &archiveLLM{}
	a, _ := archivistAssistant(t, archiveConfig("1h"), llm, dir)
	a.archive(time.Now())
	if n := llm.count(); n != 0 {
		t.Fatalf("a model was asked %d times without a candidate", n)
	}
	store, _ := seeded(t, dir)
	if metas, err := store.Metas(context.Background(), "archivist/"); err != nil || len(metas) != 0 {
		t.Fatalf("a pass with nothing to do wrote: %+v, %v", metas, err)
	}
}

// The archivist merges two memories that say the same with its tool, and the
// merge is in the log with its reason. A pass right after it makes no model
// call and writes nothing, although the interval passed.
func TestArchivistMergesAndThenSleeps(t *testing.T) {
	dir := t.TempDir()
	store, m := seeded(t, dir,
		"minerva-zugang", "Zugang zu minerva über SSH als root.",
		"minerva-ssh-access", "Zugang zu minerva per SSH als root.")
	llm := &archiveLLM{calls: []archiveCall{{toolMemoryMerge, map[string]any{
		"ids": []string{m[0].ID, m[1].ID}, "key": "minerva-ssh-access",
		"content": "Zugang zu minerva per SSH als root.", "reason": "both say the same",
	}}}}
	a, log := archivistAssistant(t, archiveConfig("1ns"), llm, dir)
	now := time.Now()
	a.archive(now)
	if n := llm.count(); n != 2 {
		t.Fatalf("the model was asked %d times, want the merge and the end", n)
	}
	ctx := context.Background()
	current, err := store.Current(ctx, 10)
	if err != nil || len(current) != 1 || current[0].Key != "minerva-ssh-access" {
		t.Fatalf("duplicates are not merged: %+v, %v", current, err)
	}
	changes, err := store.Changes(ctx, 10)
	if err != nil || len(changes) != 1 || changes[0].Action != "merge" || changes[0].Reason != "both say the same" {
		t.Fatalf("the merge is not in the log with its reason: %+v, %v", changes, err)
	}
	lines := strings.Join(log.take(), "\n")
	if !strings.Contains(lines, "memories of "+dir+": merge "+m[0].ID+" "+m[1].ID+": both say the same") ||
		!strings.Contains(lines, "memories of "+dir+": Nothing more to do.") {
		t.Fatalf("log:\n%s", lines)
	}
	a.archive(now.Add(time.Hour))
	if n := llm.count(); n != 2 {
		t.Fatalf("a second pass asked the model again (%d calls)", n)
	}
	if changes, err = store.Changes(ctx, 10); err != nil || len(changes) != 1 {
		t.Fatalf("a second pass changed something: %+v, %v", changes, err)
	}
	if err := store.UndoChange(ctx, changes[0].ID); err != nil {
		t.Fatal(err)
	}
	if current, err = store.Current(ctx, 10); err != nil || len(current) != 2 {
		t.Fatalf("the merge is not undone: %+v, %v", current, err)
	}
}

// The archivist changes only what it was shown: a memory it was not given
// it has to find or read first. It finds one in every scope.
func TestArchivistChangesOnlyWhatItSaw(t *testing.T) {
	dir := t.TempDir()
	store, m := seeded(t, dir, "editor", "the owner edits with vim")
	ctx := context.Background()
	hidden, _, err := store.Remember(ctx, "/p", "build", "the project builds with make", "session", false, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	// Only the editor is a candidate: the build was examined in its version.
	if err := store.SetMeta(ctx, archivistSeen+hidden.ID, fmt.Sprint(hidden.Version)); err != nil {
		t.Fatal(err)
	}
	llm := &archiveLLM{calls: []archiveCall{
		{toolMemoryArchive, map[string]any{"id": hidden.ID, "reason": "unseen"}},
		{toolMemoryArchive, map[string]any{"id": "nothing", "reason": "invented"}},
		{toolMemoryFind, map[string]any{"query": "make"}},
		{toolMemoryRead, map[string]any{"id": m[0].ID}},
		{toolMemoryScope, map[string]any{"id": hidden.ID, "project": "", "reason": "holds everywhere"}},
	}}
	a, _ := archivistAssistant(t, archiveConfig("1h"), llm, dir)
	a.archive(time.Now())
	told := llm.told()
	if len(told) != 5 || !strings.Contains(told[0], "was not shown to you") || !strings.Contains(told[1], "was not shown to you") ||
		!strings.Contains(told[2], hidden.ID+" | /p | ") || !strings.Contains(told[3], "the owner edits with vim") || !strings.Contains(told[4], "Done, change") {
		t.Fatalf("the tools told:\n%s", strings.Join(told, "\n"))
	}
	if e, err := store.Get(ctx, hidden.ID); err != nil || e.Archived || e.Project != "" {
		t.Fatalf("the build memory: %+v, %v", e, err)
	}
}

// A pass makes at most archivistOps changes in one profile.
func TestArchivistIsBounded(t *testing.T) {
	dir := t.TempDir()
	var kv []string
	for i := range archivistOps + 1 {
		kv = append(kv, fmt.Sprint("key", i), fmt.Sprint("memory number ", i))
	}
	store, m := seeded(t, dir, kv...)
	llm := &archiveLLM{}
	for _, e := range m {
		llm.calls = append(llm.calls, archiveCall{toolMemoryArchive, map[string]any{"id": e.ID, "reason": "outdated"}})
	}
	a, _ := archivistAssistant(t, archiveConfig("1h"), llm, dir)
	a.archive(time.Now())
	if changes, err := store.Changes(context.Background(), 100); err != nil || len(changes) != archivistOps {
		t.Fatalf("%d changes, %v", len(changes), err)
	}
	if told := llm.told(); !strings.Contains(told[len(told)-1], "end it") {
		t.Fatalf("the last change was told %q", told[len(told)-1])
	}
}

// The archivist walks the stores of every profile of the node.
func TestArchivistCoversEveryProfile(t *testing.T) {
	first, second := t.TempDir(), t.TempDir()
	for _, dir := range []string{first, second} {
		seeded(t, dir, "key", "a memory of this profile")
	}
	llm := &archiveLLM{}
	a, _ := archivistAssistant(t, archiveConfig("1h"), llm, first, second)
	a.archive(time.Now())
	if n := llm.count(); n != 2 {
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
	seeded(t, dir, "key", "a memory")
	llm := &archiveLLM{}
	a, _ := archivistAssistant(t, archiveConfig("off"), llm, dir)
	a.archive(time.Now())
	if n := llm.count(); n != 0 {
		t.Fatalf("the archivist ran while off (%d calls)", n)
	}
}

// Stop cancels a pass on its way: it does not wait for the model, and leaves
// nothing written behind. After Stop no pass starts.
func TestStopCancelsTheArchivist(t *testing.T) {
	dir := t.TempDir()
	store, _ := seeded(t, dir, "key", "a memory")
	llm := &archiveLLM{block: true}
	a, _ := archivistAssistant(t, archiveConfig("1h"), llm, dir)
	done := make(chan struct{})
	go func() {
		a.archive(time.Now())
		close(done)
	}()
	eventually(t, "the model asked", func() bool { return llm.count() == 1 })
	a.Stop()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Stop did not end the pass")
	}
	if metas, err := store.Metas(context.Background(), "archivist/"); err != nil || len(metas) != 0 {
		t.Fatalf("a cancelled pass wrote: %+v, %v", metas, err)
	}
	a.archive(time.Now())
	if n := llm.count(); n != 1 {
		t.Fatalf("a pass started after Stop (%d calls)", n)
	}
	// Serving again starts it again, once the owner is away.
	llm.mu.Lock()
	llm.block = false
	llm.mu.Unlock()
	a.idleAfter = 10 * time.Millisecond
	a.StartArchive()
	eventually(t, "a pass after the start", func() bool { return llm.count() == 2 })
	a.StopArchive()
}

// StartArchive runs a pass in the background once the owner is away, and
// nothing without profiles.
func TestStartArchive(t *testing.T) {
	llm := &archiveLLM{}
	plain, _ := recorded(t, archiveConfig("1h"), llm)
	plain.StartArchive()
	dir := t.TempDir()
	seeded(t, dir, "key", "a memory")
	a, _ := archivistAssistant(t, archiveConfig("1h"), llm, dir)
	a.idleAfter = 10 * time.Millisecond
	a.StartArchive()
	eventually(t, "a pass", func() bool { return llm.count() == 1 })
	a.StopArchive()
}

// The archivist waits for the owner to leave: a pass is due at the end of
// the interval, but no sooner than the idle time after the owner was last
// heard of.
func TestArchivistWaitsForTheOwnerToLeave(t *testing.T) {
	dir := t.TempDir()
	seeded(t, dir, "key", "a memory")
	a, _ := archivistAssistant(t, archiveConfig("1ns"), &archiveLLM{}, dir)
	now := time.Now()
	a.active = now
	at, on := a.nextArchive(now)
	if want := now.Add(a.idleAfter); !on || !at.Equal(want) {
		t.Fatalf("the archivist may run at %v (%v), want once the owner left at %v", at, on, want)
	}
	a.active = now.Add(-3 * time.Hour)
	if at, on = a.nextArchive(now); !on || at.After(now) {
		t.Fatalf("the archivist waits for an owner long gone: %v, %v", at, on)
	}
}

// A pass that used up its changes leaves what it did not get to as a
// candidate: nothing of it counts as examined. A pass that did not use them
// up marks every candidate.
func TestArchivistLeavesWhatItDidNotGetTo(t *testing.T) {
	ctx := context.Background()
	seed := func(t *testing.T, n int, archiving int) (string, []archiveCall) {
		t.Helper()
		dir := t.TempDir()
		var kv []string
		for i := 0; i < n; i++ {
			kv = append(kv, fmt.Sprintf("key-%d", i), fmt.Sprintf("memory %d", i))
		}
		_, m := seeded(t, dir, kv...)
		calls := make([]archiveCall, archiving)
		for i := range calls {
			calls[i] = archiveCall{toolMemoryArchive, map[string]any{"id": m[i].ID, "reason": "gone"}}
		}
		return dir, calls
	}
	// examined runs a pass over dir and is the store after it.
	examined := func(t *testing.T, dir string, calls []archiveCall) *memory.Store {
		t.Helper()
		a, _ := recorded(t, archiveConfig("1ns"), &archiveLLM{calls: calls})
		a.builder = a.builder.WithMemoryProfiles(func() []MemoryProfile {
			return []MemoryProfile{{Dir: dir, Store: func() (*memory.Store, error) { return memory.Open(dir) }}}
		})
		a.archive(time.Now())
		store, err := memory.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { store.Close() })
		return store
	}
	t.Run("at the limit", func(t *testing.T) {
		dir, calls := seed(t, archivistOps+2, archivistOps)
		store := examined(t, dir, calls)
		if metas, err := store.Metas(ctx, archivistSeen); err != nil || len(metas) != 0 {
			t.Fatalf("a pass at its limit marked what it did not examine: %+v, %v", metas, err)
		}
	})
	t.Run("below the limit", func(t *testing.T) {
		dir, calls := seed(t, 3, 1)
		store := examined(t, dir, calls)
		if metas, err := store.Metas(ctx, archivistSeen); err != nil || len(metas) != 3 {
			t.Fatalf("a pass below its limit marked %d of 3 candidates: %+v, %v", len(metas), metas, err)
		}
	})
}

// A pass that cannot open, read or mark a store says so in the log and goes
// on with the next profile; one within the interval does nothing.
func TestArchivistFailures(t *testing.T) {
	ctx := context.Background()
	run := func(t *testing.T, cfg config.Config, profiles ...MemoryProfile) (*archiveLLM, []string) {
		t.Helper()
		llm := &archiveLLM{}
		a, log := recorded(t, cfg, llm)
		a.builder = a.builder.WithMemoryProfiles(func() []MemoryProfile { return profiles })
		a.archive(time.Now())
		return llm, log.take()
	}
	failing := MemoryProfile{Dir: "gone", Store: func() (*memory.Store, error) { return nil, errors.New("no store") }}
	of := func(s *memory.Store) MemoryProfile {
		return MemoryProfile{Dir: "raw", Store: func() (*memory.Store, error) { return s, nil }}
	}
	t.Run("no profiles", func(t *testing.T) {
		if llm, lines := run(t, archiveConfig("1h")); llm.count() != 0 || len(lines) != 0 {
			t.Fatalf("%d calls, log %q", llm.count(), lines)
		}
	})
	t.Run("no first store", func(t *testing.T) {
		if _, lines := run(t, archiveConfig("1h"), failing); !slices.Contains(lines, "the memories cannot be archived: no store") {
			t.Fatalf("log %q", lines)
		}
	})
	t.Run("no config", func(t *testing.T) {
		a := New(func() (config.Config, error) { return config.Config{}, errors.New("no config") }, Builder{}, nil)
		var lines []string
		a.log = func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) }
		dir := t.TempDir()
		a.builder = a.builder.WithMemoryProfiles(func() []MemoryProfile {
			return []MemoryProfile{{Dir: dir, Store: func() (*memory.Store, error) { return memory.Open(dir) }}}
		})
		a.archive(time.Now())
		if len(lines) != 1 || !strings.HasPrefix(lines[0], "the memories cannot be archived: ") {
			t.Fatalf("log %q", lines)
		}
	})
	t.Run("no marker", func(t *testing.T) {
		db, store := rawStore(t)
		if _, err := db.Exec("DROP TABLE memory_meta"); err != nil {
			t.Fatal(err)
		}
		if _, lines := run(t, archiveConfig("1h"), of(store)); len(lines) != 1 || !strings.HasPrefix(lines[0], "the memories cannot be archived: ") {
			t.Fatalf("log %q", lines)
		}
	})
	t.Run("within the interval", func(t *testing.T) {
		_, store := rawStore(t)
		keepMemory(t, store, "", "key", "a memory", time.Time{})
		if err := store.Mark(ctx, archivistMarker, time.Now()); err != nil {
			t.Fatal(err)
		}
		if llm, _ := run(t, archiveConfig("1h"), of(store)); llm.count() != 0 {
			t.Fatalf("%d calls within the interval", llm.count())
		}
	})
	t.Run("a profile without its store, and no mark", func(t *testing.T) {
		db, first := rawStore(t)
		if _, err := db.Exec("CREATE TRIGGER keep BEFORE INSERT ON memory_meta BEGIN SELECT RAISE(ABORT,'kept'); END;"); err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		seeded(t, dir, "key", "a memory")
		second := MemoryProfile{Dir: dir, Store: func() (*memory.Store, error) { return memory.Open(dir) }}
		llm, lines := run(t, archiveConfig("1h"), of(first), failing, second)
		if llm.count() != 1 || !slices.Contains(lines, "the memories of gone cannot be archived: no store") ||
			!slices.ContainsFunc(lines, func(l string) bool { return strings.HasPrefix(l, "the memories cannot be archived: ") }) {
			t.Fatalf("%d calls, log %q", llm.count(), lines)
		}
	})
}

// A cancelled pass asks no model, and one without its model examines nothing.
func TestArchiveStoreFailures(t *testing.T) {
	dir := t.TempDir()
	store, _ := seeded(t, dir, "key", "a memory")
	llm := &archiveLLM{}
	a, _ := archivistAssistant(t, archiveConfig("1h"), llm, dir)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.archiveStore(ctx, dir, store, archiveConfig("1h"), time.Time{}); err == nil {
		t.Fatal("a cancelled pass reported no error")
	}
	a.builder.LLM = func(string) (agent.LLM, error) { return nil, errors.New("no access") }
	if _, err := a.archiveStore(context.Background(), dir, store, archiveConfig("1h"), time.Time{}); err == nil {
		t.Fatal("a pass without its model reported no error")
	}
	if n := llm.count(); n != 0 {
		t.Fatalf("the model was asked %d times", n)
	}
	if metas, err := store.Metas(context.Background(), archivistSeen); err != nil || len(metas) != 0 {
		t.Fatalf("a failed pass wrote: %+v, %v", metas, err)
	}
}

// Two memories that look alike but that the archivist leaves alone are not
// put to it again while neither changes.
func TestArchivistKeepsTheJudgementOfAPair(t *testing.T) {
	dir := t.TempDir()
	seeded(t, dir,
		"minerva-ssh", "minerva takes SSH as root on port 22",
		"minerva-backup", "minerva takes backups over SSH as root at night")
	llm := &archiveLLM{}
	a, _ := archivistAssistant(t, archiveConfig("1ns"), llm, dir)
	now := time.Now()
	a.archive(now)
	a.archive(now.Add(time.Hour))
	if n := llm.count(); n != 1 {
		t.Fatalf("the pair was put to the model %d times", n)
	}
}

// The tools tell the archivist what is wrong with a call, show it what it
// reads as it is, cut, core or archived, and name what a change made.
func TestArchivistTools(t *testing.T) {
	_, store := rawStore(t)
	ctx := context.Background()
	long := keepMemory(t, store, "", "long", "one fact. "+strings.Repeat("x", 600), time.Time{})
	core, err := store.CreateCore(ctx, "name", "the owner is called Lu", "session")
	if err != nil {
		t.Fatal(err)
	}
	gone := keepMemory(t, store, "", "gone", "an old note", time.Time{})
	if err := store.Archive(ctx, "", []string{gone.ID}); err != nil {
		t.Fatal(err)
	}
	llm := &archiveLLM{calls: []archiveCall{
		{toolMemoryFind, map[string]any{"query": " "}},
		{toolMemoryFind, map[string]any{"query": "nowhere"}},
		{toolMemoryRead, map[string]any{}},
		{toolMemoryRead, map[string]any{"id": "nothing"}},
		{toolMemoryRead, map[string]any{"id": gone.ID}},
		{toolMemoryRead, map[string]any{"id": long.ID}},
		{toolMemoryMerge, map[string]any{"ids": "not a list"}},
		{toolMemoryRewrite, map[string]any{"id": long.ID, "content": strings.Repeat("y", 2000), "reason": "longer"}},
		{toolMemorySplit, map[string]any{"id": long.ID, "reason": "two facts", "parts": []map[string]string{
			{"key": "fact", "content": "one fact."}, {"key": "rest", "content": strings.Repeat("x", 600)}}}},
		{toolMemoryCore, map[string]any{"id": core.ID, "core": false, "reason": "not needed in every prompt"}},
	}}
	a, _ := recorded(t, archiveConfig("1h"), llm)
	a.builder = a.builder.WithMemoryProfiles(func() []MemoryProfile {
		return []MemoryProfile{{Dir: "raw", Store: func() (*memory.Store, error) { return store, nil }}}
	})
	a.archive(time.Now())
	told := llm.told()
	for i, want := range []string{
		`needs "query"`, "No memory has these words.", `needs "id"`, "not found",
		gone.ID + " | global | ", long.ID + " | ", toolMemoryMerge + ": ", "may not say more", "New: ", "Done, change",
	} {
		if i >= len(told) || !strings.Contains(told[i], want) {
			t.Fatalf("call %d: want %q in:\n%s", i, want, strings.Join(told, "\n"))
		}
	}
	if !strings.Contains(told[4], " | archived | ") {
		t.Errorf("an archived memory reads %q", told[4])
	}
}

// The prompt shows a long memory cut, and which are core.
func TestMemoryLine(t *testing.T) {
	e := memory.Entry{ID: "i", Key: "k", Content: strings.Repeat("ä", 300), Core: true, Updated: "now"}
	line := memoryLine(e, 5)
	if line != "i | global | v0 | now | core | k: ää…" {
		t.Fatalf("line %q", line)
	}
}

// A memory_find on a store whose index is gone fails as a call, not the pass.
func TestArchivistFindFails(t *testing.T) {
	db, store := rawStore(t)
	keepMemory(t, store, "", "key", "a memory", time.Time{})
	if _, err := db.Exec("DROP TABLE memory_fts"); err != nil {
		t.Fatal(err)
	}
	llm := &archiveLLM{calls: []archiveCall{{toolMemoryFind, map[string]any{"query": "memory"}}}}
	a, _ := recorded(t, archiveConfig("1h"), llm)
	a.builder = a.builder.WithMemoryProfiles(func() []MemoryProfile {
		return []MemoryProfile{{Dir: "raw", Store: func() (*memory.Store, error) { return store, nil }}}
	})
	a.archive(time.Now())
	if told := llm.told(); len(told) != 1 || !strings.Contains(told[0], "memory_fts") {
		t.Fatalf("told %q", told)
	}
}

// A memory the archivist saw only as a cut line is not rewritten: it has to
// read the whole text first.
func TestArchivistRewritesOnlyWhatItReadInFull(t *testing.T) {
	_, store := rawStore(t)
	long := keepMemory(t, store, "", "long", "one fact. "+strings.Repeat("x", 600), time.Time{})
	llm := &archiveLLM{calls: []archiveCall{
		{toolMemoryRewrite, map[string]any{"id": long.ID, "content": "short", "reason": "shorter"}},
	}}
	a, _ := recorded(t, archiveConfig("1h"), llm)
	a.builder = a.builder.WithMemoryProfiles(func() []MemoryProfile {
		return []MemoryProfile{{Dir: "raw", Store: func() (*memory.Store, error) { return store, nil }}}
	})
	a.archive(time.Now())
	if told := llm.told(); len(told) != 1 || !strings.Contains(told[0], "was shown cut") {
		t.Fatalf("told %q", told)
	}
	if got, err := store.Get(context.Background(), long.ID); err != nil || got.Content == "short" {
		t.Fatalf("memory after the refused rewrite: %+v, %v", got, err)
	}
}
