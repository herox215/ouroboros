package board

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ouroboros/internal/agent"
	"ouroboros/internal/assistant"
	store "ouroboros/internal/board"
	"ouroboros/internal/chat"
	"ouroboros/internal/session"
)

// scripted is the agent of a node whose answers do what the board asks of
// each of its sessions, with the tools the answer was given for itself: the
// orchestrator starts what is to be started and tells the thread what is
// done, and a session that works on a ticket closes it.
type scripted struct {
	t  *testing.T
	mu sync.Mutex
	// thread is what the thread was told, and roles what each session of
	// the board was, by its ID.
	thread []string
	roles  map[string]string
}

// use calls the tool name of the answer's own with args, as the session id.
func (a *scripted) use(ctx context.Context, id, name, args string) string {
	out, err := agent.New(model{name, args}, "m", "", assistant.OwnTools(ctx)...).Respond(ctx, id, "go")
	if err != nil {
		a.t.Errorf("%s: %v", name, err)
	}
	return out
}

func (a *scripted) Run(ctx context.Context, id, text string, _ func(assistant.Step), _ func(string)) (string, error) {
	role := "thread"
	switch {
	case strings.HasPrefix(text, "[Look of the board orchestrator"):
		role = "orchestrator"
	case strings.Contains(text, "of the board, refinement."):
		role = store.Refinement
	case strings.Contains(text, "of the board, implementation."):
		role = store.Implementation
	}
	a.mu.Lock()
	a.roles[id] = role
	a.mu.Unlock()
	switch role {
	case "orchestrator":
		if assistant.MessengerOf(ctx) != nil || agent.CanAskOwner(ctx) {
			a.t.Error("the orchestrator reaches the thread or the owner")
		}
		board := text[strings.Index(text, "The board now:"):]
		switch {
		case strings.Contains(board, "NEW (1)"), strings.Contains(board, "READY (1)"):
			return a.use(ctx, id, Start, `{"ticket":1}`), nil
		case strings.Contains(board, "DONE (1)"):
			return a.use(ctx, id, Tell, `{"ticket":1}`), nil
		}
		return "Nothing to do.", nil
	case store.Refinement, store.Implementation:
		if !agent.CanAskOwner(ctx) {
			a.t.Error("a session of a ticket cannot have the owner asked")
		}
		if got := a.use(ctx, id, Mine, `{}`); !strings.Contains(got, "Ticket #1: Dark mode") {
			a.t.Errorf("the %s reads %q", role, got)
		}
		return a.use(ctx, id, Close, `{"summary":"the `+role+` is done"}`), nil
	}
	a.mu.Lock()
	a.thread = append(a.thread, text)
	a.mu.Unlock()
	return "told the owner", nil
}

func (a *scripted) Ready() error                                { return nil }
func (a *scripted) Context(string) int                          { return 0 }
func (a *scripted) Cache(string) assistant.Cache                { return assistant.Cache{} }
func (a *scripted) Contents(string) (assistant.Contents, error) { return assistant.Contents{}, nil }
func (a *scripted) Inspection(string) (agent.Inspection, bool)  { return agent.Inspection{}, false }
func (a *scripted) Conversation(string) json.RawMessage         { return nil }
func (a *scripted) Resume(string, json.RawMessage) error        { return nil }
func (a *scripted) Forget(string)                               {}
func (a *scripted) Effort() string                              { return "" }
func (a *scripted) Note(context.Context, string, string) error  { return nil }
func (a *scripted) Title(context.Context, []chat.Message) (string, error) {
	return "", nil
}
func (a *scripted) CheckTopic(context.Context, assistant.TopicCheck) (assistant.TopicDecision, error) {
	return assistant.TopicDecision{}, nil
}

// With the sessions of a node, a ticket goes all its way by looks of the
// orchestrator alone: each session finds its ticket by being the session it
// is, and the thread hears of it once, when it is done.
func TestTheBoardWithTheSessionsOfANode(t *testing.T) {
	a := &scripted{t: t, roles: map[string]string{}}
	sessions := session.New(a)
	t.Cleanup(sessions.Close)
	l := &logs{}
	b := newBoard(filepath.Join(t.TempDir(), "board.json"), sessions, nil, l.log)
	b.every = time.Hour
	sessions.Apart = b.apart
	b.start()
	t.Cleanup(b.stop)
	st := b.store.Load()
	if _, err := st.Create("Dark mode", "the settings need one", "", time.Now()); err != nil {
		t.Fatal(err)
	}
	// A look to refine it, one to implement it, one to tell the thread, and
	// then there is nothing to look at.
	for range 3 {
		b.check(context.Background(), st)
		b.wg.Wait()
	}
	if changes, seen := st.Pending(); changes != seen {
		t.Fatalf("after three looks: %d changes, %d seen", changes, seen)
	}
	got := ticket(t, st, 1)
	if got.Column != store.Done || !got.Told || got.Result != "the implementation is done" || len(got.Runs) != 2 ||
		got.Runs[0].Role != store.Refinement || got.Runs[0].Outcome != store.Ready || got.Runs[1].Role != store.Implementation || got.Runs[1].Outcome != store.Done {
		t.Fatalf("the ticket: %+v", got)
	}
	looks := st.Looks()
	if len(looks) != 3 || !strings.HasPrefix(looks[0].Result, "Session ") || looks[2].Result != "The thread was told that ticket #1 is in done." {
		t.Fatalf("the looks: %+v", looks)
	}
	sessions.Close() // every answer ended, also that of the thread
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.thread) != 1 || !strings.Contains(a.thread[0], "Board ticket #1") || !strings.Contains(a.thread[0], "the implementation is done") {
		t.Fatalf("the thread was told: %q", a.thread)
	}
	for _, run := range got.Runs {
		if a.roles[run.Session] != run.Role || !b.apart(run.Session) {
			t.Fatalf("the session %s of the %s was %q", run.Session, run.Role, a.roles[run.Session])
		}
	}
	for _, look := range looks {
		if a.roles[look.Session] != "orchestrator" {
			t.Fatalf("the session %s of a look was %q", look.Session, a.roles[look.Session])
		}
	}
	if line := l.last(); line != "" {
		t.Fatalf("the log: %q", line)
	}
}
