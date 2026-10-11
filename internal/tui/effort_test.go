package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"ouroboros/internal/openai"
	"ouroboros/plugin"
)

// /effort opens a slider at the effort of the session shown, which is that
// of the node while it has none of its own: ←→ move it, enter sets it for
// the session and as that of the node, esc goes back without. The header
// shows the effort of the session shown.
func TestEffort(t *testing.T) {
	m, b := newModel(t)
	b.status.Effort = "medium"
	b.status.Running = true
	m.Update(m.refreshStatus())
	run(m, "/effort")
	if m.screen != screenEffort || m.effort.at != 3 {
		t.Fatalf("screen %v at %d", m.screen, m.effort.at)
	}
	view := ansi.Strip(m.View().Content)
	for _, want := range []string{"auto    none    low    medium    high    max", "━━━━━━━━━━━━━━━━━━━━━━━━━━●", efforts["medium"]} {
		if !strings.Contains(view, want) {
			t.Errorf("the slider should show %q:\n%s", want, view)
		}
	}

	if !strings.Contains(ansi.Strip(m.headerView()), "effort medium") {
		t.Errorf("the header should show the effort: %s", ansi.Strip(m.headerView()))
	}
	press(m, "right", "right", "right", "l", "right", "right", "right", "right")
	if m.effort.at != 5 {
		t.Fatalf("the slider should come around to the most: %d", m.effort.at)
	}
	press(m, "left", "h")
	deliver(m, press(m, "enter"))
	if m.screen != screenChat || b.status.Effort != "medium" || m.notice != "effort medium for this session" || m.noticeErr || !m.chat.efforted || m.chat.effort != "medium" {
		t.Fatalf("screen %v, effort %q, notice %q", m.screen, b.status.Effort, m.notice)
	}

	run(m, "/effort")
	press(m, "left", "left", "left")
	if m.effort.at != 0 || !strings.Contains(ansi.Strip(m.View().Content), efforts["auto"]) {
		t.Fatalf("the slider should come to auto: %d", m.effort.at)
	}
	deliver(m, press(m, "enter"))
	m.Update(m.refreshStatus())
	if b.status.Effort != "" || m.notice != "effort auto for this session" || strings.Contains(ansi.Strip(m.headerView()), "effort") {
		t.Fatalf("effort %q, notice %q", b.status.Effort, m.notice)
	}

	run(m, "/effort")
	press(m, "right", "esc")
	if m.screen != screenChat || b.status.Effort != "" {
		t.Fatalf("esc should go back without setting: %v %q", m.screen, b.status.Effort)
	}

	b.fail = map[string]error{"effort": errors.New("the configuration is locked")}
	run(m, "/effort")
	deliver(m, press(m, "right", "enter"))
	if m.notice != "the configuration is locked" || !m.noticeErr {
		t.Fatalf("notice %q", m.notice)
	}
	// What the session does not take is not made the effort of the node.
	b.fail = map[string]error{"session effort": errors.New("the node is stopping")}
	run(m, "/effort")
	deliver(m, press(m, "right", "enter"))
	if m.notice != "the node is stopping" || !m.noticeErr || b.status.Effort != "" {
		t.Fatalf("notice %q, effort %q", m.notice, b.status.Effort)
	}
	b.fail = nil

	// The conversation of a peer has no effort to set.
	m.chat.peer = "beta"
	if run(m, "/effort"); m.screen != screenChat || !m.noticeErr || !strings.Contains(m.notice, "the model's own effort") {
		t.Fatalf("a peer: screen %v, notice %q", m.screen, m.notice)
	}
	m.chat.peer = ""

	b.status.Configured = false
	m.Update(m.refreshStatus())
	if run(m, "/effort"); m.screen != screenChat || m.notice != "set up the node first: /setup" {
		t.Fatalf("an unconfigured node: screen %v, notice %q", m.screen, m.notice)
	}
}

// Each session has its own effort: one is set, another session is shown and
// set to another, and both answer with theirs at once, also after the node
// told the UI anew what happened in them. A new session starts with the
// effort chosen last.
func TestEffortPerSession(t *testing.T) {
	m, b := newModel(t)
	b.status.Running = true
	m.Update(m.refreshStatus())
	release := make(chan struct{})
	b.ask = func(ctx context.Context, step func(Step), _ func(string)) (string, error) {
		step(Step{Text: "thinking"})
		select {
		case <-release:
			return "answered with " + effortName(openai.EffortOf(ctx)), nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	set := func(effort string) {
		t.Helper()
		run(m, "/effort")
		for range len(m.effort.choices) {
			if m.effort.choices[m.effort.at] == effort {
				break
			}
			press(m, "right")
		}
		deliver(m, press(m, "enter"))
		m.Update(m.refreshStatus())
		if m.notice != "effort "+effortName(effort)+" for this session" || m.chatEffort() != effort {
			t.Fatalf("set %q: notice %q, effort %q", effort, m.notice, m.chatEffort())
		}
	}

	set("high")
	first := m.chat
	drive(t, m, say(t, m, "think hard"), func() bool { return first.doing == "thinking" })
	drive(t, m, run(m, "/new"), func() bool { return true }) // what the new session is told comes in the next drive
	second := m.chat
	if second == first || second.efforted || m.chatEffort() != "high" {
		t.Fatalf("a new session starts with the effort chosen last: %q", m.chatEffort())
	}
	set("low")
	drive(t, m, say(t, m, "think a little"), func() bool { return second.doing == "thinking" })
	if !strings.Contains(ansi.Strip(m.headerView()), "effort low") {
		t.Errorf("the header shows the effort of the session shown: %s", ansi.Strip(m.headerView()))
	}
	close(release)
	drive(t, m, nil, func() bool { return !first.pending && !second.pending })
	last := func(c *conversation) string { return c.entries[len(c.entries)-1].text }
	if last(first) != "answered with high" || last(second) != "answered with low" {
		t.Fatalf("first %q, second %q", last(first), last(second))
	}
	m.switchTo(first)
	if !strings.Contains(ansi.Strip(m.headerView()), "effort high") {
		t.Errorf("the header shows the effort of the session shown: %s", ansi.Strip(m.headerView()))
	}

	// Watched anew, the sessions keep their efforts.
	past, _, err := b.sessions.Watch(context.Background(), first.contextID)
	if err != nil || past[len(past)-1].Kind != plugin.SessionEffort || past[len(past)-1].Text != "high" {
		t.Fatalf("past %+v, %v", past, err)
	}
	m.replay(second, nil)
	if second.efforted {
		t.Fatal("replayed without events, the effort is that of the node")
	}
}

// Of a model models.dev lists, /effort offers the efforts it takes, as its
// API names them, and says so; of one that takes none, only auto. An effort
// the model does not take is shown at the nearest one below it.
func TestEffortsTheModelTakes(t *testing.T) {
	m, b := newModel(t)
	b.status.Running, b.status.Model = true, "flash"
	m.Update(m.refreshStatus())
	b.efforts, b.effortsKnown = []string{"low", "high", "max"}, true
	m.chat.effort, m.chat.efforted = "medium", true
	run(m, "/effort")
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "auto    low    high    max") || strings.Contains(view, "none") || !strings.Contains(view, "These are the efforts flash takes, as models.dev lists them.") {
		t.Fatalf("listed:\n%s", view)
	}
	if m.effort.at != 1 {
		t.Fatalf("medium is shown at low: %d", m.effort.at)
	}
	press(m, "right", "right")
	deliver(m, press(m, "enter"))
	if m.chat.effort != "max" || b.status.Effort != "max" {
		t.Fatalf("chosen %q, node %q", m.chat.effort, b.status.Effort)
	}

	// An API's own name of an effort is offered as it is.
	b.efforts = []string{"none", "minimal", "xhigh"}
	run(m, "/effort")
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "auto    none    minimal    xhigh") || m.effort.at != 3 || !strings.Contains(view, efforts["xhigh"]) {
		t.Fatalf("at %d:\n%s", m.effort.at, view)
	}
	press(m, "left")
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, efforts["minimal"]) {
		t.Fatalf("minimal:\n%s", view)
	}
	press(m, "esc")

	b.efforts = nil
	run(m, "/effort")
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "models.dev lists no effort for flash: it thinks as it does by itself.") || m.effort.at != 0 {
		t.Fatalf("none:\n%s", view)
	}
	press(m, "right", "esc")

	b.effortsKnown = false
	run(m, "/effort")
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "auto    none    low    medium    high    max") || !strings.Contains(view, "models.dev does not list flash") {
		t.Fatalf("unknown:\n%s", view)
	}
}

// nearest finds an effort, or the most that is not more than it.
func TestNearest(t *testing.T) {
	choices := []string{"", "low", "high", "max"}
	for effort, want := range map[string]int{"": 0, "low": 1, "medium": 1, "xhigh": 2, "max": 3, "none": 0, "huge": 0} {
		if got := nearest(choices, effort); got != want {
			t.Errorf("nearest %q = %d, want %d", effort, got, want)
		}
	}
	// In whatever order the choices are: the effort itself where it is, and
	// else the most of them that is not more, which the default is for every other.
	if got := nearest([]string{"high", "low"}, "high"); got != 0 {
		t.Errorf("the effort itself is at %d", got)
	}
	if got := nearest([]string{"low", ""}, "minimal"); got != 1 {
		t.Errorf("the default is at %d", got)
	}
}
