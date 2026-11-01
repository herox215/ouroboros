package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"ouroboros/internal/health"
)

// The integrity under the button of the settings says what the last run of
// the tests of a rebuild found, while the cyberpsycho mode makes the tests a
// reading: the share of the packages that stood, or the question before the
// first run.
func TestHealthMark(t *testing.T) {
	m, _ := newModel(t)
	m.status.Cyberpsycho = true
	for _, tt := range []struct {
		name string
		r    *health.Record
		want string
	}{
		{"never measured", nil, "??% Integrity"},
		{"not run", &health.Record{}, "??% Integrity"},
		{"green", &health.Record{Ran: true, OK: true, Tests: []string{"./internal/tui/"}}, "100% Integrity"},
		{"the only package failed", &health.Record{Ran: true, Tests: []string{"./internal/tui/"}, Fail: "./internal/tui/: TestFooterFitsTheWindow"}, "0% Integrity"},
		{"one of three failed", &health.Record{Ran: true, Tests: []string{"./internal/home/", "./internal/tui/", "./plugin/"}, Fail: "./internal/tui/: TestFooterFitsTheWindow"}, "66% Integrity"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m.status.Health = tt.r
			if got := ansi.Strip(m.healthMark()); got != tt.want {
				t.Errorf("healthMark() = %q, want %q", got, tt.want)
			}
		})
	}
}

// The reading stands on the last row of the sidebar, in the cyberpsycho
// mode, and not on the line of the chat below the composer.
func TestHealthAtTheBottomOfTheSidebar(t *testing.T) {
	m, _ := newModel(t)
	m.status.Cyberpsycho = true
	m.status.Health = &health.Record{Ran: true, OK: true, Tests: []string{"./internal/tui/"}}
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	lines := strings.Split(ansi.Strip(m.View().Content), "\n")
	if last := lines[len(lines)-1]; !strings.HasPrefix(strings.TrimLeft(last, " "), "100% Integrity") {
		t.Errorf("the integrity does not begin the last line: %q", last)
	}
	if footer := ansi.Strip(m.footerView()); strings.Contains(footer, "Integrity") {
		t.Errorf("the footer still carries the integrity: %q", footer)
	}
}

// Outside the cyberpsycho mode the tests are the gate of the build and no
// reading: the sidebar says nothing of them.
func TestNoIntegrityOutsideCyberpsycho(t *testing.T) {
	m, _ := newModel(t)
	m.status.Health = &health.Record{Ran: true, OK: true, Tests: []string{"./internal/tui/"}}
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if view := ansi.Strip(m.View().Content); strings.Contains(view, "Integrity") {
		t.Errorf("the sidebar carries the integrity outside the cyberpsycho mode:\n%s", view)
	}
}
