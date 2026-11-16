package gui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	store "ouroboros/internal/board"
)

// The window shows the board of its profile beside the thread: the tickets
// in their columns, and one ticket in full when the owner opens it. It reads
// the file the node keeps the board in, see store.Read, as the terminal UI
// does: the window need not be the process of the node, and it changes
// nothing on the board.

// Board is the board as the window shows it: a lane a column, in the order
// of the board, and when the orchestrator looked at it last.
type Board struct {
	Lanes []Lane `json:"lanes"`
	// Looked is the last look of the orchestrator, nil before the first.
	Looked *store.Look `json:"looked,omitempty"`
	// Pending says that the board changed since: the orchestrator looks at
	// it within a minute.
	Pending bool `json:"pending"`
}

// Lane is a column of the board with its tickets, the latest first.
type Lane struct {
	Column string `json:"column"`
	Cards  []Card `json:"cards"`
}

// Card is a ticket as its lane shows it: what tells it apart and how it
// stands, without its text and its notes, which Ticket has.
type Card struct {
	ID     int    `json:"id"`
	Title  string `json:"title"`
	Column string `json:"column"`
	// Reason is why a ticket is blocked, its start, and From the column it
	// came from.
	Reason string `json:"reason,omitempty"`
	From   string `json:"from,omitempty"`
	// Role and Session are of the session that works on it now, since Since;
	// empty where none does.
	Role    string     `json:"role,omitempty"`
	Session string     `json:"session,omitempty"`
	Since   *time.Time `json:"since,omitempty"`
	// Agent is the agent of the profile that session runs as, and AgentName
	// what it was called when it began; both empty for a session from
	// before the board knew them, which ran as the agent of the thread.
	Agent     string    `json:"agent,omitempty"`
	AgentName string    `json:"agent_name,omitempty"`
	Told      bool      `json:"told,omitempty"`
	Changed   time.Time `json:"changed"`
	Notes     int       `json:"notes"`
}

// cardReason is how much of what blocks a ticket its card shows.
const cardReason = 240

// card is the ticket t as its lane shows it.
func card(t store.Ticket) Card {
	c := Card{ID: t.ID, Title: t.Title, Column: t.Column, From: t.From, Told: t.Told, Changed: t.Changed, Notes: len(t.Notes)}
	if reason := strings.Join(strings.Fields(t.Reason), " "); len(reason) > cardReason {
		c.Reason = strings.ToValidUTF8(reason[:cardReason], "") + " …"
	} else {
		c.Reason = reason
	}
	if run, ok := t.Active(); ok {
		c.Role, c.Session, c.Since, c.Agent, c.AgentName = run.Role, run.Session, &run.Began, run.Agent, run.AgentName
	}
	return c
}

// Board returns the board of the profile of the window, as its file has it
// now: an empty board where the profile has none.
func (w *Window) Board() (Board, error) { return w.profile.board() }

// Ticket returns the ticket id of the board in full: its text, why it is
// blocked, its result, the sessions that worked on it and its notes.
func (w *Window) Ticket(id int) (store.Ticket, error) { return w.profile.ticket(id) }

// board is the board of the profile, see Window.Board.
func (p *profile) board() (Board, error) {
	snap, err := store.Read(p.dir.Board())
	if err != nil {
		return Board{}, err
	}
	board := Board{Pending: snap.Changes != snap.Seen}
	for _, column := range store.Columns {
		lane := Lane{Column: column, Cards: []Card{}}
		// The tickets come by number: the latest come last.
		for _, t := range slices.Backward(snap.Tickets) {
			if t.Column == column {
				lane.Cards = append(lane.Cards, card(t))
			}
		}
		board.Lanes = append(board.Lanes, lane)
	}
	if n := len(snap.Looks); n > 0 {
		board.Looked = &snap.Looks[n-1]
	}
	return board, nil
}

// ticket is the ticket id of the board of the profile, see Window.Ticket.
func (p *profile) ticket(id int) (store.Ticket, error) {
	snap, err := store.Read(p.dir.Board())
	if err != nil {
		return store.Ticket{}, err
	}
	for _, t := range snap.Tickets {
		if t.ID == id {
			return t, nil
		}
	}
	return store.Ticket{}, fmt.Errorf("the board has no ticket %d any more", id)
}
