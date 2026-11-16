package board

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"ouroboros/internal/agents"
	store "ouroboros/internal/board"
)

// How the board is told in words: to the agents that read it with their
// tools, and to the owner on the screen of the board.

// short is the ID of a session as the board shows it: what tells the
// sessions of a day apart, as the thread is told them.
func short(id string) string { return id[:min(len(id), 8)] }

// clip is text with at most max bytes of it, and a mark where more was.
func clip(text string, max int) string {
	if len(text) <= max {
		return text
	}
	return strings.ToValidUTF8(text[:max], "") + " […]"
}

// oneLine is text on one line.
func oneLine(text string) string { return strings.Join(strings.Fields(text), " ") }

// ago is how long before now at was, in a word.
func ago(at, now time.Time) string {
	d := now.Sub(at)
	if d < time.Minute {
		return "just now"
	}
	if d < time.Hour {
		return strconv.Itoa(int(d/time.Minute)) + "m ago"
	}
	if d < 48*time.Hour {
		return strconv.Itoa(int(d/time.Hour)) + "h ago"
	}
	return strconv.Itoa(int(d/(24*time.Hour))) + "d ago"
}

// agentLabel is how the board names the agent a: by its ID, and by its
// name where it has one.
func agentLabel(a agents.Agent) string {
	if a.Name == "" {
		return a.ID
	}
	return fmt.Sprintf("%s (%s)", strconv.Quote(a.Name), a.ID)
}

// as is how the board names the agent the session of run runs as, after
// what it does: "" for a session from before the board knew it.
func as(run store.Run) string {
	switch {
	case run.AgentName != "":
		return " as " + strconv.Quote(run.AgentName)
	case run.Agent != "":
		return " as " + run.Agent
	}
	return ""
}

// line is the ticket t in one line: its number and title, and what holds
// for it now.
func line(t store.Ticket, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "#%d %s", t.ID, strconv.Quote(oneLine(t.Title)))
	if run, ok := t.Active(); ok {
		fmt.Fprintf(&b, " · %s by session %s%s since %s", run.Role, short(run.Session), as(run), ago(run.Began, now))
	}
	if t.Column == store.Blocked {
		fmt.Fprintf(&b, " · from %s: %s", t.From, clip(oneLine(t.Reason), 120))
	}
	if t.Told {
		b.WriteString(" · thread told")
	}
	b.WriteString(" · changed " + ago(t.Changed, now))
	return b.String()
}

// inColumn are those of tickets that are in column, the latest first: they
// come by number.
func inColumn(tickets []store.Ticket, column string) []store.Ticket {
	var in []store.Ticket
	for _, t := range slices.Backward(tickets) {
		if t.Column == column {
			in = append(in, t)
		}
	}
	return in
}

// inOrder are tickets in the order overview lists them.
func inOrder(tickets []store.Ticket) []store.Ticket {
	var all []store.Ticket
	for _, column := range store.Columns {
		all = append(all, inColumn(tickets, column)...)
	}
	return all
}

// overview is the board by its columns, a line a ticket, the latest first;
// of Done at most done of them. The ticket marked is marked, if it is listed.
func overview(tickets []store.Ticket, now time.Time, done, marked int) string {
	var b strings.Builder
	for _, column := range store.Columns {
		in := inColumn(tickets, column)
		fmt.Fprintf(&b, "%s (%d)\n", strings.ToUpper(column), len(in))
		for i, t := range in {
			if column == store.Done && i >= done {
				fmt.Fprintf(&b, "  … and %d older\n", len(in)-i)
				break
			}
			mark := "  "
			if t.ID == marked {
				mark = "> "
			}
			b.WriteString(mark + line(t, now) + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// detail is all the board keeps of the ticket t; with notes > 0 only that
// many of its notes, the latest, and of its texts the start, for a reader
// that looks at many tickets.
func detail(t store.Ticket, now time.Time, notes int) string {
	body := func(text string) string {
		if notes > 0 {
			return clip(text, 600)
		}
		return text
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Ticket #%d: %s\nColumn: %s", t.ID, oneLine(t.Title), t.Column)
	if t.Column == store.Blocked {
		fmt.Fprintf(&b, " (from %s)\nBlocked because: %s", t.From, body(t.Reason))
	}
	if t.Told {
		b.WriteString("\nThe thread was told of this.")
	}
	if t.Directory != "" {
		b.WriteString("\nProject directory: " + t.Directory)
	}
	fmt.Fprintf(&b, "\nCreated %s, changed %s.\n\n%s", ago(t.Created, now), ago(t.Changed, now), body(t.Text))
	if t.Result != "" {
		b.WriteString("\n\nResult: " + body(t.Result))
	}
	if len(t.Runs) > 0 {
		b.WriteString("\n\nSessions:")
		for _, run := range t.Runs {
			how := "works on it since " + ago(run.Began, now)
			if !run.Ended.IsZero() {
				how = "ended " + ago(run.Ended, now) + ": " + run.Outcome
			}
			fmt.Fprintf(&b, "\n- %s%s, %s, %s", short(run.Session), as(run), run.Role, how)
		}
	}
	shown := t.Notes
	if notes > 0 && len(shown) > notes {
		fmt.Fprintf(&b, "\n\nNotes, the last %d of %d:", notes, len(shown))
		shown = shown[len(shown)-notes:]
	} else if len(shown) > 0 {
		b.WriteString("\n\nNotes:")
	}
	for _, n := range shown {
		by := n.By
		if id, ok := strings.CutPrefix(by, store.BySession("")); ok {
			by = store.BySession(short(id))
		}
		fmt.Fprintf(&b, "\n- %s, %s: %s", ago(n.At, now), by, body(n.Text))
	}
	return b.String()
}
