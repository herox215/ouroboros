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
)

// learnLLM answers the calls of the review as its field says, and every
// other call "done", keeping the system prompt it was sent.
type learnLLM struct {
	mu       sync.Mutex
	review   string
	reviewed [][]openai.Message
	prompts  []string
}

func (l *learnLLM) Complete(_ context.Context, _ string, msgs []openai.Message, _ []openai.Tool) (openai.Message, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if msgs[0].Content == reviewPrompt {
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
func keepMemory(t *testing.T, s *memory.Store, topic, key, content string, expires time.Time) memory.Entry {
	t.Helper()
	e, _, err := s.Add(context.Background(), topic, key, content, "owner", expires)
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

// A conversation is told the topics of the memories when it starts, without
// core, and the same in its next answers, so that the prompt stays the same.
// The thread is told them too.
func TestTopicIsToldTheTopics(t *testing.T) {
	_, store := rawStore(t)
	keepMemory(t, store, "ouro", "build", "make build compiles ouro", time.Time{})
	keepMemory(t, store, "ouro", "tests", "make test runs the tests", time.Time{})
	keepMemory(t, store, "", "editor", "vim is the editor", time.Time{})
	keepMemory(t, store, memory.Core, "name", "Lu", time.Time{})
	llm := &learnLLM{}
	a, _ := recorded(t, ownerConfig(), llm)
	a.builder = a.builder.WithMemories(opened(store))
	ask := func(ctx context.Context, chat string) string {
		t.Helper()
		if _, err := a.Ask(ctx, chat, "wie baue ich das?", nil); err != nil {
			t.Fatal(err)
		}
		return llm.prompt()
	}
	first := ask(context.Background(), "c")
	want := "\n\n" + topicIntro + " ouro (2) · general (1)"
	if !strings.HasSuffix(first, want) || !strings.Contains(first, "- name: Lu") {
		t.Fatalf("the first prompt:\n%s", first)
	}
	keepMemory(t, store, "billing", "plan", "pro", time.Time{})
	if second := ask(context.Background(), "c"); second != first {
		t.Fatalf("the prompt changed:\n%s", second)
	}
	if got := ask(WithThread(context.Background(), &fakeThread{}), "thread"); !strings.Contains(got, topicIntro+" ouro (2) · ") || !strings.Contains(got, "billing (1)") {
		t.Fatalf("the thread:\n%s", got)
	}
	a.Forget("c")
	if got := ask(context.Background(), "c"); !strings.Contains(got, "billing (1)") {
		t.Fatalf("a conversation started anew:\n%s", got)
	}
}

// The topics a conversation loaded or added to, by the memory tools that
// worked, are kept for its review.
func TestLoadedTopics(t *testing.T) {
	a, _ := recorded(t, ownerConfig(), &learnLLM{})
	for _, s := range []agent.Step{
		{Tool: toolMemoryLoad, Args: []byte(`{"topic":"ouro"}`), Done: true},
		{Tool: toolMemoryAdd, Args: []byte(`{"topic":"OURO","key":"k"}`), Done: true},
		{Tool: toolMemoryAdd, Args: []byte(`{"key":"k"}`), Done: true},
		{Tool: toolMemoryLoad, Args: []byte(`{"topic":"open"}`)},
		{Tool: toolMemoryLoad, Args: []byte(`{"topic":"failed"}`), Done: true, Err: errors.New("x")},
		{Tool: "memory_forget", Args: []byte(`{"topic":"gone"}`), Done: true},
		{Tool: toolMemoryLoad, Args: []byte(`not json`), Done: true},
	} {
		a.loadedTopic("c", s)
	}
	if _, loaded := a.topicsOf("c"); !slices.Equal(loaded, []string{"ouro", memory.General}) {
		t.Fatalf("loaded %q", loaded)
	}
	if topics, loaded := a.topicsOf("none"); topics != "" || loaded != nil {
		t.Fatal("topics of no conversation")
	}
}

// Topics that cannot be read are in the log, and the conversation goes on
// without them.
func TestTopicsThatFail(t *testing.T) {
	db, store := rawStore(t)
	keepMemory(t, store, "ouro", "build", "make build", time.Time{})
	db.Close()
	llm := &learnLLM{}
	a, log := recorded(t, ownerConfig(), llm)
	a.builder = a.builder.WithMemories(opened(store))
	if _, err := a.Ask(context.Background(), "c", "hi", nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(llm.prompt(), topicIntro) {
		t.Fatal("topics that cannot be read are given")
	}
	if !slices.ContainsFunc(log.take(), func(l string) bool {
		return strings.HasPrefix(l, "chat c: the topics of the memories cannot be read: ")
	}) {
		t.Fatal("not in the log")
	}
	c, clog := recorded(t, ownerConfig(), llm)
	c.builder = c.builder.WithMemories(func() (*memory.Store, error) { return nil, errors.New("no store") })
	if _, err := c.Ask(context.Background(), "c", "hi", nil); err != nil {
		t.Fatal(err)
	}
	if lines := clog.take(); !slices.Contains(lines, "chat c: the topics of the memories cannot be read: no store") {
		t.Fatalf("log:\n%s", strings.Join(lines, "\n"))
	}
}

// A topic the owner left is looked back on once, with the messages of the
// owner in it, when it had enough answers; one with fewer and no signals is
// let go of. A topic that is not left yet is kept.
func TestFinishedTopicIsReviewed(t *testing.T) {
	_, store := rawStore(t)
	llm := &learnLLM{review: "REMEMBER: general | style: short answers."}
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
// on and let go of; after Stop no wait for it starts.
func TestSweepWhenTheOwnerLeft(t *testing.T) {
	_, store := rawStore(t)
	a, _ := recorded(t, ownerConfig(), &learnLLM{})
	a.builder = a.builder.WithMemories(opened(store))
	a.idleAfter = 10 * time.Millisecond
	ctx := WithReview(context.Background(), func(string) {})
	for range 2 {
		if _, err := a.Ask(ctx, "c", "hi", nil); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, "let go of the topics", func() bool {
		a.learning.Lock()
		defer a.learning.Unlock()
		return len(a.topics) == 0
	})
	b, _ := recorded(t, ownerConfig(), &learnLLM{})
	b.Stop()
	b.track("c", "hi", nil, review{})
	if b.idle != nil {
		t.Fatal("waits after Stop")
	}
}

// Of many topics a conversation is told as many as fit, never core.
func TestTopicMapIsBounded(t *testing.T) {
	topics := []memory.Topic{{Name: "core", Count: 2}}
	for i := range 100 {
		topics = append(topics, memory.Topic{Name: strings.Repeat(string(rune('a'+i%26)), 30), Count: i})
	}
	got := listing(topics)
	if len(got) > topicMapBytes || !strings.HasPrefix(got, strings.Repeat("a", 30)+" (0) · bbb") || strings.Contains(got, "core") {
		t.Fatalf("map of %d bytes: %.80s", len(got), got)
	}
	if listing(nil) != "" || listing([]memory.Topic{{Name: "Core", Count: 1}}) != "" {
		t.Fatal("a list of nothing")
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
	reply := "REMEMBER: ouro | issue: working on issue 77 (for 3 days)\n" +
		"REMEMBER: ouro | release: release on friday (for 99999999999999999999 days)\n" +
		"SKILL: review: list the risks first\n"
	want := []lesson{{"ouro", "issue", "working on issue 77", 3}, {"ouro", "release", "release on friday", maxDays}}
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
	a.reviewed("c", nil, []string{"x"}, a.reviewTools(ownerConfig()), review{suggest: func(text string) { suggested = append(suggested, text) },
		memories: []string{"style: short"}, topics: "ouro (3) · general (1)", loaded: []string{"ouro"}})
	if !slices.Equal(suggested, []string{"In the skill review: list the risks first"}) {
		t.Fatalf("suggested %q", suggested)
	}
	asked := llm.reviewed[0][1].Content
	if !strings.Contains(asked, "<skills>\n- review: How to review.\n</skills>") || !strings.Contains(asked, "<memories>\nstyle: short\n</memories>") ||
		!strings.Contains(asked, "<topics>\ntopics: ouro (3) · general (1)\nloaded in this session: ouro\n</topics>") {
		t.Fatalf("asked:\n%s", asked)
	}
	issue, err := store.Load(context.Background(), "ouro", "issue", 1)
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
