package gui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"ouroboros/internal/config"
	"ouroboros/internal/home"
	"ouroboros/internal/schedule"
	"ouroboros/internal/web"
)

// The window lists the scheduled jobs that are still to run, each as the
// agent it runs as, as the file of the profile has them: not what came of
// them, and not a job that ran once and is done.
func TestWindowListsTheScheduledJobs(t *testing.T) {
	dir := home.Dir(t.TempDir())
	at := time.Date(2026, 11, 16, 4, 0, 0, 0, time.UTC)
	jobs := []schedule.Job{
		{
			ID: "a1", Name: "Archive memories", Text: strings.Repeat("tidy ", 100), Cron: "0 4 * * *", Zone: "UTC", Agent: "archivist",
			Next: at.Add(24 * time.Hour), Runs: []schedule.Run{{At: at, Session: "s1", Status: "completed", Result: "merged two"}},
		},
		{ID: "b2", Name: "Morning", Text: "the calendar", Cron: "30 7 * * *", Zone: "UTC", Paused: true},
		{ID: "c3", Name: "Once", Text: "remind me", At: "2026-11-15T09:00", Zone: "UTC", Runs: []schedule.Run{{At: at, Status: "completed"}}},
		{ID: "d4", Name: "Later", Text: "call", At: "2026-11-20T09:00", Zone: "UTC", Next: at.Add(100 * time.Hour)},
	}
	b, err := json.Marshal(jobs)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir.Schedule(), b, 0o600); err != nil {
		t.Fatal(err)
	}

	p := fakeProfile("pythia")
	p.dir = dir
	w := newWindow(p, "pythia")
	t.Cleanup(w.Close)
	list, err := w.AgentJobs()
	if err != nil || len(list) != 3 {
		t.Fatalf("the jobs: %+v, %v", list, err)
	}
	archive, morning, later := list[0], list[1], list[2]
	if archive.Agent != "archivist" || archive.Cron != "0 4 * * *" || archive.Next == nil || !archive.Next.Equal(at.Add(24*time.Hour)) {
		t.Fatalf("the job of the archivist: %+v", archive)
	}
	if len(archive.Text) > jobText+len(" …") || !strings.HasSuffix(archive.Text, " …") {
		t.Fatalf("the text of a job is not cut: %d bytes", len(archive.Text))
	}
	if out, err := json.Marshal(archive); err != nil || strings.Contains(string(out), "merged two") || strings.Contains(string(out), "s1") {
		t.Fatalf("a job says what came of it: %s, %v", out, err)
	}
	// A job that names no agent runs as the agent of the thread.
	if morning.Agent != "thread" || !morning.Paused || morning.Next != nil {
		t.Fatalf("a job of the owner: %+v", morning)
	}
	if later.ID != "d4" || later.At != "2026-11-20T09:00" || later.Next == nil {
		t.Fatalf("a job that runs once later: %+v", later)
	}
}

// A profile without jobs lists none, and a file of jobs that cannot be read
// says why.
func TestWindowJobsOfAnEmptyOrBrokenProfile(t *testing.T) {
	p := fakeProfile("pythia")
	p.dir = home.Dir(t.TempDir())
	w := newWindow(p, "pythia")
	t.Cleanup(w.Close)
	list, err := w.AgentJobs()
	if err != nil || list == nil || len(list) != 0 {
		t.Fatalf("the jobs of an empty profile: %+v, %v", list, err)
	}
	if err := os.WriteFile(p.dir.Schedule(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := w.AgentJobs(); err == nil {
		t.Fatal("a broken file of jobs was read")
	}
	// A window without a profile directory knows no jobs.
	bare := newWindow(fakeProfile("pythia"), "pythia")
	t.Cleanup(bare.Close)
	if list, err := bare.AgentJobs(); err != nil || list == nil || len(list) != 0 {
		t.Fatalf("the jobs without a profile: %+v, %v", list, err)
	}
}

// The page lists the jobs behind the login.
func TestPageListsTheJobs(t *testing.T) {
	dir := home.Dir(t.TempDir())
	hash, err := web.Hash("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Save(string(dir), config.Config{Model: "m", Web: config.Web{Enabled: true, User: "owner", Password: hash}}); err != nil {
		t.Fatal(err)
	}
	p := fakeProfile("pythia")
	p.dir = dir
	front := newWebFront(dir, p)
	server := httptest.NewServer(front.handler)
	t.Cleanup(server.Close)
	client := stillClient()

	res := do(t, client, http.MethodGet, server.URL+"/api/jobs", nil, nil)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("the jobs without a login: %d", res.StatusCode)
	}
	res.Body.Close()
	login := signIn(t, client, server.URL, "owner", "correct horse battery")
	cookie := sessionOf(t, login)
	login.Body.Close()
	b, err := json.Marshal([]schedule.Job{{ID: "a1", Name: "Archive memories", Cron: "0 4 * * *", Zone: "UTC", Agent: "archivist", Next: time.Now().Add(time.Hour)}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir.Schedule(), b, 0o600); err != nil {
		t.Fatal(err)
	}
	res = do(t, client, http.MethodGet, server.URL+"/api/jobs", cookie, nil)
	var list []AgentJob
	if err := json.Unmarshal([]byte(body(t, res)), &list); err != nil || res.StatusCode != http.StatusOK || len(list) != 1 || list[0].Agent != "archivist" {
		t.Fatalf("the jobs of the page: %d, %+v, %v", res.StatusCode, list, err)
	}
	if err := os.WriteFile(dir.Schedule(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	res = do(t, client, http.MethodGet, server.URL+"/api/jobs", cookie, nil)
	if got := body(t, res); res.StatusCode != http.StatusConflict {
		t.Fatalf("a broken file of jobs: %d, %q", res.StatusCode, got)
	}
}
