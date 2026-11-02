package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// sidebarText is the sidebar as plain text.
func sidebarText(m *Model) string {
	return ansi.Strip(m.sidebarView(m.sideWidth(), m.height))
}

// The sidebar leads with the name of the thread as ASCII art, above the
// sessions that run; a conversation of its own has none there.
func TestTheSidebarLeadsWithTheThreadBanner(t *testing.T) {
	m, _ := threadModel(t, "", nil)
	m.Update(tea.WindowSizeMsg{Width: 200, Height: 40})
	art, ok := m.threadArt(m.sideWidth() - 2*columnPad)
	if !ok {
		t.Fatalf("no banner fits a sidebar of %d cells", m.sideWidth())
	}
	side := sidebarText(m)
	last := strings.TrimRight(art[len(art)-1], " ")
	// The list starts right below it, at the top, not in the middle.
	lines := strings.Split(side, "\n")
	if !strings.Contains(lines[len(art)], last) || !strings.Contains(lines[len(art)+2], "Nothing runs.") {
		t.Fatalf("the list does not start right below the banner:\n%s", side)
	}
	if strings.Contains(side, "RUNNING") {
		t.Fatalf("the sidebar has a heading:\n%s", side)
	}
	// It sticks to the top, whatever the list below it holds.
	if row := strings.Split(side, "\n")[1]; !strings.Contains(row, strings.TrimSpace(art[0])) {
		t.Fatalf("the banner is not at the top:\n%s", side)
	}
	if head := ansi.Strip(strings.Join(m.headerLines(m.chatWidth()-2*columnPad), "\n")); !strings.Contains(head, "THREAD") {
		t.Fatalf("the head lost the name of the thread:\n%s", head)
	}
	settle(m, m.newConversation())
	if side := sidebarText(m); strings.Contains(side, last) {
		t.Fatalf("a conversation of its own has the banner:\n%s", side)
	}
}

// A narrower sidebar takes the smaller banner.
func TestANarrowSidebarTakesTheMiniBanner(t *testing.T) {
	m, _ := threadModel(t, "", nil)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	art, ok := m.threadArt(m.sideWidth() - 2*columnPad)
	if !ok || art[0] != threadBanners[len(threadBanners)-1][0] {
		t.Fatalf("a sidebar of %d cells has the banner %q", m.sideWidth(), art)
	}
}

// The banners of the thread are pure ASCII.
func TestTheBannerIsASCII(t *testing.T) {
	for _, art := range threadBanners {
		for _, row := range art {
			for _, r := range row {
				if r > 0x7e || r < 0x20 {
					t.Fatalf("not ASCII: %q in %q", r, row)
				}
			}
		}
	}
}
