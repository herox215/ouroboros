package gui

import (
	"time"

	"ouroboros/internal/agents"
	"ouroboros/internal/schedule"
)

// The window lists the scheduled jobs of its profile, each run as one of its
// agents, the archivist's among them: what is planned, not what came of it,
// which the sessions show. A job that runs once and ran is not listed. It
// reads the file the node keeps them in, and changes nothing in it.

// AgentJob is a scheduled job, as the window lists it.
type AgentJob struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Agent is the ID of the agent the job runs as, see agents.
	Agent string `json:"agent"`
	// Text is the start of what the job is told.
	Text string `json:"text,omitempty"`
	// Cron or At is when the job runs, in Zone; Next is when it runs next,
	// nil for one that does not.
	Cron   string     `json:"cron,omitempty"`
	At     string     `json:"at,omitempty"`
	Zone   string     `json:"zone,omitempty"`
	Next   *time.Time `json:"next,omitempty"`
	Paused bool       `json:"paused,omitempty"`
}

// jobText is how much of what a job is told the window shows.
const jobText = 280

// AgentJobs lists the scheduled jobs of the profile that are still to run,
// in the order they were made.
func (w *Window) AgentJobs() ([]AgentJob, error) { return w.profile.agentJobs() }

// agentJobs lists the scheduled jobs, as Window.AgentJobs does.
func (p *profile) agentJobs() ([]AgentJob, error) {
	out := []AgentJob{}
	if p.dir == "" {
		return out, nil
	}
	jobs, err := schedule.Read(p.dir.Schedule())
	if err != nil {
		return nil, err
	}
	for _, j := range jobs {
		if j.Cron == "" && j.Next.IsZero() {
			continue // it ran once and is done
		}
		out = append(out, scheduledJob(j))
	}
	return out, nil
}

// scheduledJob is the scheduled job j as the window lists it.
func scheduledJob(j schedule.Job) AgentJob {
	job := AgentJob{
		ID: j.ID, Name: j.Name, Agent: agents.Of(j.Agent), Text: clip(j.Text, jobText),
		Cron: j.Cron, At: j.At, Zone: j.Zone, Paused: j.Paused,
	}
	if !j.Next.IsZero() {
		next := j.Next
		job.Next = &next
	}
	return job
}
