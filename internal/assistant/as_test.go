package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ouroboros/internal/agent"
	"ouroboros/internal/agents"
	"ouroboros/internal/config"
)

// An answer runs as the agent of the profile its session runs as: the prompt
// of the archivist and of the orchestrator names them, by the name the owner
// gave them, beside the agent of the thread, and goes on as that of the
// owner's agent. The agent of the thread, and an answer that names none, are
// the owner's agent itself.
func TestAnswerRunsAsItsAgent(t *testing.T) {
	cfg, b := directNode(t)
	store := agents.Open(filepath.Join(t.TempDir(), "agents.json"))
	a := New(func() (config.Config, error) { return cfg, nil }, b.WithAgents(store), discard)
	owner := `You are "me", the personal agent of the owner of this Ouroboros node. `
	system := func(ctx context.Context) string {
		t.Helper()
		got, err := a.Ask(ctx, "chat", "?system", nil)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	plain := system(context.Background())
	if !strings.HasPrefix(plain, owner) {
		t.Fatalf("the owner's agent: %q", plain)
	}
	rest := strings.TrimPrefix(plain, owner)
	if got := system(WithAgent(context.Background(), agents.Thread)); got != plain {
		t.Fatalf("the agent of the thread: %q", got)
	}
	if got := system(WithAgent(context.Background(), agents.Archivist)); got != `You are "Archivist", an agent of the owner of this Ouroboros node, beside "me", their personal agent, who talks with them. `+rest {
		t.Fatalf("the archivist: %q", got)
	}
	if err := store.Set(agents.Orchestrator, agents.Agent{Name: "Dispatch"}, cfg.Name); err != nil {
		t.Fatal(err)
	}
	if got := system(WithAgent(context.Background(), agents.Orchestrator)); !strings.HasPrefix(got, `You are "Dispatch", an agent of the owner`) || !strings.HasSuffix(got, rest) {
		t.Fatalf("the renamed orchestrator: %q", got)
	}
	// The thread has a prompt of its own, whatever the answer names.
	thread := system(WithThread(context.Background(), &fakeThread{}))
	if got := system(WithAgent(WithThread(context.Background(), &fakeThread{}), agents.Archivist)); got != thread {
		t.Fatalf("the thread as the archivist: %q", got)
	}
	// An agent the profile does not have leaves the prompt as it is.
	if got := system(WithAgent(context.Background(), "helper")); got != plain {
		t.Fatalf("no agent: %q", got)
	}
}

func TestAgentOf(t *testing.T) {
	if got := AgentOf(context.Background()); got != agents.Thread {
		t.Fatalf("no agent: %q", got)
	}
	if got := AgentOf(WithAgent(context.Background(), agents.Orchestrator)); got != agents.Orchestrator {
		t.Fatalf("the orchestrator: %q", got)
	}
}

// threadHelper is the helper of the agent of the thread b builds for cfg
// with tools, given its task by the agent of the thread.
func threadHelper(b Builder, cfg config.Config, tools []agent.Tool) (*agent.Agent, error) {
	return b.helper(cfg, tools, b.agentFor(agents.Thread, cfg, nil), agents.Thread)
}

// Every agent answers with the soul the owner gave it, at the end of its
// prompt: the agent of the thread in the thread and in its sessions, the
// others beside what they are for. An agent the owner made answers as itself,
// and once removed, as the agent of the thread.
func TestAnswerHasTheSoulOfItsAgent(t *testing.T) {
	cfg, b := directNode(t)
	store := agents.Open(filepath.Join(t.TempDir(), "agents.json"))
	var logged []string
	a := New(func() (config.Config, error) { return cfg, nil }, b.WithAgents(store), func(format string, args ...any) {
		logged = append(logged, fmt.Sprintf(format, args...))
	})
	system := func(ctx context.Context) string {
		t.Helper()
		got, err := a.Ask(ctx, "chat", "?system", nil)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	plain, thread := system(context.Background()), system(WithThread(context.Background(), &fakeThread{}))
	if strings.Contains(plain, "<soul>") || strings.Contains(thread, "<soul>") {
		t.Fatalf("a soul without one: %q", plain)
	}
	if err := store.Set(agents.Thread, agents.Agent{Soul: "Calm and dry."}, cfg.Name); err != nil {
		t.Fatal(err)
	}
	soul := fmt.Sprintf(soulPrompt, "Calm and dry.")
	if got := system(context.Background()); !strings.HasSuffix(got, soul) || !strings.HasPrefix(got, `You are "me", the personal agent`) {
		t.Fatalf("a session of the agent of the thread: %q", got)
	}
	if got := system(WithThread(context.Background(), &fakeThread{})); !strings.Contains(got, soul) || !strings.HasPrefix(got, thread[:40]) {
		t.Fatalf("the thread: %q", got)
	}

	if err := store.Set(agents.Archivist, agents.Agent{Name: "Mnemosyne", Soul: "Neat."}, cfg.Name); err != nil {
		t.Fatal(err)
	}
	got := system(WithAgent(context.Background(), agents.Archivist))
	if !strings.HasPrefix(got, `You are "Mnemosyne", an agent of the owner of this Ouroboros node, beside "me", their personal agent, who talks with them. When testing`) ||
		!strings.HasSuffix(got, fmt.Sprintf(soulPrompt, "Neat.")) || strings.Contains(got, "Calm and dry.") {
		t.Fatalf("the archivist: %q", got)
	}

	made, err := store.Create(agents.Agent{Name: "Researcher", Description: "Digs into sources.", Soul: "Thorough."}, cfg.Name)
	if err != nil {
		t.Fatal(err)
	}
	if got := system(WithAgent(context.Background(), made.ID)); !strings.HasPrefix(got, `You are "Researcher", an agent of the owner of this Ouroboros node, beside "me", their personal agent, who talks with them. What you are for, in the words of the owner: Digs into sources. `) || !strings.HasSuffix(got, fmt.Sprintf(soulPrompt, "Thorough.")) {
		t.Fatalf("an agent of the owner: %q", got)
	}
	if err := store.Delete(made.ID); err != nil {
		t.Fatal(err)
	}
	if got := system(WithAgent(context.Background(), made.ID)); !strings.HasPrefix(got, `You are "me", the personal agent`) || !strings.HasSuffix(got, soul) {
		t.Fatalf("a removed agent: %q", got)
	}
	logged = slices.DeleteFunc(logged, func(line string) bool { return !strings.Contains(line, "cannot be read") })
	if len(logged) != 1 || !strings.Contains(logged[0], "chat chat: the agent "+made.ID+" cannot be read") {
		t.Fatalf("logged %q", logged)
	}
}

// delegate names the agents of the profile as they are now, the one that
// calls it among them, and gives a task to the agent it names: the helper is
// that agent, with its soul, helping the one that called it. The archivist
// and the orchestrator are neither named nor handed a task.
func TestDelegateToAnAgent(t *testing.T) {
	cfg, b := directNode(t)
	store := agents.Open(filepath.Join(t.TempDir(), "agents.json"))
	b = b.WithAgents(store)
	made, err := store.Create(agents.Agent{Name: "Researcher", Description: "Digs into sources.", Soul: "Thorough."}, cfg.Name)
	if err != nil {
		t.Fatal(err)
	}
	bot := agent.New(nil, "m", "prompt", delegateTool(), agent.Tool{Name: "other"})
	described := func(bot *agent.Agent) string {
		_, specs := bot.Prompt()
		return specs[0].Function.Description
	}
	got := described(b.delegating(bot, made, cfg))
	if !strings.HasPrefix(got, delegateTool().Description) ||
		!strings.Contains(got, "\n- thread (\"me\"): The owner's personal agent") ||
		!strings.Contains(got, "\n- "+made.ID+" (\"Researcher\", you): Digs into sources.") ||
		strings.Contains(got, "archivist") || strings.Contains(got, "orchestrator") {
		t.Fatalf("delegate: %q", got)
	}
	// What the owner wrote of the agent of the thread is what the others read.
	if err := store.Set(agents.Thread, agents.Agent{Description: "For the thread alone, never for a task."}, cfg.Name); err != nil {
		t.Fatal(err)
	}
	if got := described(b.delegating(bot, made, cfg)); !strings.Contains(got, "\n- thread (\"me\"): For the thread alone, never for a task.") {
		t.Fatalf("delegate after the owner wrote of the thread: %q", got)
	}
	if plain := agent.New(nil, "m", "prompt", agent.Tool{Name: "other"}); b.delegating(plain, made, cfg) != plain {
		t.Fatal("an agent without delegate was changed")
	}

	cfg.Tools = &config.Tools{H2A: config.Named("*")}
	a := New(func() (config.Config, error) { return cfg, nil }, b, discard)
	ask := func(ctx context.Context, task map[string]string) string {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"tasks": []any{task}})
		got, err := a.Ask(ctx, "chat", "!delegate "+string(raw), nil)
		var answers []delegated
		if err := json.Unmarshal([]byte(result(t, got, err)), &answers); err != nil || len(answers) != 1 {
			t.Fatalf("got %s, %v", got, err)
		}
		return answers[0].Answer + answers[0].Error
	}
	got = ask(context.Background(), map[string]string{"task": "?system", "agent": made.ID})
	if !strings.HasPrefix(got, `You are "Researcher", an agent of the owner of this Ouroboros node, and you are helping "me" with one task it gave you. What you are for, in the words of the owner: Digs into sources. Do it`) ||
		!strings.HasSuffix(got, fmt.Sprintf(soulPrompt, "Thorough.")) {
		t.Fatalf("the helper as the researcher: %q", got)
	}
	// Without one the helper is the agent that calls delegate.
	got = ask(WithAgent(context.Background(), made.ID), map[string]string{"task": "?system"})
	if !strings.HasPrefix(got, `You are "Researcher", an agent of the owner of this Ouroboros node, and you are helping another instance of yourself`) {
		t.Fatalf("the helper of the researcher: %q", got)
	}
	got = ask(WithAgent(context.Background(), made.ID), map[string]string{"task": "?system", "agent": agents.Thread})
	if !strings.HasPrefix(got, `You are "me", an agent of the owner of this Ouroboros node, and you are helping "Researcher" with one task`) {
		t.Fatalf("the agent of the thread helping the researcher: %q", got)
	}
	for _, reserved := range []string{agents.Archivist, agents.Orchestrator} {
		got, err := a.Ask(context.Background(), "chat", `!delegate {"tasks":[{"task":"?system","agent":"`+reserved+`"}]}`, nil)
		if err != nil || !strings.Contains(got, "is a system agent, which is handed no task") {
			t.Fatalf("a task for the %s: %q, %v", reserved, got, err)
		}
	}
	got, err = a.Ask(context.Background(), "chat", `!delegate {"tasks":[{"task":"?system","agent":"agent-00000000"}]}`, nil)
	if err != nil || !strings.Contains(got, `there is no agent \"agent-00000000\"`) && !strings.Contains(got, `there is no agent "agent-00000000"`) {
		t.Fatalf("an agent the profile has not: %q, %v", got, err)
	}
}

// An agent with a model of its own answers with it, in its sessions and as a
// helper, through its access; one whose access cannot be reached answers with
// the model of the settings, and says so. An agent without one answers with
// the default of the settings for its scope.
func TestAgentAnswersWithItsModel(t *testing.T) {
	cfg, b := directNode(t)
	store := agents.Open(filepath.Join(t.TempDir(), "agents.json"))
	var logged []string
	llm := b.LLM
	b.LLM = func(access string) (agent.LLM, error) {
		if access == "gone" {
			return nil, errTest
		}
		return llm(access)
	}
	cfg.Tools = &config.Tools{H2A: config.Named("*")}
	a := New(func() (config.Config, error) { return cfg, nil }, b.WithAgents(store), func(format string, args ...any) {
		logged = append(logged, fmt.Sprintf(format, args...))
	})
	made, err := store.Create(agents.Agent{Name: "Researcher", Access: cfg.Access, Model: "o3-mini"}, cfg.Name)
	if err != nil {
		t.Fatal(err)
	}
	ask := func(ctx context.Context, text string) string {
		t.Helper()
		got, err := a.Ask(ctx, "chat", text, nil)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := ask(WithAgent(context.Background(), made.ID), "hi"); !strings.HasPrefix(got, "[o3-mini] ") {
		t.Fatalf("the researcher answered %q", got)
	}
	if got := ask(context.Background(), "hi"); !strings.HasPrefix(got, "[gpt-4o] ") {
		t.Fatalf("the agent of the thread answered %q", got)
	}
	if a.Model(false, made.ID) != "o3-mini" || a.Model(false, agents.Thread) != "gpt-4o" || a.Model(true, made.ID) != "gpt-4o" {
		t.Fatalf("the models: %q %q %q", a.Model(false, made.ID), a.Model(false, agents.Thread), a.Model(true, made.ID))
	}
	// A helper that is the researcher answers with its model.
	got := ask(context.Background(), `!delegate {"tasks":[{"task":"hi","agent":"`+made.ID+`"}]}`)
	if !strings.Contains(got, `[o3-mini] `) {
		t.Fatalf("the helper answered %q", got)
	}
	if err := store.Set(made.ID, agents.Agent{Name: "Researcher", Access: "gone", Model: "o3-mini"}, cfg.Name); err != nil {
		t.Fatal(err)
	}
	if got := ask(WithAgent(context.Background(), made.ID), "hi"); !strings.HasPrefix(got, "[gpt-4o] ") {
		t.Fatalf("the researcher without its access answered %q", got)
	}
	if !slices.ContainsFunc(logged, func(line string) bool {
		return strings.Contains(line, "the access gone of the agent "+made.ID+" cannot be reached")
	}) {
		t.Fatalf("logged %q", logged)
	}
}

// The agent of the thread with a model of its own answers with it wherever it
// works: in the thread, over the default of the thread, and in the sessions
// that run as it, over the default of the sessions.
func TestAgentOfTheThreadAnswersWithItsModel(t *testing.T) {
	cfg, b := directNode(t)
	cfg.ThreadAccess, cfg.ThreadModel = cfg.Access, "gpt-4.1"
	store := agents.Open(filepath.Join(t.TempDir(), "agents.json"))
	a := New(func() (config.Config, error) { return cfg, nil }, b.WithAgents(store), discard)
	ask := func(ctx context.Context, text string) string {
		t.Helper()
		got, err := a.Ask(ctx, "chat", text, nil)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	thread := WithThread(context.Background(), &fakeThread{})
	if got := ask(thread, "hi"); !strings.HasPrefix(got, "[gpt-4.1] ") {
		t.Fatalf("the thread by default answered %q", got)
	}
	if a.Model(true, "") != "gpt-4.1" || a.Model(false, agents.Thread) != "gpt-4o" {
		t.Fatalf("the defaults: %q %q", a.Model(true, ""), a.Model(false, agents.Thread))
	}
	if err := store.Set(agents.Thread, agents.Agent{Access: cfg.Access, Model: "o3-mini"}, cfg.Name); err != nil {
		t.Fatal(err)
	}
	if got := ask(thread, "hi"); !strings.HasPrefix(got, "[o3-mini] ") {
		t.Fatalf("the thread answered %q", got)
	}
	if got := ask(context.Background(), "hi"); !strings.HasPrefix(got, "[o3-mini] ") {
		t.Fatalf("a session as the agent of the thread answered %q", got)
	}
	if a.Model(true, "agent-00000000") != "o3-mini" || a.Model(false, agents.Thread) != "o3-mini" {
		t.Fatalf("the models: %q %q", a.Model(true, ""), a.Model(false, agents.Thread))
	}
}

// The thread starts a session as the agent it chooses: session_new names the
// agents with what each is for, the agent of the thread as itself, and
// refuses a system agent or one the profile does not have before a session
// starts.
func TestThreadStartsASessionAsAnAgent(t *testing.T) {
	cfg, b := directNode(t)
	store := agents.Open(filepath.Join(t.TempDir(), "agents.json"))
	made, err := store.Create(agents.Agent{Name: "Jeff", Description: "Simple research."}, cfg.Name)
	if err != nil {
		t.Fatal(err)
	}
	a := New(func() (config.Config, error) { return cfg, nil }, b.WithAgents(store), discard)
	defer a.Stop()
	thread := &fakeThread{}
	ctx := WithThread(context.Background(), thread)
	bot := b.WithAgents(store).delegating(agent.New(nil, "m", "prompt", threadTools("3 hours")...), b.WithAgents(store).agentFor(agents.Thread, cfg, nil), cfg)
	_, specs := bot.Prompt()
	var described string
	for _, spec := range specs {
		if spec.Function.Name == ToolSessionNew {
			described = spec.Function.Description
		}
	}
	if !strings.Contains(described, "Each session is done as an agent of the owner") ||
		!strings.Contains(described, "\n- thread (\"me\", you): ") || !strings.Contains(described, "\n- "+made.ID+" (\"Jeff\"): Simple research.") ||
		strings.Contains(described, "archivist") {
		t.Fatalf("session_new: %q", described)
	}
	for agentID, why := range map[string]string{agents.Orchestrator: "is a system agent", "agent-00000000": `there is no agent "agent-00000000"`} {
		got, err := a.Ask(ctx, "thread", `!session_new {"text":"go","agent":"`+agentID+`"}`, nil)
		if got := result(t, got, err); !strings.Contains(got, why) {
			t.Fatalf("a session as %s: %s", agentID, got)
		}
	}
	if len(thread.started) != 0 {
		t.Fatalf("started %q", thread.started)
	}
	for _, call := range []string{`!session_new {"text":"apples","agent":"` + made.ID + `"}`, `!session_new {"text":"pears"}`} {
		if _, err := a.Ask(ctx, "thread", call, nil); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Join(thread.started, "|") != " apples as "+made.ID+"| pears" {
		t.Fatalf("started %q", thread.started)
	}
}
