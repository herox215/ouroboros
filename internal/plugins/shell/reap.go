package shell

import (
	"maps"
	"os"
	"slices"
	"sync"
	"syscall"
	"time"
)

// What a service starts is followed in the table of the processes, so that
// it is ended with the service also when it left the group of the service,
// with setsid, and its parent ended since: every process that was ever
// seen in the group of the service, or as a child of one of its processes,
// is one of its members until it ends. A process is known by its number and
// when it started, as a number is used again.
//
// On Linux every process of a service also has its tag in its environment,
// by which it is found even if it was never seen; macOS shows the
// environment of no other process.

// serviceEnv names the variable every process of a service has in its
// environment, set to the tag of the service.
const serviceEnv = "OURO_SERVICE"

// trackEvery is how often the table of the processes is read while a
// service runs.
const trackEvery = 100 * time.Millisecond

// proc is a process as the table of the processes shows it.
type proc struct {
	pid, ppid, pgid int
	start           int64 // when it started, in a unit of the system
	zombie          bool  // it ended, and waits for its parent to collect it
}

// member is a process of a service.
type member struct {
	PID   int   `json:"pid"`
	Start int64 `json:"start"`
}

// family is what belongs to a service: its group and the processes seen in
// it or started by them.
type family struct {
	tag  string
	pgid int

	mu      sync.Mutex
	members map[member]bool
}

func newFamily(tag string, pgid int, members ...member) *family {
	f := &family{tag: tag, pgid: pgid, members: map[member]bool{}}
	for _, m := range members {
		f.members[m] = true
	}
	return f
}

// grow adds the processes of table that belong to f, and forgets the
// members that ended, and reports whether its members changed.
func (f *family) grow(table []proc) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	byPID := map[int]proc{}
	for _, p := range table {
		byPID[p.pid] = p
	}
	changed := false
	for m := range f.members {
		if p, ok := byPID[m.PID]; !ok || p.start != m.Start || p.zombie {
			delete(f.members, m)
			changed = true
		}
	}
	pids := map[int]bool{}
	for m := range f.members {
		pids[m.PID] = true
	}
	for more := true; more; {
		more = false
		for _, p := range table {
			if pids[p.pid] || p.zombie || p.pid == self || p.pid <= 1 {
				continue
			}
			if p.pgid == f.pgid || pids[p.ppid] {
				f.members[member{p.pid, p.start}] = true
				pids[p.pid] = true
				more, changed = true, true
			}
		}
	}
	return changed
}

// list are the members of f, the oldest first.
func (f *family) list() []member {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.SortedFunc(maps.Keys(f.members), func(a, b member) int { return a.PID - b.PID })
}

// end kills every process of f until none is left, and reports how many
// there were at most. It gives up after a while on one that does not end.
func (f *family) end() int {
	found := 0
	for range 50 {
		if f.pgid > 0 {
			syscall.Kill(-f.pgid, syscall.SIGKILL)
		}
		f.grow(procs())
		pids := tagged(f.tag)
		for _, m := range f.list() {
			if !slices.Contains(pids, m.PID) {
				pids = append(pids, m.PID)
			}
		}
		if len(pids) == 0 {
			break
		}
		found = max(found, len(pids))
		for _, pid := range pids {
			syscall.Kill(pid, syscall.SIGKILL)
		}
		time.Sleep(20 * time.Millisecond)
	}
	return found
}

// self is this process, which is never one of a service.
var self = os.Getpid()
