package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"ouroboros/internal/health"
)

// The mark at the left of the line below the body says what the last run of
// the tests of a rebuild found: nothing yet, green, or the package that
// failed, without the test whose name would be too long for the line.
func TestHealthMark(t *testing.T) {
	m, _ := newModel(t)
	for _, tt := range []struct {
		name string
		r    *health.Record
		want string
	}{
		{"never measured", nil, "♥ not measured"},
		{"green", &health.Record{Ran: true, OK: true}, "♥ green"},
		{"failing without a test", &health.Record{Ran: true, Fail: "./internal/tui/"}, "♥ fail ./internal/tui/"},
		{"failing", &health.Record{Ran: true, Fail: "./internal/tui/: TestFooterFitsTheWindow"}, "♥ fail ./internal/tui/"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m.status.Health = tt.r
			if got := ansi.Strip(m.healthMark()); got != tt.want {
				t.Errorf("healthMark() = %q, want %q", got, tt.want)
			}
		})
	}
}

// The mark falls back to its heart when the whole of it would not leave the
// hint whole and a cell between them.
func TestHealthMarkFallsBackToItsHeart(t *testing.T) {
	m, _ := newModel(t)
	m.chat.pending = true // the hint is "esc stop "
	m.Update(tea.WindowSizeMsg{Width: 12, Height: 30})
	if got := ansi.Strip(m.footerView()); got != "♥  esc stop " {
		t.Errorf("the footer: %q", got)
	}
}
