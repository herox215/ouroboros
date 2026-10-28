package shell

import (
	"os"

	"golang.org/x/sys/unix"
)

// procs are the processes of this user.
func procs() []proc { return procsOf(unix.SysctlKinfoProcSlice) }

// procsOf are the processes of this user as read reads them: the kernel's
// table of the processes, or what a test makes up.
func procsOf(read func(name string, args ...int) ([]unix.KinfoProc, error)) []proc {
	list, err := read("kern.proc.uid", os.Getuid())
	if err != nil {
		return nil
	}
	const zombie = 5 // SZOMB
	table := make([]proc, 0, len(list))
	for _, k := range list {
		start := k.Proc.P_starttime.Sec*1_000_000 + int64(k.Proc.P_starttime.Usec)
		table = append(table, proc{int(k.Proc.P_pid), int(k.Eproc.Ppid), int(k.Eproc.Pgid), start, k.Proc.P_stat == zombie})
	}
	return table
}

// tagged finds nothing: macOS shows the environment of no other process.
func tagged(string) []int { return nil }
