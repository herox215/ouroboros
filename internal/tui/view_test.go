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

// The stars of the splash are as far ahead of it as the setup left them.
func TestSplashViewShiftsTheSky(t *testing.T) {
	m, _ := newModel(t)
	m.splashElapsed, m.skyShift = 3*time.Second, 20*time.Second
	if m.splashView() != drawSplash(m.width, m.height, 3*time.Second, 23*time.Second, m.light) {
		t.Fatal("the sky should be shifted ahead")
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
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 30})
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
// conversation they need, and leave it its title: the screen stays as high
// as the window.
func TestSuggestionsInALowWindow(t *testing.T) {
	m, _ := newModel(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 16})
	m.input.SetValue("/")
	if rows := strings.Count(m.View().Content, "\n") + 1; rows != 16 || !strings.Contains(view(m), "│ alpha") || len(m.suggestionLines(m.chatHeight()-3)) != m.chatHeight()-3 {
		t.Fatalf("%d rows:\n%s", rows, view(m))
	}
}
