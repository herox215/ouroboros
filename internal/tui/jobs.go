package tui

import (
	"cmp"
	"fmt"
	"math"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

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
func (s *jobsScreen) view(m *Model) string {
	lines := []string{
		boldStyle.Render("Jobs"), "",
		mutedStyle.Render("The commands the agent runs in the background for the answers on their way:"),
		mutedStyle.Render("in your sessions and in the conversations of peers. They end with their answer."), "",
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
		lines = append(lines, mark+name, "    "+mutedStyle.Render(about))
	}
	return m.dialog(lines)
}

// jobsKey is the key that shows the list of the jobs, and hides it again.
const jobsKey = "ctrl+j"

// jobsListWidth is how wide the list of the jobs on the right is at most,
// with its border.
const jobsListWidth = 30

// jobsWidth is how wide the list of the jobs on the right is: a third of the
// window at most, 0 while it is hidden. While it fades out it keeps its room.
func (m *Model) jobsWidth() int {
	if !m.jobsShown && m.jobsLevel == 0 {
		return 0
	}
	return min(jobsListWidth, m.width/3)
}

// jobsList lists the jobs of the session shown on the right of the chat,
// and the services below them, as high as the chat and the input. A job too long for a line goes on in the
// next ones, indented.
func (m *Model) jobsList() string {
	w, h := m.jobsWidth(), m.bodyHeight()
	inner := max(1, w-4) // border and padding
	lines := []string{boldStyle.Render("Jobs")}
	if len(m.chat.jobs) == 0 {
		lines = append(lines, mutedStyle.Render(lipgloss.Wrap("No command runs in the background.", inner, "")))
	}
	// A line too long goes on in the next ones, indented.
	wrap := func(text string) []string {
		wrapped := strings.Split(lipgloss.Wrap(text, max(1, inner-2), ""), "\n")
		for i := 1; i < len(wrapped); i++ {
			wrapped[i] = "  " + wrapped[i]
		}
		return wrapped
	}
	for _, job := range m.chat.jobs {
		lines = append(lines, wrap(job)...)
	}
	if len(m.services) > 0 {
		lines = append(lines, "", boldStyle.Render("Services"))
	}
	for _, sv := range m.services {
		text := sv.Name
		if sv.PeerKey != "" {
			text += " · " + sv.Peer
		}
		if sv.Port > 0 {
			text += fmt.Sprintf(" · :%d", sv.Port)
		}
		lines = append(lines, wrap(text)...)
		for _, l := range wrap(sv.Command) {
			lines = append(lines, mutedStyle.Render("  "+l))
		}
	}
	list := box(false, m.light).Width(w).Height(h).MaxHeight(h).Render(strings.Join(lines, "\n"))
	if m.jobsLevel >= jobsFrames {
		return list
	}
	return m.dissolve(list, m.jobsLevel)
}

// noiseGlyphs are what the noise the list of the jobs comes in as is drawn
// with: light dots, so that it stays in the background; noiseDense is how
// many of its cells have one, the others are blank.
var noiseGlyphs = []rune("·∙:")

const noiseDense = 0.35

// dissolve is view at the level of jobsFrames it came in to: it slides in
// from the right, slowing down as it arrives, as noise that flickers from
// frame to frame, and every cell of it settles into itself at a time of its
// own, the last once it arrived. Going out it does the same backwards.
func (m *Model) dissolve(view string, level int) string {
	t := float64(level) / jobsFrames
	arrived := 1 - math.Pow(1-min(1, t/0.7), 3) // how far it slid in, from 0 to 1
	lines := strings.Split(view, "\n")
	for y, line := range lines {
		w := ansi.StringWidth(line)
		shown := int(math.Round(arrived * float64(w))) // its columns on the left that are in
		var b strings.Builder
		b.WriteString(strings.Repeat(" ", w-shown))
		from := -1 // the first column of a run of cells that are themselves
		for x := 0; x <= shown; x++ {
			cell := float64(y*w + x) // the noise goes along with the cell
			if x < shown && t >= 0.4+0.6*noise(cell) {
				if from < 0 {
					from = x
				}
				continue
			}
			if from >= 0 {
				b.WriteString(ansi.Cut(line, from, x))
				from = -1
			}
			if x < shown {
				b.WriteString(m.noiseCell(cell, level))
			}
		}
		lines[y] = b.String()
	}
	return strings.Join(lines, "\n")
}

// noiseCell is a cell of the noise at the frame level: blank or a dot, in
// the gray of a faded edge, and from frame to frame another.
func (m *Model) noiseCell(cell float64, level int) string {
	at := noise(cell + float64(level)*7.31)
	if at >= noiseDense {
		return " "
	}
	glyph := noiseGlyphs[int(at/noiseDense*float64(len(noiseGlyphs)))]
	return lipgloss.NewStyle().Foreground(fadeColor(m.light)).Render(string(glyph))
}

// The list of the jobs slides in as noise, and out again, over jobsFrames
// frames, one every jobsFrame.
const (
	jobsFrames = 14
	jobsFrame  = 25 * time.Millisecond
)

// jobsFadeMsg is a frame of the list of the jobs on its way in or out.
type jobsFadeMsg struct{}

// toggleJobs shows the list of the jobs, or hides it again: it fades in or
// out. The chat and the input make room for it at once, and take it back
// once it faded out.
func (m *Model) toggleJobs() tea.Cmd {
	m.jobsShown = !m.jobsShown
	var load tea.Cmd
	if m.jobsShown {
		m.servicesAsked++
		load = m.loadServices()
		if m.jobsLevel == 0 {
			m.layout()
		}
	}
	if m.jobsFading {
		return load // the frames on their way turn around
	}
	m.jobsFading = true
	return tea.Batch(load, tea.Tick(jobsFrame, func(time.Time) tea.Msg { return jobsFadeMsg{} }))
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

// jobsFaded takes the list of the jobs a frame further on its way, and asks
// for the next until it is whole or gone.
func (m *Model) jobsFaded() tea.Cmd {
	if m.jobsShown {
		m.jobsLevel = min(jobsFrames, m.jobsLevel+1)
	} else {
		m.jobsLevel = max(0, m.jobsLevel-1)
	}
	if m.jobsLevel == 0 && !m.jobsShown {
		m.layout() // the chat and the input take the room back
	}
	if m.jobsLevel == 0 || m.jobsLevel == jobsFrames {
		m.jobsFading = false
		return nil
	}
	return tea.Tick(jobsFrame, func(time.Time) tea.Msg { return jobsFadeMsg{} })
}
