package board

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var epoch = time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "board.json"), epoch)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func create(t *testing.T, s *Store, title string) Ticket {
	t.Helper()
	ticket, err := s.Create(title, "what is to be done", "", epoch)
	if err != nil {
		t.Fatal(err)
	}
	return ticket
}

func must(t *testing.T) func(Ticket, error) Ticket {
	return func(ticket Ticket, err error) Ticket {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return ticket
	}
}

// A ticket goes from new through its refinement to ready, through its
// implementation to progress and done, each by the session that works on it,
// and the board on disk says the same as the one in memory.
func TestTicketGoesItsWay(t *testing.T) {
	s := openTest(t)
	ticket, err := s.Create("  Dark mode  ", "  the settings need one  ", " /srv/app ", epoch)
	if err != nil {
		t.Fatal(err)
	}
	if ticket.ID != 1 || ticket.Title != "Dark mode" || ticket.Text != "the settings need one" || ticket.Directory != "/srv/app" ||
		ticket.Column != New || ticket.Created != epoch || ticket.Changed != epoch {
		t.Fatalf("created: %+v", ticket)
	}
	if role, err := ticket.Role(); role != Refinement || err != nil {
		t.Fatalf("role in new: %q, %v", role, err)
	}
	if _, ok := ticket.Active(); ok {
		t.Fatal("a new ticket has a session")
	}
	ticket = must(t)(s.Start(1, "refiner", "", "", epoch.Add(time.Minute)))
	run, ok := ticket.Active()
	if !ok || run.Session != "refiner" || run.Role != Refinement || run.Began != epoch.Add(time.Minute) || ticket.Column != New {
		t.Fatalf("refinement started: %+v", ticket)
	}
	if of, ok := s.Of("refiner"); !ok || of.ID != 1 {
		t.Fatalf("the session has no ticket: %+v", of)
	}
	if of, ok := s.Of("another"); ok || of.ID != 0 {
		t.Fatalf("another session has the ticket: %+v", of)
	}
	ticket = must(t)(s.Close(1, "refiner", " settled ", epoch.Add(2*time.Minute)))
	if ticket.Column != Ready || ticket.Result != "" || ticket.Runs[0].Outcome != Ready || ticket.Runs[0].Ended != epoch.Add(2*time.Minute) {
		t.Fatalf("refined: %+v", ticket)
	}
	if _, ok := s.Of("refiner"); ok {
		t.Fatal("a session that closed its ticket still has it")
	}
	if role, _ := ticket.Role(); role != Implementation {
		t.Fatalf("role in ready: %q", role)
	}
	ticket = must(t)(s.Start(1, "builder", "", "", epoch.Add(3*time.Minute)))
	if run, _ := ticket.Active(); ticket.Column != Progress || run.Role != Implementation {
		t.Fatalf("implementation started: %+v", ticket)
	}
	if _, err := ticket.Role(); err == nil || !strings.Contains(err.Error(), "ticket 1 is in progress") {
		t.Fatalf("a session starts in progress: %v", err)
	}
	ticket = must(t)(s.Close(1, "builder", "built it", epoch.Add(4*time.Minute)))
	if ticket.Column != Done || ticket.Result != "built it" || ticket.Runs[1].Outcome != Done || ticket.Changed != epoch.Add(4*time.Minute) {
		t.Fatalf("done: %+v", ticket)
	}
	var said []string
	for _, n := range ticket.Notes {
		said = append(said, n.By+": "+n.Text)
	}
	want := []string{
		"orchestrator: started session refiner for its refinement",
		"session refiner: settled", "session refiner: moved from new to ready",
		"orchestrator: moved from ready to progress", "orchestrator: started session builder for its implementation",
		"session builder: built it", "session builder: moved from progress to done",
	}
	if strings.Join(said, "\n") != strings.Join(want, "\n") {
		t.Fatalf("notes:\n%s", strings.Join(said, "\n"))
	}
	if !s.Knows("refiner") || !s.Knows("builder") || s.Knows("stranger") {
		t.Fatal("the board does not know its sessions")
	}
	read, err := Read(s.path)
	if err != nil || len(read.Tickets) != 1 || read.Tickets[0].Column != Done || read.Next != 1 {
		t.Fatalf("on disk: %+v, %v", read, err)
	}
	if info, err := os.Stat(s.path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode: %v, %v", info, err)
	}
	// What the store hands out is the caller's: changing it changes nothing.
	list := s.List()
	list[0].Notes[0].Text, list[0].Runs[0].Session = "changed", "changed"
	if got, _ := s.Get(1); got.Notes[0].Text == "changed" || got.Runs[0].Session == "changed" {
		t.Fatal("List hands out the board itself")
	}
	if _, err := s.Get(2); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a ticket that is not there: %v", err)
	}
}

// Only what others than the orchestrator change counts for it to look, and
// what it saw is kept with the looks, of which the board keeps the latest.
func TestChangesAndLooks(t *testing.T) {
	s := openTest(t)
	if changes, seen := s.Pending(); changes != 0 || seen != 0 {
		t.Fatalf("an empty board: %d, %d", changes, seen)
	}
	create(t, s, "one")
	must(t)(s.Comment(1, Thread, "more", epoch))
	if changes, seen := s.Pending(); changes != 2 || seen != 0 {
		t.Fatalf("after the thread: %d, %d", changes, seen)
	}
	// A ticket says which change was its last, for the orchestrator to tell
	// what it has yet to look at.
	if got := create(t, s, "two"); got.Change != 3 {
		t.Fatalf("the change of a new ticket: %d", got.Change)
	}
	if got, _ := s.Get(1); got.Change != 2 {
		t.Fatalf("the change of a ticket the thread wrote to: %d", got.Change)
	}
	must(t)(s.Start(1, "refiner", "", "", epoch))
	must(t)(s.Comment(1, Orchestrator, "noted", epoch))
	must(t)(s.Tell(1, epoch))
	if changes, _ := s.Pending(); changes != 3 {
		t.Fatalf("the orchestrator counted its own changes: %d", changes)
	}
	if got, _ := s.Get(1); !got.Told || got.Change != 2 {
		t.Fatalf("the thread was told, and the ticket says: %+v", got)
	}
	if got := must(t)(s.Close(1, "refiner", "settled", epoch)); got.Change != 4 {
		t.Fatalf("the change of a ticket its session closed: %d", got.Change)
	}
	if changes, _ := s.Pending(); changes != 4 {
		t.Fatalf("a session closed its ticket: %d", changes)
	}
	if got, _ := s.Get(1); got.Told {
		t.Fatal("the thread was told of new, not of ready")
	}
	for i := range MaxLooks + 2 {
		dropped, err := s.Looked(4, fmt.Sprintf("look-%d", i), "nothing to do", epoch.Add(time.Duration(i)*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if want := i >= MaxLooks; (len(dropped) == 1) != want || want && dropped[0] != fmt.Sprintf("look-%d", i-MaxLooks) {
			t.Fatalf("look %d dropped %v", i, dropped)
		}
	}
	if changes, seen := s.Pending(); changes != 4 || seen != 4 {
		t.Fatalf("after the looks: %d, %d", changes, seen)
	}
	looks := s.Looks()
	if len(looks) != MaxLooks || looks[0].Session != "look-2" || looks[MaxLooks-1].Result != "nothing to do" || looks[MaxLooks-1].At != epoch.Add((MaxLooks+1)*time.Minute) {
		t.Fatalf("looks: %+v", looks)
	}
	if !s.Knows("look-2") || s.Knows("look-1") || !s.Knows("refiner") {
		t.Fatal("the board knows the sessions of the looks it keeps and those of its tickets, and those alone")
	}
	looks[0].Session = "changed"
	if s.Looks()[0].Session == "changed" {
		t.Fatal("Looks hands out the board itself")
	}
}

// A session blocks its ticket with what is open, the orchestrator moves it
// back, and a session that ends without a word leaves its ticket blocked.
func TestBlocked(t *testing.T) {
	s := openTest(t)
	create(t, s, "one")
	must(t)(s.Start(1, "refiner", "", "", epoch))
	ticket := must(t)(s.Block(1, "refiner", " 1. Which colour? ", epoch.Add(time.Minute)))
	if ticket.Column != Blocked || ticket.Reason != "1. Which colour?" || ticket.From != New || ticket.Runs[0].Outcome != Blocked || ticket.Runs[0].Ended.IsZero() {
		t.Fatalf("blocked: %+v", ticket)
	}
	if _, ok := ticket.Active(); ok {
		t.Fatal("a session that blocked its ticket still works on it")
	}
	must(t)(s.Tell(1, epoch))
	ticket = must(t)(s.Move(1, New, "", epoch.Add(2*time.Minute)))
	if ticket.Column != New || ticket.Reason != "" || ticket.From != "" || ticket.Told {
		t.Fatalf("moved back: %+v", ticket)
	}
	must(t)(s.Start(1, "second", "", "", epoch))
	ticket, released, err := s.Release("second", Code, "It ended without a word.", epoch.Add(3*time.Minute))
	if err != nil || !released || ticket.Column != Blocked || ticket.From != New || ticket.Reason != "It ended without a word." ||
		ticket.Runs[1].Outcome != "ended without a result" || ticket.Runs[1].Ended != epoch.Add(3*time.Minute) {
		t.Fatalf("released: %+v, %v, %v", ticket, released, err)
	}
	if last := ticket.Notes[len(ticket.Notes)-1]; last.By != Code || last.Text != "moved from new to blocked" {
		t.Fatalf("the note of the release: %+v", last)
	}
	if _, err := s.Start(1, "third", "", "", epoch); err == nil || !strings.Contains(err.Error(), "ticket 1 is in blocked") {
		t.Fatalf("a session for a blocked ticket: %v", err)
	}
	// A session that has no ticket any more has none to take.
	changes, _ := s.Pending()
	if _, released, err := s.Release("second", Code, "again", epoch); released || err != nil {
		t.Fatalf("released twice: %v, %v", released, err)
	}
	if after, _ := s.Pending(); after != changes {
		t.Fatal("a release of nothing changed the board")
	}
	// The orchestrator blocks a ticket itself, with the reason.
	must(t)(s.Move(1, Ready, "", epoch))
	ticket = must(t)(s.Move(1, Blocked, " waits for ticket 2 ", epoch))
	if ticket.Reason != "waits for ticket 2" || ticket.From != Ready {
		t.Fatalf("blocked by the orchestrator: %+v", ticket)
	}
}

// What a change is refused for, and that a refused change changes nothing.
func TestRefused(t *testing.T) {
	s := openTest(t)
	long := strings.Repeat("x", MaxText+1)
	for what, err := range map[string]error{
		"no title":     second(s.Create(" ", "text", "", epoch)),
		"a long title": second(s.Create(long[:MaxTitle+1], "text", "", epoch)),
		"no text":      second(s.Create("title", "", "", epoch)),
		"a long text":  second(s.Create("title", long, "", epoch)),
		"no comment":   second(s.Comment(1, Thread, "  ", epoch)),
		"no summary":   second(s.Close(1, "x", "", epoch)),
		"no reason":    second(s.Block(1, "x", "", epoch)),
	} {
		if err == nil {
			t.Errorf("%s was taken", what)
		}
	}
	if len(s.List()) != 0 {
		t.Fatal("a refused ticket is on the board")
	}
	// What is exactly as long as it may be is taken.
	if _, err := s.Create(long[:MaxTitle], long[:MaxText], "", epoch); err != nil {
		t.Fatalf("a title and a text of the most they may be: %v", err)
	}
	if _, err := s.Comment(1, Thread, long[:MaxNote], epoch); err != nil {
		t.Fatalf("a comment of the most it may be: %v", err)
	}
	if _, err := s.Move(1, Blocked, long[:MaxNote], epoch); err != nil {
		t.Fatalf("a reason of the most it may be: %v", err)
	}
	s = openTest(t)
	if _, err := s.Comment(1, Thread, "text", epoch); !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "no such ticket 1") {
		t.Fatalf("a comment to no ticket: %v", err)
	}
	create(t, s, "one")
	before, _ := s.Pending()
	title, empty := "t", " "
	for what, c := range map[string]struct {
		err  error
		says string
	}{
		"nothing to change":        {second(s.Edit(1, Thread, nil, nil, nil, epoch)), "nothing to change"},
		"nothing but a title":      {second(s.Edit(7, Thread, &title, nil, nil, epoch)), "no such ticket 7"},
		"nothing but a text":       {second(s.Edit(7, Thread, nil, &title, nil, epoch)), "no such ticket 7"},
		"nothing but a directory":  {second(s.Edit(7, Thread, nil, nil, &title, epoch)), "no such ticket 7"},
		"an empty title":           {second(s.Edit(1, Thread, &empty, nil, nil, epoch)), "title must be nonempty"},
		"an empty text":            {second(s.Edit(1, Thread, &title, &empty, nil, epoch)), "text must be nonempty"},
		"a column that is none":    {second(s.Move(1, "later", "", epoch)), `there is no column "later"`},
		"the column it is in":      {second(s.Move(1, New, "", epoch)), "is in new already"},
		"progress":                 {second(s.Move(1, Progress, "", epoch)), "move it to ready and start a session"},
		"blocked for nothing":      {second(s.Move(1, Blocked, " ", epoch)), "with the reason"},
		"blocked for too much":     {second(s.Move(1, Blocked, long[:MaxNote+1], epoch)), "with the reason"},
		"a reason elsewhere":       {second(s.Move(1, Done, "because", epoch)), "only a move to blocked takes a reason"},
		"closing without a run":    {second(s.Close(1, "stranger", "done", epoch)), "session stranger does not work on ticket 1"},
		"blocking without a run":   {second(s.Block(1, "stranger", "stuck", epoch)), "session stranger does not work on ticket 1"},
		"telling of no ticket":     {second(s.Tell(7, epoch)), "no such ticket 7"},
		"starting for no ticket":   {second(s.Start(7, "x", "", "", epoch)), "no such ticket 7"},
		"moving no ticket":         {second(s.Move(7, Done, "", epoch)), "no such ticket 7"},
		"reading no ticket's role": {second(Ticket{ID: 3, Column: Done}.Role()), "ticket 3 is in done"},
	} {
		if c.err == nil || !strings.Contains(c.err.Error(), c.says) {
			t.Errorf("%s: %v", what, c.err)
		}
	}
	if after, _ := s.Pending(); after != before {
		t.Fatal("a refused change counted")
	}
	if got, _ := s.Get(1); got.Title != "one" || len(got.Notes) != 0 {
		t.Fatalf("a refused change changed the ticket: %+v", got)
	}
	// A ticket a session works on stays where it is, and takes no other.
	must(t)(s.Start(1, "refiner", "", "", epoch))
	if _, err := s.Move(1, Done, "", epoch); err == nil || !strings.Contains(err.Error(), "session refiner works on ticket 1: stop it first") {
		t.Fatalf("moved under its session: %v", err)
	}
	if _, err := s.Start(1, "other", "", "", epoch); err == nil || !strings.Contains(err.Error(), "session refiner works on ticket 1 already") {
		t.Fatalf("a second session for the ticket: %v", err)
	}
	if got, _ := s.Get(1); len(got.Runs) != 1 {
		t.Fatalf("runs: %+v", got.Runs)
	}
	if _, err := s.Close(1, "other", "done", epoch); err == nil {
		t.Fatal("another session closed the ticket")
	}
	if _, err := s.Block(1, "other", "stuck", epoch); err == nil {
		t.Fatal("another session blocked the ticket")
	}
}

func second[T any](_ T, err error) error { return err }

// A session changes its ticket, and what it leaves out stays.
func TestEdit(t *testing.T) {
	s := openTest(t)
	create(t, s, "one")
	title, body, directory := " Better title ", " all of it ", " /srv/app "
	ticket := must(t)(s.Edit(1, BySession("refiner"), &title, nil, nil, epoch.Add(time.Minute)))
	if ticket.Title != "Better title" || ticket.Text != "what is to be done" || ticket.Notes[0].Text != "changed the title" || ticket.Notes[0].By != "session refiner" {
		t.Fatalf("title: %+v", ticket)
	}
	ticket = must(t)(s.Edit(1, BySession("refiner"), nil, &body, &directory, epoch.Add(time.Minute)))
	if ticket.Title != "Better title" || ticket.Text != "all of it" || ticket.Directory != "/srv/app" || ticket.Notes[1].Text != "changed the text, directory" {
		t.Fatalf("text and directory: %+v", ticket)
	}
	if ticket.Changed != epoch.Add(time.Minute) {
		t.Fatalf("changed: %v", ticket.Changed)
	}
}

// A ticket keeps its latest notes, and takes sessions only so often.
func TestLimits(t *testing.T) {
	s := openTest(t)
	create(t, s, "one")
	for i := range MaxNotes + 3 {
		must(t)(s.Comment(1, Thread, fmt.Sprintf("note %d", i), epoch))
	}
	if got, _ := s.Get(1); len(got.Notes) != MaxNotes || got.Notes[0].Text != "note 3" {
		t.Fatalf("notes: %d, the first %q", len(got.Notes), got.Notes[0].Text)
	}
	for i := range MaxRuns {
		must(t)(s.Start(1, fmt.Sprintf("s%d", i), "", "", epoch))
		if _, released, err := s.Release(fmt.Sprintf("s%d", i), Code, "failed", epoch); !released || err != nil {
			t.Fatal(released, err)
		}
		must(t)(s.Move(1, New, "", epoch))
	}
	if _, err := s.Start(1, "one too many", "", "", epoch); err == nil || !strings.Contains(err.Error(), "12 sessions worked on ticket 1 already") {
		t.Fatalf("a session beyond the limit: %v", err)
	}
	// A full board drops what is done longest, and takes nothing when
	// nothing is done.
	for i := 2; i <= MaxTickets; i++ {
		create(t, s, fmt.Sprintf("ticket %d", i))
	}
	if _, err := s.Create("too many", "text", "", epoch); !errors.Is(err, ErrFull) {
		t.Fatalf("a full board: %v", err)
	}
	must(t)(s.Move(5, Done, "", epoch.Add(2*time.Hour)))
	must(t)(s.Move(4, Done, "", epoch.Add(time.Hour)))
	must(t)(s.Move(6, Done, "", epoch.Add(3*time.Hour)))
	must(t)(s.Move(1, Done, "", epoch.Add(time.Minute)))
	// The first ticket of the board is the one done longest, and goes.
	ticket, err := s.Create("one more", "text", "", epoch)
	if err != nil || ticket.ID != MaxTickets+1 {
		t.Fatalf("one more: %+v, %v", ticket, err)
	}
	if _, err := s.Get(1); !errors.Is(err, ErrNotFound) {
		t.Fatal("the ticket done longest stayed")
	}
	if _, err := s.Get(4); err != nil {
		t.Fatal("a ticket done later went")
	}
	// Then the one done longest is not the first of the board.
	must(t)(s.Move(2, Done, "", epoch.Add(4*time.Hour)))
	must(t)(s.Create("and another", "text", "", epoch))
	if _, err := s.Get(4); !errors.Is(err, ErrNotFound) {
		t.Fatal("the ticket done longest stayed")
	}
	if _, err := s.Get(2); err != nil {
		t.Fatal("the first ticket of the board went, though one was done longer")
	}
	if _, err := s.Get(5); err != nil {
		t.Fatal("a ticket done later went")
	}
	if len(s.List()) != MaxTickets {
		t.Fatalf("%d tickets", len(s.List()))
	}
}

// A node that starts anew finds no session of the one before: their tickets
// are blocked, and the orchestrator has something to look at.
func TestOpenInterrupts(t *testing.T) {
	s := openTest(t)
	create(t, s, "refined")
	create(t, s, "idle")
	must(t)(s.Start(1, "refiner", "", "", epoch))
	before, _ := s.Pending()
	again, err := Open(s.path, epoch.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	ticket, _ := again.Get(1)
	if ticket.Column != Blocked || ticket.From != New || ticket.Runs[0].Outcome != "interrupted" || ticket.Runs[0].Ended != epoch.Add(time.Hour) ||
		ticket.Reason != "The node started anew while session refiner worked on it, in its refinement." {
		t.Fatalf("interrupted: %+v", ticket)
	}
	if idle, _ := again.Get(2); idle.Column != New || len(idle.Notes) != 0 {
		t.Fatalf("a ticket nobody worked on: %+v", idle)
	}
	if after, _ := again.Pending(); after != before+1 || ticket.Change != after {
		t.Fatalf("changes: %d after %d, and the ticket says %d", after, before, ticket.Change)
	}
	// With nothing interrupted, opening changes nothing.
	third, err := Open(s.path, epoch.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if now, _ := third.Pending(); now != before+1 {
		t.Fatalf("opening changed the board: %d", now)
	}
}

// A board that cannot be read or written is an error, and nothing is lost
// or overwritten for it.
func TestFiles(t *testing.T) {
	dir := t.TempDir()
	if snap, err := Read(filepath.Join(dir, "none.json")); err != nil || len(snap.Tickets) != 0 {
		t.Fatalf("a board that is not there: %+v, %v", snap, err)
	}
	broken := filepath.Join(dir, "broken.json")
	if err := os.WriteFile(broken, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(broken, epoch); err == nil || !strings.Contains(err.Error(), "cannot be read") {
		t.Fatalf("a broken board: %v", err)
	}
	if b, _ := os.ReadFile(broken); string(b) != "{not json" {
		t.Fatal("a broken board was overwritten")
	}
	if _, err := Read(dir); err == nil {
		t.Fatal("a directory was read as a board")
	}
	if _, err := Open(filepath.Join(broken, "under", "board.json"), epoch); err == nil {
		t.Fatal("a board under a file was opened")
	}
	// A change that cannot be written did not happen.
	locked := filepath.Join(dir, "locked")
	if err := os.Mkdir(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	s, err := Open(filepath.Join(locked, "board.json"), epoch)
	if err != nil {
		t.Fatal(err)
	}
	create(t, s, "kept")
	if err := os.Chmod(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o700) })
	if _, err := s.Create("lost", "text", "", epoch); err == nil {
		t.Skip("this user writes where it may not: nothing to check")
	}
	if list := s.List(); len(list) != 1 || list[0].Title != "kept" {
		t.Fatalf("a change that was not written holds: %+v", list)
	}
	if _, released, err := s.Release("nobody", Code, "why", epoch); released || err != nil {
		t.Fatal(released, err)
	}
	must(t)(Ticket{}, os.Chmod(locked, 0o700))
	must(t)(s.Start(1, "refiner", "", "", epoch))
	must(t)(Ticket{}, os.Chmod(locked, 0o500))
	if _, released, err := s.Release("refiner", Code, "why", epoch); released || err == nil {
		t.Fatalf("a release that was not written: %v, %v", released, err)
	}
	// An interrupted ticket that cannot be written fails the opening.
	if _, err := Open(s.path, epoch); err == nil {
		t.Fatal("opened, though the interrupted ticket could not be blocked")
	}
}
