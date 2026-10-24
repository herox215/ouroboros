package tui

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

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

// While an answer is on its way, the line of the status above the composer
// says what is waited for, as the GUI says where the conversation stands,
// and a picture moves on its left.
func TestWaitIsShownOnAPicture(t *testing.T) {
	m, b := newModel(t)
	w := m.transcriptWidth()
	if m.waiting() != "" || ansi.Strip(m.statusLine(w)) != strings.Repeat(" ", w-15)+"SESSION · READY" {
		t.Fatalf("nothing is waited for before a message: %q", ansi.Strip(m.statusLine(w)))
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
	// The picture is the next one, and what is waited for stands on its
	// right, in the line above the composer.
	label := "alpha is thinking"
	wait := func() string {
		text := strings.ToUpper("Session · " + label)
		return string(arts[m.chat.art%len(arts)].row(w-lipgloss.Width(text)-2, m.ticks)) + "  " + text
	}
	if m.chat.art != 1 || !strings.Contains(ansi.Strip(m.View().Content), wait()) {
		t.Fatalf("the wait should be shown on picture 1 as\n%s\nnot on %d:\n%s", wait(), m.chat.art, ansi.Strip(m.View().Content))
	}
	if got := m.statusLine(w); lipgloss.Width(got) != w || strings.Contains(got, "…") {
		t.Fatalf("the status is one row of %d cells, what is waited for as it is: %q", w, got)
	}
	// It moves with the spinner.
	before := m.statusLine(w)
	m.Update(m.spinner.Tick())
	if m.ticks != 1 || m.statusLine(w) == before || !strings.Contains(ansi.Strip(m.View().Content), wait()) {
		t.Fatalf("the picture should move:\n%s", ansi.Strip(m.View().Content))
	}
	// What the agent waits for takes the place of its thinking once it has
	// run for a while: a call that ends at once does not flash its name.
	drive(t, m, cmd, func() bool { return m.chat.doing == "asking beta" })
	m.now = func() time.Time { return m.chat.since.Add(toolShown - time.Millisecond) }
	if !strings.Contains(ansi.Strip(m.View().Content), wait()) {
		t.Fatalf("a call that just began should leave the agent thinking:\n%s", ansi.Strip(m.View().Content))
	}
	m.now = func() time.Time { return m.chat.since.Add(toolShown) }
	label = "asking beta"
	if m.chat.art != 1 || !strings.Contains(ansi.Strip(m.View().Content), wait()) {
		t.Fatalf("the call should stand on the same picture, not on %d:\n%s", m.chat.art, ansi.Strip(m.View().Content))
	}
	// In a line too narrow for it, what is waited for is cut, and no
	// picture is left.
	if got := ansi.Strip(m.statusLine(12)); got != "SESSION · A…" {
		t.Fatalf("a narrow wait: %q", got)
	}

	release()
	drive(t, m, nil, func() bool { return !m.chat.pending })
	if m.waiting() != "" || strings.Contains(m.View().Content, "ASKING BETA") {
		t.Fatalf("no wait is shown once the answer is in:\n%s", ansi.Strip(m.View().Content))
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
	if want := string(arts[0].row(w, m.ticks)[:20]); !strings.HasPrefix(ansi.Strip(m.statusLine(w)), want) {
		t.Fatalf("after the last picture comes the first:\n%s", m.statusLine(w))
	}
}

// A tool that began a moment ago is not named yet: the agent is thinking
// until it has run for a while.
func TestToolIsNamedAfterAWhile(t *testing.T) {
	m, _ := newModel(t)
	at := time.Unix(100, 0)
	m.now = func() time.Time { return at }
	m.chat.doing, m.chat.since = "reading a file", at.Add(-299*time.Millisecond)
	if got := m.waiting(); got != "alpha is thinking" {
		t.Fatalf("after 299ms: %q", got)
	}
	m.chat.since = at.Add(-300 * time.Millisecond)
	if got := m.waiting(); got != "reading a file" {
		t.Fatalf("after 300ms: %q", got)
	}
}

// The jobs that run in the background for the answer follow what the agent
// waits for, until they end, or the answer does.
func TestJobsAreShown(t *testing.T) {
	m, _ := newModel(t)
	c := m.chat
	m.apply(c, Event{Kind: plugin.SessionMessage, Text: "serve it"}, true)
	m.apply(c, Event{Kind: plugin.SessionJobs, Text: "job 1 · npm run dev\njob 2 · go test ./..."}, true)
	if got := m.waiting(); got != "alpha is thinking · job 1 · npm run dev · job 2 · go test ./..." {
		t.Fatalf("with jobs: %q", got)
	}
	if !strings.Contains(ansi.Strip(m.statusLine(120)), "ALPHA IS THINKING · JOB 1 · NPM RUN DEV") {
		t.Fatalf("view:\n%s", m.statusLine(120))
	}
	m.apply(c, Event{Kind: plugin.SessionJobs}, true)
	if got := m.waiting(); got != "alpha is thinking" {
		t.Fatalf("once they ended: %q", got)
	}
	m.apply(c, Event{Kind: plugin.SessionJobs, Text: "job 3 · sleep 9"}, true)
	m.apply(c, Event{Kind: plugin.SessionAnswer, Text: "done"}, true)
	if c.jobs != nil || m.waiting() != "" {
		t.Fatalf("after the answer: %q, %q", c.jobs, m.waiting())
	}
}
