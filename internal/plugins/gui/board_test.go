package gui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	store "ouroboros/internal/board"
	"ouroboros/internal/config"
	"ouroboros/internal/home"
	"ouroboros/internal/web"
)

var boardEpoch = time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)

// boardOf is the board of the profile in dir, as the node keeps it.
func boardOf(t *testing.T, dir home.Dir) *store.Store {
	t.Helper()
	st, err := store.Open(dir.Board(), boardEpoch)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// someBoard puts three tickets on the board of dir: one a session refines,
// one blocked with a long reason, one done, and a look of the orchestrator.
func someBoard(t *testing.T, dir home.Dir) *store.Store {
	t.Helper()
	st := boardOf(t, dir)
	must := func(_ store.Ticket, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.Create("Dark mode", "the settings need one", "/srv/app", boardEpoch))
	must(st.Create("Export", "as CSV", "", boardEpoch.Add(time.Minute)))
	must(st.Create("Typo", "in the footer", "", boardEpoch.Add(2*time.Minute)))
	must(st.Create("Search", "over all notes", "", boardEpoch.Add(3*time.Minute)))
	must(st.Start(1, "0123456789abcdef", "agent-2f288108", "Jeff", boardEpoch.Add(4*time.Minute)))
	must(st.Move(2, store.Blocked, "1. Which\n  separator? "+strings.Repeat("é", 150), boardEpoch.Add(5*time.Minute)))
	must(st.Tell(2, boardEpoch.Add(6*time.Minute)))
	must(st.Comment(2, store.Thread, "a semicolon", boardEpoch.Add(7*time.Minute)))
	must(st.Move(3, store.Done, "", boardEpoch.Add(8*time.Minute)))
	return st
}

// The window shows the board by its lanes, the latest ticket of each first,
// with how each stands, and says when the orchestrator looked last.
func TestWindowShowsTheBoard(t *testing.T) {
	dir := home.Dir(t.TempDir())
	w := newWindow(&profile{dir: dir}, "Luna")
	t.Cleanup(w.Close)
	// A profile without a board has an empty one, with every lane there.
	empty, err := w.Board()
	if err != nil {
		t.Fatal(err)
	}
	if raw, _ := json.Marshal(empty); string(raw) != `{"lanes":[{"column":"new","cards":[]},{"column":"ready","cards":[]},{"column":"progress","cards":[]},{"column":"blocked","cards":[]},{"column":"done","cards":[]}],"pending":false}` {
		t.Fatalf("an empty board: %s", raw)
	}
	st := someBoard(t, dir)
	board, err := w.Board()
	if err != nil {
		t.Fatal(err)
	}
	if !board.Pending || board.Looked != nil {
		t.Fatalf("a board nobody looked at: %+v", board)
	}
	ids := func(lane Lane) (out []int) {
		for _, c := range lane.Cards {
			out = append(out, c.ID)
		}
		return out
	}
	for i, want := range []struct {
		column string
		ids    string
	}{{store.New, "[4 1]"}, {store.Ready, "[]"}, {store.Progress, "[]"}, {store.Blocked, "[2]"}, {store.Done, "[3]"}} {
		if lane := board.Lanes[i]; lane.Column != want.column || fmt.Sprint(ids(lane)) != want.ids {
			t.Fatalf("lane %d: %s with %v, want %s with %s", i, lane.Column, ids(lane), want.column, want.ids)
		}
	}
	refined := board.Lanes[0].Cards[1]
	began := boardEpoch.Add(4 * time.Minute)
	if refined.Title != "Dark mode" || refined.Column != store.New || refined.Role != store.Refinement || refined.Session != "0123456789abcdef" || refined.Agent != "agent-2f288108" || refined.AgentName != "Jeff" ||
		refined.Since == nil || !refined.Since.Equal(began) || refined.Reason != "" || refined.Notes != 1 || !refined.Changed.Equal(began) {
		t.Fatalf("the card of a ticket a session refines: %+v", refined)
	}
	if idle := board.Lanes[0].Cards[0]; idle.Role != "" || idle.Session != "" || idle.Since != nil || idle.Agent != "" || idle.Notes != 0 {
		t.Fatalf("the card of a ticket nobody works on: %+v", idle)
	}
	blocked := board.Lanes[3].Cards[0]
	if want := "1. Which separator? " + strings.Repeat("é", 110) + " …"; blocked.Reason != want || blocked.From != store.New || !blocked.Told || blocked.Notes != 3 {
		t.Fatalf("the card of a blocked ticket: %+v", blocked)
	}
	// A reason that fits is shown whole.
	if _, err := st.Move(4, store.Blocked, strings.Repeat("x", cardReason), boardEpoch); err != nil {
		t.Fatal(err)
	}
	changes, _ := st.Pending()
	if _, err := st.Looked(changes, "look-session", "Nothing to do.", boardEpoch.Add(9*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Looked(changes, "last-look", "Told the thread.", boardEpoch.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	board, err = w.Board()
	if err != nil {
		t.Fatal(err)
	}
	if got := board.Lanes[3].Cards[0]; got.ID != 4 || got.Reason != strings.Repeat("x", cardReason) {
		t.Fatalf("a reason that fits: %+v", got)
	}
	if board.Pending || board.Looked == nil || board.Looked.Session != "last-look" || board.Looked.Result != "Told the thread." {
		t.Fatalf("a board the orchestrator looked at: %+v", board.Looked)
	}
	// A ticket in full has what its card leaves out.
	ticket, err := w.Ticket(2)
	if err != nil {
		t.Fatal(err)
	}
	if ticket.Title != "Export" || ticket.Text != "as CSV" || !strings.HasPrefix(ticket.Reason, "1. Which\n  separator?") || len(ticket.Notes) != 3 || ticket.Notes[2].Text != "a semicolon" {
		t.Fatalf("the ticket in full: %+v", ticket)
	}
	if _, err := w.Ticket(9); err == nil || err.Error() != "the board has no ticket 9 any more" {
		t.Fatalf("a ticket that is not there: %v", err)
	}
	// A board that cannot be read is an error, not an empty board.
	if err := os.WriteFile(dir.Board(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Board(); err == nil {
		t.Fatal("a broken board was shown")
	}
	if _, err := w.Ticket(1); err == nil {
		t.Fatal("a ticket of a broken board was shown")
	}
}

// The page of the thread shows the board as the window does, behind the
// login.
func TestThePageShowsTheBoard(t *testing.T) {
	dir := home.Dir(t.TempDir())
	hash, err := web.Hash("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Save(string(dir), config.Config{Model: "m", Web: config.Web{Enabled: true, User: "owner", Password: hash}}); err != nil {
		t.Fatal(err)
	}
	p := fakeProfile("pythia")
	p.dir = dir
	server := httptest.NewServer(newWebFront(dir, p).handler)
	t.Cleanup(server.Close)
	client := stillClient()
	someBoard(t, dir)
	for _, path := range []string{"/api/board", "/api/ticket?id=1"} {
		res := do(t, client, http.MethodGet, server.URL+path, nil, nil)
		if res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s without a login: %d", path, res.StatusCode)
		}
		res.Body.Close()
	}
	login := signIn(t, client, server.URL, "owner", "correct horse battery")
	cookie := sessionOf(t, login)
	login.Body.Close()
	res := do(t, client, http.MethodGet, server.URL+"/api/board", cookie, nil)
	if res.StatusCode != http.StatusOK || !strings.HasPrefix(res.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("the board: %d, %q", res.StatusCode, res.Header.Get("Content-Type"))
	}
	var board Board
	if err := json.Unmarshal([]byte(body(t, res)), &board); err != nil {
		t.Fatal(err)
	}
	if len(board.Lanes) != 5 || len(board.Lanes[0].Cards) != 2 || board.Lanes[4].Cards[0].Title != "Typo" || !board.Pending {
		t.Fatalf("the board of the page: %+v", board)
	}
	res = do(t, client, http.MethodGet, server.URL+"/api/ticket?id=1", cookie, nil)
	var ticket store.Ticket
	if err := json.Unmarshal([]byte(body(t, res)), &ticket); err != nil || res.StatusCode != http.StatusOK {
		t.Fatal(res.StatusCode, err)
	}
	if ticket.ID != 1 || ticket.Text != "the settings need one" || ticket.Directory != "/srv/app" || len(ticket.Runs) != 1 {
		t.Fatalf("the ticket of the page: %+v", ticket)
	}
	res = do(t, client, http.MethodGet, server.URL+"/api/ticket?id=nine", cookie, nil)
	if got := body(t, res); res.StatusCode != http.StatusBadRequest || !strings.Contains(got, "a ticket is named by its number") {
		t.Fatalf("a ticket without a number: %d, %q", res.StatusCode, got)
	}
	res = do(t, client, http.MethodGet, server.URL+"/api/ticket?id=9", cookie, nil)
	if got := body(t, res); res.StatusCode != http.StatusConflict || got != "the board has no ticket 9 any more" {
		t.Fatalf("a ticket that is not there: %d, %q", res.StatusCode, got)
	}
	if err := os.WriteFile(dir.Board(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	res = do(t, client, http.MethodGet, server.URL+"/api/board", cookie, nil)
	if got := body(t, res); res.StatusCode != http.StatusConflict || !strings.Contains(got, "cannot be read") {
		t.Fatalf("a broken board: %d, %q", res.StatusCode, got)
	}
}
