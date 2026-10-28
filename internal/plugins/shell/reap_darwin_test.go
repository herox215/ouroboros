package shell

import (
	"errors"
	"os"
	"slices"
	"testing"

	"golang.org/x/sys/unix"
)

// The table of the processes holds this one, with its parent and group, and
// is empty where the kernel does not give it.
func TestProcsOf(t *testing.T) {
	table := procs()
	i := slices.IndexFunc(table, func(p proc) bool { return p.pid == os.Getpid() })
	if i < 0 {
		t.Fatal("this process is not in the table")
	}
	if p := table[i]; p.ppid != os.Getppid() || p.pgid != unix.Getpgrp() || p.start <= 0 || p.zombie {
		t.Errorf("this process: %+v", p)
	}
	failing := func(string, ...int) ([]unix.KinfoProc, error) { return nil, errors.New("no table") }
	if got := procsOf(failing); got != nil {
		t.Errorf("without a table: %+v", got)
	}
}
