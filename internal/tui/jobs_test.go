package tui

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
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

// ctrl+j lists the jobs of the session in the column on the right: the
// conversation makes room for it, a job too long for a line goes on in the
// next, the line of the keys says how to hide it again, and the line of the
// wait no longer names them. ctrl+j again hides the list.
func TestJobsList(t *testing.T) {
	m, _ := newModel(t)
	c := m.chat
	m.apply(c, Event{Kind: plugin.SessionMessage, Text: "serve it"}, true)
	m.apply(c, Event{Kind: plugin.SessionJobs, Text: "job 1 · npm run dev\njob 2 · go test ./internal/tui/... -run TestJobsList -count=1"}, true)
	wide := m.history.Width()

	m.Update(tea.KeyPressMsg{Code: 'j', Mod: tea.ModCtrl})
	if !m.jobsShown || m.railWidth() == 0 || m.history.Width() >= wide {
		t.Fatalf("shown %v, rail %d, history %d of %d", m.jobsShown, m.railWidth(), m.history.Width(), wide)
	}
	if got := m.waiting(); got != "alpha is thinking" {
		t.Fatalf("the wait with the list: %q", got)
	}
	for _, l := range strings.Split(m.View().Content, "\n") {
		if w := ansi.StringWidth(l); w != m.width {
			t.Fatalf("a line is %d wide, not %d: %q", w, m.width, l)
		}
	}
	var list []string
	for _, l := range strings.Split(ansi.Strip(m.railView(m.railWidth(), m.height)), "\n") {
		list = append(list, strings.TrimSpace(l))
	}
	text := strings.Join(list, "\n")
	if !strings.Contains(text, "JOBS") || !strings.Contains(text, "job 1 · npm run\ndev") || !strings.Contains(text, "job 2 · go test") || !strings.Contains(text, "-count=1") {
		t.Fatalf("the list:\n%s", text)
	}
	if got := ansi.Strip(m.footerHint()); !strings.Contains(got, jobsKey+" hide") {
		t.Fatalf("the hint:\n%s", got)
	}

	m.Update(tea.KeyPressMsg{Code: 'j', Mod: tea.ModCtrl})
	if m.jobsShown || m.history.Width() != wide || !strings.Contains(m.waiting(), "job 1 · npm run dev") {
		t.Fatalf("hidden again: %v, history %d, %q", m.jobsShown, m.history.Width(), m.waiting())
	}
}

// loaded runs what cmd asks for and gives the UI what comes of it, a batch
// one by one; what that asks for in turn is not waited for.
func loaded(m *Model, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		m.Update(msg)
		return
	}
	for _, c := range batch {
		if c != nil {
			m.Update(c())
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
	loaded(m, cmd)
	list := ansi.Strip(strings.Join(m.jobLines(m.chat, 40), "\n"))
	if !strings.Contains(list, "SERVICES") || !strings.Contains(list, "web · :5173") || !strings.Contains(list, "npm run dev") ||
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

// While the list of ctrl+j is shown, the services are asked for anew every
// while; what was asked for before the list was hidden, or shown anew since,
// is dropped and asks for no more.
func TestServicesAskedForAnewWhileShown(t *testing.T) {
	m, b := newModel(t)
	running := []plugin.ServiceInfo{{ID: "t1", Name: "web", Command: "npm run dev"}}
	b.sessions.ServicesFrom(func() []session.Services {
		return []session.Services{{List: func() []plugin.ServiceInfo { return running }, Stop: func(string) bool { return false }}}
	})
	m.toggleJobs()
	asked := m.servicesAsked
	tick := m.servicesLoaded(servicesMsg{running, asked})
	if tick == nil || len(m.services) != 1 {
		t.Fatalf("the services shown: %+v", m.services)
	}
	if msg, ok := tick().(servicesTick); !ok || msg.asked != asked {
		t.Fatalf("a while later: %+v", msg)
	}
	_, load := m.Update(servicesTick{asked})
	if load == nil {
		t.Fatal("the services are not asked for anew")
	}
	if msg, ok := load().(servicesMsg); !ok || msg.asked != asked || len(msg.list) != 1 || msg.list[0].ID != "t1" {
		t.Fatalf("asked anew: %+v", msg)
	}
	if _, cmd := m.Update(servicesTick{asked - 1}); cmd != nil {
		t.Error("a tick of before asks anew")
	}
	if m.servicesLoaded(servicesMsg{nil, asked - 1}) != nil || len(m.services) != 1 {
		t.Errorf("a list of before: %+v", m.services)
	}
	m.toggleJobs() // hidden
	if _, cmd := m.Update(servicesTick{asked}); cmd != nil {
		t.Error("a hidden list asks anew")
	}
	if m.servicesLoaded(servicesMsg{nil, asked}) != nil || len(m.services) != 1 {
		t.Errorf("a hidden list: %+v", m.services)
	}
}

// /jobs lists the services below the jobs, the one selected marked, each
// with its port if it has one, and stops the one selected; up and down move
// the selection, which stays on an entry when the list gets shorter.
func TestJobsScreenWithJobsAndServices(t *testing.T) {
	m, b := newModel(t)
	at := time.Unix(1000, 0)
	m.now = func() time.Time { return at }
	running := []plugin.ServiceInfo{
		{ID: "t1", Name: "web", Command: "npm run dev", Since: at, Until: at.Add(time.Hour)},
		{ID: "t2", Name: "api", Command: "go run .", Port: 8080, Since: at, Until: at.Add(time.Hour)},
	}
	var stopped []string
	b.sessions.ServicesFrom(func() []session.Services {
		return []session.Services{{List: func() []plugin.ServiceInfo { return running }, Stop: func(id string) bool { stopped = append(stopped, id); return true }}}
	})
	m.Update(run(m, "/jobs")())
	if v := view(m); strings.Contains(v, "localhost:0") || !strings.Contains(v, "localhost:8080") || !strings.Contains(v, "› service web") {
		t.Fatalf("/jobs:\n%s", v)
	}
	// With no job before them, the first service is the first entry.
	m.Update(press(m, "enter")())
	if !slices.Equal(stopped, []string{"t1"}) {
		t.Fatalf("stopped %v", stopped)
	}
	// With a job before them, the services follow it.
	m.jobs.list = []plugin.Job{{N: 1, Command: "sleep 9", Since: at}}
	press(m, "down")
	if v := view(m); !strings.Contains(v, "› service web") || strings.Contains(v, "› job") {
		t.Fatalf("the first service selected:\n%s", v)
	}
	press(m, "down")
	if v := view(m); !strings.Contains(v, "› service api") {
		t.Fatalf("the second service selected:\n%s", v)
	}
	m.Update(press(m, "enter")())
	press(m, "up")
	if m.jobs.sel != 1 || !slices.Equal(stopped, []string{"t1", "t2"}) {
		t.Fatalf("selected %d, stopped %v", m.jobs.sel, stopped)
	}
	m.jobs.sel = 2
	m.jobsLoaded(jobsMsg{list: m.jobs.list, services: running[:1], opened: m.jobs.opened})
	if m.jobs.sel != 1 {
		t.Fatalf("with one service fewer the selection is %d", m.jobs.sel)
	}
	// Without services there is no head for them.
	m.jobs.services = nil
	if v := view(m); strings.Contains(v, "Services") {
		t.Fatalf("no services:\n%s", v)
	}
}

// The list of ctrl+j is a third of a narrow window wide, and names the
// tasks and the services only when there are some, a port only where there
// is one.
func TestJobsListParts(t *testing.T) {
	m, _ := newModel(t)
	m.Update(tea.WindowSizeMsg{Width: 60, Height: 30})
	m.jobsShown = true
	m.layout()
	if w := m.railWidth(); w != 20 {
		t.Fatalf("the list is %d wide in 60", w)
	}
	rail := m.railView(m.railWidth(), m.height)
	list := ansi.Strip(rail)
	if lipgloss.Width(rail) != 20 || strings.Contains(list, "TASKS") || strings.Contains(list, "SERVICES") {
		t.Fatalf("the list:\n%s", list)
	}
	m.chat.tasks = []string{"task 1 · read"}
	m.services = []plugin.ServiceInfo{{Name: "web", Command: "npm run dev"}, {Name: "api", Command: "go run .", Port: 8080}}
	list = ansi.Strip(strings.Join(m.jobLines(m.chat, 30), "\n"))
	if !strings.Contains(list, "TASKS") || !strings.Contains(list, "SERVICES") || strings.Contains(list, ":0") || !strings.Contains(list, "api · :8080") {
		t.Fatalf("the list:\n%s", list)
	}
}
