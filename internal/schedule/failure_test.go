package schedule

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// A range needs a number on both sides, and a step without a range runs from
// the given start to the end of the field.
func TestCronRangeAndOpenEndedStep(t *testing.T) {
	if _, e := ParseCron("1-x * * * *"); e == nil {
		t.Fatal("accepted a range without an end")
	}
	c, e := ParseCron("5/20 * * * *")
	if e != nil {
		t.Fatal(e)
	}
	got, e := c.Next(epoch, time.UTC)
	if e != nil || !got.Equal(epoch.Add(5*time.Minute)) {
		t.Fatalf("%s %v", got, e)
	}
	got, _ = c.Next(got, time.UTC)
	if !got.Equal(epoch.Add(25 * time.Minute)) {
		t.Fatalf("second run at %s", got)
	}
}

// Without a location, Next uses the machine location.
func TestCronNextDefaultsToLocal(t *testing.T) {
	c, _ := ParseCron("0 9 * * *")
	want, _ := c.Next(epoch, time.Local)
	got, e := c.Next(epoch, nil)
	if e != nil || !got.Equal(want) {
		t.Fatalf("%s %v, want %s", got, e, want)
	}
}

// Open fails when the directory of the store cannot exist, when the lock
// cannot be taken, and when the store is not a readable file.
func TestOpenFailures(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if e := os.WriteFile(file, nil, 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := Open(filepath.Join(file, "schedule.json")); e == nil {
		t.Fatal("opened a store below a file")
	}
	locked := filepath.Join(dir, "locked.json")
	if e := os.Mkdir(locked+".lock", 0700); e != nil {
		t.Fatal(e)
	}
	if _, e := Open(locked); e == nil {
		t.Fatal("opened a store whose lock is a directory")
	}
	folder := filepath.Join(dir, "folder.json")
	if e := os.Mkdir(folder, 0700); e != nil {
		t.Fatal(e)
	}
	if _, e := Open(folder); e == nil {
		t.Fatal("opened a directory as a store")
	}
}

// A next run beyond the year 9999 has no JSON form. The store refuses the job
// instead of writing a file it could not read back.
func TestCreateRefusesUnwritableNextRun(t *testing.T) {
	s := openTest(t)
	now := time.Date(9999, 12, 31, 12, 0, 0, 0, time.UTC)
	if _, e := s.Create(Job{Name: "n", Text: "t", Cron: "0 0 * * *", Zone: "UTC"}, now); e == nil {
		t.Fatal("stored a next run in the year 10000")
	}
	if len(s.List()) != 0 {
		t.Fatal("failed create left a job")
	}
}

// A store whose directory became read-only reports the failed write.
func TestCreateReportsFailedWrite(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root writes anywhere")
	}
	s := openTest(t)
	mustCreate(t, s, once(epoch.Add(time.Hour)))
	dir := filepath.Dir(s.path)
	if e := os.Chmod(dir, 0500); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { os.Chmod(dir, 0700) })
	if _, e := s.Create(once(epoch.Add(time.Hour)), epoch); e == nil {
		t.Fatal("create succeeded in a read-only directory")
	}
}

// Update, Remove and Finish find their job among others, and report a
// missing one.
func TestMissingJobs(t *testing.T) {
	s := openTest(t)
	mustCreate(t, s, once(epoch.Add(time.Hour)))
	second := mustCreate(t, s, once(epoch.Add(time.Hour)))
	got, e := s.Update(second.ID, func(j *Job) error { j.Name = "Second"; return nil }, epoch)
	if e != nil || got.Name != "Second" {
		t.Fatalf("%+v %v", got, e)
	}
	if e = s.Finish(second.ID, Run{Status: "completed"}); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Update("absent", func(*Job) error { return nil }, epoch); !errors.Is(e, ErrNotFound) {
		t.Fatalf("update: %v", e)
	}
	if e = s.Remove("absent"); !errors.Is(e, ErrNotFound) {
		t.Fatalf("remove: %v", e)
	}
	if e = s.Finish("absent", Run{Status: "completed"}); !errors.Is(e, ErrNotFound) {
		t.Fatalf("finish: %v", e)
	}
	if e = s.Finish(second.ID, Run{}); e == nil {
		t.Fatal("finished without a status")
	}
}

// Each invalid field is refused with its own reason.
func TestValidationReasons(t *testing.T) {
	s := openTest(t)
	for _, j := range []Job{
		{Name: " ", Text: "t", At: epoch.Add(time.Hour).Format(time.RFC3339), Zone: "UTC"},
		{Name: "n", Text: "t", At: epoch.Add(time.Hour).Format(time.RFC3339), Zone: "UTC", Ask: "sometimes"},
		{Name: "n", Text: "t", At: "tomorrow", Zone: "UTC"},
		{Name: "n", Text: "t", Cron: "x * * * *", Zone: "UTC"},
		{Name: "n", Text: "t", Cron: "10,30,50 * * * *", Zone: "Australia/Lord_Howe"},
	} {
		if _, e := s.Create(j, epoch); e == nil {
			t.Errorf("accepted %+v", j)
		}
	}
}

// A job without a zone needs the machine zone. Without one it is refused.
func TestValidationNeedsMachineZone(t *testing.T) {
	noMachineZone(t)
	s := openTest(t)
	j := once(epoch.Add(time.Hour))
	j.Zone = ""
	if _, e := s.Create(j, epoch); e == nil {
		t.Fatal("created a job without any zone")
	}
}

// A store edited by hand can hold a schedule that no longer works. Claim then
// fails instead of advancing the job.
func TestClaimRefusesBrokenSchedules(t *testing.T) {
	for _, j := range []struct{ Cron, Zone string }{
		{"x", "UTC"}, {"0 * * * *", "Not/A_Zone"}, {"0 0 31 2 *", "UTC"},
	} {
		p := filepath.Join(t.TempDir(), "schedule.json")
		if e := os.WriteFile(p, []byte(`[{"id":"a","name":"n","text":"t","cron":"`+j.Cron+`","zone":"`+j.Zone+`","ask":"thread","created":"2026-01-01T00:00:00Z","next":"2026-01-01T00:00:00Z"}]`), 0600); e != nil {
			t.Fatal(e)
		}
		s, e := Open(p)
		if e != nil {
			t.Fatal(e)
		}
		if due, e := s.Claim(epoch.Add(time.Hour)); e == nil {
			t.Errorf("claimed %+v: %v", j, due)
		}
	}
}

// noMachineZone points MachineZone at files that name no usable zone.
func noMachineZone(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("TZ", "")
	oldLocal, oldZone := localtimeFile, timezoneFile
	t.Cleanup(func() { localtimeFile, timezoneFile = oldLocal, oldZone })
	localtimeFile = filepath.Join(dir, "missing")
	timezoneFile = filepath.Join(dir, "missing")
}

// MachineZone tries TZ, the link of /etc/localtime and /etc/timezone. It
// skips empty and absolute names and names it cannot load.
func TestMachineZoneCandidates(t *testing.T) {
	noMachineZone(t)
	if _, e := MachineZone(); e == nil {
		t.Fatal("found a zone where none is named")
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "zoneinfo", "Not", "Real")
	if e := os.MkdirAll(filepath.Dir(target), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(target, nil, 0600); e != nil {
		t.Fatal(e)
	}
	localtimeFile = filepath.Join(dir, "localtime")
	if e := os.Symlink(target, localtimeFile); e != nil {
		t.Fatal(e)
	}
	timezoneFile = filepath.Join(dir, "timezone")
	if e := os.WriteFile(timezoneFile, []byte("Asia/Tokyo\n"), 0600); e != nil {
		t.Fatal(e)
	}
	t.Setenv("TZ", ":/usr/share/zoneinfo/UTC")
	if got, e := MachineZone(); e != nil || got != "Asia/Tokyo" {
		t.Fatalf("%q %v", got, e)
	}
}
