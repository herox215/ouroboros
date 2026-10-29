// Package procgroup starts a command as the first process of a group of its
// own and kills that group with it, so that nothing the command started
// outlives it.
//
// It sends the signals for the packages that use it. A package that signals
// process IDs is not mutated by the mutation tests: a mutant could let a PID
// of 0 or -1 through, and the signal would go to the group of the tests or to
// every process of the user. Kept here, behind a check of the PID, the
// signals leave the packages that use them open to mutation.
package procgroup
