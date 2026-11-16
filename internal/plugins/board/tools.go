package board

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"ouroboros/internal/agent"
	"ouroboros/internal/assistant"
	store "ouroboros/internal/board"
	"ouroboros/plugin"
)

// The tools of the board. The thread has three, which the plugin
// contributes for it alone. The others are no tools of the owner's agent:
// the orchestrator gets its own with every look, and a session that works on
// a ticket those of its ticket, see session.Work, so no other answer has them.
const (
	// The tools of the thread, see assistant.ToolTicketCreate.
	Create  = assistant.ToolTicketCreate
	Comment = assistant.ToolTicketComment
	Read    = assistant.ToolBoardRead
	// The tools of the orchestrator, besides Read and Comment.
	Start  = "ticket_start"
	Move   = "ticket_move"
	Stop   = "ticket_stop"
	Status = "ticket_status"
	Tell   = "thread_tell"
	// The tools of a session that works on a ticket, besides Comment.
	Mine   = "ticket_read"
	Update = "ticket_update"
	Close  = "ticket_close"
	Block  = "ticket_block"
)

// tool is a tool of the board that takes its arguments as T.
func tool[T any](name, description, schema string, run func(ctx context.Context, args T) (string, error)) plugin.Tool {
	return plugin.Tool{Name: name, Description: description, Schema: schema, OwnerOnly: true,
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var args T
			if err := plugin.Decode(name, raw, &args); err != nil {
				return "", err
			}
			return run(ctx, args)
		}}
}

// project is directory as the project directory of a ticket: absolute, and
// there. None is fine: the refinement finds it.
func project(directory string) (string, error) {
	directory = strings.TrimSpace(directory)
	if directory == "" {
		return "", nil
	}
	if !filepath.IsAbs(directory) {
		return "", fmt.Errorf("the directory %q is not absolute: give the whole path, or leave it out", directory)
	}
	if info, err := os.Stat(directory); err != nil || !info.IsDir() {
		return "", fmt.Errorf("there is no directory %q: give the project directory as it is, or leave it out", directory)
	}
	return directory, nil
}

// The arguments of the tools.
type (
	creating struct {
		Title     string `json:"title"`
		Text      string `json:"text"`
		Directory string `json:"directory"`
	}
	commenting struct {
		Ticket int    `json:"ticket"`
		Text   string `json:"text"`
	}
	reading struct {
		Ticket int `json:"ticket"`
	}
	starting struct {
		Ticket int    `json:"ticket"`
		Agent  string `json:"agent"`
	}
	moving struct {
		Ticket int    `json:"ticket"`
		Column string `json:"column"`
		Reason string `json:"reason"`
	}
	updating struct {
		Title     *string `json:"title"`
		Text      *string `json:"text"`
		Directory *string `json:"directory"`
	}
	noting struct {
		Text string `json:"text"`
	}
	closing struct {
		Summary string `json:"summary"`
	}
	blocking struct {
		Reason string `json:"reason"`
	}
)

const ticketParam = `"ticket":{"type":"integer","description":"the number of the ticket"}`

// read is the board, or with id the ticket of that number in full.
func (b *board) read(st *store.Store, id int) (string, error) {
	if id == 0 {
		tickets := st.List()
		if len(tickets) == 0 {
			return "The board is empty.", nil
		}
		return overview(tickets, b.now(), 10, 0), nil
	}
	t, err := st.Get(id)
	if err != nil {
		return "", err
	}
	return detail(t, b.now(), 0), nil
}

const readAbout = "Read the board: its tickets by column, new, ready, progress, blocked and done, a line each with what holds for it now. " +
	"With ticket, read that ticket in full instead: its text, why it is blocked, its result, the sessions that worked on it and its notes."

const readSchema = `{"type":"object","properties":{` + ticketParam + `},"required":[]}`

// threadTools are the tools of the board for the thread, and for it alone.
func (b *board) threadTools() []plugin.Tool {
	tools := []plugin.Tool{
		tool(Create, "Put a ticket on the board, in new, for work that is more than a quick answer: it is refined by a session of its own, which asks what only the owner can answer, and then implemented by another. "+
			"Both see the ticket alone, so write it to stand for itself: what the owner wants, in their words and language, with what of this conversation is needed to understand it. "+
			"You are told when it is done or needs the owner. Returns the number of the ticket.",
			`{"type":"object","properties":{`+
				`"title":{"type":"string","description":"what it is about, in a few words"},`+
				`"text":{"type":"string","description":"what is to be done, as the owner asked for it, self-contained"},`+
				`"directory":{"type":"string","description":"the absolute project directory it is to be done in, where you know it; leave it out otherwise"}`+
				`},"required":["title","text"]}`,
			func(ctx context.Context, a creating) (string, error) {
				st, err := b.open()
				if err != nil {
					return "", err
				}
				directory, err := project(a.Directory)
				if err != nil {
					return "", err
				}
				t, err := st.Create(a.Title, a.Text, directory, b.now())
				if err != nil {
					return "", err
				}
				// The board does the work: the thread acknowledges at most.
				agent.Handed(ctx)
				return fmt.Sprintf("Ticket #%d is on the board, in new: it is refined first and then implemented, and you are told when it is done or needs the owner. Say one short acknowledgement of receipt to the owner now, in their language, and end your answer.", t.ID), nil
			}),
		tool(Comment, "Write a comment to a ticket of the board: what the owner answers to what a blocked ticket asks, what they add to a ticket or correct in it, or that they want it dropped. "+
			"Write it in their words and language, self-contained. A session that works on the ticket is told at once; otherwise the board goes on from the comment within a minute.",
			`{"type":"object","properties":{`+ticketParam+`,`+
				`"text":{"type":"string","description":"the comment"}`+
				`},"required":["ticket","text"]}`,
			func(ctx context.Context, a commenting) (string, error) {
				st, err := b.open()
				if err != nil {
					return "", err
				}
				t, told, err := b.said(st, a.Ticket, store.Thread, a.Text)
				if err != nil {
					return "", err
				}
				agent.Handed(ctx)
				how := "the board goes on from it within a minute"
				if told {
					how = "the session that works on it was told"
				}
				return fmt.Sprintf("The comment is at ticket #%d, which is in %s: %s. Say one short acknowledgement of receipt to the owner now, in their language, and end your answer.", t.ID, t.Column, how), nil
			}),
		tool(Read, readAbout+" Use it when the owner asks how things stand.", readSchema,
			func(_ context.Context, a reading) (string, error) {
				st, err := b.open()
				if err != nil {
					return "", err
				}
				return b.read(st, a.Ticket)
			}),
	}
	for i := range tools {
		tools[i].ThreadOnly = true
	}
	return tools
}

// orchestratorTools are the tools of the orchestrator for a look at st.
func (b *board) orchestratorTools(st *store.Store) []plugin.Tool {
	return []plugin.Tool{
		tool(Read, readAbout, readSchema, func(_ context.Context, a reading) (string, error) { return b.read(st, a.Ticket) }),
		tool(Start, "Start a session for a ticket, as one of the agents of the board: one in new is refined by it, one in ready implemented, which moves it to progress. "+
			"It fails when as many sessions work as may at once, or when another ticket is implemented in the same project directory: leave the ticket where it is then.",
			`{"type":"object","properties":{`+ticketParam+`,`+
				`"agent":{"type":"string","description":"the ID of the agent the session runs as, of those the board lists; leave it out for the agent of the thread"}`+
				`},"required":["ticket"]}`,
			func(_ context.Context, a starting) (string, error) { return b.startFor(st, a.Ticket, a.Agent) }),
		tool(Move, "Move a ticket no session works on to another column: back to new or ready to have it worked on again, to done when it was dropped, or to blocked with the reason. "+
			"A ticket gets into progress only by starting its implementation.",
			`{"type":"object","properties":{`+ticketParam+`,`+
				`"column":{"type":"string","enum":["new","ready","blocked","done"]},`+
				`"reason":{"type":"string","description":"only with blocked: why it does not go on"}`+
				`},"required":["ticket","column"]}`,
			func(_ context.Context, a moving) (string, error) {
				t, err := st.Move(a.Ticket, a.Column, a.Reason, b.now())
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("Ticket #%d is in %s.", t.ID, t.Column), nil
			}),
		tool(Comment, "Write a comment to a ticket: why you decided something about it, or what whoever works on it next should know. A session that works on it is told at once.",
			`{"type":"object","properties":{`+ticketParam+`,`+
				`"text":{"type":"string","description":"the comment"}`+
				`},"required":["ticket","text"]}`,
			func(_ context.Context, a commenting) (string, error) {
				t, told, err := b.said(st, a.Ticket, store.Orchestrator, a.Text)
				if err != nil {
					return "", err
				}
				if told {
					return fmt.Sprintf("The comment is at ticket #%d, and the session that works on it was told.", t.ID), nil
				}
				return fmt.Sprintf("The comment is at ticket #%d.", t.ID), nil
			}),
		tool(Stop, "Stop the session that works on a ticket: the ticket is blocked then, for you to move where it belongs. Only for a session that clearly does not go on, or a ticket that was dropped.",
			`{"type":"object","properties":{`+ticketParam+`},"required":["ticket"]}`,
			func(ctx context.Context, a reading) (string, error) {
				t, err := st.Get(a.Ticket)
				if err != nil {
					return "", err
				}
				run, ok := t.Active()
				if !ok {
					return fmt.Sprintf("No session works on ticket #%d.", t.ID), nil
				}
				// The ticket is taken from the session before it is stopped:
				// so it is blocked as stopped, not as failed, see await.
				if _, _, err := st.Release(run.Session, store.Orchestrator, "The orchestrator stopped session "+short(run.Session)+", in its "+run.Role+".", b.now()); err != nil {
					return "", err
				}
				if err := b.sessions.Cancel(ctx, run.Session); err != nil {
					return "", err
				}
				return fmt.Sprintf("Session %s is stopped, and ticket #%d is blocked: move it where it belongs.", short(run.Session), t.ID), nil
			}),
		tool(Status, "Tell what the session that works on a ticket does now: whether its answer is on its way and since when, which tools run in it, and what it waits for.",
			`{"type":"object","properties":{`+ticketParam+`},"required":["ticket"]}`,
			func(_ context.Context, a reading) (string, error) {
				t, err := st.Get(a.Ticket)
				if err != nil {
					return "", err
				}
				run, ok := t.Active()
				if !ok {
					return fmt.Sprintf("No session works on ticket #%d.", t.ID), nil
				}
				if state := b.sessions.WorkState(run.Session); state != "" {
					return state, nil
				}
				return fmt.Sprintf("The node no longer holds session %s of ticket #%d: stop it, and the ticket is yours to move.", short(run.Session), t.ID), nil
			}),
		tool(Tell, "Tell the thread, which talks to the owner, of a ticket that is done, or that is blocked and needs the owner: it is told the result or what blocks it as the ticket has it, so add in text only what else it should know. "+
			"The thread tells the owner, and writes what they answer to the ticket as a comment. Once per column a ticket is in.",
			`{"type":"object","properties":{`+ticketParam+`,`+
				`"text":{"type":"string","description":"what the thread should know besides what the ticket says; leave it out for nothing"}`+
				`},"required":["ticket"]}`,
			func(ctx context.Context, a commenting) (string, error) { return b.tell(ctx, st, a.Ticket, a.Text) }),
	}
}

// tell tells the thread of the ticket id of st as it stands, with what the
// orchestrator adds, more, and records that it was told.
func (b *board) tell(ctx context.Context, st *store.Store, id int, more string) (string, error) {
	t, err := st.Get(id)
	if err != nil {
		return "", err
	}
	if t.Told {
		return "", fmt.Errorf("the thread was told already that ticket %d is in %s", id, t.Column)
	}
	var text strings.Builder
	fmt.Fprintf(&text, "Ticket #%d %q of the board is in %s.", t.ID, oneLine(t.Title), t.Column)
	switch t.Column {
	case store.Done:
		if t.Result != "" {
			text.WriteString("\n\nResult, as the session that implemented it says:\n" + t.Result)
		}
		text.WriteString("\n\nTell the owner what was done, in your words.")
	case store.Blocked:
		fmt.Fprintf(&text, "\n\nIt does not go on because:\n%s\n\nIf this asks the owner something, ask them, and write what they answer to the ticket with %s and ticket %d: the board goes on from there. Do not hand it to a session.", t.Reason, Comment, t.ID)
	}
	if more = strings.TrimSpace(more); more != "" {
		text.WriteString("\n\nThe orchestrator of the board adds:\n" + more)
	}
	if err := b.sessions.Notify(ctx, fmt.Sprintf("Board ticket #%d", t.ID), text.String()); err != nil {
		return "", err
	}
	if _, err := st.Tell(id, b.now()); err != nil {
		return "", err
	}
	return fmt.Sprintf("The thread was told that ticket #%d is in %s.", t.ID, t.Column), nil
}

// errNoTicket is what a tool of a ticket fails with in a session that works
// on none: it closed or blocked its ticket, or the ticket was taken from it.
var errNoTicket = errors.New("this session works on no ticket any more: it was closed or blocked, or taken from it. End your answer")

// ticketTools are the tools of a session that works on a ticket of st: each
// is about the ticket of the session that calls it.
func (b *board) ticketTools(st *store.Store) []plugin.Tool {
	// mine is the ticket the session of ctx works on, and that session.
	mine := func(ctx context.Context) (store.Ticket, string, error) {
		// A session that was just started is known to the board once assign
		// is free, see startFor.
		sid := plugin.ContextID(ctx)
		b.assign.Lock()
		t, ok := st.Of(sid)
		b.assign.Unlock()
		if !ok {
			return t, sid, errNoTicket
		}
		return t, sid, nil
	}
	return []plugin.Tool{
		tool(Mine, "Read your ticket again, in full: its text, the sessions that worked on it and its notes, with the comments others wrote meanwhile.",
			`{"type":"object","properties":{},"required":[]}`,
			func(ctx context.Context, _ struct{}) (string, error) {
				t, _, err := mine(ctx)
				if err != nil {
					return "", err
				}
				return detail(t, b.now(), 0), nil
			}),
		tool(Update, "Write into your ticket what whoever works on it after you needs: another title, another text, which replaces the one it has, or its project directory. Leave out what stays.",
			`{"type":"object","properties":{`+
				`"title":{"type":"string","description":"what it is about, in a few words"},`+
				`"text":{"type":"string","description":"what is to be done, complete: it replaces the text of the ticket"},`+
				`"directory":{"type":"string","description":"the absolute project directory it is to be done in"}`+
				`},"required":[]}`,
			func(ctx context.Context, a updating) (string, error) {
				t, sid, err := mine(ctx)
				if err != nil {
					return "", err
				}
				if a.Directory != nil {
					directory, err := project(*a.Directory)
					if err != nil {
						return "", err
					}
					a.Directory = &directory
				}
				if _, err := st.Edit(t.ID, store.BySession(sid), a.Title, a.Text, a.Directory, b.now()); err != nil {
					return "", err
				}
				return fmt.Sprintf("Ticket #%d is changed.", t.ID), nil
			}),
		tool(Comment, "Write a note to your ticket, for whoever works on it after you and for the owner: what you found, what you decided, how far you are.",
			`{"type":"object","properties":{"text":{"type":"string","description":"the note"}},"required":["text"]}`,
			func(ctx context.Context, a noting) (string, error) {
				t, sid, err := mine(ctx)
				if err != nil {
					return "", err
				}
				if _, err := st.Comment(t.ID, store.BySession(sid), a.Text, b.now()); err != nil {
					return "", err
				}
				return fmt.Sprintf("The note is at ticket #%d.", t.ID), nil
			}),
		tool(Close, "Close your work on your ticket: a refinement moves it to ready, to be implemented, an implementation to done. The ticket is no longer yours then: end your answer.",
			`{"type":"object","properties":{"summary":{"type":"string","description":"after a refinement what you settled; after an implementation what you did, how you checked it and what is left open"}},"required":["summary"]}`,
			func(ctx context.Context, a closing) (string, error) {
				t, sid, err := mine(ctx)
				if err != nil {
					return "", err
				}
				if t, err = st.Close(t.ID, sid, a.Summary, b.now()); err != nil {
					return "", err
				}
				return fmt.Sprintf("Ticket #%d is in %s, and no longer yours. End your answer now, in a line.", t.ID, t.Column), nil
			}),
		tool(Block, "Block your ticket, when you cannot go on with it: with the questions only the owner can answer, or with what fails and you cannot fix. The ticket is no longer yours then: end your answer.",
			`{"type":"object","properties":{"reason":{"type":"string","description":"the questions, numbered and self-contained, with their options, or what is wrong"}},"required":["reason"]}`,
			func(ctx context.Context, a blocking) (string, error) {
				t, sid, err := mine(ctx)
				if err != nil {
					return "", err
				}
				if _, err := st.Block(t.ID, sid, a.Reason, b.now()); err != nil {
					return "", err
				}
				return fmt.Sprintf("Ticket #%d is blocked, and no longer yours. End your answer now, in a line.", t.ID), nil
			}),
	}
}
