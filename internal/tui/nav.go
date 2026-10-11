package tui

// A list that is walked with the arrow keys does not stop at its ends: the
// step up from the first entry is the last, the step down from the last is
// the first. See cycle.

// cycle is the index that is by that many steps from at within n, taken
// around the list. Without an entry to select it is 0, and so is the entry
// out of reach of a list that has since grown shorter.
func cycle(at, by, n int) int {
	if n <= 1 {
		return 0
	}
	if at < 0 {
		at = 0
	}
	if at >= n {
		at = n - 1
	}
	return (at + by%n + n) % n
}
