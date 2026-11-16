package board

import (
	"context"
	"fmt"

	store "ouroboros/internal/board"
	"ouroboros/plugin"
)

// screen is /board in the terminal UI: the board by its columns, and below
// it one ticket in full, which the actions choose. It reads the file of the
// board, which the node writes: the UI need not be the process of the node.
func (b *board) screen() plugin.Screen {
	step := func(by int64) func(context.Context) (string, error) {
		return func(context.Context) (string, error) {
			b.selected.Add(by)
			snap, err := store.Read(b.path)
			if err != nil || len(snap.Tickets) == 0 {
				return "", err
			}
			return fmt.Sprintf("ticket #%d", b.chosen(snap.Tickets).ID), nil
		}
	}
	next, previous := step(1), step(-1)
	return plugin.Screen{
		Command: "board",
		Title:   "Board",
		Body: func(context.Context) (string, error) {
			snap, err := store.Read(b.path)
			if err != nil {
				return "", err
			}
			return b.shown(snap), nil
		},
		Actions: []plugin.Action{{Name: "next ticket", Run: next}, {Name: "previous ticket", Run: previous}},
	}
}

// chosen is the ticket of tickets, which are some, that the screen shows in
// full: the actions step through them in the order the columns list them.
func (b *board) chosen(tickets []store.Ticket) store.Ticket {
	listed := inOrder(tickets)
	n := int64(len(listed))
	return listed[((b.selected.Load()%n)+n)%n]
}

// shown is what the screen shows of the board snap.
func (b *board) shown(snap store.Snapshot) string {
	if len(snap.Tickets) == 0 {
		return "The board is empty. Ask your agent for something that is real work, and it puts a ticket here."
	}
	now := b.now()
	chosen := b.chosen(snap.Tickets)
	text := overview(snap.Tickets, now, len(snap.Tickets), chosen.ID)
	if n := len(snap.Looks); n > 0 {
		look := snap.Looks[n-1]
		text += fmt.Sprintf("\n\nThe orchestrator looked %s, in session %s: %s", ago(look.At, now), short(look.Session), look.Result)
	}
	if snap.Changes != snap.Seen {
		text += "\n\nThe board changed since: the orchestrator looks within a minute."
	}
	return text + "\n\n" + detail(chosen, now, 0)
}
