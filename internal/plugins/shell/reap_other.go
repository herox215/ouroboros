//go:build !darwin && !linux

package shell

// procs are none where the table of the processes cannot be read: a
// service ends with its group alone.
func procs() []proc { return nil }

// tagged finds nothing where the environment of other processes cannot be
// read.
func tagged(string) []int { return nil }
