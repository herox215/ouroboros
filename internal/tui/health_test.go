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

// The reading stands under the gear at the bottom of the sidebar, in the
// cyberpsycho mode, and not on the line of the chat below the composer.
func TestHealthUnderTheGear(t *testing.T) {
	m, _ := newModel(t)
	m.status.Cyberpsycho = true
	m.status.Health = &health.Record{Ran: true, OK: true, Tests: []string{"./internal/tui/"}}
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	lines := strings.Split(ansi.Strip(m.View().Content), "\n")
	last, above := lines[len(lines)-1], lines[len(lines)-2]
	if !strings.HasPrefix(strings.TrimLeft(last, " "), "100% Integrity") {
		t.Errorf("the integrity does not begin the last line: %q", last)
	}
	if !strings.Contains(above, gear) {
		t.Errorf("the gear does not stand above the integrity: %q", above)
	}
	if footer := ansi.Strip(m.footerView()); strings.Contains(footer, "Integrity") {
		t.Errorf("the footer still carries the integrity: %q", footer)
	}
}

// Outside the cyberpsycho mode the tests are the gate of the build and no
// reading: the sidebar says nothing of them, and the gear takes the last row.
func TestNoIntegrityOutsideCyberpsycho(t *testing.T) {
	m, _ := newModel(t)
	m.status.Health = &health.Record{Ran: true, OK: true, Tests: []string{"./internal/tui/"}}
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	view := ansi.Strip(m.View().Content)
	if strings.Contains(view, "Integrity") {
		t.Errorf("the sidebar carries the integrity outside the cyberpsycho mode:\n%s", view)
	}
	lines := strings.Split(view, "\n")
	if last := strings.TrimLeft(lines[len(lines)-1], " "); !strings.HasPrefix(last, gear) {
		t.Errorf("the gear does not stand on the last line: %q", last)
	}
}
