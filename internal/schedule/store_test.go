package schedule

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, e := Open(filepath.Join(t.TempDir(), "schedule.json"))
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func once(at time.Time) Job {
	return Job{Name: "Reminder", Text: "Call the landlord", At: at.Format(time.RFC3339), Zone: "UTC"}
}
func mustCreate(t *testing.T, s *Store, j Job) Job {
	t.Helper()
	j, e := s.Create(j, epoch)
	if e != nil {
		t.Fatal(e)
	}
	return j
}

func TestStoreLifecycle(t *testing.T) {
	s := openTest(t)
	j := mustCreate(t, s, once(epoch.Add(time.Hour)))
	if j.ID == "" || j.Ask != "thread" || j.Created != epoch || j.Next != epoch.Add(time.Hour) {
		t.Fatalf("bad defaults: %+v", j)
	}
	list := s.List()
	list[0].Text = "changed"
	got, _ := s.Get(j.ID)
	if got.Text != j.Text {
		t.Fatal("List aliases store")
	}
	if due, e := s.Claim(j.Next.Add(-time.Second)); e != nil || len(due) != 0 {
		t.Fatalf("early claim: %v %v", due, e)
	}
	due, e := s.Claim(j.Next)
	if e != nil || len(due) != 1 || !due[0].Next.Equal(j.Next) {
		t.Fatalf("claim: %v %v", due, e)
	}
	got, _ = s.Get(j.ID)
	if !got.Next.IsZero() || got.Runs[0].Status != "running" {
		t.Fatalf("claim not persisted: %+v", got)
	}
	due, e = s.Claim(j.Next)
	if e != nil || len(due) != 0 {
		t.Fatal("claimed twice")
	}
	if e = s.Finish(j.ID, Run{Status: "completed", Session: "session-1", Result: "done\nsecond line"}); e != nil {
		t.Fatal(e)
	}
	reopened, e := Open(s.path)
	if e != nil {
		t.Fatal(e)
	}
	got, _ = reopened.Get(j.ID)
	if len(got.Runs) != 1 || got.Runs[0].Result != "done" || got.Runs[0].Session != "session-1" {
		t.Fatalf("finish: %+v", got)
	}
	info, e := os.Stat(s.path)
	if e != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("mode: %v %v", info, e)
	}
	if e = s.Remove(j.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Get(j.ID); !errors.Is(e, ErrNotFound) {
		t.Fatal(e)
	}
}

func TestValidationAndLimit(t *testing.T) {
	s := openTest(t)
	for _, j := range []Job{once(epoch), once(epoch.Add(-time.Hour)), {Name: "n", Text: "t", Cron: "* * * * *", Zone: "UTC"}, {Name: "n", Text: "t", Cron: "0 0 31 2 *", Zone: "UTC"}} {
		if _, e := s.Create(j, epoch); e == nil {
			t.Fatalf("accepted %+v", j)
		}
	}
	j := once(epoch.Add(time.Hour))
	j.Text = strings.Repeat("x", MaxText+1)
	if _, e := s.Create(j, epoch); e == nil {
		t.Fatal("accepted long text")
	}
	j = once(epoch.Add(time.Hour))
	j.Zone = "Not/A_Zone"
	if _, e := s.Create(j, epoch); e == nil {
		t.Fatal("accepted zone")
	}
	j = once(epoch.Add(time.Hour))
	j.Cron = "0 * * * *"
	if _, e := s.Create(j, epoch); e == nil {
		t.Fatal("accepted both times")
	}
	for i := 0; i < MaxJobs; i++ {
		mustCreate(t, s, once(epoch.Add(time.Hour)))
	}
	if _, e := s.Create(once(epoch.Add(time.Hour)), epoch); !errors.Is(e, ErrFull) {
		t.Fatal(e)
	}
}

func TestUpdateRollbackAndHistoryIsolation(t *testing.T) {
	s := openTest(t)
	j := mustCreate(t, s, once(epoch.Add(time.Hour)))
	s.Finish(j.ID, Run{Status: "completed", Result: "original"})
	_, e := s.Update(j.ID, func(j *Job) error { j.Runs[0].Result = "mutated"; j.Text = "changed"; return errors.New("stop") }, epoch)
	if e == nil {
		t.Fatal("callback ignored")
	}
	got, _ := s.Get(j.ID)
	if got.Text != j.Text || got.Runs[0].Result != "original" {
		t.Fatal("failed update mutated store")
	}
	_, e = s.Update(j.ID, func(j *Job) error { j.At = epoch.Format(time.RFC3339); return nil }, epoch)
	if e == nil {
		t.Fatal("past update accepted")
	}
	got, e = s.Update(j.ID, func(j *Job) error { j.Text = "new"; j.Paused = true; j.ID = "bad"; j.Next = epoch; return nil }, epoch.Add(2*time.Hour))
	if e != nil || got.ID != j.ID || got.Next != j.Next {
		t.Fatalf("update: %+v %v", got, e)
	}
	due, e := s.Claim(epoch.Add(3 * time.Hour))
	if e != nil || len(due) != 0 {
		t.Fatal("paused job claimed")
	}
}

func TestLateMissedAndRecovery(t *testing.T) {
	for _, delay := range []time.Duration{2 * time.Hour, 24 * time.Hour, 24*time.Hour + time.Second} {
		t.Run(delay.String(), func(t *testing.T) {
			s := openTest(t)
			j := mustCreate(t, s, Job{Name: "n", Text: "t", Cron: "0 * * * *", Zone: "UTC"})
			now := j.Next.Add(delay)
			due, e := s.Claim(now)
			if e != nil || len(due) != 1 || due[0].Next != j.Next {
				t.Fatalf("late: %v %v", due, e)
			}
			got, _ := s.Get(j.ID)
			if !got.Next.After(now) {
				t.Fatal("next did not advance")
			}
			status := "running"
			if delay > 24*time.Hour {
				status = "missed"
			}
			if got.Runs[0].Status != status {
				t.Fatal(got.Runs)
			}
			re, e := Open(s.path)
			if e != nil {
				t.Fatal(e)
			}
			got, _ = re.Get(j.ID)
			if status == "running" {
				status = "interrupted"
			}
			if got.Runs[0].Status != status {
				t.Fatal(got.Runs)
			}
			due, e = re.Claim(now)
			if e != nil || len(due) != 0 {
				t.Fatal("recovery repeated claim")
			}
		})
	}
}

func TestOverlapHistoryAndConcurrentClaims(t *testing.T) {
	s := openTest(t)
	j := mustCreate(t, s, Job{Name: "n", Text: "t", Cron: "0 * * * *", Zone: "UTC"})
	var wg sync.WaitGroup
	var mu sync.Mutex
	count := 0
	for i := 0; i < 12; i++ {
		wg.Go(func() {
			jobs, e := s.Claim(j.Next)
			if e != nil {
				t.Error(e)
			}
			mu.Lock()
			count += len(jobs)
			mu.Unlock()
		})
	}
	wg.Wait()
	if count != 1 {
		t.Fatalf("%d claims", count)
	}
	if _, e := s.Claim(j.Next.Add(time.Hour)); e != nil {
		t.Fatal(e)
	}
	if e := s.Finish(j.ID, Run{Status: "skipped"}); e != nil {
		t.Fatal(e)
	}
	got, _ := s.Get(j.ID)
	if len(got.Runs) != 2 || got.Runs[0].Status != "running" || got.Runs[1].Status != "skipped" {
		t.Fatal(got.Runs)
	}
	if e := s.Finish(j.ID, Run{Status: "completed"}); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 15; i++ {
		if e := s.Finish(j.ID, Run{Status: "completed"}); e != nil {
			t.Fatal(e)
		}
	}
	got, _ = s.Get(j.ID)
	if len(got.Runs) != 10 {
		t.Fatal(len(got.Runs))
	}
	got.Runs[0].Status = "mutated"
	again, _ := s.Get(j.ID)
	if again.Runs[0].Status == "mutated" {
		t.Fatal("history aliases")
	}
}

func TestCorruptStore(t *testing.T) {
	p := filepath.Join(t.TempDir(), "schedule.json")
	if e := os.WriteFile(p, []byte("{broken"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := Open(p); e == nil {
		t.Fatal("accepted corruption")
	}
	b, _ := os.ReadFile(p)
	if string(b) != "{broken" {
		t.Fatal("overwrote corruption")
	}
}

func TestIndependentHandlesClaimAtomically(t *testing.T) {
	s := openTest(t)
	if _, err := os.Stat(s.path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty startup created a schedule: %v", err)
	}
	j := mustCreate(t, s, once(epoch.Add(time.Hour)))
	other, err := Open(s.path)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	count := 0
	for _, handle := range []*Store{s, other} {
		wg.Go(func() {
			jobs, err := handle.Claim(j.Next)
			if err != nil {
				t.Error(err)
			}
			mu.Lock()
			count += len(jobs)
			mu.Unlock()
		})
	}
	wg.Wait()
	if count != 1 {
		t.Fatalf("independent handles claimed %d times", count)
	}
}

func TestDefaultZoneIsStableName(t *testing.T) {
	t.Setenv("TZ", "Europe/Berlin")
	s := openTest(t)
	j := once(epoch.Add(time.Hour))
	j.Zone = ""
	j = mustCreate(t, s, j)
	if j.Zone != "Europe/Berlin" {
		t.Fatalf("zone=%q", j.Zone)
	}
}
