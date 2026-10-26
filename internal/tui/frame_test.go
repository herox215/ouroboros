package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"ouroboros/plugin"
)

// threadWithSessions is a UI on the thread in a window of w×h, as the GUI
// shows it: two parts, the first handed to the session "a1b2-tests", which
// is done, the second to "c3d4-fix", which calls bash.
func threadWithSessions(t *testing.T, w, h int) (m *Model, done, fix *conversation) {
	t.Helper()
	m, _ = newModel(t)
	at := time.Date(2026, 10, 23, 9, 0, 0, 0, time.Local)
	m.now = func() time.Time { return at.Add(time.Hour) }
	m.chat = m.thread()
	c := m.chat
	ev := func(c *conversation, e Event) { m.apply(c, e, false) }
	ev(c, Event{Kind: plugin.SessionMessage, Text: "run the tests", At: at})
	ev(c, Event{Kind: plugin.SessionHanded, Text: "a1b2"})
	ev(c, Event{Kind: plugin.SessionAnswer, Text: numbered(1500), At: at.Add(time.Minute)})
	ev(c, Event{Kind: plugin.SessionMessage, Text: "fix the first", At: at.Add(30 * time.Minute)})
	ev(c, Event{Kind: plugin.SessionHanded, Text: "c3d4"})
	ev(c, Event{Kind: plugin.SessionHanded, Text: "c3d4"}) // told twice, listed once
	ev(c, Event{Kind: plugin.SessionHanded})               // names none
	ev(c, Event{Kind: plugin.SessionAnswer, Text: "fixed", At: at.Add(31 * time.Minute)})

	done = m.conversation("a1b2-tests")
	ev(done, Event{Kind: plugin.SessionMessage, Text: "run them", At: at})
	ev(done, Event{Kind: plugin.SessionTitle, Text: "Tests"})
	ev(done, Event{Kind: plugin.SessionStep, Tool: "bash", Text: "bash · go test", At: at})
	ev(done, Event{Kind: plugin.SessionStep, Tool: "bash", Text: "bash · go test", Done: true, At: at.Add(time.Minute)})
	ev(done, Event{Kind: plugin.SessionAnswer, Text: "two fail", At: at.Add(2 * time.Minute)})

	fix = m.conversation("c3d4-fix")
	ev(fix, Event{Kind: plugin.SessionTask, Text: "task 1 · fix glide", At: at.Add(30 * time.Minute)})
	ev(fix, Event{Kind: plugin.SessionTitle, Text: "Fix"})
	for i, tool := range []string{"read", "edit", "bash"} {
		at := at.Add(time.Duration(31+i) * time.Minute)
		ev(fix, Event{Kind: plugin.SessionStep, Tool: tool, Text: tool + " · scroll.go", At: at})
		ev(fix, Event{Kind: plugin.SessionStep, Tool: tool, Text: tool + " · scroll.go", Done: true, At: at})
	}
	ev(fix, Event{Kind: plugin.SessionStep, Tool: "bash", Text: "bash · go test ./...", At: at.Add(50 * time.Minute)})
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return m, done, fix
}

// strip is what the screen shows of s, without its colors.
func strip(s string) string { return ansi.Strip(s) }

// Beside the thread, the sidebar lists the sessions of the part of it in
// view, as the GUI does, and the column on the right says where they stand:
// at the end the newest part, scrolled up the part in the middle.
func TestBesideThePartOfTheThreadInView(t *testing.T) {
	m, done, fix := threadWithSessions(t, 170, 46)
	if got := m.chat.handed[m.chat.exchangeOf(len(m.chat.entries)-1)]; len(got) != 1 || got[0] != "c3d4" {
		t.Fatalf("the newest part is handed to %q", got)
	}
	list, at, thread := m.sidebarSessions()
	if len(list) != 1 || list[0] != fix || !thread || at.IsZero() {
		t.Fatalf("beside the newest part: %d sessions, at %v", len(list), at)
	}
	view := strip(m.View().Content)
	for _, want := range []string{"SESSIONS", "T – 09:30", "ACTIVE", "FIX", "State: working · own", "CONTEXT", "[STATE]",
		"FIX · WORKING", "● calls go test ./...", "next  the result of bash", "4 tools · 3 kinds", "THREAD · READY · 1 ACTIVE SESSION"} {
		if !strings.Contains(view, want) {
			t.Fatalf("no %q beside the thread:\n%s", want, view)
		}
	}
	if strings.Contains(view, "TESTS") {
		t.Fatalf("a session of the older part is beside the newest:\n%s", view)
	}

	m.history.GotoTop()
	if list, _, _ := m.sidebarSessions(); len(list) != 1 || list[0] != done {
		t.Fatalf("scrolled up, %d sessions are beside it", len(list))
	}
	view = strip(m.View().Content)
	for _, want := range []string{"T – 09:00", "TODAY", "TESTS", "TESTS", "● done", "last  bash · go test", "1 tool · 1 kind"} {
		if !strings.Contains(view, want) {
			t.Fatalf("no %q beside the older part:\n%s", want, view)
		}
	}
	// Each column is as wide as it is, and the screen as the window.
	for _, l := range strings.Split(m.View().Content, "\n") {
		if w := lipgloss.Width(l); w != m.width {
			t.Fatalf("a line of %d cells in %d: %q", w, m.width, strip(l))
		}
	}
}

func TestExpiredThreadHasNoSessionsBesideIt(t *testing.T) {
	m, _, fix := threadWithSessions(t, 170, 46)
	entries := len(m.chat.entries)
	now := m.now().Add(24 * time.Hour)
	m.now = func() time.Time { return now }
	m.Update(tickMsg{})
	if len(m.chat.entries) != entries || len(m.blockStarts) != 0 {
		t.Fatal("expired thread was not hidden or archived entries were changed")
	}
	if list, at, thread := m.sidebarSessions(); len(list) != 0 || !at.IsZero() || !thread {
		t.Fatalf("empty thread has %d sessions beside it, at %v", len(list), at)
	}
	if title, note, details := m.railContent(30); title != "[STATE]" || note != "None yet" || len(details) != 0 {
		t.Fatalf("empty thread context: %q, %q, %v", title, note, details)
	}
	view := strip(m.View().Content)
	for _, old := range []string{"FIX", "TESTS", "T –", "go test"} {
		if strings.Contains(view, old) {
			t.Fatalf("empty thread still shows %q:\n%s", old, view)
		}
	}
	m.apply(m.chat, Event{Kind: plugin.SessionMessage, Text: "continue fixing", At: now}, false)
	m.apply(m.chat, Event{Kind: plugin.SessionHanded, Text: "c3d4"}, false)
	m.renderHistory(true)
	if list, _, _ := m.sidebarSessions(); len(list) != 1 || list[0] != fix {
		t.Fatal("new visible exchange does not show its own session")
	}
}

// A part of the thread that is handed to no session has none beside it, and
// another conversation lists all of the sessions of the owner, the one shown
// selected, with its own stand on the right.
func TestBesideAConversationOfItsOwn(t *testing.T) {
	m, _, fix := threadWithSessions(t, 170, 46)
	m.apply(m.chat, Event{Kind: plugin.SessionMessage, Text: "and now?", At: m.now()}, false)
	m.renderHistory(true)
	if view := strip(m.View().Content); !strings.Contains(view, "None yet") || strings.Contains(view, "FIX") {
		t.Fatalf("a part handed to none:\n%s", view)
	}
	m.switchTo(fix)
	view := strip(m.View().Content)
	for _, want := range []string{"To the thread", "FIX", "TESTS", "SESSION · ", "FIX · WORKING", "T – "} {
		if !strings.Contains(view, want) {
			t.Fatalf("no %q beside a conversation of its own:\n%s", want, view)
		}
	}
	empty := m.newChat()
	m.switchTo(empty)
	if title, note, details := m.railContent(30); title != "[STATE]" || note != "None yet" || details != nil {
		t.Fatalf("a new conversation stands at %q, %q, %q", title, note, details)
	}
	if rows := m.sidebarRows(30); !strings.Contains(strip(rows[len(rows)-5].text), "NEW CONVERSATION") {
		t.Fatalf("the new conversation is not listed: %+v", rows)
	}
}

// Where a session stands, as its events tell, and what the column on the
// right says of it.
func TestStandOfASession(t *testing.T) {
	m, _ := newModel(t)
	at := time.Unix(1000, 0)
	for _, tt := range []struct {
		name   string
		c      conversation
		phase  string
		want   []string
		live   bool
		failed bool
	}{
		{"empty", conversation{}, "empty", []string{"nothing yet", "last  no call yet", "next  nothing"}, false, false},
		{"asked", conversation{asking: &asking{question: "Which one?"}, entries: []entry{{mine: true}}}, "asked", []string{"asks you", "question Which one?", "next  your answer"}, true, false},
		{"thinking", conversation{pending: true, entries: []entry{{mine: true}}}, "thinking", []string{"thinks", "next  a tool or the answer"}, true, false},
		{"answering", conversation{running: true, partial: "so", entries: []entry{{mine: true}}}, "answering", []string{"writes the answer", "next  the end of the answer"}, true, false},
		{"calling", conversation{pending: true, entries: []entry{{calls: []call{{text: "bash", tool: "bash", at: at}}}}}, "tool", []string{"● calls", "next  the result of bash"}, true, false},
		{"done", conversation{entries: []entry{{calls: []call{{text: "read · a", tool: "read", done: true, failed: true, at: at}}}, {text: "ok"}}}, "done", []string{"done", "last  read · a"}, false, false},
		{"failed", conversation{entries: []entry{{text: "broke", failed: true}}}, "failed", []string{"failed"}, false, true},
		{"canceled", conversation{entries: []entry{{text: "stopped before it answered", failed: true}}}, "canceled", []string{"canceled"}, false, false},
		{"jobs", conversation{pending: true, jobs: []string{"job 1 · serve", "job 2 · watch"}, entries: []entry{{mine: true}}}, "thinking", []string{"running job 1 · serve …"}, true, false},
	} {
		s := standOf(&tt.c)
		lines := m.artefact(&tt.c, 60)
		text := strip(strings.Join(lines, "\n"))
		if s.phase != tt.phase || s.live != tt.live || s.bad != tt.failed || strings.Contains(text, "WORKING") != tt.live && tt.name != "asked" {
			t.Errorf("%s: %+v", tt.name, s)
		}
		for _, want := range tt.want {
			if !strings.Contains(text, want) {
				t.Errorf("%s: no %q in\n%s", tt.name, want, text)
			}
		}
	}
	// A call that is the name of its tool says nothing more of what it does.
	c := conversation{pending: true, entries: []entry{{calls: []call{{text: "bash", tool: "bash"}}}}}
	if s := standOf(&c); s.what != "" {
		t.Fatalf("what the call does: %q", s.what)
	}
	// While a session works, its dot beats.
	c.entries[0].calls[0].text = "bash · go test"
	m.ticks = 0
	still := m.artefact(&c, 60)[1]
	m.ticks = 8
	if m.artefact(&c, 60)[1] == still {
		t.Fatal("the dot of a working session does not beat")
	}
}

// The tools of a session are one chart: a lane for each of the most used,
// their calls where their time puts them, the call on its way blinking.
func TestToolChart(t *testing.T) {
	m, _ := newModel(t)
	at := time.Unix(1000, 0)
	var calls []call
	for i, tool := range []string{"a", "b", "c", "d", "e", "f", "g", "a"} {
		calls = append(calls, call{tool: tool, text: tool, done: true, at: at.Add(time.Duration(i) * time.Minute)})
	}
	lines := m.toolChart(stand{calls: calls}, 40)
	if len(lines) != len(laneShades)+1 || !strings.HasPrefix(strip(lines[0]), "a ") || strip(lines[len(lines)-1]) != "8 tools · 7 kinds · 6 lanes" {
		t.Fatalf("the chart of 7 tools:\n%s", strip(strings.Join(lines, "\n")))
	}
	if m.toolChart(stand{calls: calls}, 15) != nil {
		t.Fatal("a chart too narrow for its calls is drawn")
	}
	if m.toolChart(stand{}, 40) != nil {
		t.Fatal("a chart without calls is drawn")
	}
	// Calls without a time are spread by their order; a call of a tool
	// without a name is named by its text.
	one := m.toolChart(stand{calls: []call{{text: "read · a", done: true}}}, 40)
	if len(one) != 2 || !strings.HasPrefix(strip(one[0]), "read ") || strip(one[1]) != "1 tool · 1 kind" {
		t.Fatalf("the chart of one call:\n%s", strip(strings.Join(one, "\n")))
	}
	pending := call{tool: "bash", text: "bash · x", at: at.Add(9 * time.Minute)}
	s := stand{calls: calls[:2], pending: &pending, live: true}
	m.ticks = 0
	shown := strings.Join(m.toolChart(s, 40), "\n")
	m.ticks = 6
	if blinked := strings.Join(m.toolChart(s, 40), "\n"); blinked == shown || !strings.Contains(strip(shown), "┊") {
		t.Fatalf("the call on its way does not blink, or now is not marked:\n%s", strip(shown))
	}
}

// What is clicked beside the history does what it does in the GUI.
func TestClicksBesideTheHistory(t *testing.T) {
	m, _, fix := threadWithSessions(t, 170, 46)
	thread := m.chat
	rows, top := m.sidebarLayout(m.sideWidth()-2*columnPad, m.height)
	at := -1
	for i, r := range rows {
		if r.c == fix {
			at = top + i
			break
		}
	}
	if at < 0 {
		t.Fatal("the session is not in the sidebar")
	}
	click := func(x, y int) tea.Cmd { return mouseAt(m, tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft}) }
	click(columnPad+1, at)
	if m.chat != fix {
		t.Fatal("a click on a session shows it")
	}
	click(columnPad+1, 0) // the margin above the list
	if m.chat != fix || m.screen != screenChat {
		t.Fatal("a click beside the sessions does something")
	}
	if click(columnPad, m.height-2); m.screen != screenSettings {
		t.Fatalf("the button of the settings opens %v", m.screen)
	}
	press(m, "esc")
	right := m.sideWidth() + m.chatWidth() - columnPad
	click(right-2, 2)
	if m.chat != thread {
		t.Fatal("To the thread takes the owner there")
	}
	stop := m.transcriptLeft() + promptWidth + m.composerInner() + 1
	row := m.height - 2 - m.pendingRows()
	if click(stop, row) != nil {
		t.Fatal("Stop without an answer on its way does something")
	}
	m.chat.pending = true
	if click(stop, row) == nil {
		t.Fatal("Stop does not stop the answer")
	}
	m.chat.pending = false
	m.input.SetValue("hello")
	if click(stop+stopWidth+1, row) == nil || m.input.Value() != "" {
		t.Fatal("the button sends what is written")
	}
	// A window without a sidebar has nothing in it.
	m.Update(tea.WindowSizeMsg{Width: 60, Height: 30})
	if m.sidebarAt(5) != nil {
		t.Fatal("a session in a sidebar that is not there")
	}
}

// The sessions of the sidebar are grouped by when they last changed.
func TestDateGroups(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.Local)
	for at, want := range map[time.Time]string{
		{}:                       "No date",
		now.Add(-time.Hour):      "Today",
		now.Add(-20 * time.Hour): "Yesterday",
		now.AddDate(0, 0, -3):    "Older",
	} {
		if got := dateGroup(at, now); got != want {
			t.Errorf("%v: %q, want %q", at, got, want)
		}
	}
	if got := clock(now.AddDate(0, 0, -3), now); got != "20 Oct 2026, 12:00" {
		t.Errorf("another day: %q", got)
	}
}

// A part of the thread handed to more sessions than the column on the right
// has room for says the rest is cut, and the sidebar scrolls to the one
// shown.
func TestManySessionsBesideThePart(t *testing.T) {
	m, _, _ := threadWithSessions(t, 170, 24)
	for _, id := range []string{"c3d4-a", "c3d4-b", "c3d4-c", "c3d4-d"} {
		s := m.conversation(id)
		m.apply(s, Event{Kind: plugin.SessionMessage, Text: id, At: m.now()}, false)
		m.apply(s, Event{Kind: plugin.SessionAnswer, Text: "ok", At: m.now()}, false)
	}
	_, _, details := m.railContent(30)
	rail := strip(m.railView(m.railWidth(), m.height))
	if len(details) < m.height || !strings.Contains(rail, "…") {
		t.Fatalf("the column on the right:\n%s", rail)
	}
	m.switchTo(m.chats["c3d4-d"])
	rows, _ := m.sidebarLayout(30, m.height)
	shown := false
	for _, r := range rows {
		shown = shown || r.c == m.chat
	}
	if !shown || strip(rows[0].text) != "SESSIONS" {
		t.Fatalf("the sidebar does not scroll to the one shown: %+v", rows)
	}
}

// A line faded towards the top keeps its colors, each nearer the
// background, and what has none gets the color of the text.
func TestFadeLineKeepsItsColors(t *testing.T) {
	line := "a\x1b[38;5;3mb\x1b[mc\x1b[39md\x1b[1;38;2;255;0;0;48;2;0;0;255me\x1b[0m"
	got := fadeLine(line, 0.5)
	if strip(got) != "abcde" || strings.Contains(got, "38;5;3") || strings.Contains(got, "255;0;0") || !strings.Contains(got, "\x1b[1;38;2;") {
		t.Fatalf("faded %q", got)
	}
	text := lipgloss.NewStyle().Foreground(blend(ui.background, ui.text, 0.5)).Render("x")
	if !strings.HasPrefix(got, text[:strings.Index(text, "x")]) {
		t.Fatalf("what has no color is not in the faded text color: %q", got)
	}
}

// Answers that do not fit in a row go on in the next, and down goes around
// them and the composer.
func TestQuestionButtonsWrap(t *testing.T) {
	m := asksThis(t, `{"question":"Which?","options":["`+strings.Repeat("a", 40)+`","`+strings.Repeat("b", 40)+`","c"]}`)
	panel := strip(strings.Join(m.panelLines(), "\n"))
	if !strings.Contains(panel, strings.Repeat("a", 40)+" \n "+strings.Repeat("b", 40)) {
		t.Fatalf("the answers in one row:\n%s", panel)
	}
	a := m.chat.asking
	press(m, "down", "down", "down")
	if !a.own() {
		t.Fatalf("down from the last answer goes to own words: %d", a.sel)
	}
	if press(m, "down"); a.sel != len(a.options) {
		t.Fatalf("down from own words is the composer's: %d", a.sel)
	}
}

// A screen that takes the whole window heads it with what the chat heads
// itself with, on one line, and keeps the line of what waits for a decision.
func TestTheHeadOfAWholeScreen(t *testing.T) {
	m, b := newModel(t)
	b.trust = Contacts{Suggestions: []plugin.Suggestion{{ID: "s1", Text: "faster"}}}
	m.Update(m.loadContacts())
	run(m, "/context")
	view := strip(m.View().Content)
	lines := strings.Split(view, "\n")
	if !strings.HasPrefix(lines[0], " OUROBOROS · NEW CONVERSATION · ") || !strings.Contains(lines[0], "✦ 1 SUGGESTIONS") ||
		!strings.Contains(lines[len(lines)-1], "● 1 suggestions") {
		t.Fatalf("the context:\n%s", view)
	}
}
