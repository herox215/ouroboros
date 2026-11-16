// Package board is the board of a profile: tickets the thread puts on it,
// an orchestrator that looks at it when it changed, and the sessions that
// work on a ticket each.
//
// The thread creates a ticket in New rather than start a session for real
// work, see assistant.ToolTicketCreate. Every minute the node checks whether
// others than the orchestrator changed the board, and if so, the
// orchestrator looks at it once: a short session of its own with the tools
// of the orchestrator alone, which starts a session for a ticket in New, to
// refine it, and for one in Ready, to implement it, and tells the thread of
// one that is done or blocked. A session that works on a ticket has the tools
// of the owner's agent and those of its ticket, and reaches nobody but the
// ticket: it closes it, which moves it on, or blocks it with what is open.
// One that ends without either, or runs too long, leaves its ticket blocked.
package board

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ouroboros/internal/agents"
	"ouroboros/internal/assistant"
	store "ouroboros/internal/board"
	"ouroboros/internal/home"
	"ouroboros/internal/session"
	"ouroboros/plugin"
)

const (
	// every is how often the node checks whether the board changed.
	every = time.Minute
	// lookLimit is how long the orchestrator may look at the board.
	lookLimit = 10 * time.Minute
	// maxRunning is how many sessions work on tickets at once.
	maxRunning = 3
	// refineLimit and implementLimit are how long a session may work on a
	// ticket in each role, before it is stopped and the ticket blocked.
	refineLimit    = time.Hour
	implementLimit = 4 * time.Hour
)

// sessions is what the board does with the sessions of the node:
// *session.Service.
type sessions interface {
	RunWork(context.Context, session.Work) (string, <-chan session.ScheduledResult, error)
	TellWork(id, text string) bool
	WorkState(id string) string
	Cancel(context.Context, string) error
	Delete(context.Context, string) error
	Notify(context.Context, string, string) error
}

type needs struct {
	Service *session.Service
	Builder assistant.Builder
}

// Plugin is the board of the profile in dir.
func Plugin(dir home.Dir) plugin.Plugin {
	return plugin.New("board", func(ctx *plugin.Context, n needs) error {
		b := newBoard(dir.Board(), n.Service, toolNames(n.Builder), dir.Log)
		b.agents = func() []agents.Agent {
			cfg, _ := dir.Config() // without a name, the agent of the thread is unnamed
			list, err := dir.Agents().List(cfg.Name)
			if err != nil {
				b.report("the agents cannot be read, the built-in ones work on tickets: %v", err)
			}
			return list
		}
		n.Service.Apart = b.apart
		plugin.Add(ctx, plugin.Serving{Start: b.start, Stop: b.stop})
		ctx.OnStop(b.stop)
		for _, tool := range b.threadTools() {
			plugin.Add(ctx, tool)
		}
		plugin.Add(ctx, b.screen())
		return nil
	})
}

// toolNames returns the names of the tools the owner's agent can have, as b
// builds it.
func toolNames(b assistant.Builder) func() []string {
	return func() []string {
		var names []string
		for _, t := range b.Available() {
			names = append(names, t.Name)
		}
		return names
	}
}

// board is the board while the node serves. life serializes start and stop;
// assign is held while a session is started for a ticket, until the board
// knows of it, so that the session finds its ticket from its first call on.
// Neither is taken by apart, which the sessions ask with their own lock held.
type board struct {
	life     sync.Mutex
	assign   sync.Mutex
	path     string
	store    atomic.Pointer[store.Store]
	sessions sessions
	// known returns the names of the tools of the owner's agent.
	known func() []string
	// agents returns the agents of the profile as they are now; nil for a
	// board whose sessions all run as the agent of the thread.
	agents func() []agents.Agent
	log    func(string, ...any)
	now    func() time.Time
	every  time.Duration
	look   time.Duration
	max    int
	// limits are how long a session may work on a ticket, by its role.
	limits map[string]time.Duration
	root   context.Context
	cancel context.CancelFunc
	done   chan struct{}
	wg     sync.WaitGroup
	// selected is the ticket the screen of the board shows in full, by its
	// place among the tickets it lists.
	selected atomic.Int64
}

func newBoard(path string, s sessions, known func() []string, log func(string, ...any)) *board {
	return &board{path: path, sessions: s, known: known, log: log, now: time.Now, every: every, look: lookLimit, max: maxRunning,
		limits: map[string]time.Duration{store.Refinement: refineLimit, store.Implementation: implementLimit}}
}

func (b *board) report(format string, args ...any) {
	if b.log != nil {
		b.log("board: "+format, args...)
	}
}

// errClosed is what a tool of the board fails with while the board is not
// open: before the node serves, or when its file could not be read.
var errClosed = errors.New("the board is not open: see the log of the node")

// open returns the board, which is open while the node serves.
func (b *board) open() (*store.Store, error) {
	if st := b.store.Load(); st != nil {
		return st, nil
	}
	return nil, errClosed
}

// apart reports whether the session id is one of the board: it works on a
// ticket or did, or the orchestrator looked at the board in it. The thread
// is told of none of them, see session.Service.Apart.
func (b *board) apart(id string) bool {
	st := b.store.Load()
	return st != nil && st.Knows(id)
}

// start opens the board and begins to check it, as the node begins to serve.
func (b *board) start() {
	b.life.Lock()
	defer b.life.Unlock()
	if b.cancel != nil {
		return
	}
	st, err := store.Open(b.path, b.now())
	if err != nil {
		b.report("it cannot be opened, and stays closed: %v", err)
		return
	}
	b.store.Store(st)
	b.root, b.cancel = context.WithCancel(context.Background())
	b.done = make(chan struct{})
	go b.loop(b.root, st, b.done)
}

// stop ends the checks and the sessions that work on tickets, as the node
// stops: their tickets are blocked when the board is opened again.
func (b *board) stop() {
	b.life.Lock()
	defer b.life.Unlock()
	if b.cancel == nil {
		return
	}
	b.cancel()
	<-b.done
	b.wg.Wait()
	b.cancel = nil
	b.store.Store(nil)
}

// loop has the orchestrator look at the board st every b.every, until ctx
// ends.
func (b *board) loop(ctx context.Context, st *store.Store, done chan<- struct{}) {
	defer close(done)
	for {
		timer := time.NewTimer(b.every)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		b.check(ctx, st)
	}
}

// orchestratorName is the title of a session the orchestrator looks in.
const orchestratorName = "Board orchestrator"

// check has the orchestrator look at the board st once, if others changed
// it since it looked last: in a session of its own with its tools alone. A
// look that failed is tried again with the next check.
func (b *board) check(ctx context.Context, st *store.Store) {
	changes, seen := st.Pending()
	if changes == seen {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, b.look)
	defer cancel()
	tools := b.orchestratorTools(st)
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.Name
	}
	id, result, err := b.sessions.RunWork(ctx, session.Work{Name: orchestratorName, Text: orchestratorText(st.List(), seen, b.now(), b.max, b.workers()), Tools: names, Own: tools, Agent: agents.Orchestrator})
	if err != nil {
		b.report("the orchestrator cannot look: %v", err)
		return
	}
	r := <-result
	said := firstLine(r.Text)
	if r.Err != nil {
		b.report("the orchestrator failed in session %s: %v", id, r.Err)
		said, changes = "failed: "+firstLine(r.Err.Error()), seen
	}
	dropped, err := st.Looked(changes, id, said, b.now())
	if err != nil {
		b.report("the look of the orchestrator cannot be kept: %v", err)
	}
	// The board keeps its last looks to show them; the sessions of older
	// ones go, so that they do not pile up.
	for _, old := range dropped {
		if err := b.sessions.Delete(context.Background(), old); err != nil {
			b.report("the session %s of an old look cannot be deleted: %v", old, err)
		}
	}
}

// firstLine is the first line of text, and of that at most 300 bytes.
func firstLine(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	return clip(line, 300)
}

// workers are the agents a session of a ticket may run as: those of the
// profile but the reserved ones, which do only their own work. Without the
// agents of the profile, the agent of the thread alone.
func (b *board) workers() []agents.Agent {
	if b.agents == nil {
		return []agents.Agent{{ID: agents.Thread, Kind: agents.Thread}}
	}
	return slices.DeleteFunc(b.agents(), func(a agents.Agent) bool { return agents.Reserved(a.ID) })
}

// worker is the agent id among the workers, the agent of the thread for "";
// a reserved agent, or one the profile does not have, is refused with the
// IDs of those it may be.
func (b *board) worker(id string) (agents.Agent, error) {
	id = agents.Of(strings.TrimSpace(id))
	if agents.Reserved(id) {
		return agents.Agent{}, fmt.Errorf("%q is a system agent, which works on no ticket", id)
	}
	var ids []string
	for _, a := range b.workers() {
		if a.ID == id {
			return a, nil
		}
		ids = append(ids, a.ID)
	}
	return agents.Agent{}, fmt.Errorf("there is no agent %q (there are: %s)", id, strings.Join(ids, ", "))
}

// startFor starts a session for the ticket id, in the role its column asks
// for, as the agent as, "" for the agent of the thread, and returns what the
// orchestrator is told of it. At most b.max sessions work on tickets at
// once, and one at a time implements in a project directory.
func (b *board) startFor(st *store.Store, id int, as string) (string, error) {
	who, err := b.worker(as)
	if err != nil {
		return "", err
	}
	b.assign.Lock()
	defer b.assign.Unlock()
	t, err := st.Get(id)
	if err != nil {
		return "", err
	}
	role, err := t.Role()
	if err != nil {
		return "", err
	}
	running := 0
	for _, other := range st.List() {
		run, ok := other.Active()
		if !ok {
			continue
		}
		running++
		if role == store.Implementation && run.Role == role && t.Directory != "" && other.Directory == t.Directory {
			return "", fmt.Errorf("ticket %d is implemented in %s already, and one at a time is: leave ticket %d in %s, you look again when that session ended", other.ID, t.Directory, id, store.Ready)
		}
	}
	if running >= b.max {
		return "", fmt.Errorf("%d sessions work on tickets already, and no more do at once: leave ticket %d where it is, you look again when one of them ended", running, id)
	}
	var tools []string
	if b.known != nil {
		// A session of a ticket asks the owner nothing itself: what is open
		// it writes to the ticket and blocks it.
		tools = slices.DeleteFunc(b.known(), func(name string) bool { return name == assistant.ToolUserQuestion })
	}
	ctx, cancel := context.WithTimeout(b.root, b.limits[role])
	sid, result, err := b.sessions.RunWork(ctx, session.Work{
		Name: fmt.Sprintf("#%d %s (%s)", t.ID, oneLine(t.Title), role), Text: ticketText(t, role, b.now()),
		Directory: t.Directory, Ask: true, Tools: tools, Own: b.ticketTools(st),
		// The work of a ticket is done as the agent the orchestrator chose:
		// it starts it, but does not do it.
		Agent: who.ID,
	})
	if err != nil {
		cancel()
		return "", fmt.Errorf("no session started for ticket %d: %w", id, err)
	}
	if _, err := st.Start(id, sid, who.ID, who.Name, b.now()); err != nil {
		cancel()
		return "", err
	}
	b.wg.Add(1)
	go b.await(cancel, st, sid, role, result)
	return fmt.Sprintf("Session %s started for the %s of ticket %d, as %s.", short(sid), role, id, agentLabel(who)), nil
}

// await waits for the session sid, which works on a ticket of st in role,
// to end, and blocks its ticket if it still has it then: it ended without
// closing or blocking it. cancel gives back what its time limit held.
func (b *board) await(cancel context.CancelFunc, st *store.Store, sid, role string, result <-chan session.ScheduledResult) {
	defer b.wg.Done()
	defer cancel()
	r := <-result
	why := "Session " + short(sid) + " ended its " + role + " without closing or blocking the ticket."
	if errors.Is(r.Err, context.DeadlineExceeded) {
		why = fmt.Sprintf("Session %s was stopped: its %s took longer than %s.", short(sid), role, b.limits[role])
	} else if errors.Is(r.Err, context.Canceled) {
		// The orchestrator takes the ticket before it stops a session, see
		// Stop: what is left to block was stopped by another.
		why = "Session " + short(sid) + " was stopped in its " + role + ", by the owner or because the node stopped."
	} else if r.Err != nil {
		why = "Session " + short(sid) + " ended its " + role + " with an error: " + firstLine(r.Err.Error())
	} else if strings.TrimSpace(r.Text) != "" {
		why += " It said last: " + clip(oneLine(r.Text), 600)
	}
	if _, _, err := st.Release(sid, store.Code, why, b.now()); err != nil {
		b.report("the ticket of session %s cannot be blocked: %v", sid, err)
	}
}

// said says text at the ticket id of st, as by, and tells the session that
// works on it, if one does, that there is a comment. It returns the ticket,
// and whether a session was told.
func (b *board) said(st *store.Store, id int, by, text string) (store.Ticket, bool, error) {
	t, err := st.Comment(id, by, text, b.now())
	if err != nil {
		return t, false, err
	}
	run, ok := t.Active()
	if !ok {
		return t, false, nil
	}
	told := b.sessions.TellWork(run.Session, fmt.Sprintf("[Board: the %s wrote a new comment to your ticket #%d. This is generated by ouro, not a message of the owner here; take it into account now.]\n\n%s", by, id, strings.TrimSpace(text)))
	return t, told, nil
}
