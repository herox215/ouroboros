package tui

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"ouroboros/internal/session"
	"ouroboros/plugin"
)

// /jobs lists the jobs of the node, with where they run and for how long,
// asks for them anew while it is open, and stops the one selected.
func TestJobsScreen(t *testing.T) {
	m, b := newModel(t)
	at := time.Unix(1000, 0)
	m.now = func() time.Time { return at }
	var stopped []int
	started, gate := make(chan struct{}), make(chan struct{})
	b.ask = func(ctx context.Context, _ func(Step), _ func(string)) (string, error) {
		session.TellJobs(ctx, []plugin.Job{
			{N: 1, Command: "npm run dev", Since: at.Add(-65 * time.Second)},
			{N: 2, Command: "go test ./...", Since: at.Add(-time.Second)},
		}, func(n int) bool { stopped = append(stopped, n); return true })
		close(started)
		<-gate
		return "served", nil
	}
	if err := b.sessions.Ask(context.Background(), "s1", "serve it"); err != nil {
		t.Fatal(err)
	}
	<-started

	cmd := run(m, "/jobs")
	if m.screen != screenJobs || !strings.Contains(ansi.Strip(m.View().Content), "…") {
		t.Fatalf("before the list came:\n%s", m.View().Content)
	}
	_, tick := m.Update(cmd())
	m.Update(tick()) // asks anew, a jobsEvery later
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "› job 1 · npm run dev") || !strings.Contains(view, "in “serve it” · for 1m5s") ||
		!strings.Contains(view, "  job 2 · go test ./...") || !strings.Contains(view, "enter stop") {
		t.Fatalf("the list:\n%s", view)
	}
	press(m, "up")
	press(m, "down", "down")
	if m.jobs.sel != 1 {
		t.Fatalf("selected %d", m.jobs.sel)
	}
	m.Update(press(m, "enter")())
	if len(stopped) != 1 || stopped[0] != 2 || m.notice != "job 2 stopped · go test ./..." {
		t.Fatalf("stopped %v, notice %q", stopped, m.notice)
	}

	// It is asked for anew while it is open since, and only then.
	opened := m.jobs.opened
	if m.jobsTicked(jobsTick{opened - 1}) != nil || m.jobsLoaded(jobsMsg{opened: opened - 1}) != nil {
		t.Fatal("a list asked for before it was opened again")
	}
	close(gate)
	until := time.Now().Add(5 * time.Second)
	for {
		if msg := m.jobsTicked(jobsTick{opened})(); len(msg.(jobsMsg).list) == 0 {
			m.Update(msg)
			break
		}
		if time.Now().After(until) {
			t.Fatal("the jobs did not end with the answer")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(ansi.Strip(m.View().Content), "No command runs in the background.") || press(m, "enter") != nil {
		t.Fatalf("without jobs:\n%s", m.View().Content)
	}
	press(m, "esc")
	if m.screen != screenChat || m.jobsTicked(jobsTick{opened}) != nil || m.jobsLoaded(jobsMsg{opened: opened}) != nil {
		t.Fatal("a closed list is asked for")
	}

	// A list that cannot be had says why; a job of a peer is named by it.
	b.fail = map[string]error{"jobs": errors.New("the node is not running")}
	m.Update(run(m, "/jobs")())
	if !strings.Contains(ansi.Strip(m.View().Content), "The jobs cannot be listed: the node is not running") {
		t.Fatalf("failed:\n%s", m.View().Content)
	}
	m.jobs.err, m.jobs.list = nil, []plugin.Job{{Session: "peer-x/c", Peer: "luna", N: 3, Command: "serve", Since: at}}
	if !strings.Contains(ansi.Strip(m.View().Content), "for the peer luna · for 0s") {
		t.Fatalf("of a peer:\n%s", m.View().Content)
	}
}

// ctrl+j lists the jobs of the session on the right: the chat and the input
// make room for the list, a job too long for a line goes on in the next, the
// footer says how to hide it again, and the line of the wait no longer names
// them. ctrl+j again hides the list.
func TestJobsList(t *testing.T) {
	m, _ := newModel(t)
	c := m.chat
	m.apply(c, Event{Kind: plugin.SessionMessage, Text: "serve it"}, true)
	m.apply(c, Event{Kind: plugin.SessionJobs, Text: "job 1 · npm run dev\njob 2 · go test ./internal/tui/... -run TestJobsList -count=1"}, true)
	wide := m.input.Width()

	_, fade := m.Update(tea.KeyPressMsg{Code: 'j', Mod: tea.ModCtrl})
	// It forms from noise: its room is taken at once, and empty before the
	// first frame.
	if m.jobsLevel != 0 || m.input.Width() != wide-30 || strings.Contains(ansi.Strip(m.View().Content), "Jobs") {
		t.Fatalf("the list before it faded in: level %d\n%s", m.jobsLevel, m.View().Content)
	}
	fadeAll(m, fade)
	if m.jobsLevel != jobsFrames || m.jobsFading {
		t.Fatalf("the list after it faded in: level %d, fading %v", m.jobsLevel, m.jobsFading)
	}
	if !m.jobsShown || m.jobsWidth() != 30 || m.input.Width() != wide-30 || m.history.Width() != wide-30 {
		t.Fatalf("shown %v, list %d, input %d, history %d", m.jobsShown, m.jobsWidth(), m.input.Width(), m.history.Width())
	}
	if got := m.waiting(); got != "alpha is thinking" {
		t.Fatalf("the wait with the list: %q", got)
	}
	lines := strings.Split(ansi.Strip(m.View().Content), "\n")
	var list []string
	for _, l := range lines[1 : len(lines)-1] {
		if w := ansi.StringWidth(l); w != m.width {
			t.Fatalf("a line of the body is %d wide, not %d: %q", w, m.width, l)
		}
		list = append(list, strings.Trim(ansi.Cut(l, m.width-30, m.width), "│╭╮╰╯─ "))
	}
	text := strings.Join(list, "\n")
	if !strings.Contains(text, "Jobs\njob 1 · npm run dev\njob 2 · go test") || !strings.Contains(text, "-count=1") {
		t.Fatalf("the list:\n%s", text)
	}
	if got := ansi.Strip(m.footerView()); !strings.Contains(got, jobsKey+" hide") {
		t.Fatalf("the hint of the footer:\n%s", got)
	}

	_, fade = m.Update(tea.KeyPressMsg{Code: 'j', Mod: tea.ModCtrl})
	// It fades out: it keeps its room until it is gone.
	if m.jobsShown || m.input.Width() != wide-30 {
		t.Fatalf("the list fading out: %v, input %d", m.jobsShown, m.input.Width())
	}
	fadeAll(m, fade)
	if m.jobsShown || m.jobsLevel != 0 || m.input.Width() != wide || !strings.Contains(m.waiting(), "job 1 · npm run dev") {
		t.Fatalf("hidden again: %v, input %d, %q", m.jobsShown, m.input.Width(), m.waiting())
	}
}

// fadeAll runs the frames of the list of the jobs on its way in or out,
// until there are no more.
// The services it asks for come in on the way; asking for them anew is not
// waited for.
func fadeAll(m *Model, cmd tea.Cmd) {
	for cmd != nil {
		msg := cmd()
		cmd = nil
		batch, ok := msg.(tea.BatchMsg)
		if !ok {
			batch = tea.BatchMsg{func() tea.Msg { return msg }}
		}
		for _, c := range batch {
			if c == nil {
				continue
			}
			msg := c()
			if _, next := m.Update(msg); isFade(msg) {
				cmd = next
			}
		}
	}
}

func isFade(msg tea.Msg) bool { _, ok := msg.(jobsFadeMsg); return ok }

// The list of the jobs slides in from the right as noise: at first there is
// nothing of it, then its left part on the right, as noise, and at last the
// list itself.
func TestJobsListSlidesInAsNoise(t *testing.T) {
	m, _ := newModel(t)
	m.apply(m.chat, Event{Kind: plugin.SessionMessage, Text: "serve it"}, true)
	m.apply(m.chat, Event{Kind: plugin.SessionJobs, Text: "job 1 · npm run dev"}, true)
	m.jobsShown, m.jobsLevel = true, jobsFrames
	list := m.jobsList() // whole
	w := m.jobsWidth()
	if whole := ansi.Strip(m.dissolve(list, jobsFrames)); whole != ansi.Strip(list) {
		t.Fatalf("arrived:\n%s", whole)
	}
	if none := ansi.Strip(m.dissolve(list, 0)); strings.TrimSpace(none) != "" {
		t.Fatalf("before it comes in:\n%s", none)
	}
	early := strings.Split(ansi.Strip(m.dissolve(list, 2)), "\n")
	if !strings.HasPrefix(early[0], strings.Repeat(" ", w/4)) || strings.TrimSpace(early[0]) == "" {
		t.Fatalf("on its way in, the left part of it on the right:\n%s", strings.Join(early, "\n"))
	}
	half := ansi.Strip(m.dissolve(list, jobsFrames/2))
	if !strings.ContainsAny(half, string(noiseGlyphs)) || strings.Contains(half, "npm run dev") {
		t.Fatalf("halfway:\n%s", half)
	}
	for level := range jobsFrames + 1 {
		for _, l := range strings.Split(m.dissolve(list, level), "\n") {
			if ansi.StringWidth(l) != w {
				t.Fatalf("at %d a line is %d wide, not %d: %q", level, ansi.StringWidth(l), w, ansi.Strip(l))
			}
		}
	}
}

// The services that run past the answers are listed below the jobs: in the
// list of ctrl+j, also with no answer on its way, and in /jobs, which stops
// them.
func TestServicesListed(t *testing.T) {
	m, b := newModel(t)
	at := time.Unix(1000, 0)
	m.now = func() time.Time { return at }
	running := []plugin.ServiceInfo{
		{ID: "t1", Name: "web", Command: "npm run dev", Port: 5173, Since: at.Add(-2 * time.Minute), Until: at.Add(time.Hour)},
		{ID: "t2", Name: "mock", PeerKey: "k", Command: "node mock.js", Since: at.Add(-time.Minute), Until: at.Add(time.Hour)},
	}
	var stopped []string
	b.sessions.ServicesFrom(func() []session.Services {
		return []session.Services{{
			List: func() []plugin.ServiceInfo { return running },
			Stop: func(id string) bool { stopped = append(stopped, id); return true },
		}}
	})

	_, cmd := m.Update(tea.KeyPressMsg{Code: 'j', Mod: tea.ModCtrl})
	fadeAll(m, cmd)
	list := ansi.Strip(m.jobsList())
	if !strings.Contains(list, "Services") || !strings.Contains(list, "web · :5173") || !strings.Contains(list, "npm run dev") ||
		!strings.Contains(list, "mock · ") {
		t.Fatalf("the list:\n%s", list)
	}
	m.Update(tea.KeyPressMsg{Code: 'j', Mod: tea.ModCtrl})

	m.Update(run(m, "/jobs")())
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "No command runs in the background.") || !strings.Contains(view, "› service web · npm run dev") ||
		!strings.Contains(view, "yours · for 2m0s · until") || !strings.Contains(view, "localhost:5173") || !strings.Contains(view, "for the peer ") {
		t.Fatalf("/jobs:\n%s", view)
	}
	press(m, "down")
	m.Update(press(m, "enter")())
	if !slices.Equal(stopped, []string{"t2"}) {
		t.Fatalf("stopped %v", stopped)
	}
}
