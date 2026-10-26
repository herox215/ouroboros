package schedule

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ouroboros/internal/a2a"
	"ouroboros/internal/agent"
	"ouroboros/internal/openai"
	store "ouroboros/internal/schedule"
	"ouroboros/internal/session"
	"ouroboros/internal/workdir"
)

type model struct{ name, args string }

func (m model) Complete(_ context.Context, _ string, msgs []openai.Message, _ []openai.Tool) (openai.Message, error) {
	last := msgs[len(msgs)-1]
	if last.Role == "tool" {
		return openai.Message{Content: last.Content}, nil
	}
	return openai.Message{ToolCalls: []openai.ToolCall{{ID: "call", Function: openai.FunctionCall{Name: m.name, Arguments: m.args}}}}, nil
}
func call(t *testing.T, s *scheduler, ctx context.Context, name, args string) string {
	t.Helper()
	bot := agent.New(model{name, args}, "m", "", s.tools()...)
	out, err := bot.Respond(ctx, "owner-session", "manage a scheduled job")
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func saved(t *testing.T, out string) view {
	t.Helper()
	var v view
	if err := json.Unmarshal([]byte(out), &v); err != nil || v.Job.ID == "" {
		t.Fatalf("invalid saved job: %s (%v)", out, err)
	}
	return v
}

type invocation struct {
	ctx                   context.Context
	name, text, directory string
	ask                   bool
	result                chan session.ScheduledResult
}
type fakeSessions struct {
	calls         chan invocation
	notifications []string
	err           error
}

func (f *fakeSessions) Notify(_ context.Context, name, text string) error {
	f.notifications = append(f.notifications, name+"\n"+text)
	return f.err
}

func (f *fakeSessions) RunScheduled(ctx context.Context, name, text, directory string, ask bool) (string, <-chan session.ScheduledResult, error) {
	r := make(chan session.ScheduledResult, 1)
	f.calls <- invocation{ctx, name, text, directory, ask, r}
	return "scheduled-session", r, f.err
}
func receive(t *testing.T, f *fakeSessions) invocation {
	t.Helper()
	select {
	case call := <-f.calls:
		return call
	case <-time.After(4 * time.Second):
		t.Fatal("scheduled session did not start")
		return invocation{}
	}
}
func fixture(t *testing.T) (*scheduler, *fakeSessions, *atomic.Int64) {
	t.Helper()
	now := &atomic.Int64{}
	now.Store(time.Date(2026, 10, 26, 8, 0, 0, 0, time.UTC).UnixNano())
	f := &fakeSessions{calls: make(chan invocation, 20)}
	s := &scheduler{path: filepath.Join(t.TempDir(), "schedule.json"), sessions: f, now: func() time.Time { return time.Unix(0, now.Load()).UTC() }, limit: time.Hour, active: map[string]bool{}}
	t.Cleanup(s.close)
	return s, f, now
}
func create(t *testing.T, s *scheduler, cron string, delta time.Duration) store.Job {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.open(); err != nil {
		t.Fatal(err)
	}
	j := store.Job{Name: "Review", Text: "Review open PRs", Zone: "UTC", Directory: "/project", Ask: "thread", Cron: cron}
	if cron == "" {
		j.At = s.now().Add(delta).Format(time.RFC3339)
	}
	j, err := s.store.Create(j, s.now())
	if err != nil {
		t.Fatal(err)
	}
	return j
}
func TestToolsCRUDAndTrustedContext(t *testing.T) {
	t.Setenv("TZ", "Europe/Berlin")
	s, _, _ := fixture(t)
	ctx := workdir.With(context.Background(), "/project/current")
	v := saved(t, call(t, s, ctx, Update, `{"action":"create","name":"Morning","text":"Review PRs","cron":"0 9 * * 1-5"}`))
	if v.Job.Ask != "thread" || v.Job.Directory != "/project/current" || v.Job.Zone != "Europe/Berlin" || len(v.NextRuns) != 3 || !strings.Contains(v.NextRuns[0], "CET") {
		t.Fatalf("bad context or preview: %+v", v)
	}
	id := v.Job.ID
	out := call(t, s, ctx, Read, `{}`)
	if !strings.Contains(out, id) {
		t.Fatal(out)
	}
	v = saved(t, call(t, s, ctx, Update, `{"action":"update","id":"`+id+`","at":"2026-10-27T12:00","ask":"never","paused":true}`))
	if v.Job.Cron != "" || v.Job.At != "2026-10-27T12:00:00+01:00" || !v.Job.Paused || v.Job.Ask != "never" {
		t.Fatalf("bad replacement: %+v", v)
	}
	v = saved(t, call(t, s, ctx, Update, `{"action":"update","id":"`+id+`","paused":false}`))
	if v.Job.Paused {
		t.Fatal("explicit false ignored")
	}
	if err := s.store.Finish(id, store.Run{At: s.now(), Status: "completed", Result: "a result"}); err != nil {
		t.Fatal(err)
	}
	out = call(t, s, ctx, Read, `{"id":"`+id+`"}`)
	if !strings.Contains(out, "a result") {
		t.Fatal(out)
	}
	out = call(t, s, ctx, Update, `{"action":"remove","id":"`+id+`"}`)
	if !strings.Contains(out, "removed") || len(s.store.List()) != 0 {
		t.Fatal(out)
	}
}
func TestStrictInputsAndOwnerOnly(t *testing.T) {
	s, _, _ := fixture(t)
	tools := s.tools()
	if len(tools) != 2 || tools[0].Name != Read || tools[1].Name != Update {
		t.Fatal("unexpected tools")
	}
	for _, tool := range tools {
		if !tool.OwnerOnly || !json.Valid([]byte(tool.Schema)) {
			t.Fatalf("invalid tool: %+v", tool)
		}
		for _, ctx := range []context.Context{context.Background(), a2a.WithOrigin(context.Background(), a2a.Origin{}), a2a.WithOrigin(context.Background(), a2a.Origin{PublicKey: "peer"})} {
			if _, err := tool.Run(ctx, json.RawMessage(`{}`)); err == nil {
				t.Fatal("unauthorized call accepted")
			}
		}
		// Invoke the handler inside a real peer conversation, bypassing tool filtering.
		probe := agent.NewTool("probe", "", nil, func(ctx context.Context, _ struct{}) (string, error) { return tool.Run(ctx, json.RawMessage(`{}`)) })
		bot := agent.New(model{"probe", "{}"}, "m", "", probe)
		out, err := bot.Respond(a2a.WithOrigin(context.Background(), a2a.Origin{PublicKey: "peer"}), "peer-session", "hi")
		if err != nil || !strings.Contains(out, "owner-only") {
			t.Fatalf("peer accepted: %s %v", out, err)
		}
	}
	for _, raw := range []string{
		`null`, `[]`, `{} {}`, `{"action":null}`, `{"action":"create","action":"remove"}`,
		`{"action":"add"}`, `{"action":"create","name":"n","text":"t"}`,
		`{"action":"create","id":"other","name":"n","text":"t","cron":"0 9 * * *"}`,
		`{"action":"create","name":"n","text":"t","at":"2030-01-01T00:00Z","cron":"0 9 * * *"}`,
		`{"action":"create","name":"n","text":"t","cron":"* * * * *"}`,
		`{"action":"create","name":"n","text":"t","cron":"0 9 * * *","directory":"/evil"}`,
		`{"action":"create","name":"n","text":"t","cron":"0 9 * * *","zone":"UTC"}`,
		`{"action":"create","name":"n","text":"t","cron":"0 9 * * *","peer":"evil"}`,
		`{"action":"create","name":"n","text":"t","cron":"0 9 * * *","ask":"sometimes"}`,
		`{"action":"update","id":"x"}`, `{"action":"update","id":"x","text":""}`,
		`{"action":"remove","id":"x","paused":false}`, `{"action":"remove"}`,
		`{"action":"create","name":"n","text":"t","at":"yesterday"}`,
		`{"action":"create","name":"n","text":"t","at":"2020-01-01T00:00:00Z"}`,
	} {
		out := call(t, s, context.Background(), Update, raw)
		if !strings.HasPrefix(out, "error:") {
			t.Fatalf("accepted %s: %s", raw, out)
		}
	}
	for _, raw := range []string{`{"id":null}`, `{"peer":"x"}`, `{"id":"x","id":"y"}`} {
		if out := call(t, s, context.Background(), Read, raw); !strings.HasPrefix(out, "error:") {
			t.Fatalf("read accepted %s: %s", raw, out)
		}
	}
	if s.store != nil && len(s.store.List()) != 0 {
		t.Fatal("invalid call mutated store")
	}
}
func TestClaimDispatchResultAndNoOverlap(t *testing.T) {
	s, f, clock := fixture(t)
	job := create(t, s, "*/15 * * * *", 0)
	clock.Store(job.Next.Add(2 * time.Minute).UnixNano())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.tick(ctx)
	c := receive(t, f)
	current, err := s.store.Get(job.ID)
	if err != nil || !current.Next.After(s.now()) || current.Runs[0].Status != "running" {
		t.Fatalf("dispatch before durable claim: %+v %v", current, err)
	}
	if c.name != job.Name || c.directory != job.Directory || !c.ask || !strings.Contains(c.text, "not a message from the owner now") || !strings.Contains(c.text, "08:15") {
		t.Fatalf("wrong generated request: %+v", c)
	}
	clock.Store(current.Next.UnixNano())
	s.tick(ctx)
	select {
	case <-f.calls:
		t.Fatal("overlapping run started")
	default:
	}
	current, _ = s.store.Get(job.ID)
	if len(current.Runs) != 2 || current.Runs[1].Status != "skipped" {
		t.Fatalf("missing overlap record: %+v", current.Runs)
	}
	c.result <- session.ScheduledResult{Text: "First line\nSecond line"}
	s.wg.Wait()
	current, _ = s.store.Get(job.ID)
	if current.Runs[0].Status != "late" || current.Runs[0].Result != "First line" || current.Runs[0].Session != "scheduled-session" || current.Runs[1].Status != "skipped" {
		t.Fatalf("bad history: %+v", current.Runs)
	}
}
func TestMissedNotExecutedOrDuplicated(t *testing.T) {
	s, f, clock := fixture(t)
	job := create(t, s, "", time.Minute)
	clock.Store(job.Next.Add(25 * time.Hour).UnixNano())
	s.tick(context.Background())
	s.tick(context.Background())
	select {
	case <-f.calls:
		t.Fatal("missed job executed")
	default:
	}
	if len(f.notifications) != 1 || !strings.Contains(f.notifications[0], job.ID) || !strings.Contains(f.notifications[0], job.Name) || !strings.Contains(f.notifications[0], "not executed") || strings.Contains(f.notifications[0], job.Text) {
		t.Fatalf("wrong missed notification: %q", f.notifications)
	}
	current, _ := s.store.Get(job.ID)
	if len(current.Runs) != 1 || current.Runs[0].Status != "missed" || !current.Next.IsZero() {
		t.Fatalf("bad missed claim: %+v", current)
	}
}
func TestStopStartAndRestartDoNotRepeat(t *testing.T) {
	s, f, clock := fixture(t)
	job := create(t, s, "", time.Minute)
	clock.Store(job.Next.UnixNano())
	s.start()
	s.start()
	c := receive(t, f)
	s.stop()
	if c.ctx.Err() == nil {
		t.Fatal("Stop did not cancel run")
	}
	s.start()
	s.stop()
	select {
	case <-f.calls:
		t.Fatal("run repeated after Stop/Start")
	default:
	}
	// Fresh scheduler/store simulates a process restart, not only Serving restart.
	fresh := &scheduler{path: s.path, sessions: f, now: s.now, limit: time.Hour, active: map[string]bool{}}
	t.Cleanup(fresh.close)
	fresh.tick(context.Background())
	select {
	case <-f.calls:
		t.Fatal("run repeated after reopening store")
	default:
	}
	current, _ := fresh.store.Get(job.ID)
	if len(current.Runs) != 1 || current.Runs[0].Status != "failed" {
		t.Fatalf("missing canceled history: %+v", current.Runs)
	}
	s.close()
	s.start()
	if s.cancel != nil {
		t.Fatal("closed scheduler restarted")
	}
}
func TestTimerRearmedByTool(t *testing.T) {
	s, f, _ := fixture(t)
	s.now = time.Now
	s.start()
	// Starting with no jobs arms a long wait; creation and an earlier update
	// must both rearm it rather than leaving the original timer in place.
	far := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	out := call(t, s, context.Background(), Update, `{"action":"create","name":"Soon","text":"Say hello","at":"`+far+`","ask":"never"}`)
	job := saved(t, out).Job
	at := time.Now().Add(2 * time.Second).UTC().Format(time.RFC3339)
	saved(t, call(t, s, context.Background(), Update, `{"action":"update","id":"`+job.ID+`","at":"`+at+`"}`))
	c := receive(t, f)
	if c.ask {
		t.Fatal("never allowed questions")
	}
	c.result <- session.ScheduledResult{Text: "Hello"}
	s.stop()
	current, _ := s.store.Get(job.ID)
	if len(current.Runs) != 1 {
		t.Fatalf("timer did not claim once: %+v", current)
	}
}
func TestRunFailuresAndLimit(t *testing.T) {
	for _, kind := range []string{"start", "result", "timeout", "closed"} {
		t.Run(kind, func(t *testing.T) {
			s, f, clock := fixture(t)
			s.limit = 20 * time.Millisecond
			if kind == "start" {
				f.err = errors.New("could not start")
			}
			job := create(t, s, "", time.Minute)
			clock.Store(job.Next.UnixNano())
			s.tick(context.Background())
			c := receive(t, f)
			switch kind {
			case "result":
				c.result <- session.ScheduledResult{Err: errors.New("run failed")}
			case "closed":
				close(c.result)
			}
			s.wg.Wait()
			current, _ := s.store.Get(job.ID)
			if len(current.Runs) != 1 || current.Runs[0].Status != "failed" || current.Runs[0].Result == "" {
				t.Fatalf("failure not recorded: %+v", current.Runs)
			}
			if c.ctx.Err() == nil {
				t.Fatal("run context not canceled")
			}
		})
	}
}
func TestAtAndPreviewDST(t *testing.T) {
	if _, err := parseAt("2027-03-28T02:30", "Europe/Berlin"); err == nil {
		t.Fatal("nonexistent DST time accepted")
	}
	for _, at := range []string{"2026-10-26T09:00", "2026-10-26 09:00", "2026-10-26T09:00:00"} {
		got, err := parseAt(at, "Europe/Berlin")
		if err != nil || got != "2026-10-26T09:00:00+01:00" {
			t.Fatalf("local at %s: %s %v", at, got, err)
		}
	}
	next := time.Date(2027, 3, 27, 8, 0, 0, 0, time.UTC)
	v := jobView(store.Job{Cron: "0 9 * * *", Zone: "Europe/Berlin", Next: next})
	if len(v.NextRuns) != 3 || !strings.Contains(v.NextRuns[0], "09:00 CET") || !strings.Contains(v.NextRuns[1], "09:00 CEST") {
		t.Fatalf("DST preview: %+v", v)
	}
	if len(firstLine(strings.Repeat("ü", 300)+"\nmore")) > 512 {
		t.Fatal("unbounded result")
	}
}

func TestEmptyOptionalSchedulingFields(t *testing.T) {
	t.Setenv("TZ", "Europe/Berlin")
	s, _, _ := fixture(t)
	ctx := context.Background()
	v := saved(t, call(t, s, ctx, Update, `{"action":"create","id":"","name":"Test","text":"Say hello","at":"2026-10-27T09:00:00+01:00","cron":"","ask":"never","paused":false}`))
	if v.Job.Cron != "" || v.Job.At == "" {
		t.Fatal(v)
	}
	if out := call(t, s, ctx, Read, `{"id":""}`); !strings.Contains(out, v.Job.ID) {
		t.Fatal(out)
	}
}
