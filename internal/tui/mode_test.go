package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// The dialog of the modes stays quiet at off and yolo, and comes apart a
// little while it is at cyberpsycho: the glitch stands in the dialog then,
// and the row keeps the width its clicks have without it.
func TestModeDialogCyberpsychoGlitch(t *testing.T) {
	m, _ := newModel(t)
	m.screen = screenMode
	for _, tt := range []struct {
		at     int
		glitch bool
	}{{0, false}, {1, false}, {2, true}} {
		m.mode.at = tt.at
		view, hits := m.mode.view(m)
		plain := ansi.Strip(view)
		if got := strings.Contains(plain, "▓"); got != tt.glitch {
			t.Errorf("%s: glitch %v, want %v:\n%s", modeNames[tt.at], got, tt.glitch, plain)
		}
		if want := len(modeNames[tt.at]) + 2; hits[tt.at].to-hits[tt.at].from != want {
			t.Errorf("%s: the click is %d cells wide, want %d", modeNames[tt.at], hits[tt.at].to-hits[tt.at].from, want)
		}
	}
}
