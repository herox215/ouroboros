package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"ouroboros/plugin"
)

// The history is as wide as the box leaves it, and an answer is wrapped to
// it below its indent: every line of it begins with the indent.
func TestAnswerIsWrappedToTheHistory(t *testing.T) {
	m, b := newModel(t)
	if m.history.Width() != 96 {
		t.Fatalf("the history is %d wide in 100", m.history.Width())
	}
	b.reply = numbered(200)
	answered(t, m, say(t, m, "ping"))
	lines := strings.Split(ansi.Strip(m.history.GetContent()), "\n")
	for _, l := range lines[4:] {
		if !strings.HasPrefix(l, "  w") || ansi.StringWidth(l) > 96 {
			t.Fatalf("a line of the answer %q in\n%s", l, strings.Join(lines, "\n"))
		}
	}
}

// Legacy contacts do not create a banner, header badge or approval list.
func TestLegacyContactsDoNotCreatePendingUI(t *testing.T) {
	m, b := newModel(t)
	before, height := view(m), m.history.Height()
	b.trust = Contacts{
		Incoming:  []plugin.Request{{PublicKey: "peer", Name: "legacy peer", Proposed: true}},
		Outgoing:  []plugin.Request{{PublicKey: "out", Name: "legacy outgoing"}},
		Proposals: []plugin.Proposal{{ID: "p1", Field: "legacy field"}},
	}
	m.Update(m.loadContacts())
	if m.pendingRows() != 0 || m.suggestionRequests.unread != 0 || m.history.Height() != height || view(m) != before {
		t.Fatalf("legacy contacts changed the chat UI:\n%s", view(m))
	}
	run(m, "/suggestions")
	v := view(m)
	if !strings.Contains(v, "No suggestions.") || strings.Contains(v, "legacy") || strings.Contains(v, "[accept]") || press(m, "enter") != nil || press(m, "a") != nil {
		t.Fatalf("legacy contacts entered suggestions:\n%s", v)
	}
}

func TestPendingCommandIsAbsent(t *testing.T) {
	m, _ := newModel(t)
	for _, name := range []string{"pending", "approvals"} {
		if len(m.matching("/"+name)) != 0 {
			t.Fatalf("/%s is offered", name)
		}
		run(m, "/"+name)
		if m.screen != screenChat || m.notice != "unknown command /"+name+": /help lists them" {
			t.Fatalf("/%s is still bound: %q", name, m.notice)
		}
	}
	run(m, "/help")
	if v := view(m); strings.Contains(v, "/pending") || strings.Contains(v, "/approvals") || !strings.Contains(v, "/suggestions") {
		t.Fatalf("obsolete help:\n%s", v)
	}
}

// A key of ten characters is short enough to be shown whole.
func TestShortKey(t *testing.T) {
	if got := shortKey("0123456789"); got != "0123456789" {
		t.Fatalf("ten: %q", got)
	}
	if got := shortKey("0123456789a"); got != "0123456789…" {
		t.Fatalf("eleven: %q", got)
	}
}

// In the debug mode the header is as wide as the screen, the mark on its
// right; a screen too narrow for more shows only the mark.
func TestDebugHeaderFillsTheLine(t *testing.T) {
	m, b := newModel(t)
	b.status.Debug = true
	m.Update(m.refreshStatus())
	debug := " DEBUG MODE "
	for _, width := range []int{40, 13, 12} {
		m.Update(tea.WindowSizeMsg{Width: width, Height: 30})
		header := ansi.Strip(m.headerView())
		if lipgloss.Width(header) != width || !strings.HasSuffix(header, debug) {
			t.Fatalf("%d wide: %q", width, header)
		}
	}
	if header := ansi.Strip(m.headerView()); header != debug {
		t.Fatalf("only the mark: %q", header)
	}
	m.Update(tea.WindowSizeMsg{Width: 30, Height: 30})
	if header := ansi.Strip(m.headerView()); !strings.Contains(header, "…") {
		t.Fatalf("the rest should be cut: %q", header)
	}
}

// A node that is not set up shows the logo above the hint when the chat has
// room for both, and the hint alone when not.
func TestLogoNeedsRoom(t *testing.T) {
	m, b := newModel(t)
	b.status = Status{}
	m.Update(m.refreshStatus())
	width, height := logoWidth()+4, len(logoLines)+11 // borders, header, input and footer
	logo := strings.TrimSpace(ansi.Strip(logoLines[len(logoLines)/2]))
	for _, size := range []struct {
		width, height int
		shown         bool
	}{{width, height, true}, {width - 1, height, false}, {width, height - 1, false}} {
		m.Update(tea.WindowSizeMsg{Width: size.width, Height: size.height})
		v := view(m)
		if strings.Contains(v, logo) != size.shown || !strings.Contains(v, "Type /setup") {
			t.Fatalf("%d×%d, shown %v:\n%s", size.width, size.height, size.shown, v)
		}
	}
}

// In a low window the suggestions of a command take the rows of the
// conversation they need, and leave it one line: the screen stays as high
// as the window.
func TestSuggestionsInALowWindow(t *testing.T) {
	m, _ := newModel(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 16})
	m.input.SetValue("/")
	m.inputShown = true
	m.layout()
	if rows := strings.Count(m.View().Content, "\n") + 1; rows != 16 || strings.Contains(view(m), "│ alpha") || len(m.suggestionLines(m.chatHeight()-3)) != m.chatHeight()-3 {
		t.Fatalf("%d rows:\n%s", rows, view(m))
	}
}

// The footer names the conversation on its left only once there is a word
// in it and something to say of it.
func TestChatTitleNeedsAConversation(t *testing.T) {
	m, _ := newModel(t)
	m.chat.context = 1200
	if got := m.chatTitle(); got != "" {
		t.Errorf("a chat without a word: %q", got)
	}
	fill(m, 1)
	if got := ansi.Strip(m.chatTitle()); !strings.HasPrefix(got, " context ") {
		t.Errorf("with a word: %q", got)
	}
	m.status.Configured = false
	if got := m.chatTitle(); got != "" {
		t.Errorf("a node that is not set up: %q", got)
	}
	m.status.Configured, m.chat.context = true, 0
	if got := m.chatTitle(); got != "" {
		t.Errorf("nothing known of the context: %q", got)
	}
}

// The footer is one line as wide as the window: the hint whole on the
// right, a cell before the edge, and the conversation in what it leaves.
func TestFooterFitsTheWindow(t *testing.T) {
	m, _ := newModel(t)
	fill(m, 1)
	m.chat.context, m.chat.pending = 1200, true
	title, hint := ansi.Strip(m.chatTitle()), "esc stop "
	wide := lipgloss.Width(title)
	for _, width := range []int{100, len(hint) + wide + 1, len(hint) + wide, len(hint) + 3, len(hint) + 1, len(hint)} {
		m.Update(tea.WindowSizeMsg{Width: width, Height: 30})
		got := ansi.Strip(m.footerView())
		if lipgloss.Width(got) != width || !strings.HasSuffix(got, hint) {
			t.Fatalf("at %d cells: %q", width, got)
		}
		left := strings.TrimRight(strings.TrimSuffix(got, hint), " ")
		switch room := width - len(hint) - 1; {
		case room >= wide:
			if left != title {
				t.Errorf("at %d cells the conversation is %q", width, left)
			}
		case room > 0:
			if lipgloss.Width(left) != room || !strings.HasSuffix(left, "…") {
				t.Errorf("at %d cells the conversation is cut to %q", width, left)
			}
		default:
			if left != "" {
				t.Errorf("at %d cells there is no room for %q", width, left)
			}
		}
	}
	// What there is to notice comes before the hint, apart from it; alone,
	// it stands alone.
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.setNotice("look", false)
	notice := lipgloss.NewStyle().Foreground(accent).Render("look")
	if got := m.footerHint(); got != notice+mutedStyle.Render("  │  ")+mutedStyle.Render("esc stop") {
		t.Errorf("a notice with a hint: %q", got)
	}
	m.chat.pending = false
	if got := m.footerHint(); got != notice+mutedStyle.Render("") {
		t.Errorf("a notice alone: %q", got)
	}
}

// A rune is wide where the terminal gives it two cells: in the blocks of
// Chinese, Japanese and Korean, and only there, up to the edges of each.
func TestWideRune(t *testing.T) {
	for _, r := range []rune{'a', 0x10ff, 0x1160, 0x2e7f, 0xa4d0, 0xabff, 0xd7a4, 0xf8ff, 0xfb00, 0xfe2f, 0xfe70, 0xfeff, 0xff61, 0xffdf, 0xffe7, 0x1ffff} {
		if wideRune(r) {
			t.Errorf("%U is wide", r)
		}
	}
	for _, r := range []rune{0x1100, 0x115f, 0x2e80, 0xa4cf, 0xac00, 0xd7a3, 0xf900, 0xfaff, 0xfe30, 0xfe6f, 0xff00, 0xff60, 0xffe0, 0xffe6, 0x20000, '两'} {
		if !wideRune(r) {
			t.Errorf("%U is not wide", r)
		}
	}
}

// The newest word comes in as noise and settles letter by letter, each
// letter with glyphs of its own from frame to frame; what is no letter or
// digit stands from the first.
func TestThreadInSettlesLetterByLetter(t *testing.T) {
	for frame, want := range []string{"at ╲:┼╪▖▗", "at 3:2▌▐░", "at 3:2│┼m", "at 3:23pm"} {
		if got := threadIn("at 3:23pm", frame); got != want {
			t.Errorf("frame %d: %q, want %q", frame, got, want)
		}
	}
}

// The requests list what waits: Accept all and the head of the suggested
// changes only when there is something for them, and each suggestion with
// its text wrapped below it and what was measured, if anything was.
func TestPendingScreenSuggestions(t *testing.T) {
	m, b := newModel(t)
	run(m, "/suggestions")
	if v := view(m); strings.Contains(v, "Accept all") || strings.Contains(v, "Changes of ouro") {
		t.Fatalf("nothing waits:\n%s", v)
	}
	long := strings.Repeat("make the chat faster ", 12)
	b.trust = Contacts{
		Incoming:    []plugin.Request{{PublicKey: "peer", Name: "luna"}},
		Proposals:   []plugin.Proposal{{ID: "p1", Field: "name"}},
		Suggestions: []plugin.Suggestion{{ID: "s1", Text: long}, {ID: "s2", Text: "the second", Signals: []string{"42 seconds"}}},
	}
	m.Update(m.loadContacts())
	press(m, "down")
	v := view(m)
	if strings.Count(v, "measured:") != 1 || !strings.Contains(v, "measured: 42 seconds") || !strings.Contains(v, "Changes of ouro the reviews of answers suggest") {
		t.Fatalf("the suggestions:\n%s", v)
	}
	lines := strings.Split(v, "\n")
	marked := lineWith(lines, "› suggestion")
	if marked < 0 || !strings.Contains(lines[marked+1], "the second") {
		t.Fatalf("the second suggestion is not the one selected:\n%s", v)
	}
	for _, l := range lines {
		if inner := strings.TrimPrefix(strings.TrimSpace(l), "│ "); strings.Contains(inner, "faster") && !strings.HasPrefix(inner, "    ") {
			t.Errorf("a line of the suggestion is not indented: %q", l)
		}
	}
}

// What a review suggests is wrapped to the width of the chat.
func TestSuggestionEntryFitsTheChat(t *testing.T) {
	m, _ := newModel(t)
	m.chat.add(entry{suggestion: true, text: strings.Repeat("make the chat faster ", 20)})
	m.renderHistory(true)
	for _, l := range strings.Split(ansi.Strip(m.history.GetContent()), "\n") {
		if w := ansi.StringWidth(l); w > m.history.Width() {
			t.Errorf("a line of %d cells in a chat of %d: %q", w, m.history.Width(), l)
		}
	}
}

// Without the box of the input the footer says how to stop an answer on its
// way, not how to write; with the list of the jobs it says how to hide it,
// after what else it says.
func TestFooterHintsWithoutTheInput(t *testing.T) {
	m, _ := newModel(t)
	m.inputShown = false
	if got := ansi.Strip(m.footerHint()); got != "type to write" {
		t.Errorf("idle: %q", got)
	}
	m.chat.pending = true
	if got := ansi.Strip(m.footerHint()); got != "esc stop" {
		t.Errorf("an answer on its way: %q", got)
	}
	m.jobsShown = true
	if got := ansi.Strip(m.footerHint()); got != "esc stop · "+jobsKey+" hide" {
		t.Errorf("with the jobs: %q", got)
	}
	m.chat.pending, m.inputShown = false, true
	if got := ansi.Strip(m.footerHint()); got != jobsKey+" hide" {
		t.Errorf("with the jobs alone: %q", got)
	}
}

// With more suggestions than a low dialog shows, the list scrolls to the
// selected one, and keeps the title in view.
func TestPendingScreenScrollsToSelection(t *testing.T) {
	m, b := newModel(t)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	b.trust = Contacts{Suggestions: []plugin.Suggestion{
		{ID: "s1", Text: "first suggestion"},
		{ID: "s2", Text: "second suggestion"},
		{ID: "s3", Text: "third suggestion"},
		{ID: "s4", Text: "fourth suggestion"},
	}}
	m.Update(m.loadContacts())
	run(m, "/suggestions")
	for range 3 {
		press(m, "down")
	}
	v := view(m)
	if !strings.Contains(v, "Suggestions") || !strings.Contains(v, "fourth suggestion") || strings.Contains(v, "first suggestion") {
		t.Fatalf("the selected suggestion is not in view:\n%s", v)
	}
}
