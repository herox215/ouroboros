// Package schedule exposes owner-only scheduled answers and runs their timer.
package schedule

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"ouroboros/internal/home"
	store "ouroboros/internal/schedule"
	"ouroboros/internal/session"
	"ouroboros/internal/workdir"
	"ouroboros/plugin"
)

const (
	Read     = "schedule_read"
	Update   = "schedule_update"
	runLimit = time.Hour
)

type scheduled interface {
	Notify(context.Context, string, string) error
	RunScheduled(context.Context, string, string, string, bool) (string, <-chan session.ScheduledResult, error)
}

// machineZone finds the zone of new jobs. Tests replace it.
var machineZone = store.MachineZone

type needs struct{ Sessions plugin.Sessions }

func Plugin(dir home.Dir) plugin.Plugin {
	return plugin.New("schedule", func(ctx *plugin.Context, n needs) error {
		s := &scheduler{path: dir.Schedule(), sessions: n.Sessions, log: dir.Log, now: time.Now, limit: runLimit}
		plugin.Add(ctx, plugin.Serving{Start: s.start, Stop: s.stop})
		ctx.OnStop(s.close)
		for _, tool := range s.tools() {
			plugin.Add(ctx, tool)
		}
		return nil
	})
}

// life serializes complete Start/Stop transitions. mu protects the store,
// timer wakeups and active IDs; Stop never waits with mu held.
type scheduler struct {
	life     sync.Mutex
	mu       sync.Mutex
	path     string
	store    *store.Store
	sessions scheduled
	log      func(string, ...any)
	now      func() time.Time
	limit    time.Duration
	cancel   context.CancelFunc
	wake     chan struct{}
	done     chan struct{}
	active   map[string]bool
	wg       sync.WaitGroup
	closed   bool
}

func (s *scheduler) open() error {
	if s.closed {
		return errors.New("scheduler is closed")
	}
	if s.store == nil {
		st, err := store.Open(s.path)
		if err != nil {
			return err
		}
		s.store = st
	}
	return nil
}
func (s *scheduler) report(format string, args ...any) {
	if s.log != nil {
		s.log("schedule: "+format, args...)
	}
}
func (s *scheduler) changed() {
	if s.wake != nil {
		select {
		case s.wake <- struct{}{}:
		default:
		}
	}
}
func (s *scheduler) start() {
	s.life.Lock()
	defer s.life.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.wake = make(chan struct{}, 1)
	s.done = make(chan struct{})
	s.active = make(map[string]bool)
	go s.loop(ctx, s.wake, s.done)
}
func (s *scheduler) stop()  { s.shutdown(false) }
func (s *scheduler) close() { s.shutdown(true) }
func (s *scheduler) shutdown(final bool) {
	s.life.Lock()
	defer s.life.Unlock()
	s.mu.Lock()
	s.closed = s.closed || final
	cancel, done := s.cancel, s.done
	if cancel != nil {
		cancel()
	}
	s.mu.Unlock()
	if cancel != nil {
		<-done
		s.wg.Wait()
		s.mu.Lock()
		s.cancel = nil
		s.mu.Unlock()
	}
}
func (s *scheduler) loop(ctx context.Context, wake <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	for {
		if ctx.Err() != nil {
			return
		}
		delay := s.tick(ctx)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

// tick claims durably before starting anything. Claim returns the old Next,
// even when it advances a recurring job past many missed occurrences.
func (s *scheduler) tick(ctx context.Context) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx.Err() != nil {
		return time.Minute
	}
	if err := s.open(); err != nil {
		s.report("open: %v", err)
		return time.Minute
	}
	now := s.now()
	jobs, err := s.store.Claim(now)
	if err != nil {
		s.report("claim: %v", err)
		return time.Minute
	}
	for _, job := range jobs {
		switch {
		case now.Sub(job.Next) > 24*time.Hour:
			// Claim has already recorded this as missed, not running.
			// Send only outcome metadata, never the stored job instructions.
			text := fmt.Sprintf("Scheduled job %q (%s), planned for %s, was missed because it is more than 24 hours late. It was not executed.", job.Name, job.ID, readable(job.Next, job.Zone))
			if err := s.sessions.Notify(ctx, "Missed scheduled job", text); err != nil {
				s.report("notify missed %s: %v", job.ID, err)
			}
			s.report("missed %q (%s), planned for %s; not executed", job.Name, job.ID, job.Next.Format(time.RFC3339))
		case s.active[job.ID]:
			s.finish(job.ID, store.Run{At: now, Status: "skipped", Result: "Previous run is still active."})
		default:
			s.active[job.ID] = true
			s.wg.Add(1)
			go s.run(ctx, job, now)
		}
	}
	delay := time.Hour
	for _, job := range s.store.List() {
		if job.Paused || job.Next.IsZero() {
			continue
		}
		d := job.Next.Sub(now)
		if d < delay {
			delay = d
		}
	}
	if delay < time.Millisecond {
		delay = time.Millisecond
	}
	return delay
}
func (s *scheduler) finish(id string, run store.Run) {
	if err := s.store.Finish(id, run); err != nil {
		s.report("finish %s: %v", id, err)
	}
}
func (s *scheduler) run(parent context.Context, job store.Job, now time.Time) {
	defer s.wg.Done()
	ctx, cancel := context.WithTimeout(parent, s.limit)
	defer cancel()
	run := store.Run{At: now, Status: "completed"}
	if now.Sub(job.Next) > time.Minute {
		run.Status = "late"
	}
	text := fmt.Sprintf("[Scheduled job %q (%s), planned for %s; running at %s. The owner created it on %s. This is a generated scheduled request, not a message from the owner now.]\n\n%s", job.Name, job.ID, readable(job.Next, job.Zone), readable(now, job.Zone), job.Created.Format(time.RFC3339), job.Text)
	id, result, err := s.sessions.RunScheduled(ctx, job.Name, text, job.Directory, job.Ask != "never")
	run.Session = id
	if err == nil {
		select {
		case r, ok := <-result:
			if !ok {
				err = errors.New("scheduled session ended without a result")
			} else {
				run.Result, err = r.Text, r.Err
			}
		case <-ctx.Done():
			err = ctx.Err()
		}
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err != nil {
		run.Status = "failed"
		run.Result = err.Error()
	}
	run.Result = firstLine(run.Result)
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.active, job.ID)
	s.finish(job.ID, run)
}
func firstLine(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	if len(line) > 512 {
		line = line[:512]
		for !utf8.ValidString(line) {
			line = line[:len(line)-1]
		}
	}
	return line
}
func readable(t time.Time, zone string) string {
	if t.IsZero() {
		return ""
	}
	if loc, err := time.LoadLocation(zone); err == nil {
		t = t.In(loc)
	}
	return t.Format("Mon 02 Jan 2006 15:04 MST (Z07:00)")
}

// strictObject rejects unknown fields, null, duplicate keys and trailing JSON.
// Pointers then distinguish an omitted update field from an explicit false.
func strictObject(raw json.RawMessage, dst any) error {
	if len(raw) > 16*1024 {
		return errors.New("schedule arguments exceed 16 KiB")
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return errors.New("schedule arguments must be an object")
	}
	seen := map[string]bool{}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return err
		}
		name := key.(string)
		if seen[name] {
			return fmt.Errorf("duplicate argument %q", name)
		}
		seen[name] = true
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return err
		}
		if string(value) == "null" {
			return fmt.Errorf("argument %q cannot be null", name)
		}
	}
	if _, err := dec.Token(); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("unexpected trailing JSON")
	}
	dec = json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}
func owner(ctx context.Context) error {
	if _, peer := plugin.OriginFrom(ctx); peer {
		return errors.New("scheduled jobs are owner-only")
	}
	if plugin.ContextID(ctx) == "" {
		return errors.New("scheduled jobs require an owner conversation")
	}
	return ctx.Err()
}

type updateArgs struct {
	Action string  `json:"action"`
	ID     string  `json:"id"`
	Name   *string `json:"name"`
	Text   *string `json:"text"`
	At     *string `json:"at"`
	Cron   *string `json:"cron"`
	Ask    *string `json:"ask"`
	Paused *bool   `json:"paused"`
}

func (a updateArgs) fields() bool {
	return a.Name != nil || a.Text != nil || a.At != nil || a.Cron != nil || a.Ask != nil || a.Paused != nil
}
func (a updateArgs) validate() error {
	switch a.Action {
	case "create":
		if a.ID != "" || a.Name == nil || a.Text == nil || (a.At == nil) == (a.Cron == nil) {
			return errors.New("create requires name, text and exactly one of at or cron, and takes no id")
		}
	case "update":
		if strings.TrimSpace(a.ID) == "" || !a.fields() {
			return errors.New("update requires id and at least one changed field")
		}
	case "remove":
		if strings.TrimSpace(a.ID) == "" || a.fields() {
			return errors.New("remove requires only action and id")
		}
	default:
		return errors.New("action must be create, update or remove")
	}
	if a.At != nil && a.Cron != nil {
		return errors.New("supply at or cron, not both")
	}
	for _, field := range []struct {
		name  string
		value *string
		max   int
	}{{"name", a.Name, 128}, {"text", a.Text, 4096}, {"at", a.At, 128}, {"cron", a.Cron, 256}} {
		if field.value != nil && (strings.TrimSpace(*field.value) == "" || len(*field.value) > field.max) {
			return fmt.Errorf("%s must be nonempty and at most %d bytes", field.name, field.max)
		}
	}
	if a.Ask != nil && *a.Ask != "thread" && *a.Ask != "never" {
		return errors.New("ask must be thread or never")
	}
	return nil
}
func (a updateArgs) apply(job *store.Job) error {
	if a.Name != nil {
		job.Name = strings.TrimSpace(*a.Name)
	}
	if a.Text != nil {
		job.Text = *a.Text
	}
	if a.Ask != nil {
		job.Ask = *a.Ask
	}
	if a.Paused != nil {
		job.Paused = *a.Paused
	}
	if a.Cron != nil {
		job.Cron = *a.Cron
		job.At = ""
	}
	if a.At != nil {
		at, err := parseAt(*a.At, job.Zone)
		if err != nil {
			return err
		}
		job.At = at
		job.Cron = ""
	}
	return nil
}
func parseAt(text, zone string) (string, error) {
	if t, err := time.Parse(time.RFC3339, text); err == nil {
		return t.Format(time.RFC3339), nil
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return "", err
	}
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02T15:04", "2006-01-02 15:04"} {
		if t, err := time.ParseInLocation(layout, text, loc); err == nil && t.Format(layout) == text {
			return t.Format(time.RFC3339), nil
		}
	}
	return "", errors.New("at must be RFC3339 or YYYY-MM-DDTHH:MM in the job's time zone; use an explicit offset for ambiguous daylight-saving times")
}

func output(v any, err error) (string, error) {
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(v)
	return string(b), err
}

type view struct {
	Job      store.Job `json:"job"`
	Next     string    `json:"next_readable,omitempty"`
	NextRuns []string  `json:"next_runs,omitempty"`
}

func jobView(job store.Job) view {
	v := view{Job: job, Next: readable(job.Next, job.Zone)}
	if job.Next.IsZero() || job.Paused {
		return v
	}
	v.NextRuns = []string{v.Next}
	if job.Cron == "" {
		return v
	}
	cron, err := store.ParseCron(job.Cron)
	if err != nil {
		return v
	}
	loc, err := time.LoadLocation(job.Zone)
	if err != nil {
		return v
	}
	next := job.Next
	for range 2 {
		next, err = cron.Next(next, loc)
		if err != nil {
			break
		}
		v.NextRuns = append(v.NextRuns, readable(next, job.Zone))
	}
	return v
}
func (s *scheduler) tools() []plugin.Tool {
	const about = "Owner-only scheduled model answers: every run uses the model and costs money. Results and, by default, questions go to the owner's thread. Times without an offset use the node's time zone. "
	return []plugin.Tool{
		{Name: Read, OwnerOnly: true, Description: about + "List jobs and their next run, or read one job with its last ten runs by id.", Schema: `{"type":"object","properties":{"id":{"type":"string"}},"additionalProperties":false}`, Run: s.read},
		{Name: Update, OwnerOnly: true, Description: about + "Manage jobs with action create, update or remove. Create requires name, text and exactly one of at (RFC3339 or local YYYY-MM-DDTHH:MM) or cron (five fields); ask defaults to thread, or never for unattended runs. The current conversation's project is saved automatically. Update requires id and changed fields; at and cron replace each other. Remove takes only action and id. At most 32 jobs; recurring runs must be at least 15 minutes apart. Returns the saved job and next run.", Schema: `{"type":"object","properties":{"action":{"type":"string","enum":["create","update","remove"]},"id":{"type":"string"},"name":{"type":"string"},"text":{"type":"string"},"at":{"type":"string"},"cron":{"type":"string"},"ask":{"type":"string","enum":["thread","never"]},"paused":{"type":"boolean"}},"required":["action"],"additionalProperties":false}`, Run: s.update},
	}
}
func (s *scheduler) read(ctx context.Context, raw json.RawMessage) (string, error) {
	if err := owner(ctx); err != nil {
		return "", err
	}
	var args struct {
		ID *string `json:"id"`
	}
	if err := strictObject(raw, &args); err != nil {
		return "", err
	}
	if args.ID != nil && strings.TrimSpace(*args.ID) == "" {
		args.ID = nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.open(); err != nil {
		return "", err
	}
	if args.ID != nil {
		job, err := s.store.Get(*args.ID)
		return output(jobView(job), err)
	}
	jobs := []view{}
	for _, job := range s.store.List() {
		jobs = append(jobs, jobView(job))
	}
	return output(jobs, nil)
}
func (s *scheduler) update(ctx context.Context, raw json.RawMessage) (string, error) {
	if err := owner(ctx); err != nil {
		return "", err
	}
	var a updateArgs
	if err := strictObject(raw, &a); err != nil {
		return "", err
	}
	// Some model interfaces supply empty strings for unused optional fields.
	for _, field := range []**string{&a.Name, &a.Text, &a.At, &a.Cron, &a.Ask} {
		if *field != nil && **field == "" {
			*field = nil
		}
	}
	if err := a.validate(); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.open(); err != nil {
		return "", err
	}
	now := s.now()
	var job store.Job
	var err error
	switch a.Action {
	case "create":
		zone, e := machineZone()
		if e != nil {
			return "", e
		}
		directory := workdir.Of(ctx)
		if directory != "" && !filepath.IsAbs(directory) {
			return "", errors.New("current project must be an absolute trusted path")
		}
		job = store.Job{Zone: zone, Directory: directory, Ask: "thread"}
		if err = a.apply(&job); err == nil {
			job, err = s.store.Create(job, now)
		}
	case "update":
		job, err = s.store.Update(a.ID, a.apply, now)
	case "remove":
		if err := s.store.Remove(a.ID); err != nil {
			return "", err
		}
		s.changed()
		return "Scheduled job removed.", nil
	}
	if err != nil {
		return "", err
	}
	s.changed()
	return output(jobView(job), nil)
}
