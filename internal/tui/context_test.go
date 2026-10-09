package tui

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"ouroboros/plugin"
)

// The chat shows how long the context of the conversation is, from the first
// call of the LLM on; a new conversation has none yet.
func TestChatShowsTheContext(t *testing.T) {
	m, b := newModel(t)
	if view := ansi.Strip(m.View().Content); strings.Contains(view, "context") {
		t.Fatalf("before the first call:\n%s", view)
	}
	b.contextSize = 46285
	b.says = []string{"I will ask beta"}
	b.steps = []Step{{Text: "asking beta"}, {Text: "asking beta", Done: true}}
	answered(t, m, say(t, m, "ask beta"))
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "alpha  · context 45.2 KiB ≈ 11.6k tokens") || strings.Contains(view, "cached") {
		t.Fatalf("after the answer:\n%s", view)
	}
	// Once the API told what it had cached, the chat shows it too: of the
	// last call, and of the session.
	b.cache = plugin.SessionCache{Tokens: 1000, Cached: 920, AllTokens: 3000, AllCached: 2520}
	answered(t, m, say(t, m, "again"))
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "alpha  · context 45.2 KiB ≈ 11.6k tokens  · cached 92%, session 84%") {
		t.Fatalf("with the cache:\n%s", view)
	}
	run(m, "/new")
	if m.chat.context != 0 || m.chat.cache.Tokens != 0 || strings.Contains(ansi.Strip(m.View().Content), "context") {
		t.Fatalf("a new conversation: %d", m.chat.context)
	}
	for n, want := range map[int]string{1: "0.0 KiB ≈ 0 tokens", 3999: "3.9 KiB ≈ 999 tokens", 4000: "3.9 KiB ≈ 1.0k tokens", 409600: "400.0 KiB ≈ 102.4k tokens"} {
		if got := contextSize(n); got != want {
			t.Errorf("%d: %q; want %q", n, got, want)
		}
	}
}

// /context shows what the LLM is sent, message by message, and scrolls
// through it; esc goes back to the chat.
func TestContextScreenShowsTheContext(t *testing.T) {
	m, b := newModel(t)
	b.contextSize = 4000
	b.cache = plugin.SessionCache{Tokens: 1000, Cached: 500, AllTokens: 1000, AllCached: 500}
	answered(t, m, say(t, m, "ping"))
	b.contents = []Message{{"system", "You are alpha."}, {"tool run", "Run JavaScript"}, {"user", "ping"}, {"assistant", "→ run {}"},
		{"tool", strings.Repeat("line\n", 60)}, {"assistant", "pong"}}
	cmd := run(m, "/context")
	if m.screen != screenContext || m.input.Value() != "" {
		t.Fatal("/context should open the context screen")
	}
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "…") || !strings.Contains(view, "Context  3.9 KiB ≈ 1.0k tokens at the last call, cached 50%, session 50%") {
		t.Fatalf("while the context is on its way:\n%s", view)
	}
	m.Update(cmd())
	view := ansi.Strip(m.View().Content)
	for _, want := range []string{" system  #1 ─", "─ 14 bytes", "▎ You are alpha.", " tool run  #2 ─", " user  #3 ─", "─ 4 bytes", " assistant  #4 ─", "─ 10 bytes", "▎ → run ", "0%"} {
		if !strings.Contains(view, want) {
			t.Fatalf("the context should show %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "pong") || strings.Contains(view, "{}") {
		t.Fatalf("the end is below the screen, and a call without arguments has none:\n%s", view)
	}
	for _, k := range []string{"down", "j", "pgdown", "end"} {
		scroll(t, m, press(m, k)) // each key glides on its own
	}
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "pong") || !strings.Contains(view, "100%") {
		t.Fatalf("the end should be in sight:\n%s", view)
	}
	for _, k := range []string{"up", "k", "pgup", "home"} {
		scroll(t, m, press(m, k)) // each key glides on its own
	}
	if m.context.port.YOffset() != 0 {
		t.Fatalf("home should go to the top: %d", m.context.port.YOffset())
	}
	turn(t, m, tea.MouseWheelDown)
	if m.context.port.YOffset() == 0 || m.history.YOffset() != 0 {
		t.Fatal("the wheel should scroll the context, not the chat")
	}
	m.Update(tea.WindowSizeMsg{Width: 60, Height: 20})
	if m.context.port.Width() != 56 || m.context.port.Height() != 14 {
		t.Fatalf("the context should fit the window: %dx%d", m.context.port.Width(), m.context.port.Height())
	}
	press(m, "pgdown")
	if m.glide == nil {
		t.Fatal("a page key should start a glide of the context")
	}
	press(m, "esc")
	if m.glide != nil || m.screen != screenChat {
		t.Fatal("esc should end the scroll of the context and go back to the chat")
	}
}

// A context the backend cannot give in full shows why, with what there is;
// an empty one says so, and one of a conversation that was left is dropped.
func TestContextScreenWithoutAContext(t *testing.T) {
	m, b := newModel(t)
	b.fail = map[string]error{"contents": errors.New("the node is not set up")}
	m.Update(run(m, "/context")())
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "✗ the node is not set up") || !strings.Contains(view, "Nothing yet.") || strings.Contains(view, "at the last call") {
		t.Fatalf("the error should be shown:\n%s", view)
	}
	press(m, "esc")
	b.fail = nil
	b.contents = []Message{{"user", "of another conversation"}}
	cmd := run(m, "/context")
	m.newConversation()
	m.Update(cmd())
	if strings.Contains(ansi.Strip(m.View().Content), "of another conversation") {
		t.Fatal("the context of a conversation that was left should be dropped")
	}
}

// rowLines is a tool result of n lines: "row 00", "row 01" and on.
func rowLines(n int) string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = fmt.Sprintf("row %02d", i)
	}
	return strings.Join(lines, "\n")
}

// searchable opens the context screen on a context of 60 rows.
func searchable(t *testing.T) *Model {
	t.Helper()
	m, b := newModel(t)
	b.contents = []Message{{"tool", rowLines(60)}}
	m.Update(run(m, "/context")())
	return m
}

// lineOf is the line of the context screen text is on.
func lineOf(t *testing.T, m *Model, text string) int {
	t.Helper()
	for i, l := range m.context.lines {
		if strings.Contains(l, text) {
			return i
		}
	}
	t.Fatalf("%q is not in the context", text)
	return 0
}

// markedAt is the line of the view, without colors, where text is marked
// with style; "" when it is nowhere.
func markedAt(m *Model, style lipgloss.Style, text string) string {
	for _, l := range strings.Split(m.View().Content, "\n") {
		if strings.Contains(l, style.Render(text)) {
			return ansi.Strip(l)
		}
	}
	return ""
}

// search types text after / and gives what the last key returned.
func search(m *Model, text string) tea.Cmd {
	keys := []string{"/"}
	for _, r := range text {
		keys = append(keys, string(r))
	}
	return press(m, keys...)
}

// / searches the context as it is typed and marks every match; the arrows
// go from match to match, around the ends. enter keeps the search, esc ends
// it, and only then goes back to the chat.
func TestContextScreenSearches(t *testing.T) {
	m := searchable(t)
	press(m, "/")
	view := ansi.Strip(m.View().Content)
	if !m.context.typing || !strings.Contains(view, "/") || !strings.Contains(view, "  no match") ||
		strings.TrimSpace(footer(m)) != "↑↓ previous/next match · enter keep · esc end search · ctrl+c quit" {
		t.Fatalf("/ should start a search:\n%s", view)
	}
	if press(m, "down") != nil || press(m, "up") != nil {
		t.Fatal("without a match there is nowhere to go")
	}
	for _, k := range strings.Split("row 1", "") {
		press(m, k)
	}
	view = ansi.Strip(m.View().Content)
	if !strings.Contains(view, "/row 1") || !strings.Contains(view, "  1/10") {
		t.Fatalf("the matches should be counted:\n%s", view)
	}
	if !strings.Contains(markedAt(m, currentStyle, "row 1"), "row 10") || !strings.Contains(markedAt(m, matchStyle, "row 1"), "row 11") ||
		markedAt(m, matchStyle, "row 0") != "" {
		t.Fatal("the match the search is at should be marked other than the rest")
	}
	press(m, "left") // moves in what is typed, which stays as it is
	press(m, "down")
	if !strings.Contains(ansi.Strip(m.View().Content), "2/10") || !strings.Contains(markedAt(m, currentStyle, "row 1"), "row 11") {
		t.Fatal("down should go to the next match")
	}
	press(m, "up", "up")
	if !strings.Contains(ansi.Strip(m.View().Content), "10/10") {
		t.Fatal("up from the first match should go to the last")
	}
	press(m, "down")
	if !strings.Contains(ansi.Strip(m.View().Content), "1/10") {
		t.Fatal("down from the last match should go to the first")
	}
	press(m, "enter")
	view = ansi.Strip(m.View().Content)
	if m.context.typing || !strings.Contains(view, "/row 1") || !strings.Contains(view, "  1/10") ||
		strings.TrimSpace(footer(m)) != "↑↓ previous/next match · j/k pgup/pgdn scroll · / search anew · esc end search · ctrl+c quit" {
		t.Fatalf("enter should keep the search:\n%s", view)
	}
	press(m, "down")
	if !strings.Contains(ansi.Strip(m.View().Content), "2/10") {
		t.Fatal("a kept search should go from match to match too")
	}
	press(m, "up", "up")
	if !strings.Contains(ansi.Strip(m.View().Content), "10/10") {
		t.Fatal("a kept search should go back around the start too")
	}
	scroll(t, m, press(m, "j")) // j and k still scroll, and the page keys
	if m.context.port.YOffset() != 1 {
		t.Fatalf("j should scroll a line down: %d", m.context.port.YOffset())
	}
	scroll(t, m, press(m, "k"))
	if m.context.port.YOffset() != 0 {
		t.Fatalf("k should scroll a line up: %d", m.context.port.YOffset())
	}
	scroll(t, m, press(m, "pgdown"))
	scroll(t, m, press(m, "pgup"))
	if m.context.port.YOffset() != 0 {
		t.Fatalf("pgup should scroll a page up: %d", m.context.port.YOffset())
	}
	press(m, "esc")
	view = ansi.Strip(m.View().Content)
	if m.screen != screenContext || m.context.searching() || len(m.context.matches) != 0 || strings.Contains(view, "/row") ||
		strings.TrimSpace(footer(m)) != "↑↓ pgup/pgdn scroll · / search · esc back · ctrl+c quit" || strings.Contains(m.View().Content, matchStyle.Render("row 1")) {
		t.Fatalf("esc should end the search:\n%s", view)
	}
	press(m, "esc")
	if m.screen != screenChat {
		t.Fatal("esc without a search should go back to the chat")
	}

	m.Update(run(m, "/context")())
	search(m, "row 5")
	press(m, "esc")
	if m.context.searching() || m.screen != screenContext {
		t.Fatal("esc while the search is typed should end it")
	}
	search(m, " ")
	press(m, "enter")
	if m.context.searching() {
		t.Fatal("a search of nothing but spaces should end with enter")
	}
	press(m, "/")
	m.Update(tea.PasteMsg{Content: "row 5"})
	if m.context.search.Value() != "row 5" || len(m.context.matches) != 10 || m.input.Value() != "" {
		t.Fatalf("what is pasted should go into the search: %q, %d", m.context.search.Value(), len(m.context.matches))
	}
	press(m, "enter", "/")
	if m.context.search.Value() != "" || !m.context.typing {
		t.Fatal("/ should start a search anew")
	}
	press(m, "esc")
	m.Update(tea.PasteMsg{Content: "hi"})
	if m.input.Value() != "hi" || m.screen != screenContext {
		t.Fatal("without a search, what is pasted should go into the chat, as before")
	}
	search(m, "row")
	m.screen = screenChat // however the screen is left
	m.Update(run(m, "/context")())
	if m.context.searching() {
		t.Fatal("the context screen should open without a search")
	}
}

// A search finds the first match from the line at the top on, or the first
// of all when there is none below. A match in sight is left where it is; one
// out of sight comes a third of the view below the top. The matches in sight
// are marked, from the top line on.
func TestContextScreenGoesToAMatch(t *testing.T) {
	m := searchable(t)
	h, row30 := m.context.port.Height(), lineOf(t, m, "row 30")
	m.context.port.SetYOffset(row30)
	scroll(t, m, search(m, "row"))
	if m.context.port.YOffset() != row30 || !strings.Contains(ansi.Strip(m.View().Content), "31/60") {
		t.Fatalf("the first match from the top on should be gone to, at the top: %d", m.context.port.YOffset())
	}
	content, view := m.View().Content, ansi.Strip(m.View().Content)
	if !strings.Contains(markedAt(m, currentStyle, "row"), "row 30") || !strings.Contains(markedAt(m, matchStyle, "row"), "row 31") ||
		strings.Contains(view, "row 29") || !strings.Contains(view, fmt.Sprintf("row %d", 29+h)) || strings.Contains(view, fmt.Sprintf("row %d", 30+h)) {
		t.Fatalf("the matches in sight should be marked, from the top line on:\n%s", content)
	}
	press(m, "esc")

	m.context.port.SetYOffset(row30 - h) // row 30 is the first line below the view
	scroll(t, m, search(m, "row 3"))
	if want := row30 - h/3; m.context.port.YOffset() != want {
		t.Fatalf("a match below the view should come a third below the top: %d, want %d", m.context.port.YOffset(), want)
	}
	press(m, "esc")

	m.context.port.SetYOffset(topLine(&m.context.port))
	scroll(t, m, search(m, "row 0"))
	if m.context.port.YOffset() != 0 || !strings.Contains(ansi.Strip(m.View().Content), "1/10") {
		t.Fatalf("without a match below, the first one should be gone to: %d", m.context.port.YOffset())
	}
	press(m, "esc")

	// A match on the way into sight is not gone to again.
	search(m, "row 4")
	if press(m, "down") != nil {
		t.Fatal("the next match is in sight where the view is going")
	}
	scroll(t, m, scrollTick())
	if want := lineOf(t, m, "row 40") - h/3; m.context.port.YOffset() != want {
		t.Fatalf("the view should rest where the first match is in sight: %d, want %d", m.context.port.YOffset(), want)
	}
	press(m, "esc")

	scroll(t, m, search(m, "row 59"))
	if m.context.port.YOffset() != topLine(&m.context.port) {
		t.Fatalf("a match at the end should go no further than the end: %d", m.context.port.YOffset())
	}
}

// Two matches on a line are marked both, and the text between them stays.
func TestContextScreenMarksMatchesOnALine(t *testing.T) {
	m, b := newModel(t)
	b.contents = []Message{{"user", "ab ab"}}
	m.Update(run(m, "/context")())
	search(m, "AB")
	if line := markedAt(m, currentStyle, "ab"); !strings.Contains(line, "▎ ab ab ") || markedAt(m, matchStyle, "ab") != line {
		t.Fatalf("both should be marked:\n%q", m.View().Content)
	}
}

// Wrapped anew in a narrower window, a word that is too long for a line is
// broken; a match in it is gone, and the search is at a match there still is.
func TestContextScreenSearchesAnewWhenWrapped(t *testing.T) {
	m, b := newModel(t)
	word := "needle" + strings.Repeat("n", 14)
	b.contents = []Message{{"user", word}, {"tool", strings.Repeat("z", 25) + word + strings.Repeat("z", 15)}}
	m.Update(run(m, "/context")())
	search(m, word)
	press(m, "down")
	if !strings.Contains(ansi.Strip(m.View().Content), "2/2") {
		t.Fatalf("both should be found:\n%s", ansi.Strip(m.View().Content))
	}
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 30})
	if !strings.Contains(ansi.Strip(m.View().Content), "1/1") {
		t.Fatalf("the broken word should not be found:\n%s", ansi.Strip(m.View().Content))
	}
}

// Words are found across the end of a line the message is wrapped at, past
// the bar the next line hangs on.
func TestContextScreenSearchesAcrossTheBar(t *testing.T) {
	m, b := newModel(t)
	b.contents = []Message{{"user", "alpha beta"}}
	m.Update(tea.WindowSizeMsg{Width: 14, Height: 30})
	m.Update(run(m, "/context")())
	search(m, "alpha beta")
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "▎ alpha ") || len(m.context.matches) != 1 || len(m.context.matches[0]) != 2 {
		t.Fatalf("the words should be found on two lines, %v:\n%s", m.context.matches, view)
	}
}

// contextFades is how the context screen shows the lines of its viewport, as
// fades has it.
func contextFades(t *testing.T, m *Model) string {
	t.Helper()
	return fades(t, m, strings.Split(m.context.view(m), "\n")[3:], m.context.port.View()) // below the border, the title and the line of the search
}

// The context fades out over two lines at an edge of the box where more of it
// is beyond, as the history of the chat does: at the top only at the bottom,
// a line down at both edges, at the end only at the top. A context that fits
// into the box does not fade.
func TestContextScreenFadesAtItsEdges(t *testing.T) {
	m := searchable(t)
	n := m.context.port.Height()
	if got, want := contextFades(t, m), strings.Repeat(".", n-2)+"de"; got != want {
		t.Fatalf("at the top only the bottom should fade: %s, want %s", got, want)
	}
	scroll(t, m, press(m, "j"))
	if got, want := contextFades(t, m), "ed"+strings.Repeat(".", n-4)+"de"; got != want {
		t.Fatalf("a line down both edges should fade: %s, want %s", got, want)
	}
	scroll(t, m, press(m, "end"))
	if got, want := contextFades(t, m), "ed"+strings.Repeat(".", n-2); got != want {
		t.Fatalf("at the end only the top should fade: %s, want %s", got, want)
	}

	m, b := newModel(t)
	b.contents = []Message{{"user", "hi"}}
	m.Update(run(m, "/context")())
	if got, want := contextFades(t, m), strings.Repeat(".", n); got != want {
		t.Fatalf("a context that fits should not fade: %s, want %s", got, want)
	}
}

// A match on a faded line is marked as on any other line, and the rest of
// the line stays faded: at an edge in the color of the edge, next to it dim.
func TestContextScreenMarksMatchesOnFadedLines(t *testing.T) {
	m := searchable(t)
	m.context.port.SetYOffset(lineOf(t, m, "row 30"))
	search(m, "row")
	lines := strings.Split(m.context.view(m), "\n")[3:]
	n := m.context.port.Height()
	edge := lipgloss.NewStyle().Foreground(fadeColor(m.light))
	fade := edge.Render("x")
	fade = fade[:strings.Index(fade, "x")]
	if !strings.Contains(lines[0], edge.Render("▎ ")+currentStyle.Render("row")+fade+" 30 ") {
		t.Fatalf("the match at the top edge should be marked, and the rest of its line faded:\n%q", lines[0])
	}
	if !strings.Contains(lines[1], matchStyle.Render("row")+"\x1b[2m") || !strings.Contains(lines[1], "\x1b[2m 31 ") {
		t.Fatalf("the match on the dim line should be marked, and the rest of its line dim:\n%q", lines[1])
	}
	if !strings.Contains(lines[n-2], matchStyle.Render("row")+"\x1b[2m") || !strings.Contains(lines[n-2], fmt.Sprintf("\x1b[2m %d ", 28+n)) {
		t.Fatalf("the match on the dim line at the bottom should be marked, and the rest of its line dim:\n%q", lines[n-2])
	}
	if !strings.Contains(lines[n-1], edge.Render("▎ ")+matchStyle.Render("row")+fade+fmt.Sprintf(" %d ", 29+n)) {
		t.Fatalf("the match at the bottom edge should be marked, and the rest of its line faded:\n%q", lines[n-1])
	}
}

// find looks for words without regard to case, and white space between them
// matches any, also the end of a line and the padding around it, which no
// span covers. Its columns are cells.
func TestFind(t *testing.T) {
	for name, tc := range map[string]struct {
		lines  []string
		search string
		want   []match
	}{
		"case":          {[]string{"Alpha beta"}, "ALPHA", []match{{{0, 0, 5}}}},
		"ends of lines": {[]string{"ab", "ba"}, "b", []match{{{0, 1, 2}}, {{1, 0, 1}}}},
		"on a line":     {[]string{"ab ab"}, "ab", []match{{{0, 0, 2}}, {{0, 3, 5}}}},
		"wrapped":       {[]string{"  say hello   ", "  world  "}, "hello  world", []match{{{0, 6, 11}, {1, 2, 7}}}},
		"blank line":    {[]string{"foo", "", "bar"}, "foo bar", []match{{{0, 0, 3}, {2, 0, 3}}}},
		"three lines":   {[]string{"a b", "c", "d e"}, "b c d", []match{{{0, 2, 3}, {1, 0, 1}, {2, 0, 1}}}},
		"cells":         {[]string{"für 日本 dich"}, "dich", []match{{{0, 9, 13}}}},
		"literally":     {[]string{"a.b axb"}, "a.b", []match{{{0, 0, 3}}}},
		"no match":      {[]string{"abc"}, "abd", nil},
		"no words":      {[]string{"a b"}, "  ", nil},
	} {
		if got := find(tc.lines, tc.search); !slices.EqualFunc(got, tc.want, slices.Equal) {
			t.Errorf("%s: %v, want %v", name, got, tc.want)
		}
	}
}

// Every answer has its one line of calls, also while it is on its way, which
// counts its calls as far as they have come; an answer without calls has
// none.
func TestChatSumsUpTheCallsOfAnAnswer(t *testing.T) {
	m, b := newModel(t)
	b.steps = []Step{{Text: "asking beta"}, {Text: "asking beta ✓ 1s", Done: true}, {Text: "asking beta"}, {Text: "asking beta ✗ offline", Done: true}}
	answered(t, m, say(t, m, "ask beta twice"))
	b.steps = nil
	answered(t, m, say(t, m, "thanks"))
	gate := make(chan struct{})
	b.ask = func(_ context.Context, step func(Step), _ func(string)) (string, error) {
		step(Step{Text: "reading a.go"})
		<-gate
		return "", nil
	}
	cmd := say(t, m, "read a.go")
	drive(t, m, cmd, func() bool { return m.chat.doing == "reading a.go" }) // the call is announced
	view := trimmedLines(m)
	if !strings.Contains(view, "│   ↳ Used 2 tools, 1 failed\n") || strings.Count(view, "↳") != 2 ||
		!strings.Contains(view, "│   ↳ Used 1 tool\n") {
		t.Fatalf("the answer before is summed up, the one on its way too:\n%s", view)
	}
	close(gate)
	drive(t, m, nil, func() bool { return !m.chat.pending })
	if view := trimmedLines(m); !strings.Contains(view, "│   ↳ Used 1 tool\n") || strings.Contains(view, "↳ ·") {
		t.Fatalf("the answer is in:\n%s", view)
	}
}

// The line of an answer goes below its last round, also when the answer is
// the first entry of the conversation; a message without calls has none, and
// the answer on its way has as far as it has come.
func TestSummaries(t *testing.T) {
	read := []call{{text: "reading a ✓ 1ms", done: true}}
	both := append(slices.Clone(read), read...)
	for name, tc := range map[string]struct {
		c    conversation
		want map[int]string
	}{
		"first entry": {conversation{entries: []entry{{calls: read}, {text: "done"}}}, map[int]string{0: (&conversation{}).summary(read)}},
		"two rounds":  {conversation{entries: []entry{{calls: read}, {calls: read}}}, map[int]string{0: "", 1: (&conversation{}).summary(both)}},
		"no calls":    {conversation{entries: []entry{{mine: true, text: "hi"}, {text: "hello"}}}, map[int]string{}},
		"on its way":  {conversation{entries: []entry{{calls: read}, {calls: read}}, pending: true}, map[int]string{0: "", 1: (&conversation{}).summary(both)}},
	} {
		if got := tc.c.summaries(); !maps.Equal(got, tc.want) {
			t.Errorf("%s: %v, want %v", name, got, tc.want)
		}
	}
}
