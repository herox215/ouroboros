package tui

import (
	"slices"
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

// The columns beside the thread show the sessions that run now, wherever it
// is scrolled: the sidebar lists them, the column on the right says where
// they stand, and one that ends leaves both.
func TestColumnsShowTheSessionsThatRun(t *testing.T) {
	m, done, fix := threadWithSessions(t, 170, 46)
	if list := m.runningSessions(); len(list) != 1 || list[0] != fix {
		t.Fatalf("running: %v", list)
	}
	view := strip(m.View().Content)
	for _, want := range []string{"RUNNING", "FIX", "State: working · own", "FIX · WORKING",
		"● calls go test ./...", "next  the result of bash", "4 tools · 3 kinds", "THREAD · READY · 1 ACTIVE SESSION"} {
		if !strings.Contains(view, want) {
			t.Fatalf("no %q beside the thread:\n%s", want, view)
		}
	}
	if strings.Contains(view, "TESTS") || strings.Contains(view, "TODAY") {
		t.Fatalf("a session that does not run is beside the thread:\n%s", view)
	}
	// Scrolling the thread changes nothing beside it.
	m.history.GotoTop()
	if list := m.runningSessions(); len(list) != 1 || list[0] != fix {
		t.Fatalf("scrolled up, %v runs", list)
	}
	if view := strip(m.View().Content); !strings.Contains(view, "FIX · WORKING") || strings.Contains(view, "TESTS") {
		t.Fatalf("scrolled up:\n%s", view)
	}
	// A session that ends leaves both columns.
	m.apply(fix, Event{Kind: plugin.SessionAnswer, Text: "fixed", At: m.now()}, false)
	if list := m.runningSessions(); len(list) != 0 || done.running {
		t.Fatalf("still running: %v", list)
	}
	view = strip(m.View().Content)
	for _, want := range []string{"RUNNING", "Nothing runs."} {
		if !strings.Contains(view, want) {
			t.Fatalf("no %q when nothing runs:\n%s", want, view)
		}
	}
	if strings.Contains(view, "FIX · WORKING") || strings.Contains(view, "● calls") {
		t.Fatalf("a session that ended is still shown:\n%s", view)
	}
	for _, l := range strings.Split(m.View().Content, "\n") {
		if w := lipgloss.Width(l); w != m.width {
			t.Fatalf("a line of %d cells in %d: %q", w, m.width, strip(l))
		}
	}
}

// A session of its own has the column on the right to itself: where it
// stands, what it works with and what it did, in more detail than beside the
// thread, and not the other sessions that run.
func TestBesideAConversationOfItsOwn(t *testing.T) {
	m, _, fix := threadWithSessions(t, 170, 46)
	other := m.conversation("e5f6-other")
	m.apply(other, Event{Kind: plugin.SessionMessage, Text: "do else", At: m.now()}, false)
	m.apply(other, Event{Kind: plugin.SessionTitle, Text: "Other"}, false)
	fix.directory, fix.jobs = "/tmp/ouro", []string{"job 1 · npm run dev"}
	m.switchTo(fix)
	view := strip(m.View().Content)
	for _, want := range []string{"To the thread", "SESSION", "FIX · WORKING", "State: working · own",
		"● calls go test ./...", "next  the result of bash", "project /tmp/ouro", "2 of the loop",
		"WORK", "CALLS", "read · scroll.go"} {
		if !strings.Contains(view, want) {
			t.Fatalf("no %q beside the session:\n%s", want, view)
		}
	}
	if rail := strip(m.railView(m.railWidth(), m.height)); strings.Contains(rail, "OTHER") {
		t.Fatalf("another session that runs is beside the one shown:\n%s", rail)
	}
	// The sidebar lists every session that runs, also the other one.
	var list strings.Builder
	for _, r := range m.sidebarRows(30) {
		list.WriteString(strip(r.text) + "\n")
	}
	if !strings.Contains(list.String(), "OTHER") || !strings.Contains(list.String(), "FIX") {
		t.Fatalf("the sidebar:\n%s", list.String())
	}
	// A conversation that does not run has the column to itself too.
	empty := m.newChat()
	m.switchTo(empty)
	if view := strip(m.View().Content); !strings.Contains(view, "SESSION") || !strings.Contains(view, "nothing yet") {
		t.Fatalf("the column beside a new conversation:\n%s", view)
	}
	if rows := m.sidebarRows(30); strings.Contains(strip(rows[len(rows)-1].text), "NEW CONVERSATION") {
		t.Fatalf("a conversation that does not run is listed: %+v", rows)
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

// The sessions that run keep their place in both columns while they work,
// and one that began anew stands last.
func TestRunningSessionsKeepTheirPlace(t *testing.T) {
	m, _ := newModel(t)
	m.chat = m.thread() // the thread lists the sessions that run beside it
	at := time.Date(2026, 10, 26, 9, 0, 0, 0, time.Local)
	m.now = func() time.Time { return at }
	m.Update(tea.WindowSizeMsg{Width: 170, Height: 40})
	ids := []string{"s1", "s2", "s3"}
	for i, id := range ids {
		m.apply(m.conversation(id), Event{Kind: plugin.SessionMessage, Text: id, At: at.Add(time.Duration(i) * time.Minute)}, false)
	}
	order := func() []string {
		var got []string
		for _, c := range m.runningSessions() {
			got = append(got, c.contextID)
		}
		return got
	}
	if got := order(); !slices.Equal(got, ids) {
		t.Fatalf("begun in a row: %v", got)
	}
	// A session that works on changes nothing of the order of the others.
	m.apply(m.chats["s2"], Event{Kind: plugin.SessionStep, Tool: "bash", Text: "bash · go test", At: at.Add(20 * time.Minute)}, false)
	m.apply(m.chats["s1"], Event{Kind: plugin.SessionAnswering, Text: "so", At: at.Add(21 * time.Minute)}, false)
	if got := order(); !slices.Equal(got, ids) {
		t.Fatalf("while they work: %v", got)
	}
	// One that ended gives its place up, and one that begins anew stands last.
	m.apply(m.chats["s1"], Event{Kind: plugin.SessionAnswer, Text: "done", At: at.Add(22 * time.Minute)}, false)
	if got := order(); !slices.Equal(got, []string{"s2", "s3"}) {
		t.Fatalf("after one ended: %v", got)
	}
	m.apply(m.chats["s1"], Event{Kind: plugin.SessionMessage, Text: "again", At: at.Add(23 * time.Minute)}, false)
	if got := order(); !slices.Equal(got, []string{"s2", "s3", "s1"}) {
		t.Fatalf("after one began anew: %v", got)
	}
	// The column on the right shows them in the same order.
	rail := strip(m.railView(m.railWidth(), m.height))
	s2, s3, s1 := strings.Index(rail, "S2 · WORKING"), strings.Index(rail, "S3 · WORKING"), strings.Index(rail, "S1 · WORKING")
	if s2 < 0 || s3 < 0 || s1 < 0 || !(s2 < s3 && s3 < s1) {
		t.Fatalf("the column on the right is in another order (%d, %d, %d):\n%s", s2, s3, s1, rail)
	}
}

// A lane of the chart keeps its place and its shade while its tool is used
// more: the way an answer goes about its tools does not make the picture
// jump about.
func TestToolChartKeepsTheOrderOfItsLanes(t *testing.T) {
	m, _ := newModel(t)
	at := time.Unix(1000, 0)
	used := func(tool string, minute int) call {
		return call{tool: tool, text: tool, done: true, at: at.Add(time.Duration(minute) * time.Minute)}
	}
	first := m.toolChart(stand{calls: []call{used("read", 0), used("bash", 1), used("bash", 2)}}, 40)
	again := m.toolChart(stand{calls: []call{used("read", 0), used("bash", 1), used("bash", 2), used("bash", 3)}}, 40)
	if len(first) != 3 || !strings.HasPrefix(strip(first[0]), "read ") || !strings.HasPrefix(strip(first[1]), "bash ") {
		t.Fatalf("the chart:\n%s", strip(strings.Join(first, "\n")))
	}
	if first[0] != again[0] || !strings.HasPrefix(strip(again[1]), "bash ") || !strings.HasSuffix(strip(again[1]), " 3") {
		t.Fatalf("the lanes moved:\n%s\n%s", strip(strings.Join(first, "\n")), strip(strings.Join(again, "\n")))
	}
}

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

// More sessions than the column on the right has room for say the rest is
// cut; the sidebar lists them and scrolls to the one shown.
func TestManyRunningSessionsBeside(t *testing.T) {
	m, _, _ := threadWithSessions(t, 170, 24)
	for _, id := range []string{"c3d4-a", "c3d4-b", "c3d4-c", "c3d4-d"} {
		s := m.conversation(id)
		m.apply(s, Event{Kind: plugin.SessionMessage, Text: id, At: m.now()}, false)
	}
	if n := len(m.runningSessions()); n != 5 {
		t.Fatalf("%d sessions run", n)
	}
	rail := strip(m.railView(m.railWidth(), m.height))
	if !strings.Contains(rail, "…") {
		t.Fatalf("the column on the right:\n%s", rail)
	}
	m.switchTo(m.chats["c3d4-d"])
	rows, _ := m.sidebarLayout(30, m.height)
	shown := false
	for _, r := range rows {
		shown = shown || r.c == m.chat
	}
	if !shown || strip(rows[0].text) != "RUNNING" {
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
