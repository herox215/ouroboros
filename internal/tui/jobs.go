package tui

import (
	"cmp"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"

	"ouroboros/plugin"
)

// jobsScreen lists the commands that run in the background for the answers
// on their way, for /jobs: in the sessions of the owner and in the
// conversations of peers; and below them the services that run past the
// answers. The one selected can be stopped. The list is asked for anew
// every jobsEvery while it is open.
type jobsScreen struct {
	list     []plugin.Job
	services []plugin.ServiceInfo
	err      error // why the list could not be asked for
	loaded   bool  // the list came once since it was opened
	sel      int
	// opened counts how often the screen was opened: a list that was asked
	// for before it was opened again asks for no more.
	opened int
}

// jobsEvery is how often an open list of the jobs is asked for anew.
const jobsEvery = time.Second

type (
	// jobsMsg is the list of the jobs, asked for while the screen was
	// opened for the time opened.
	jobsMsg struct {
		list     []plugin.Job
		services []plugin.ServiceInfo
		err      error
		opened   int
	}
	// jobsTick asks for the list anew.
	jobsTick struct{ opened int }
)

// openJobs opens the list of the jobs of the node.
func (m *Model) openJobs() tea.Cmd {
	s := &m.jobs
	s.opened++
	s.loaded, s.err, s.sel = false, nil, 0
	m.screen = screenJobs
	return m.loadJobs(s.opened)
}

// loadJobs asks for the jobs of the node.
func (m *Model) loadJobs(opened int) tea.Cmd {
	return func() tea.Msg {
		list, err := m.backend.Jobs(m.ctx)
		services, serr := m.backend.Services(m.ctx)
		return jobsMsg{list, services, cmp.Or(err, serr), opened}
	}
}

// jobsLoaded shows the list, and asks for it anew in a while, as long as
// the screen is open since it was asked for.
func (m *Model) jobsLoaded(msg jobsMsg) tea.Cmd {
	s := &m.jobs
	if msg.opened != s.opened || m.screen != screenJobs {
		return nil
	}
	s.list, s.services, s.err, s.loaded = msg.list, msg.services, msg.err, true
	s.sel = min(s.sel, max(0, s.count()-1))
	return tea.Tick(jobsEvery, func(time.Time) tea.Msg { return jobsTick{msg.opened} })
}

// jobsTicked asks for the list anew, if the screen is still open since.
func (m *Model) jobsTicked(msg jobsTick) tea.Cmd {
	if msg.opened != m.jobs.opened || m.screen != screenJobs {
		return nil
	}
	return m.loadJobs(msg.opened)
}

// count is how many entries the screen lists: the jobs, then the services.
func (s *jobsScreen) count() int { return len(s.list) + len(s.services) }

func (s *jobsScreen) handleKey(m *Model, msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "up", "k":
		s.sel = max(0, s.sel-1)
	case "down", "j":
		s.sel = min(max(0, s.count()-1), s.sel+1)
	case "esc":
		m.screen = screenChat
	case "enter", "s":
		if s.sel >= s.count() {
			return nil
		}
		if s.sel >= len(s.list) {
			sv := s.services[s.sel-len(s.list)]
			return func() tea.Msg {
				return actionMsg{"service " + sv.Name + " stopped", m.backend.StopService(m.ctx, sv.ID)}
			}
		}
		job := s.list[s.sel]
		return func() tea.Msg {
			return actionMsg{fmt.Sprintf("job %d stopped · %s", job.N, job.Command), m.backend.StopJob(m.ctx, job.Session, job.N)}
		}
	}
	return nil
}

// view shows, in a dialog, each job with where it runs and for how long.
func (s *jobsScreen) view(m *Model) (string, []hit) {
	lines := []string{
		boldStyle.Render("Jobs"), "",
		mutedStyle.Render("The commands the agent runs in the background for the answers on their way:"),
		mutedStyle.Render("in your sessions and in the conversations of peers. They end with their answer."), "",
	}
	// A click on a job or a service selects it, and on the one selected
	// stops it.
	var hits []hit
	entry := func(i int) {
		do := choosing(i == s.sel, func() { s.sel = i }, func() tea.Cmd { return s.handleKey(m, enterKey) })
		hits = append(hits, onLine(len(lines), do), onLine(len(lines)+1, do))
	}
	switch {
	case s.err != nil:
		lines = append(lines, errStyle.Render("The jobs cannot be listed: "+s.err.Error()))
	case !s.loaded:
		lines = append(lines, mutedStyle.Render("…"))
	case len(s.list) == 0:
		lines = append(lines, mutedStyle.Render("No command runs in the background."))
	}
	for i, j := range s.list {
		where := "in “" + short(j.Title, 40) + "”"
		if j.Peer != "" {
			where = "for the peer " + j.Peer
		}
		mark, name := "  ", fmt.Sprintf("job %d · %s", j.N, j.Command)
		if i == s.sel {
			mark, name = "› ", boldStyle.Foreground(accent).Render(name)
		}
		took := m.now().Sub(j.Since).Round(time.Second)
		entry(i)
		lines = append(lines, mark+name, "    "+mutedStyle.Render(fmt.Sprintf("%s · for %s", where, took)))
	}
	if len(s.services) > 0 {
		lines = append(lines, "", boldStyle.Render("Services"), "",
			mutedStyle.Render("What runs past the answers, until it ends, is stopped, its time is up, or the node stops."), "")
	}
	for i, sv := range s.services {
		whose := "yours"
		if sv.PeerKey != "" {
			whose = "for the peer " + sv.Peer
		}
		mark, name := "  ", "service "+sv.Name+" · "+sv.Command
		if len(s.list)+i == s.sel {
			mark, name = "› ", boldStyle.Foreground(accent).Render(name)
		}
		about := fmt.Sprintf("%s · for %s · until %s", whose, m.now().Sub(sv.Since).Round(time.Second), sv.Until.Format("15:04"))
		if sv.Port > 0 {
			about += fmt.Sprintf(" · localhost:%d", sv.Port)
		}
		entry(len(s.list) + i)
		lines = append(lines, mark+name, "    "+mutedStyle.Render(about))
	}
	return m.dialog(lines), hits
}

// jobsKey is the key that shows the list of the jobs, and hides it again.
const jobsKey = "ctrl+j"

// toggleJobs lists the jobs in the column on the right, or what stands
// there again. A window too narrow for the column gets it for the jobs.
func (m *Model) toggleJobs() tea.Cmd {
	m.jobsShown = !m.jobsShown
	m.layout()
	if !m.jobsShown {
		return nil
	}
	m.servicesAsked++
	return m.loadServices()
}

type (
	// servicesMsg is the list of the services, asked for while the list of
	// the jobs was shown for the time asked.
	servicesMsg struct {
		list  []plugin.ServiceInfo
		asked int
	}
	// servicesTick asks for the services anew.
	servicesTick struct{ asked int }
)

// loadServices asks for the services that run past the answers.
func (m *Model) loadServices() tea.Cmd {
	asked := m.servicesAsked
	return func() tea.Msg {
		list, _ := m.backend.Services(m.ctx) // the list shows what it can
		return servicesMsg{list, asked}
	}
}

// servicesLoaded shows the services, and asks for them anew in a while, as
// long as the list is shown since they were asked for.
func (m *Model) servicesLoaded(msg servicesMsg) tea.Cmd {
	if msg.asked != m.servicesAsked || !m.jobsShown {
		return nil
	}
	m.services = msg.list
	return tea.Tick(jobsEvery, func(time.Time) tea.Msg { return servicesTick{msg.asked} })
}
