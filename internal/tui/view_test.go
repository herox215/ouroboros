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

// Without requests of this node, none is said to wait. The choices go below
// the proposal selected.
func TestPendingScreenWithProposalsOnly(t *testing.T) {
	m, b := newModel(t)
	b.trust = Contacts{Proposals: []plugin.Proposal{
		{ID: "p1", Field: "tools.a2a", Now: `[]`, Then: `["list_peers"]`, Reason: "peers"},
		{ID: "p2", Field: "name", Now: `"alpha"`, Then: `"omega"`, Reason: "a better name"},
	}}
	m.Update(m.loadContacts())
	run(m, "/pending")
	press(m, "j")
	v := view(m)
	if strings.Contains(v, "Waiting for the other side") || strings.Count(v, "[accept]") != 1 ||
		strings.Index(v, "[accept]") < strings.Index(v, "a better name") {
		t.Fatalf("the view:\n%s", v)
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
