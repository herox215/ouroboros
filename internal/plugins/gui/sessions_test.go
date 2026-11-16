package gui

import (
	"strings"
	"testing"
	"time"

	"ouroboros/internal/home"
	"ouroboros/plugin"
)

var sessionsEpoch = time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)

// The window lists the sessions of the profile with what each works on and
// what runs in the background for it, and it leaves out what the node says of
// a peer's conversation.
func TestWindowListsSessions(t *testing.T) {
	dir := home.Dir(t.TempDir())
	someBoard(t, dir)
	s := &fakeSessions{
		list: []plugin.SessionInfo{
			{ID: "0123456789abcdef", Title: "Dark mode", Running: true, Last: sessionsEpoch.Add(time.Minute)},
			{ID: "ffffffffffffffff", Title: "Export", Last: sessionsEpoch},
		},
		jobs: []plugin.Job{
			{Session: "0123456789abcdef", N: 1, Command: "go test ./...", Since: sessionsEpoch},
			{Session: "ffffffffffffffff", Peer: "lena", Title: "Export", N: 1, Command: "ls"},
		},
	}
	w := newWindow(&profile{sessions: s, dir: dir}, "Luna")
	t.Cleanup(w.Close)

	list, err := w.Sessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("%d sessions, want 2: %+v", len(list), list)
	}
	work := list[0]
	if work.Title != "Dark mode" || !work.Running {
		t.Errorf("the first session is %+v", work)
	}
	// The board says which ticket the session refines, since when.
	if work.Ticket != 1 || work.Work != "Dark mode" || work.Role != "refinement" {
		t.Errorf("the ticket of the session is %+v", work)
	}
	if work.Since == nil || !work.Since.Equal(boardEpoch.Add(4*time.Minute)) {
		t.Errorf("the session works since %v", work.Since)
	}
	if len(work.Jobs) != 1 || work.Jobs[0].Command != "go test ./..." {
		t.Errorf("the jobs of the session are %+v", work.Jobs)
	}
	// A command of a peer's conversation is no job of a session of the owner.
	if jobs := list[1].Jobs; len(jobs) != 0 {
		t.Errorf("the second session has jobs %+v", jobs)
	}
	if list[1].Ticket != 0 || list[1].Since != nil {
		t.Errorf("the second session works on a ticket: %+v", list[1])
	}
}

// A session that works on nothing and has no job is listed as it stands, also
// when the board and the jobs cannot be read.
func TestSessionsWithoutBoardOrJobs(t *testing.T) {
	s := &fakeSessions{
		list:    []plugin.SessionInfo{{ID: "abc", Title: "Something", Last: sessionsEpoch}},
		jobsErr: errTest,
	}
	w := newWindow(&profile{sessions: s}, "Luna")
	t.Cleanup(w.Close)

	list, err := w.Sessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != "abc" || list[0].Work != "" || len(list[0].Jobs) != 0 {
		t.Fatalf("the list is %+v", list)
	}
}

// One session is shown in full: its line of the list and what it told lately,
// the oldest first, without the state that only applies while it runs.
func TestWindowShowsOneSession(t *testing.T) {
	s := &fakeSessions{
		list: []plugin.SessionInfo{{ID: "abc", Title: "Export", Running: true, Last: sessionsEpoch.Add(time.Minute)}},
		past: []plugin.SessionEvent{
			{Kind: plugin.SessionTitle, Text: "Export", At: sessionsEpoch},
			{Kind: plugin.SessionThought, Text: "hmm", At: sessionsEpoch},
			{Kind: plugin.SessionMessage, Text: "Mach das CSV", At: sessionsEpoch},
			{Kind: plugin.SessionStep, Text: "bash · go test ./...", At: sessionsEpoch},
			{Kind: plugin.SessionWorking, N: 1, At: sessionsEpoch},
			{Kind: plugin.SessionAnswer, Text: strings.Repeat("a", sessionText+10), At: sessionsEpoch},
		},
	}
	w := newWindow(&profile{sessions: s}, "Luna")
	t.Cleanup(w.Close)

	track, err := w.Session("abc")
	if err != nil {
		t.Fatal(err)
	}
	if track.Title != "Export" || !track.Running {
		t.Errorf("the session is %+v", track.Session)
	}
	if len(track.Events) != 3 {
		t.Fatalf("%d events, want the message, the step and the answer: %+v", len(track.Events), track.Events)
	}
	if track.Events[0].Kind != plugin.SessionMessage || track.Events[0].Text != "Mach das CSV" {
		t.Errorf("the first event is %+v", track.Events[0])
	}
	if track.Events[1].Kind != plugin.SessionStep || track.Events[1].Text != "bash · go test ./..." {
		t.Errorf("the step is %+v", track.Events[1])
	}
	// A long answer is cut, not carried whole into the page.
	if text := track.Events[2].Text; len(text) > sessionText+4 || !strings.HasSuffix(text, " …") {
		t.Errorf("the answer was not cut: %d bytes", len(text))
	}
}

// A session shows the latest of what it told, and nothing of an older answer.
func TestOneSessionKeepsTheLatestEvents(t *testing.T) {
	past := make([]plugin.SessionEvent, 0, sessionEvents+5)
	for i := range sessionEvents + 5 {
		past = append(past, plugin.SessionEvent{Kind: plugin.SessionMessage, Text: string(rune('a' + i%26)), N: i, At: sessionsEpoch})
	}
	s := &fakeSessions{list: []plugin.SessionInfo{{ID: "abc"}}, past: past}
	w := newWindow(&profile{sessions: s}, "Luna")
	t.Cleanup(w.Close)

	track, err := w.Session("abc")
	if err != nil {
		t.Fatal(err)
	}
	if len(track.Events) != sessionEvents {
		t.Fatalf("%d events, want %d", len(track.Events), sessionEvents)
	}
	if last := track.Events[len(track.Events)-1]; last.N != sessionEvents+4 {
		t.Errorf("the latest event is %+v, want N %d", last, sessionEvents+4)
	}
}

// A session that says nothing yet has no events, and not nil: the page reads
// a list either way.
func TestOneSessionWithoutEvents(t *testing.T) {
	s := &fakeSessions{list: []plugin.SessionInfo{{ID: "abc", Title: "Neu"}}}
	w := newWindow(&profile{sessions: s}, "Luna")
	t.Cleanup(w.Close)

	track, err := w.Session("abc")
	if err != nil {
		t.Fatal(err)
	}
	if track.Events == nil || len(track.Events) != 0 {
		t.Errorf("the events are %#v", track.Events)
	}
	if track.Title != "Neu" {
		t.Errorf("the session is %+v", track.Session)
	}
}

var errTest = &testError{}

type testError struct{}

func (*testError) Error() string { return "the jobs cannot be listed" }
