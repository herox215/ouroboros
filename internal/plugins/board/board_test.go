package board

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"ouroboros/internal/agent"
	"ouroboros/internal/agents"
	"ouroboros/internal/assistant"
	store "ouroboros/internal/board"
	"ouroboros/internal/openai"
	"ouroboros/internal/session"
	"ouroboros/plugin"
)

var epoch = time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)

// work is a session the board started with the fake sessions.
type work struct {
	id string
	session.Work
	ctx    context.Context
	result chan session.ScheduledResult
}

// fakeSessions are the sessions of a node as the board uses them: they
// start nothing, and keep what the board asked for.
type fakeSessions struct {
	mu                                      sync.Mutex
	started                                 chan *work
	n                                       int
	told                                    map[string][]string
	untold                                  bool // no session is there to be told
	states                                  map[string]string
	canceled, deleted, notified             []string
	runErr, cancelErr, deleteErr, notifyErr error
}

func (f *fakeSessions) RunWork(ctx context.Context, w session.Work) (string, <-chan session.ScheduledResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.runErr != nil {
		return "", nil, f.runErr
	}
	f.n++
	started := &work{id: fmt.Sprintf("%08d-session", f.n), Work: w, ctx: ctx, result: make(chan session.ScheduledResult, 1)}
	// A session ends when what started it stops it, as those of the node do.
	go func() {
		<-ctx.Done()
		select {
		case started.result <- session.ScheduledResult{Err: ctx.Err()}:
		default:
		}
	}()
	f.started <- started
	return started.id, started.result, nil
}

func (f *fakeSessions) TellWork(id, text string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.untold {
		return false
	}
	f.told[id] = append(f.told[id], text)
	return true
}

func (f *fakeSessions) WorkState(id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.states[id]
}

func (f *fakeSessions) Cancel(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.canceled = append(f.canceled, id)
	return f.cancelErr
}

func (f *fakeSessions) Delete(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deleted = append(f.deleted, id)
	return f.deleteErr
}

func (f *fakeSessions) Notify(_ context.Context, name, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.notifyErr != nil {
		return f.notifyErr
	}
	f.notified = append(f.notified, name+"\n"+text)
	return nil
}

// logs keeps what the board told the log of the node.
type logs struct {
	mu    sync.Mutex
	lines []string
}

func (l *logs) log(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

// last is the last line of the log, "" for none, and empties it.
func (l *logs) last() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	line := ""
	if len(l.lines) > 0 {
		line = l.lines[len(l.lines)-1]
	}
	l.lines = nil
	return line
}

// closed is a board that was not started, with its sessions and its log.
func closed(t *testing.T) (*board, *fakeSessions, *logs) {
	t.Helper()
	f := &fakeSessions{started: make(chan *work, 64), told: map[string][]string{}, states: map[string]string{}}
	l := &logs{}
	b := newBoard(filepath.Join(t.TempDir(), "board.json"), f, func() []string { return []string{"bash", assistant.ToolUserQuestion, "read"} }, l.log)
	b.now = func() time.Time { return epoch }
	b.every = time.Hour
	t.Cleanup(b.stop)
	return b, f, l
}

// fixture is a board as the node serves it, which checks nothing by itself.
func fixture(t *testing.T) (*board, *store.Store, *fakeSessions, *logs) {
	t.Helper()
	b, f, l := closed(t)
	b.start()
	st := b.store.Load()
	if st == nil {
		t.Fatalf("the board did not open: %q", l.lines)
	}
	return b, st, f, l
}

// model calls the tool name with args once, and answers with its result.
type model struct{ name, args string }

func (m model) Complete(_ context.Context, _ string, msgs []openai.Message, _ []openai.Tool) (openai.Message, error) {
	last := msgs[len(msgs)-1]
	if last.Role == "tool" {
		return openai.Message{Content: last.Content}, nil
	}
	return openai.Message{ToolCalls: []openai.ToolCall{{ID: "call", Function: openai.FunctionCall{Name: m.name, Arguments: m.args}}}}, nil
}

// call runs the tool name of tools with args, in an answer of the session
// sid, and returns what the tool returned.
func call(t *testing.T, tools []plugin.Tool, sid, name, args string) (string, error) {
	t.Helper()
	tools = slices.Clone(tools)
	at := slices.IndexFunc(tools, func(tool plugin.Tool) bool { return tool.Name == name })
	if at < 0 {
		t.Fatalf("there is no tool %s", name)
	}
	var out string
	err := errors.New("the tool did not run")
	run := tools[at].Run
	tools[at].Run = func(ctx context.Context, raw json.RawMessage) (string, error) {
		out, err = run(ctx, raw)
		return out, err
	}
	if _, respondErr := agent.New(model{name, args}, "m", "", tools...).Respond(context.Background(), sid, "go"); respondErr != nil {
		t.Fatal(respondErr)
	}
	return out, err
}

// ok is what a call that has to work returned.
func ok(t *testing.T) func(string, error) string {
	return func(out string, err error) string {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
}

// refused fails unless a call was refused with something that holds says.
func refused(t *testing.T, says string) func(string, error) {
	return func(out string, err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), says) {
			t.Fatalf("not refused with %q: %q, %v", says, out, err)
		}
	}
}

func receive(t *testing.T, f *fakeSessions) *work {
	t.Helper()
	select {
	case w := <-f.started:
		return w
	case <-time.After(5 * time.Second):
		t.Fatal("no session started")
		return nil
	}
}

func names(tools []plugin.Tool) []string {
	out := make([]string, len(tools))
	for i, tool := range tools {
		out[i] = tool.Name
	}
	return out
}

// look has the orchestrator look at the board once: it returns the session
// of the look, and a function that ends the look with a result and waits
// until the board took it.
func look(t *testing.T, b *board, st *store.Store, f *fakeSessions) (*work, func(session.ScheduledResult)) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		b.check(context.Background(), st)
	}()
	w := receive(t, f)
	return w, func(r session.ScheduledResult) {
		t.Helper()
		w.result <- r
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("the look did not end")
		}
	}
}

func ticket(t *testing.T, st *store.Store, id int) store.Ticket {
	t.Helper()
	got, err := st.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// The thread puts a ticket on the board, reads the board and comments on a
// ticket, with tools that are its own alone, and only while the board is open.
func TestTheThreadAndTheBoard(t *testing.T) {
	b, _, _ := closed(t)
	tools := b.threadTools()
	if got := names(tools); !slices.Equal(got, []string{assistant.ToolTicketCreate, assistant.ToolTicketComment, assistant.ToolBoardRead}) {
		t.Fatalf("the tools of the thread: %v", got)
	}
	for _, tool := range tools {
		if !tool.ThreadOnly || !tool.OwnerOnly {
			t.Errorf("%s is not for the thread of the owner alone", tool.Name)
		}
	}
	for name, args := range map[string]string{Create: `{"title":"Dark mode","text":"the settings need one"}`, Comment: `{"ticket":1,"text":"more"}`, Read: `{}`} {
		refused(t, "the board is not open")(call(t, tools, "thread", name, args))
	}
	b.start()
	st := b.store.Load()
	if out := ok(t)(call(t, tools, "thread", Read, `{}`)); out != "The board is empty." {
		t.Fatalf("an empty board: %q", out)
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	refused(t, "is not absolute")(call(t, tools, "thread", Create, `{"title":"Dark mode","text":"t","directory":"relative/project"}`))
	refused(t, "there is no directory")(call(t, tools, "thread", Create, `{"title":"Dark mode","text":"t","directory":"`+filepath.Join(dir, "none")+`"}`))
	refused(t, "there is no directory")(call(t, tools, "thread", Create, `{"title":"Dark mode","text":"t","directory":"`+file+`"}`))
	refused(t, "title must be nonempty")(call(t, tools, "thread", Create, `{"title":" ","text":"t"}`))
	refused(t, "it has no")(call(t, tools, "thread", Create, `{"title":"Dark mode","text":"t","colour":"red"}`))
	if len(st.List()) != 0 {
		t.Fatal("a refused ticket is on the board")
	}
	out := ok(t)(call(t, tools, "thread", Create, `{"title":"Dark mode","text":"the settings need one","directory":" `+dir+` "}`))
	if !strings.HasPrefix(out, "Ticket #1 is on the board, in new") || !strings.Contains(out, "one short acknowledgement") {
		t.Fatalf("created: %q", out)
	}
	if got := ticket(t, st, 1); got.Title != "Dark mode" || got.Text != "the settings need one" || got.Directory != dir || got.Column != store.New {
		t.Fatalf("the ticket: %+v", got)
	}
	ok(t)(call(t, tools, "thread", Create, `{"title":"Second","text":"another"}`))
	if out := ok(t)(call(t, tools, "thread", Read, `{}`)); !strings.Contains(out, "NEW (2)\n  #2 \"Second\"") || !strings.Contains(out, `#1 "Dark mode"`) || !strings.Contains(out, "DONE (0)") {
		t.Fatalf("the board: %s", out)
	}
	if out := ok(t)(call(t, tools, "thread", Read, `{"ticket":1}`)); !strings.Contains(out, "Ticket #1: Dark mode") || !strings.Contains(out, "the settings need one") || !strings.Contains(out, "Project directory: "+dir) {
		t.Fatalf("the ticket in full: %s", out)
	}
	refused(t, "no such ticket 9")(call(t, tools, "thread", Read, `{"ticket":9}`))
	refused(t, "no such ticket 9")(call(t, tools, "thread", Comment, `{"ticket":9,"text":"more"}`))
	refused(t, "text must be nonempty")(call(t, tools, "thread", Comment, `{"ticket":1,"text":" "}`))
	out = ok(t)(call(t, tools, "thread", Comment, `{"ticket":1,"text":"in blue, please"}`))
	if !strings.Contains(out, "The comment is at ticket #1, which is in new: the board goes on from it within a minute") {
		t.Fatalf("commented: %q", out)
	}
	if got := ticket(t, st, 1); len(got.Notes) != 1 || got.Notes[0].By != store.Thread || got.Notes[0].Text != "in blue, please" {
		t.Fatalf("the comment: %+v", got.Notes)
	}
}

// The orchestrator looks at a board that changed, in a session with its
// tools alone, and the board goes its way from there: a ticket is refined,
// implemented, blocked, answered and done, each by who it is for.
func TestTheWayOfATicket(t *testing.T) {
	b, st, f, l := fixture(t)
	b.check(context.Background(), st)
	if len(f.started) != 0 {
		t.Fatal("the orchestrator looked at a board that did not change")
	}
	dir := t.TempDir()
	if _, err := st.Create("Dark mode", "the settings need one", dir, epoch); err != nil {
		t.Fatal(err)
	}

	// The first look: the ticket is new, and a session refines it.
	orch, end := look(t, b, st, f)
	if orch.Name != orchestratorName || orch.Agent != agents.Orchestrator || orch.Ask || orch.Directory != "" || !slices.Equal(orch.Tools, names(orch.Own)) ||
		!slices.Equal(orch.Tools, []string{Read, Start, Move, Comment, Stop, Status, Tell}) {
		t.Fatalf("the look: %+v", orch.Work)
	}
	for _, part := range []string{"You are the orchestrator of the board", "At most 3 sessions work on tickets at once", "The board now:\n\nNEW (1)", "---\nChanged since you looked last.\nTicket #1: Dark mode", "the settings need one"} {
		if !strings.Contains(orch.Text, part) {
			t.Fatalf("the look is not told %q:\n%s", part, orch.Text)
		}
	}
	if out := ok(t)(call(t, orch.Own, orch.id, Read, `{"ticket":1}`)); !strings.Contains(out, "Ticket #1: Dark mode") {
		t.Fatalf("the orchestrator reads: %s", out)
	}
	if out := ok(t)(call(t, orch.Own, orch.id, Start, `{"ticket":1}`)); out != "Session 00000002 started for the refinement of ticket 1, as thread." {
		t.Fatalf("started: %q", out)
	}
	refine := receive(t, f)
	// The orchestrator starts the work of a ticket, but an agent does it:
	// the agent of the thread where it chose none.
	if refine.Name != "#1 Dark mode (refinement)" || refine.Agent != agents.Thread || !refine.Ask || refine.Directory != dir || !slices.Equal(refine.Tools, []string{"bash", "read"}) ||
		!slices.Equal(names(refine.Own), []string{Mine, Update, Comment, Close, Block}) {
		t.Fatalf("the refinement: %+v", refine.Work)
	}
	for _, part := range []string{"[Ticket #1 of the board, refinement.", "You do not implement it", "You talk to nobody", "The ticket:\n\nTicket #1: Dark mode", "the settings need one"} {
		if !strings.Contains(refine.Text, part) {
			t.Fatalf("the refinement is not told %q:\n%s", part, refine.Text)
		}
	}
	if deadline, has := refine.ctx.Deadline(); !has || time.Until(deadline) > refineLimit || time.Until(deadline) < refineLimit-time.Minute {
		t.Fatalf("the time of the refinement: %v", deadline)
	}
	end(session.ScheduledResult{Text: "Started the refinement of ticket 1.\nAnd more."})
	if looks := st.Looks(); len(looks) != 1 || looks[0].Session != orch.id || looks[0].Result != "Started the refinement of ticket 1." || looks[0].At != epoch {
		t.Fatalf("the look is kept as %+v", looks)
	}
	if changes, seen := st.Pending(); changes != seen {
		t.Fatalf("after the look: %d changes, %d seen", changes, seen)
	}
	if !b.apart(orch.id) || !b.apart(refine.id) || b.apart("stranger") {
		t.Fatal("the sessions of the board are not apart, or another is")
	}

	// The refinement fills the ticket in and closes it: it is ready.
	if out := ok(t)(call(t, refine.Own, refine.id, Mine, `{}`)); !strings.Contains(out, "refinement, works on it since just now") {
		t.Fatalf("the refinement reads its ticket: %s", out)
	}
	refused(t, "is not absolute")(call(t, refine.Own, refine.id, Update, `{"directory":"relative"}`))
	refused(t, "title must be nonempty")(call(t, refine.Own, refine.id, Update, `{"title":" "}`))
	refused(t, "nothing to change")(call(t, refine.Own, refine.id, Update, `{}`))
	if out := ok(t)(call(t, refine.Own, refine.id, Update, `{"title":"Dark mode for the settings","directory":"`+dir+`"}`)); out != "Ticket #1 is changed." {
		t.Fatalf("updated: %q", out)
	}
	refused(t, "text must be nonempty")(call(t, refine.Own, refine.id, Comment, `{"text":""}`))
	if out := ok(t)(call(t, refine.Own, refine.id, Comment, `{"text":"the colours are in theme.css"}`)); out != "The note is at ticket #1." {
		t.Fatalf("noted: %q", out)
	}
	refused(t, "summary must be nonempty")(call(t, refine.Own, refine.id, Close, `{"summary":""}`))
	refused(t, "reason must be nonempty")(call(t, refine.Own, refine.id, Block, `{"reason":""}`))
	refused(t, "this session works on no ticket")(call(t, refine.Own, "another-session", Mine, `{}`))
	if out := ok(t)(call(t, refine.Own, refine.id, Close, `{"summary":"scope settled"}`)); !strings.HasPrefix(out, "Ticket #1 is in ready, and no longer yours.") {
		t.Fatalf("closed: %q", out)
	}
	for name, args := range map[string]string{Mine: `{}`, Update: `{"title":"x"}`, Comment: `{"text":"x"}`, Close: `{"summary":"x"}`, Block: `{"reason":"x"}`} {
		refused(t, "this session works on no ticket any more")(call(t, refine.Own, refine.id, name, args))
	}
	refine.result <- session.ScheduledResult{Text: "Done."}
	b.wg.Wait()
	if got := ticket(t, st, 1); got.Column != store.Ready || got.Title != "Dark mode for the settings" || got.Notes[len(got.Notes)-3].Text != "the colours are in theme.css" {
		t.Fatalf("refined: %+v", got)
	}

	// The second look: the ticket is ready, and a session implements it,
	// which blocks it with a question; the thread is told.
	orch, end = look(t, b, st, f)
	ok(t)(call(t, orch.Own, orch.id, Start, `{"ticket":1}`))
	build := receive(t, f)
	if build.Name != "#1 Dark mode for the settings (implementation)" || !strings.Contains(build.Text, "[Ticket #1 of the board, implementation.") || !strings.Contains(build.Text, "- 00000002 as thread, refinement, ended just now: ready") {
		t.Fatalf("the implementation: %q\n%s", build.Name, build.Text)
	}
	if deadline, _ := build.ctx.Deadline(); time.Until(deadline) < implementLimit-time.Minute {
		t.Fatalf("the time of the implementation: %v", deadline)
	}
	// A comment while a session works is told to it at once.
	if out := ok(t)(call(t, b.threadTools(), "thread", Comment, `{"ticket":1,"text":"and the sidebar too"}`)); !strings.Contains(out, "which is in progress: the session that works on it was told") {
		t.Fatalf("commented: %q", out)
	}
	if out := ok(t)(call(t, orch.Own, orch.id, Comment, `{"ticket":1,"text":"mind ticket 2"}`)); out != "The comment is at ticket #1, and the session that works on it was told." {
		t.Fatalf("the orchestrator commented: %q", out)
	}
	if told := f.told[build.id]; len(told) != 2 || !strings.Contains(told[0], "the thread wrote a new comment to your ticket #1") || !strings.HasSuffix(told[0], "\n\nand the sidebar too") ||
		!strings.Contains(told[1], "the orchestrator wrote a new comment") {
		t.Fatalf("the session was told: %q", told)
	}
	f.states[build.id] = "Session 00000004: working"
	if out := ok(t)(call(t, orch.Own, orch.id, Status, `{"ticket":1}`)); out != "Session 00000004: working" {
		t.Fatalf("status: %q", out)
	}
	delete(f.states, build.id)
	if out := ok(t)(call(t, orch.Own, orch.id, Status, `{"ticket":1}`)); !strings.Contains(out, "The node no longer holds session 00000004 of ticket #1") {
		t.Fatalf("status of a session that is gone: %q", out)
	}
	refused(t, "session 00000004-session works on ticket 1: stop it first")(call(t, orch.Own, orch.id, Move, `{"ticket":1,"column":"done"}`))
	if out := ok(t)(call(t, build.Own, build.id, Block, `{"reason":"1. Which blue?"}`)); !strings.HasPrefix(out, "Ticket #1 is blocked, and no longer yours.") {
		t.Fatalf("blocked: %q", out)
	}
	build.result <- session.ScheduledResult{Text: "Blocked."}
	b.wg.Wait()
	if out := ok(t)(call(t, orch.Own, orch.id, Status, `{"ticket":1}`)); out != "No session works on ticket #1." {
		t.Fatalf("status without a session: %q", out)
	}
	if out := ok(t)(call(t, orch.Own, orch.id, Tell, `{"ticket":1,"text":" it waits for the colour "}`)); out != "The thread was told that ticket #1 is in blocked." {
		t.Fatalf("told: %q", out)
	}
	want := "Board ticket #1\nTicket #1 \"Dark mode for the settings\" of the board is in blocked.\n\nIt does not go on because:\n1. Which blue?\n\n" +
		"If this asks the owner something, ask them, and write what they answer to the ticket with ticket_comment and ticket 1: the board goes on from there. Do not hand it to a session.\n\n" +
		"The orchestrator of the board adds:\nit waits for the colour"
	if len(f.notified) != 1 || f.notified[0] != want {
		t.Fatalf("the thread was told:\n%s", f.notified)
	}
	refused(t, "the thread was told already that ticket 1 is in blocked")(call(t, orch.Own, orch.id, Tell, `{"ticket":1}`))
	end(session.ScheduledResult{Text: "Told the thread."})

	// The owner answers through the thread; the third look moves the ticket
	// back and has it implemented, and stops a session that hangs.
	f.untold = true
	if out := ok(t)(call(t, b.threadTools(), "thread", Comment, `{"ticket":1,"text":"navy"}`)); !strings.Contains(out, "which is in blocked: the board goes on from it within a minute") {
		t.Fatalf("answered: %q", out)
	}
	orch, end = look(t, b, st, f)
	if !strings.Contains(orch.Text, "Blocked because: 1. Which blue?") || !strings.Contains(orch.Text, "thread: navy") || !strings.Contains(orch.Text, "The thread was told of this.") {
		t.Fatalf("the look is not told of the answer:\n%s", orch.Text)
	}
	refused(t, `there is no column "later"`)(call(t, orch.Own, orch.id, Move, `{"ticket":1,"column":"later"}`))
	if out := ok(t)(call(t, orch.Own, orch.id, Move, `{"ticket":1,"column":"ready"}`)); out != "Ticket #1 is in ready." {
		t.Fatalf("moved: %q", out)
	}
	ok(t)(call(t, orch.Own, orch.id, Start, `{"ticket":1}`))
	hangs := receive(t, f)
	// A session that is told nothing, as it just ended, is not said to be.
	if out := ok(t)(call(t, orch.Own, orch.id, Comment, `{"ticket":1,"text":"go on"}`)); out != "The comment is at ticket #1." {
		t.Fatalf("the orchestrator commented: %q", out)
	}
	f.cancelErr = errors.New("no such session")
	refused(t, "no such session")(call(t, orch.Own, orch.id, Stop, `{"ticket":1}`))
	f.cancelErr = nil
	if got := ticket(t, st, 1); got.Column != store.Blocked || got.Reason != "The orchestrator stopped session 00000006, in its implementation." || got.From != store.Progress {
		t.Fatalf("stopped: %+v", got)
	}
	if out := ok(t)(call(t, orch.Own, orch.id, Stop, `{"ticket":1}`)); out != "No session works on ticket #1." {
		t.Fatalf("stopped twice: %q", out)
	}
	hangs.result <- session.ScheduledResult{Err: context.Canceled}
	b.wg.Wait()
	if got := ticket(t, st, 1); got.Reason != "The orchestrator stopped session 00000006, in its implementation." {
		t.Fatalf("the stopped session blocked its ticket again: %+v", got)
	}
	ok(t)(call(t, orch.Own, orch.id, Move, `{"ticket":1,"column":"ready"}`))
	ok(t)(call(t, orch.Own, orch.id, Start, `{"ticket":1}`))
	build = receive(t, f)
	if out := ok(t)(call(t, orch.Own, orch.id, Stop, `{"ticket":1}`)); out != "Session 00000007 is stopped, and ticket #1 is blocked: move it where it belongs." {
		t.Fatalf("stopped: %q", out)
	}
	if !slices.Equal(f.canceled, []string{hangs.id, build.id}) {
		t.Fatalf("stopped sessions: %v", f.canceled)
	}
	build.result <- session.ScheduledResult{Err: context.Canceled}
	b.wg.Wait()
	ok(t)(call(t, orch.Own, orch.id, Move, `{"ticket":1,"column":"ready"}`))
	ok(t)(call(t, orch.Own, orch.id, Start, `{"ticket":1}`))
	build = receive(t, f)
	end(session.ScheduledResult{Text: "Moved it back and started it."})

	// The implementation closes the ticket: it is done, and the thread is
	// told the result.
	if out := ok(t)(call(t, build.Own, build.id, Close, `{"summary":"built, and the tests pass"}`)); !strings.HasPrefix(out, "Ticket #1 is in done, and no longer yours.") {
		t.Fatalf("closed: %q", out)
	}
	build.result <- session.ScheduledResult{Text: "Done."}
	b.wg.Wait()
	orch, end = look(t, b, st, f)
	if !strings.Contains(orch.Text, "DONE (1)") || !strings.Contains(orch.Text, "Result: built, and the tests pass") {
		t.Fatalf("the look is not told that it is done:\n%s", orch.Text)
	}
	f.notifyErr = errors.New("the thread takes nothing")
	refused(t, "the thread takes nothing")(call(t, orch.Own, orch.id, Tell, `{"ticket":1}`))
	if ticket(t, st, 1).Told {
		t.Fatal("the thread was not told, and the ticket says it was")
	}
	f.notifyErr = nil
	ok(t)(call(t, orch.Own, orch.id, Tell, `{"ticket":1}`))
	want = "Board ticket #1\nTicket #1 \"Dark mode for the settings\" of the board is in done.\n\nResult, as the session that implemented it says:\nbuilt, and the tests pass\n\nTell the owner what was done, in your words."
	if f.notified[len(f.notified)-1] != want {
		t.Fatalf("the thread was told:\n%s", f.notified[len(f.notified)-1])
	}
	// A ticket that was dropped is done without a result, and one in
	// another column is told as it stands.
	if _, err := st.Create("Dropped", "never mind", "", epoch); err != nil {
		t.Fatal(err)
	}
	ok(t)(call(t, orch.Own, orch.id, Tell, `{"ticket":2}`))
	if got := f.notified[len(f.notified)-1]; got != "Board ticket #2\nTicket #2 \"Dropped\" of the board is in new." {
		t.Fatalf("the thread was told of a new ticket:\n%s", got)
	}
	ok(t)(call(t, orch.Own, orch.id, Move, `{"ticket":2,"column":"done"}`))
	ok(t)(call(t, orch.Own, orch.id, Tell, `{"ticket":2}`))
	if got := f.notified[len(f.notified)-1]; got != "Board ticket #2\nTicket #2 \"Dropped\" of the board is in done.\n\nTell the owner what was done, in your words." {
		t.Fatalf("the thread was told of a dropped ticket:\n%s", got)
	}
	for name, args := range map[string]string{Read: `{"ticket":9}`, Start: `{"ticket":9}`, Move: `{"ticket":9,"column":"done"}`, Comment: `{"ticket":9,"text":"x"}`, Stop: `{"ticket":9}`, Status: `{"ticket":9}`, Tell: `{"ticket":9}`} {
		refused(t, "no such ticket 9")(call(t, orch.Own, orch.id, name, args))
	}
	if out := ok(t)(call(t, orch.Own, orch.id, Read, `{}`)); !strings.Contains(out, "DONE (2)") {
		t.Fatalf("the orchestrator reads the board: %s", out)
	}
	end(session.ScheduledResult{Text: "Told the thread."})
	if line := l.last(); line != "" {
		t.Fatalf("the log: %q", line)
	}
	// The look after that sees a ticket that is done and told in a line
	// only, unless something happened to it since: a comment asks for more.
	if _, err := st.Comment(2, store.Thread, "one more thing", epoch); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Create("Third", "another", "", epoch); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Start(3, "refiner", "", "", epoch); err != nil {
		t.Fatal(err)
	}
	orch, end = look(t, b, st, f)
	if strings.Contains(orch.Text, "Ticket #1:") || !strings.Contains(orch.Text, "---\nChanged since you looked last.\nTicket #2: Dropped") || !strings.Contains(orch.Text, "thread: one more thing") {
		t.Fatalf("what the look is shown in full:\n%s", orch.Text)
	}
	end(session.ScheduledResult{})
	// What it saw is not marked again, but a ticket that is not done stays
	// in full.
	if _, err := st.Comment(2, store.Thread, "and another", epoch); err != nil {
		t.Fatal(err)
	}
	orch, end = look(t, b, st, f)
	if !strings.Contains(orch.Text, "---\nTicket #3: Third") || !strings.Contains(orch.Text, "---\nChanged since you looked last.\nTicket #2: Dropped") {
		t.Fatalf("what the look after is shown in full:\n%s", orch.Text)
	}
	end(session.ScheduledResult{})
}

// Only so many sessions work on tickets at once, and one at a time
// implements in a project directory.
func TestOnlySoManySessions(t *testing.T) {
	b, st, f, _ := fixture(t)
	dir, other := t.TempDir(), t.TempDir()
	for _, directory := range []string{dir, dir, other, "", ""} {
		created, err := st.Create("Ticket", "text", directory, epoch)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.Move(created.ID, store.Ready, "", epoch); err != nil {
			t.Fatal(err)
		}
	}
	tools := b.orchestratorTools(st)
	b.max = 4
	ok(t)(call(t, tools, "orchestrator", Start, `{"ticket":1}`))
	refused(t, "ticket 1 is implemented in "+dir+" already, and one at a time is: leave ticket 2 in ready")(call(t, tools, "orchestrator", Start, `{"ticket":2}`))
	ok(t)(call(t, tools, "orchestrator", Start, `{"ticket":3}`))
	// Tickets without a project are in nobody's way.
	ok(t)(call(t, tools, "orchestrator", Start, `{"ticket":4}`))
	ok(t)(call(t, tools, "orchestrator", Start, `{"ticket":5}`))
	if _, err := st.Create("One more", "text", "", epoch); err != nil {
		t.Fatal(err)
	}
	refused(t, "4 sessions work on tickets already, and no more do at once: leave ticket 6 where it is")(call(t, tools, "orchestrator", Start, `{"ticket":6}`))
	if got := ticket(t, st, 6); len(got.Runs) != 0 {
		t.Fatalf("a session beyond the limit: %+v", got.Runs)
	}
	// A refinement in the directory of an implementation is in nobody's way
	// either: it only reads.
	b.max = 6
	if _, err := st.Create("To refine", "text", dir, epoch); err != nil {
		t.Fatal(err)
	}
	ok(t)(call(t, tools, "orchestrator", Start, `{"ticket":7}`))
	refused(t, "ticket 1 is in progress")(call(t, tools, "orchestrator", Start, `{"ticket":1}`))
	for range 5 {
		receive(t, f)
	}
	// A session that cannot start leaves its ticket as it is.
	f.runErr = errors.New("the node starts anew")
	refused(t, "no session started for ticket 6: the node starts anew")(call(t, tools, "orchestrator", Start, `{"ticket":6}`))
	f.runErr = nil
	if got := ticket(t, st, 6); len(got.Runs) != 0 || got.Column != store.New {
		t.Fatalf("a session that did not start: %+v", got)
	}
	// One the board cannot take is stopped again.
	for i := range store.MaxRuns {
		sid := fmt.Sprintf("failed-%d", i)
		if _, err := st.Start(6, sid, "", "", epoch); err != nil {
			t.Fatal(err)
		}
		if _, _, err := st.Release(sid, store.Code, "failed", epoch); err != nil {
			t.Fatal(err)
		}
		if _, err := st.Move(6, store.New, "", epoch); err != nil {
			t.Fatal(err)
		}
	}
	b.max = 7
	refused(t, "12 sessions worked on ticket 6 already")(call(t, tools, "orchestrator", Start, `{"ticket":6}`))
	if w := receive(t, f); w.ctx.Err() == nil {
		t.Fatal("the session the board did not take still runs")
	}
	// Without a list of the tools of the agent, a session gets all of them.
	b.known = nil
	if _, err := st.Create("Unlimited", "text", "", epoch); err != nil {
		t.Fatal(err)
	}
	ok(t)(call(t, tools, "orchestrator", Start, `{"ticket":8}`))
	if w := receive(t, f); w.Tools != nil {
		t.Fatalf("tools without a list: %v", w.Tools)
	}
}

// A refinement only reads: it is in the way of nothing in its directory,
// and nothing there is in its way.
func TestRefinementsShareADirectory(t *testing.T) {
	b, st, _, _ := fixture(t)
	dir := t.TempDir()
	for range 3 {
		if _, err := st.Create("Ticket", "text", dir, epoch); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.Move(3, store.Ready, "", epoch); err != nil {
		t.Fatal(err)
	}
	tools := b.orchestratorTools(st)
	for _, id := range []string{"1", "2", "3"} {
		ok(t)(call(t, tools, "orchestrator", Start, `{"ticket":`+id+`}`))
	}
	if got := ticket(t, st, 3); got.Column != store.Progress {
		t.Fatalf("the implementation beside two refinements: %+v", got)
	}
}

// The limits of the board are what the manual says.
func TestLimitsStay(t *testing.T) {
	if every != 60*time.Second || lookLimit != 600*time.Second || maxRunning != 3 || refineLimit != 3600*time.Second || implementLimit != 14400*time.Second {
		t.Fatal("a limit of the board changed: the manual names them")
	}
}

// A session that ends without closing or blocking its ticket leaves it
// blocked, with how it ended.
func TestASessionThatEndsWithoutAWord(t *testing.T) {
	for name, c := range map[string]struct {
		result session.ScheduledResult
		reason string
	}{
		"it said something": {session.ScheduledResult{Text: "I looked\nat it."}, "Session 00000001 ended its refinement without closing or blocking the ticket. It said last: I looked at it."},
		"it said nothing":   {session.ScheduledResult{Text: " "}, "Session 00000001 ended its refinement without closing or blocking the ticket."},
		"it failed":         {session.ScheduledResult{Err: errors.New("the model is gone\nand more")}, "Session 00000001 ended its refinement with an error: the model is gone"},
		"it ran too long":   {session.ScheduledResult{Err: fmt.Errorf("stopped: %w", context.DeadlineExceeded)}, "Session 00000001 was stopped: its refinement took longer than 1h0m0s."},
		"it was stopped":    {session.ScheduledResult{Err: fmt.Errorf("stopped: %w", context.Canceled)}, "Session 00000001 was stopped in its refinement, by the owner or because the node stopped."},
	} {
		t.Run(name, func(t *testing.T) {
			b, st, f, _ := fixture(t)
			if _, err := st.Create("Ticket", "text", "", epoch); err != nil {
				t.Fatal(err)
			}
			before, _ := st.Pending()
			ok(t)(call(t, b.orchestratorTools(st), "orchestrator", Start, `{"ticket":1}`))
			receive(t, f).result <- c.result
			b.wg.Wait()
			got := ticket(t, st, 1)
			if got.Column != store.Blocked || got.From != store.New || got.Reason != c.reason || got.Runs[0].Outcome != "ended without a result" {
				t.Fatalf("the ticket: %+v", got)
			}
			if after, _ := st.Pending(); after != before+1 {
				t.Fatal("the orchestrator has nothing to look at")
			}
		})
	}
	// A node that stops takes its sessions with it.
	b, st, f, _ := fixture(t)
	if _, err := st.Create("Ticket", "text", "", epoch); err != nil {
		t.Fatal(err)
	}
	ok(t)(call(t, b.orchestratorTools(st), "orchestrator", Start, `{"ticket":1}`))
	w := receive(t, f)
	b.stop()
	if w.ctx.Err() == nil {
		t.Fatal("the session outlived the board")
	}
	if got := ticket(t, st, 1); got.Reason != "Session 00000001 was stopped in its refinement, by the owner or because the node stopped." {
		t.Fatalf("the ticket of a node that stopped: %+v", got)
	}
	// A ticket that cannot be blocked is in the log.
	b, st, f, l := fixture(t)
	if _, err := st.Create("Ticket", "text", "", epoch); err != nil {
		t.Fatal(err)
	}
	ok(t)(call(t, b.orchestratorTools(st), "orchestrator", Start, `{"ticket":1}`))
	w = receive(t, f)
	readOnly(t, b.path)
	refused(t, "permission denied")(call(t, b.orchestratorTools(st), "orchestrator", Stop, `{"ticket":1}`))
	refused(t, "permission denied")(call(t, b.orchestratorTools(st), "orchestrator", Tell, `{"ticket":1}`))
	w.result <- session.ScheduledResult{}
	b.wg.Wait()
	if line := l.last(); !strings.Contains(line, "board: the ticket of session 00000001-session cannot be blocked") {
		t.Fatalf("the log: %q", line)
	}
}

// readOnly makes the directory of the board at path one nothing is written
// in, until the test ends, and skips the test for a user that writes anyway.
func readOnly(t *testing.T, path string) {
	t.Helper()
	dir := filepath.Dir(path)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	if f, err := os.CreateTemp(dir, "probe"); err == nil {
		f.Close()
		t.Skip("this user writes where it may not: nothing to check")
	}
}

// A look that fails is tried again, and the board keeps its last looks
// alone: the sessions of older ones are deleted.
func TestLooksThatFail(t *testing.T) {
	b, st, f, l := fixture(t)
	if _, err := st.Create("Ticket", "text", "", epoch); err != nil {
		t.Fatal(err)
	}
	f.runErr = errors.New("the node is not set up")
	b.check(context.Background(), st)
	if line := l.last(); line != "board: the orchestrator cannot look: the node is not set up" {
		t.Fatalf("the log: %q", line)
	}
	f.runErr = nil
	if len(st.Looks()) != 0 {
		t.Fatal("a look that did not start is kept")
	}
	failed, end := look(t, b, st, f)
	end(session.ScheduledResult{Text: "half an answer", Err: errors.New("the model is gone\nand more")})
	if line := l.last(); line != "board: the orchestrator failed in session 00000001-session: the model is gone\nand more" {
		t.Fatalf("the log: %q", line)
	}
	if looks := st.Looks(); len(looks) != 1 || looks[0].Result != "failed: the model is gone" {
		t.Fatalf("the failed look: %+v", looks)
	}
	if changes, seen := st.Pending(); changes == seen {
		t.Fatal("a failed look saw the changes")
	}
	// It is tried again, and what is seen then is what there was to see
	// when the look began: a change during it is looked at next.
	_, end = look(t, b, st, f)
	if _, err := st.Comment(1, store.Thread, "meanwhile", epoch); err != nil {
		t.Fatal(err)
	}
	end(session.ScheduledResult{Text: "Nothing to do."})
	if changes, seen := st.Pending(); changes != 2 || seen != 1 {
		t.Fatalf("after a change during the look: %d changes, %d seen", changes, seen)
	}
	f.deleteErr = errors.New("it is gone already")
	for i := 2; i <= store.MaxLooks; i++ {
		_, end = look(t, b, st, f)
		end(session.ScheduledResult{Text: "Nothing to do."})
		if _, err := st.Comment(1, store.Thread, "again", epoch); err != nil {
			t.Fatal(err)
		}
		if i < store.MaxLooks && len(f.deleted) != 0 {
			t.Fatalf("look %d deleted %v", i, f.deleted)
		}
	}
	if !slices.Equal(f.deleted, []string{failed.id}) {
		t.Fatalf("deleted: %v", f.deleted)
	}
	if line := l.last(); line != "board: the session 00000001-session of an old look cannot be deleted: it is gone already" {
		t.Fatalf("the log: %q", line)
	}
	f.deleteErr = nil
	_, end = look(t, b, st, f)
	end(session.ScheduledResult{Text: "Nothing to do."})
	if len(f.deleted) != 2 || l.last() != "" {
		t.Fatalf("deleted: %v", f.deleted)
	}
	// A look that cannot be kept is in the log.
	if _, err := st.Comment(1, store.Thread, "again", epoch); err != nil {
		t.Fatal(err)
	}
	_, end = look(t, b, st, f)
	readOnly(t, b.path)
	end(session.ScheduledResult{Text: "Nothing to do."})
	if line := l.last(); !strings.Contains(line, "board: the look of the orchestrator cannot be kept") {
		t.Fatalf("the log: %q", line)
	}
}

// The board is checked while the node serves, and is closed before and
// after: a look on its way ends with the node.
func TestTheBoardWhileTheNodeServes(t *testing.T) {
	b, f, l := closed(t)
	b.every = time.Millisecond
	if b.apart("anything") {
		t.Fatal("a closed board knows a session")
	}
	b.stop() // nothing to stop yet
	b.start()
	b.start() // it runs already
	st := b.store.Load()
	if _, err := st.Create("Ticket", "text", "", epoch); err != nil {
		t.Fatal(err)
	}
	w := receive(t, f)
	if w.Name != orchestratorName {
		t.Fatalf("the board started %q by itself", w.Name)
	}
	b.stop()
	if w.ctx.Err() == nil {
		t.Fatal("the look outlived the board")
	}
	if b.store.Load() != nil {
		t.Fatal("the board is open after the node stopped")
	}
	if line := l.last(); !strings.Contains(line, "the orchestrator failed in session 00000001-session: context canceled") {
		t.Fatalf("the log: %q", line)
	}
	b.stop()
	// It opens again with what it kept.
	b.every = time.Hour
	b.start()
	if !b.apart(w.id) {
		t.Fatal("the board forgot the session of its look")
	}
	b.stop()
	// A board that cannot be read stays closed, and says so.
	if err := os.WriteFile(b.path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	b.start()
	if b.store.Load() != nil || b.cancel != nil {
		t.Fatal("a broken board opened")
	}
	if line := l.last(); !strings.Contains(line, "board: it cannot be opened, and stays closed") {
		t.Fatalf("the log: %q", line)
	}
	// A board without a log says nothing, and does not fail for it.
	b.log = nil
	b.start()
}

// The screen shows the board as its file has it, and one ticket in full,
// which its actions choose.
func TestScreen(t *testing.T) {
	b, st, _, _ := fixture(t)
	screen := b.screen()
	if screen.Command != "board" || screen.Title != "Board" || len(screen.Actions) != 2 {
		t.Fatalf("the screen: %+v", screen)
	}
	body := func() string {
		t.Helper()
		text, err := screen.Body(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return text
	}
	act := func(n int) string {
		t.Helper()
		text, err := screen.Actions[n].Run(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return text
	}
	if text := body(); !strings.HasPrefix(text, "The board is empty.") {
		t.Fatalf("an empty board: %q", text)
	}
	if text := act(0); text != "" {
		t.Fatalf("a step on an empty board: %q", text)
	}
	b.selected.Store(0)
	for _, title := range []string{"First", "Second", "Third"} {
		if _, err := st.Create(title, "the text of "+strings.ToLower(title), "", epoch); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.Move(1, store.Done, "", epoch); err != nil {
		t.Fatal(err)
	}
	// The latest of a column comes first, and it is the one shown in full.
	text := body()
	if !strings.HasPrefix(text, "NEW (2)\n> #3 \"Third\" · changed just now\n  #2 \"Second\" · changed just now\nREADY (0)\nPROGRESS (0)\nBLOCKED (0)\nDONE (1)\n  #1 \"First\"") ||
		!strings.Contains(text, "\n\nThe board changed since: the orchestrator looks within a minute.\n\nTicket #3: Third\n") || !strings.Contains(text, "the text of third") ||
		strings.Contains(text, "The orchestrator looked") {
		t.Fatalf("the board:\n%s", text)
	}
	changes, _ := st.Pending()
	if _, err := st.Looked(changes, "0000000a-session", "Nothing to do.", epoch); err != nil {
		t.Fatal(err)
	}
	if text := body(); !strings.Contains(text, "\n\nThe orchestrator looked just now, in session 0000000a: Nothing to do.\n\nTicket #3") || strings.Contains(text, "The board changed since") {
		t.Fatalf("the board after a look:\n%s", text)
	}
	for _, want := range []string{"ticket #2", "ticket #1", "ticket #3"} {
		if got := act(0); got != want {
			t.Fatalf("the next ticket: %q, want %q", got, want)
		}
	}
	if got := act(1); got != "ticket #1" {
		t.Fatalf("the ticket before the first: %q", got)
	}
	if text := body(); !strings.Contains(text, "\n> #1 \"First\"") || !strings.Contains(text, "\n\nTicket #1: First\nColumn: done") {
		t.Fatalf("the board with the last ticket chosen:\n%s", text)
	}
	// A board that cannot be read is an error of the screen.
	b.stop()
	if err := os.WriteFile(b.path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := screen.Body(context.Background()); err == nil {
		t.Fatal("a broken board was shown")
	}
	if _, err := screen.Actions[0].Run(context.Background()); err == nil {
		t.Fatal("a step on a broken board")
	}
}

// How the board is told in words.
func TestViews(t *testing.T) {
	for d, want := range map[time.Duration]string{
		0: "just now", 59 * time.Second: "just now", time.Minute: "1m ago", 59 * time.Minute: "59m ago",
		time.Hour: "1h ago", 47 * time.Hour: "47h ago", 48 * time.Hour: "2d ago", 100 * time.Hour: "4d ago",
	} {
		if got := ago(epoch.Add(-d), epoch); got != want {
			t.Errorf("%v ago: %q, want %q", d, got, want)
		}
	}
	if got := clip("abc", 3); got != "abc" {
		t.Errorf("a text that fits: %q", got)
	}
	if got := clip("abcd", 3); got != "abc […]" {
		t.Errorf("a text that does not fit: %q", got)
	}
	if got := clip("aé", 2); got != "a […]" {
		t.Errorf("a text cut within a letter: %q", got)
	}
	if got := short("0123456789"); got != "01234567" {
		t.Errorf("a long ID: %q", got)
	}
	if got := short("0123"); got != "0123" {
		t.Errorf("a short ID: %q", got)
	}
	if got := firstLine("  one\ntwo  "); got != "one" {
		t.Errorf("the first line: %q", got)
	}
	blocked := store.Ticket{
		ID: 4, Title: "Dark\nmode", Text: strings.Repeat("x", 700), Column: store.Blocked, From: store.Progress, Reason: "1. Which\nblue? " + strings.Repeat("y", 200),
		Told: true, Directory: "/srv/app", Result: "half of it", Created: epoch.Add(-3 * time.Hour), Changed: epoch.Add(-5 * time.Minute),
		Runs: []store.Run{
			{Session: "0123456789", Role: store.Refinement, Began: epoch.Add(-2 * time.Hour), Ended: epoch.Add(-time.Hour), Outcome: store.Ready},
			{Session: "abcdefghij", Role: store.Implementation, Began: epoch.Add(-30 * time.Minute)},
		},
		Notes: []store.Note{
			{At: epoch.Add(-2 * time.Hour), By: store.Thread, Text: "first"},
			{At: epoch.Add(-time.Hour), By: store.BySession("0123456789"), Text: "second"},
			{At: epoch.Add(-time.Minute), By: store.Orchestrator, Text: "third"},
		},
	}
	want := `#4 "Dark mode" · implementation by session abcdefgh since 30m ago · from progress: 1. Which blue? ` + strings.Repeat("y", 105) + ` […] · thread told · changed 5m ago`
	if got := line(blocked, epoch); got != want {
		t.Errorf("the line of a ticket:\n%s", got)
	}
	full := detail(blocked, epoch, 0)
	for _, part := range []string{
		"Ticket #4: Dark mode\nColumn: blocked (from progress)\nBlocked because: 1. Which\nblue? " + strings.Repeat("y", 200) + "\nThe thread was told of this.\nProject directory: /srv/app\nCreated 3h ago, changed 5m ago.\n\n" + strings.Repeat("x", 700) + "\n\nResult: half of it",
		"\n\nSessions:\n- 01234567, refinement, ended 1h ago: ready\n- abcdefgh, implementation, works on it since 30m ago",
		"\n\nNotes:\n- 2h ago, thread: first\n- 1h ago, session 01234567: second\n- 1m ago, orchestrator: third",
	} {
		if !strings.Contains(full, part) {
			t.Errorf("the ticket in full lacks %q:\n%s", part, full)
		}
	}
	brief := detail(blocked, epoch, 2)
	if !strings.Contains(brief, "\n\n"+strings.Repeat("x", 600)+" […]\n") || !strings.Contains(brief, "\n\nNotes, the last 2 of 3:\n- 1h ago, session 01234567: second\n- 1m ago, orchestrator: third") || strings.Contains(brief, "first") {
		t.Errorf("the ticket in brief:\n%s", brief)
	}
	if all := detail(blocked, epoch, 3); !strings.Contains(all, "\n\nNotes:\n- 2h ago, thread: first") {
		t.Errorf("a ticket with as many notes as are shown:\n%s", all)
	}
	plain := store.Ticket{ID: 5, Title: "Plain", Text: "text", Column: store.New, Created: epoch, Changed: epoch}
	if got := detail(plain, epoch, 0); got != "Ticket #5: Plain\nColumn: new\nCreated just now, changed just now.\n\ntext" {
		t.Errorf("a plain ticket:\n%s", got)
	}
	if got := line(plain, epoch); got != `#5 "Plain" · changed just now` {
		t.Errorf("the line of a plain ticket: %q", got)
	}
	var done []store.Ticket
	for i := 1; i <= 4; i++ {
		done = append(done, store.Ticket{ID: i, Title: "Done", Column: store.Done, Changed: epoch})
	}
	if got := overview(append(done, plain), epoch, 2, 5); got != "NEW (1)\n> #5 \"Plain\" · changed just now\nREADY (0)\nPROGRESS (0)\nBLOCKED (0)\nDONE (4)\n  #4 \"Done\" · changed just now\n  #3 \"Done\" · changed just now\n  … and 2 older" {
		t.Errorf("the board:\n%s", got)
	}
}

// The tools of the owner's agent are named as its builder has them.
func TestToolNames(t *testing.T) {
	quiet := func(context.Context, struct{}) (string, error) { return "", nil }
	got := toolNames(assistant.Builder{Tools: func() []agent.Tool { return []agent.Tool{agent.NewTool("bash", "", nil, quiet)} }})()
	if !slices.Contains(got, "bash") || !slices.Contains(got, assistant.ToolUserQuestion) {
		t.Fatalf("the tools: %v", got)
	}
}

// The orchestrator is told the agents a session of a ticket may run as, with
// what each is for, and starts the work of a ticket as the one it chooses:
// the board keeps which, and shows it. A system agent, or one the profile
// does not have, works on no ticket.
func TestTheOrchestratorChoosesTheAgent(t *testing.T) {
	b, st, f, _ := fixture(t)
	b.agents = func() []agents.Agent {
		return []agents.Agent{
			{ID: agents.Thread, Kind: agents.Thread, Name: "pythia", Description: "For the thread alone."},
			{ID: agents.Archivist, Kind: agents.Archivist, Name: "Archivist", Reserved: true},
			{ID: "agent-2f288108", Kind: agents.Custom, Name: "Jeff", Description: "Simple research."},
		}
	}
	if _, err := st.Create("Apples", "a short research", "", epoch); err != nil {
		t.Fatal(err)
	}
	orch, end := look(t, b, st, f)
	for _, part := range []string{
		"Choose with agent of " + Start,
		"The agents a session can run as:\n- \"pythia\" (thread): For the thread alone.\n- \"Jeff\" (agent-2f288108): Simple research.\n\nThe board now:",
	} {
		if !strings.Contains(orch.Text, part) {
			t.Fatalf("the look is not told %q:\n%s", part, orch.Text)
		}
	}
	if strings.Contains(orch.Text, "(archivist)") {
		t.Fatalf("the look offers the archivist:\n%s", orch.Text)
	}
	refused(t, `"archivist" is a system agent, which works on no ticket`)(call(t, orch.Own, orch.id, Start, `{"ticket":1,"agent":"archivist"}`))
	refused(t, `there is no agent "agent-00000000" (there are: thread, agent-2f288108)`)(call(t, orch.Own, orch.id, Start, `{"ticket":1,"agent":"agent-00000000"}`))
	if out := ok(t)(call(t, orch.Own, orch.id, Start, `{"ticket":1,"agent":"agent-2f288108"}`)); !strings.HasSuffix(out, `ticket 1, as "Jeff" (agent-2f288108).`) {
		t.Fatalf("started: %q", out)
	}
	refine := receive(t, f)
	if refine.Agent != "agent-2f288108" {
		t.Fatalf("the refinement runs as %q", refine.Agent)
	}
	got := ticket(t, st, 1)
	if run, ok := got.Active(); !ok || run.Agent != "agent-2f288108" || run.AgentName != "Jeff" {
		t.Fatalf("the run: %+v", got.Runs)
	}
	if out := ok(t)(call(t, orch.Own, orch.id, Read, `{}`)); !strings.Contains(out, `refinement by session `+short(refine.id)+` as "Jeff" since`) {
		t.Fatalf("the board: %s", out)
	}
	if out := ok(t)(call(t, orch.Own, orch.id, Read, `{"ticket":1}`)); !strings.Contains(out, "- "+short(refine.id)+` as "Jeff", refinement, works on it`) || !strings.Contains(out, `as "Jeff"`) {
		t.Fatalf("the ticket: %s", out)
	}
	end(session.ScheduledResult{Text: "Started."})
}
