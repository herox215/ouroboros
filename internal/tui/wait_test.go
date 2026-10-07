package tui

import (
	"context"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"ouroboros/plugin"
)

// frames are the pictures of arts at the tick 5, 24 cells wide.
var frames = []string{
	"▁▁▂▃▃▄▅▆▆▇▇▇▇▇▇▇▆▆▅▄▃▃▂▁",
	";;;;+++++++;;;;:::···...",
	"  ·∙••••∙∙·  ·∙∙••••∙·  ",
	"          ░░▒▒▓▓████▓▓▒▒",
	"          ++ .* *       ",
	"··•●●●•·······••●●••····",
	"╎│╎    ·  ╎│·╎│ │       ",
	"⡀⢀⠠⠐⠈⠁⠂⠄⡀⢀⠠⠐⠈⠁⠂⠄⡀⢀⠠⠐⠈⠁⠂⠄",
	"_╱‾╲_╱‾╲_╱‾╲_╱‾╲_╱‾╲_╱‾╲",
	"•●●●●●    ·····∙∙∙∙∙••••",
}

func TestArtsDraw(t *testing.T) {
	if len(arts) != len(frames) {
		t.Fatalf("%d pictures, %d frames", len(arts), len(frames))
	}
	for i, a := range arts {
		got := string(a.row(24, 5))
		if got != frames[i] || lipgloss.Width(got) != 24 {
			t.Errorf("picture %d, %d cells wide:\n%s\nwant:\n%s", i, lipgloss.Width(got), got, frames[i])
		}
		if string(a.row(24, 8)) == frames[i] {
			t.Errorf("picture %d should move", i)
		}
	}
}

func TestWaitIsShownOnAPicture(t *testing.T) {
	m, b := newModel(t)
	if m.waiting() != "" || m.waitView(96) != "" {
		t.Fatal("nothing is waited for before a message")
	}
	gate := make(chan struct{})
	release := sync.OnceFunc(func() { close(gate) })
	t.Cleanup(release) // a test that fails leaves no call waiting
	b.ask = func(_ context.Context, step func(Step), _ func(string)) (string, error) {
		step(Step{Text: "asking beta"})
		<-gate
		return "pong", nil
	}
	cmd := say(t, m, "ping")
	// The picture is the next one, and what is waited for stands in its
	// middle, in the row below the history.
	label := " alpha is thinking "
	wait := func() string {
		row, from := arts[1].row(96, m.ticks), (96-lipgloss.Width(label))/2
		return "│ " + string(row[:from]) + label + string(row[from+lipgloss.Width(label):]) + " │\n╰"
	}
	if m.chat.art != 1 || !strings.Contains(ansi.Strip(m.View().Content), wait()) {
		t.Fatalf("the wait should be shown on picture 1 as\n%s\nnot on %d:\n%s", wait(), m.chat.art, m.View().Content)
	}
	if got := m.waitView(96); !strings.Contains(got, label) || strings.Contains(got, "…") || lipgloss.Width(got) != 96 {
		t.Fatalf("the wait is one row of 96 cells, what is waited for as it is: %q", got)
	}
	// It moves with the spinner.
	before := m.waitView(96)
	m.Update(m.spinner.Tick())
	if m.ticks != 1 || m.waitView(96) == before || !strings.Contains(ansi.Strip(m.View().Content), wait()) {
		t.Fatalf("the picture should move:\n%s", m.View().Content)
	}
	// What the agent waits for takes the place of its thinking.
	drive(t, m, cmd, func() bool { return m.chat.doing == "asking beta" })
	label = " asking beta "
	if m.chat.art != 1 || !strings.Contains(ansi.Strip(m.View().Content), wait()) {
		t.Fatalf("the call should stand on the same picture, not on %d:\n%s", m.chat.art, m.View().Content)
	}
	// In a window too narrow for it, what is waited for is cut.
	m.Update(tea.WindowSizeMsg{Width: 12, Height: 30})
	if got := ansi.Strip(m.waitView(8)); got != " asking…" {
		t.Fatalf("a narrow wait: %q", got)
	}
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})

	release()
	drive(t, m, nil, func() bool { return !m.chat.pending })
	if m.waiting() != "" || strings.Contains(m.View().Content, " asking beta ") {
		t.Fatalf("no wait is shown once the answer is in:\n%s", m.View().Content)
	}
	// The next message gets the next picture, also one of another client.
	c := m.chat
	m.apply(c, Event{Kind: plugin.SessionMessage, Text: "again"}, true)
	m.apply(c, Event{Kind: plugin.SessionMessage, Text: "again"}, true) // the wait is shown already
	c.pending = false
	m.apply(c, Event{Kind: plugin.SessionMessage, Text: "again"}, false) // read again, it was shown before
	if c.art != 2 || !c.pending {
		t.Fatalf("picture %d", c.art)
	}
	// After the last picture comes the first.
	c.art = len(arts)
	if want := string(arts[0].row(96, m.ticks)[:30]); !strings.HasPrefix(ansi.Strip(m.waitView(96)), want) {
		t.Fatalf("after the last picture comes the first:\n%s", m.waitView(96))
	}
}
