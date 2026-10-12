package shell

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"ouroboros/internal/agent"
	"ouroboros/internal/openai"
	"ouroboros/internal/session"
)

// llmCalling is an LLM that calls bash with each of calls in turn, and then
// answers.
type llmCalling struct{ calls []map[string]any }

func (l *llmCalling) Complete(_ context.Context, _ string, msgs []openai.Message, _ []openai.Tool) (openai.Message, error) {
	asked := 0
	for _, m := range msgs {
		if m.Role == "tool" {
			asked++
		}
	}
	if asked == len(l.calls) {
		return openai.Message{Content: "done"}, nil
	}
	args, _ := json.Marshal(l.calls[asked])
	return openai.Message{ToolCalls: []openai.ToolCall{{ID: fmt.Sprint("c", asked), Type: "function", Function: openai.FunctionCall{Name: "bash", Arguments: string(args)}}}}, nil
}

// answer has an agent call bash with settings and each of calls in one
// answer, each as a script with ten seconds left would, and returns what
// they returned, an error as "error: " and what it says. What the owner is
// told of the jobs goes to w.told.
func (w *world) answer(settings string, calls ...map[string]any) []string {
	w.t.Helper()
	tool := w.tool(settings)
	var got []string
	var mu sync.Mutex
	var stop func(int) bool // the jobs of the answer, for the owner
	run := tool.Run
	tool.Run = func(ctx context.Context, args json.RawMessage) (string, error) {
		// A call {"owner stops": n} is the owner stopping the job n.
		var owner struct {
			Stops *int `json:"owner stops"`
		}
		if json.Unmarshal(args, &owner); owner.Stops != nil {
			mu.Lock()
			stopJob := stop
			mu.Unlock()
			result := fmt.Sprint("the owner stopped it: ", stopJob(*owner.Stops))
			got = append(got, result)
			return result, nil
		}
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		result, err := run(ctx, args)
		if err != nil {
			result = "error: " + err.Error()
		}
		got = append(got, result)
		return result, err
	}
	w.toldMu.Lock()
	w.told = nil
	w.toldMu.Unlock()
	ctx := session.WithJobs(context.Background(), func(running []session.Job, stopJob func(int) bool) {
		mu.Lock()
		defer mu.Unlock()
		w.toldMu.Lock()
		defer w.toldMu.Unlock()
		var lines []string
		for _, j := range running {
			if time.Since(j.Since) > time.Minute {
				w.t.Errorf("job %d runs since %s", j.N, j.Since)
			}
			lines = append(lines, fmt.Sprintf("job %d · %s", j.N, j.Command))
		}
		w.told = append(w.told, strings.Join(lines, ", "))
		stop = stopJob
	})
	if _, err := agent.New(&llmCalling{calls}, "m", "sys", tool).Respond(ctx, "c", "go"); err != nil {
		w.t.Fatal(err)
	}
	return got
}

// A command in the background runs on while the agent goes on: it shows
// what it printed since it was last asked, waits for it to end, stops it,
// and the answer ends with all it left running.
func TestBackground(t *testing.T) {
	needSandbox(t)
	w := newWorld(t)
	loop := "echo up; until [ -f stop ]; do sleep 0.1; done; echo bye"
	late := "sleep 1; echo late > late"
	got := w.answer(`{"allow": ["~/share"]}`,
		map[string]any{"command": loop, "background": true, "dir": "~/share"},
		map[string]any{"job": 1, "timeout": 1},
		map[string]any{"command": "touch stop", "dir": "~/share"},
		map[string]any{"job": 1, "timeout": 5},
		map[string]any{"job": 1},
		map[string]any{"command": "sleep 30; echo late", "background": true},
		map[string]any{"job": 2, "stop": true},
		map[string]any{"job": 3},
		map[string]any{"job": 1, "command": "true"},
		map[string]any{"stop": true},
		map[string]any{"command": "echo short; sleep 30", "background": true, "timeout": 1},
		map[string]any{"job": 3, "timeout": 5},
		map[string]any{"command": "sleep 30 # for the owner", "background": true},
		map[string]any{"owner stops": 4},
		map[string]any{"owner stops": 1},
		map[string]any{"owner stops": 9},
		map[string]any{"job": 4, "timeout": 5},
		map[string]any{"command": late, "background": true, "dir": "~/share"},
	)
	want := []string{
		`job 1 runs in the background: {"job": 1} shows what it printed since`,
		"job 1 runs, for 1s; what it printed since:\nup\n",
		"exit 0\n",
		"job 1 ended: exit 0\nbye\n",
		"job 1 ended: exit 0\n",
		"job 2 runs in the background",
		"job 2 was stopped after 0s; what it printed:\n",
		"error: there is no job 3: this answer started jobs 1 to 2",
		`error: "job" goes without "command"`,
		`error: "stop" goes with "job"`,
		"job 3 runs in the background",
		"job 3 was stopped after 1s; what it printed:\nshort\n",
		"job 4 runs in the background",
		"the owner stopped it: true",
		"the owner stopped it: false",
		"the owner stopped it: false",
		"job 4 was stopped after 0s",
		"job 5 runs in the background",
	}
	if len(got) != len(want) {
		t.Fatalf("got %q", got)
	}
	for i := range want {
		if !strings.HasPrefix(got[i], want[i]) {
			t.Errorf("call %d: %q, want %q", i+1, got[i], want[i])
		}
	}
	time.Sleep(1500 * time.Millisecond)
	if got := w.content("~/share/late"); got != "<none>" {
		t.Errorf("a job ran on after the answer: %q", got)
	}
	// The owner is told the jobs that run as they start and end: last
	// none, once the answer stopped them.
	first := "job 1 · " + loop
	w.toldMu.Lock()
	told := strings.Join(w.told, " | ")
	w.toldMu.Unlock()
	if told != first+" |  | job 2 · sleep 30; echo late |  | job 3 · echo short; sleep 30 |  | job 4 · sleep 30 # for the owner |  | job 5 · sleep 1; echo late > late | " {
		t.Errorf("told %q", told)
	}
}

// A job is had within an answer alone, and no more than MaxJobs run at
// once.
func TestJobs(t *testing.T) {
	w := newWorld(t)
	w.unsandboxed = true
	ctx := context.Background()
	if _, err := w.bash(ctx, "{}", map[string]any{"command": "true", "background": true}); err == nil || err.Error() != "bash runs a command in the background within an answer alone" {
		t.Errorf("outside of an answer: %v", err)
	}
	if _, err := w.bash(ctx, "{}", map[string]any{"job": 1}); err == nil || err.Error() != "there are no jobs outside of an answer" {
		t.Errorf("a job outside of an answer: %v", err)
	}
	calls := []map[string]any{{"job": 1}}
	for i := range MaxJobs + 1 {
		calls = append(calls, map[string]any{"command": fmt.Sprint("sleep 30 # ", i), "background": true})
	}
	start := time.Now()
	got := w.answer("{}", calls...)
	if got[0] != "error: there is no job 1: this answer started no command in the background" ||
		!strings.HasPrefix(got[MaxJobs], "job 8 runs") || got[MaxJobs+1] != "error: 8 commands run in the background already: stop one first" {
		t.Errorf("got %q", got)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("the answer waited for its jobs: %s", time.Since(start))
	}
}
