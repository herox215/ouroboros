package assistant

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"ouroboros/internal/agent"
	"ouroboros/internal/config"
	"ouroboros/internal/memory"
	"ouroboros/internal/openai"
	"ouroboros/internal/storage"
	"ouroboros/internal/workdir"
)

// learnLLM answers the calls of the learning loop as its fields say, and
// every other call "done", keeping the system prompt it was sent.
type learnLLM struct {
	mu       sync.Mutex
	words    string
	wordsErr error
	review   string
	reviewed [][]openai.Message
	prompts  []string
}

func (l *learnLLM) Complete(_ context.Context, _ string, msgs []openai.Message, _ []openai.Tool) (openai.Message, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	switch msgs[0].Content {
	case keywordPrompt:
		return openai.Message{Content: l.words}, l.wordsErr
	case reviewPrompt:
		l.reviewed = append(l.reviewed, msgs)
		return openai.Message{Content: l.review}, nil
	}
	l.prompts = append(l.prompts, msgs[0].Content)
	return openai.Message{Content: "done"}, nil
}

// prompt is the system prompt of the last answer.
func (l *learnLLM) prompt() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.prompts[len(l.prompts)-1]
}

// rawStore is a store of memories with the pool it is on, to break it with.
func rawStore(t *testing.T) (*sql.DB, *memory.Store) {
	t.Helper()
	db, err := storage.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s, err := memory.New(db)
	if err != nil {
		t.Fatal(err)
	}
	return db, s
}

// keepMemory keeps a memory for a test.
func keepMemory(t *testing.T, s *memory.Store, project, key, content string, expires time.Time) memory.Entry {
	t.Helper()
	e, _, err := s.Remember(context.Background(), project, key, content, "owner", false, expires)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

// eventually waits until ok, at most two seconds.
func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(2 * time.Second); !ok(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("never %s", what)
		}
	}
}

// A topic is given the memories of its project at once, and from its next
// answer on those its first message has words of; then its prompt stays the
// same. Each counts as loaded once. The thread is given none.
func TestTopicIsGivenItsMemories(t *testing.T) {
	_, store := rawStore(t)
	build := keepMemory(t, store, "/p", "build", "make build compiles ouro", time.Time{})
	keepMemory(t, store, "/p", "tests", "make test runs the tests", time.Time{})
	editor := keepMemory(t, store, "", "editor", "vim is the editor", time.Time{})
	keepMemory(t, store, "", "far", "unrelated", time.Time{})
	if _, err := store.CreateCore(context.Background(), "name", "Lu", "owner"); err != nil {
		t.Fatal(err)
	}
	llm := &learnLLM{words: "editor build"}
	a, _ := recorded(t, ownerConfig(), llm)
	a.builder = a.builder.WithMemories(opened(store))
	ctx := workdir.With(context.Background(), "/p")
	ask := func(ctx context.Context) string {
		t.Helper()
		if _, err := a.Ask(ctx, "c", "wie baue ich das?", nil); err != nil {
			t.Fatal(err)
		}
		return llm.prompt()
	}
	first := ask(ctx)
	for _, want := range []string{"- name: Lu", topicIntro + "\n- tests: make test runs the tests\n- build: make build compiles ouro\n", "memory_search finds more memories, on: "} {
		if !strings.Contains(first, want) {
			t.Fatalf("the first prompt lacks %q:\n%s", want, first)
		}
	}
	if !strings.Contains(first, "editor") || strings.Contains(first, "- editor:") || strings.Contains(first, "· build") {
		t.Fatalf("the map:\n%s", first)
	}
	eventually(t, "found the memories of the words", func() bool {
		a.learning.Lock()
		defer a.learning.Unlock()
		return a.topics["c"].found
	})
	second := ask(ctx)
	if !strings.HasSuffix(second, "- build: make build compiles ouro\n- editor: vim is the editor\nmemory_search finds more memories, on: far") {
		t.Fatalf("the second prompt:\n%s", second)
	}
	if third := ask(ctx); third != second {
		t.Fatalf("the prompt changed again:\n%s", third)
	}
	for _, e := range []memory.Entry{build, editor} {
		if got, _ := store.Read(context.Background(), e.Project, e.ID); got.Loaded != 1 {
			t.Errorf("%s loaded %d times", e.Key, got.Loaded)
		}
	}
	if _, err := a.Ask(WithThread(ctx, &fakeThread{}), "thread", "hi", nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(llm.prompt(), topicIntro) {
		t.Fatal("the thread is given the memories of a topic")
	}
}

// What cannot be read, counted or found of the memories of a topic is in the
// log, and the topic goes on without them.
func TestTopicMemoriesThatFail(t *testing.T) {
	db, store := rawStore(t)
	keepMemory(t, store, "/p", "build", "make build", time.Time{})
	if _, err := db.Exec("CREATE TRIGGER keep BEFORE UPDATE ON memories BEGIN SELECT RAISE(ABORT,'kept'); END;"); err != nil {
		t.Fatal(err)
	}
	llm := &learnLLM{wordsErr: errors.New("no words")}
	a, log := recorded(t, ownerConfig(), llm)
	a.builder = a.builder.WithMemories(opened(store))
	if _, err := a.Ask(workdir.With(context.Background(), "/p"), "c", "build", nil); err != nil {
		t.Fatal(err)
	}
	eventually(t, "logged the words that failed", func() bool {
		return slices.ContainsFunc(log.take(), func(l string) bool { return l == "chat c: the memories of the topic cannot be found: no words" })
	})
	if !strings.Contains(llm.prompt(), "- build: make build") {
		t.Fatal("a memory that cannot be counted is not given")
	}

	otherDB, other := rawStore(t)
	b, blog := recorded(t, ownerConfig(), llm)
	b.builder = b.builder.WithMemories(opened(other))
	otherDB.Close()
	if _, err := b.Ask(context.Background(), "c", "hi", nil); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(blog.take(), func(l string) bool { return strings.HasPrefix(l, "chat c: the memories of the topic cannot be read: ") }) {
		t.Fatal("not in the log")
	}

	c, clog := recorded(t, ownerConfig(), &learnLLM{words: "build"})
	c.builder = c.builder.WithMemories(func() (*memory.Store, error) { return nil, errors.New("no store") })
	if _, err := c.Ask(context.Background(), "c", "hi", nil); err != nil {
		t.Fatal(err)
	}
	c.keywords("c", "hi", "", nil)
	lines := clog.take()
	for _, want := range []string{"chat c: the memories of the topic cannot be read: no store", "chat c: the memories of the topic cannot be found: no store"} {
		if !slices.Contains(lines, want) {
			t.Errorf("the log lacks %q:\n%s", want, strings.Join(lines, "\n"))
		}
	}
	c.keywords("gone", "hi", "", nil) // a topic let go of meanwhile is no matter
}

// The words of a topic cannot be asked for without a configuration or an
// access.
func TestLearningWithoutTheLLM(t *testing.T) {
	broken := New(func() (config.Config, error) { return config.Config{}, errors.New("no config") }, Builder{}, discard)
	if _, err := broken.searchWords(context.Background(), "x"); err == nil {
		t.Error("words without a configuration")
	}
	opened := 0 // the agent opens the access first, the call again
	closed := New(func() (config.Config, error) { return ownerConfig(), nil }, Builder{
		LLM: func(string) (agent.LLM, error) {
			if opened++; opened%2 == 0 {
				return nil, errors.New("no access")
			}
			return &learnLLM{}, nil
		},
		Tools: func() []agent.Tool { return nil },
	}, discard)
	if _, err := closed.searchWords(context.Background(), "x"); err == nil {
		t.Error("words without an access")
	}
}

// A topic the owner left is looked back on once, with the messages of the
// owner in it, when it had enough answers; one with fewer and no signals is
// let go of. A topic that is not left yet is kept.
func TestFinishedTopicIsReviewed(t *testing.T) {
	_, store := rawStore(t)
	llm := &learnLLM{review: "REMEMBER: global style: short answers."}
	a, log := recorded(t, ownerConfig(), llm)
	a.builder = a.builder.WithMemories(opened(store))
	got := make(chan Remembered, 4)
	ctx := WithRemembered(WithReview(context.Background(), func(string) {}), func(r Remembered) { got <- r })
	long := strings.Repeat(" and more", 10) // no follow-up
	for _, text := range []string{"first" + long, "second" + long, "third" + long} {
		if _, err := a.Ask(ctx, "c", text, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.Ask(ctx, "d", "once", nil); err != nil {
		t.Fatal(err)
	}
	a.finish(time.Now())
	if len(llm.reviewed) != 0 {
		t.Fatal("reviewed a topic not left")
	}
	a.finish(time.Now().Add(idleTime))
	if r := <-got; r.Memory.Key != "style" || r.Memory.Source != "c" {
		t.Fatalf("remembered %+v", r)
	}
	if len(llm.reviewed) != 1 || len(a.topics) != 0 {
		t.Fatalf("reviewed %d, topics %v", len(llm.reviewed), a.topics)
	}
	asked := llm.reviewed[0][1].Content
	for _, want := range []string{"- topic finished: the owner left the topic after 3 answers", `message from the owner: "first and more`, `message from the owner: "third and more`} {
		if !strings.Contains(asked, want) {
			t.Errorf("the review lacks %q:\n%s", want, asked)
		}
	}
	if _, err := a.Ask(ctx, "c", "again", nil); err != nil {
		t.Fatal(err)
	}
	a.config = func() (config.Config, error) { return config.Config{}, errors.New("no config") }
	a.topics["c"].signaled = true
	log.take()
	a.finish(time.Now().Add(idleTime))
	if lines := log.take(); !slices.Contains(lines, "the topics left cannot be reviewed: no config") {
		t.Fatalf("log:\n%s", strings.Join(lines, "\n"))
	}
}

// Once the owner is away for idleAfter, the topics they left are looked back
// on and the memories tidied; after Stop no wait for it starts.
func TestSweepWhenTheOwnerLeft(t *testing.T) {
	_, store := rawStore(t)
	expired := keepMemory(t, store, "", "issue", "working on issue 77", time.Now().Add(-time.Minute))
	a, _ := recorded(t, ownerConfig(), &learnLLM{})
	a.builder = a.builder.WithMemories(opened(store))
	a.idleAfter = 10 * time.Millisecond
	ctx := WithReview(context.Background(), func(string) {})
	for range 2 {
		if _, err := a.Ask(ctx, "c", "hi", nil); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, "tidied", func() bool {
		e, _ := store.Get(context.Background(), expired.ID)
		return e.Archived
	})
	a.learning.Lock()
	left := len(a.topics)
	a.learning.Unlock()
	if left != 0 {
		t.Fatalf("%d topics kept", left)
	}
	b, _ := recorded(t, ownerConfig(), &learnLLM{})
	b.Stop()
	b.track("c", "hi", nil, review{})
	if b.idle != nil {
		t.Fatal("waits after Stop")
	}
}

// The memories are tidied whenever the owner left: what expired is
// archived, and what fails is in the log.
func TestTidy(t *testing.T) {
	db, store := rawStore(t)
	ctx := context.Background()
	vim := keepMemory(t, store, "", "vim", "the owner uses vim", time.Time{})
	keepMemory(t, store, "", "issue", "working on issue 77", time.Now().Add(-time.Minute))
	a, log := recorded(t, ownerConfig(), &learnLLM{})
	a.tidy(time.Now()) // without memories nothing is
	a.builder = a.builder.WithMemories(opened(store))
	a.tidy(time.Now())
	if lines := log.take(); !slices.Contains(lines, "memories: 1 archived, as they expired or were never used") {
		t.Errorf("log:\n%s", strings.Join(lines, "\n"))
	}
	active, err := store.Active(ctx, 10)
	if err != nil || len(active) != 1 || active[0].ID != vim.ID {
		t.Fatalf("active: %+v, %v", active, err)
	}
	if _, err := db.Exec("DROP TABLE memories"); err != nil {
		t.Fatal(err)
	}
	a.tidy(time.Now())
	if lines := log.take(); len(lines) != 1 || !strings.HasPrefix(lines[0], "the memories cannot be tidied: ") {
		t.Errorf("log:\n%s", strings.Join(lines, "\n"))
	}
	b, blog := recorded(t, ownerConfig(), &learnLLM{})
	b.builder = b.builder.WithMemories(func() (*memory.Store, error) { return nil, errors.New("no store") })
	b.tidy(time.Now())
	if lines := blog.take(); !slices.Contains(lines, "the memories cannot be tidied: no store") {
		t.Fatalf("log:\n%s", strings.Join(lines, "\n"))
	}
}

// Of many keys a topic is told as many as fit.
func TestTopicMapIsBounded(t *testing.T) {
	var keys []string
	for i := range 100 {
		keys = append(keys, strings.Repeat(string(rune('a'+i%26)), 30))
	}
	if got := block(nil, keys); len(got) > topicMapBytes+100 || !strings.HasPrefix(got, "memory_search finds more memories, on: aaa") {
		t.Fatalf("map of %d bytes: %.80s", len(got), got)
	}
	if block(nil, nil) != "" {
		t.Fatal("a block of nothing")
	}
}

// Topics the owner left are looked back on in the order of their IDs.
func TestFinishedTopicsInOrder(t *testing.T) {
	_, store := rawStore(t)
	llm := &learnLLM{review: "NONE"}
	a, _ := recorded(t, ownerConfig(), llm)
	a.builder = a.builder.WithMemories(opened(store))
	for _, chat := range []string{"b", "a"} {
		a.track(chat, "message from the owner: "+chat, []Signal{{SignalSlowTool, "x"}}, review{suggest: func(string) {}})
	}
	a.Stop()
	a.finish(time.Now().Add(idleTime))
	if len(llm.reviewed) != 2 || !strings.Contains(llm.reviewed[0][1].Content, "message from the owner: a") {
		t.Fatalf("reviewed %d", len(llm.reviewed))
	}
}

// A memory of a state ends after the days the review says, at most maxDays,
// and a change of a skill is a suggestion of its own, which asks the agent to
// change the skill.
func TestReviewOfStatesAndSkills(t *testing.T) {
	reply := "REMEMBER: project issue: working on issue 77 (for 3 days)\n" +
		"REMEMBER: project release: release on friday (for 99999999999999999999 days)\n" +
		"SKILL: review: list the risks first\n"
	want := []lesson{{"project", "issue", "working on issue 77", 3}, {"project", "release", "release on friday", maxDays}}
	if got := lessons(reply); !slices.Equal(got, want) {
		t.Fatalf("lessons %+v", got)
	}
	if got := skillChange(reply); got != "In the skill review: list the risks first" {
		t.Fatalf("skill %q", got)
	}
	for _, bad := range []string{"SKILL: : x", "SKILL: review", "SKILL: two words: x", "SKILL: a/b: x", ""} {
		if got := skillChange(bad); got != "" {
			t.Errorf("skill of %q: %q", bad, got)
		}
	}
	task := SuggestedTask("In the skill review: list the risks first", []string{"x"})
	if !strings.HasPrefix(task, skillIntro) || !strings.Contains(task, ToolSkillWrite) || !tasked(task) || !tasked(SuggestedTask("x", nil)) || tasked("x") {
		t.Fatalf("task %q", task)
	}

	_, store := rawStore(t)
	llm := &learnLLM{review: reply}
	a, _ := recorded(t, ownerConfig(), llm)
	a.builder = a.builder.WithMemories(opened(store))
	skills := agent.NewTool(ToolSkill, "Load a skill. The skills:\n- review: How to review.", nil, func(context.Context, struct{}) (string, error) { return "", nil })
	tools := a.builder.Tools
	a.builder.Tools = func() []agent.Tool { return append(tools(), skills) }
	var suggested []string
	a.reviewed("c", nil, []string{"x"}, a.reviewTools(ownerConfig()), review{suggest: func(text string) { suggested = append(suggested, text) }, project: "/p", memories: []string{"style: short"}})
	if !slices.Equal(suggested, []string{"In the skill review: list the risks first"}) {
		t.Fatalf("suggested %q", suggested)
	}
	asked := llm.reviewed[0][1].Content
	if !strings.Contains(asked, "<skills>\n- review: How to review.\n</skills>") || !strings.Contains(asked, "<memories>\nstyle: short\n</memories>") {
		t.Fatalf("asked:\n%s", asked)
	}
	issue, err := store.Search(context.Background(), "/p", "project", "issue", 1)
	if err != nil || len(issue) != 1 || issue[0].Expires == "" {
		t.Fatalf("a state: %+v, %v", issue, err)
	}
	if until, _ := time.Parse(time.RFC3339, issue[0].Expires); time.Until(until) < 71*time.Hour || time.Until(until) > 73*time.Hour {
		t.Fatalf("expires %s", issue[0].Expires)
	}
}

// A tool that failed and then worked in the same answer is a signal.
func TestRecoveredIsASignal(t *testing.T) {
	var m measures
	m.step("bash", "a", time.Second, true)
	m.step("read", "b", time.Second, false)
	m.step("bash", "c", time.Second, false)
	m.step("bash", "d", time.Second, false)
	got := m.signals(time.Second, 1, 0, 0, "ok", nil)
	if len(got) != 1 || got[0] != (Signal{SignalRecovered, "a call of bash failed, a later one worked"}) {
		t.Fatalf("signals %+v", got)
	}
}
