package tui

import "testing"

// A list walked with the arrow keys goes around at its ends; an entry out of
// reach of a list that has since grown shorter counts as its last, and one
// before the first as the first.
func TestCycle(t *testing.T) {
	for _, tt := range []struct{ at, by, n, want int }{
		{0, 1, 3, 1},
		{2, 1, 3, 0},
		{0, -1, 3, 2},
		{0, -4, 3, 2},
		{1, 7, 3, 2},
		{3, 0, 3, 2},
		{7, 0, 3, 2},
		{7, -1, 3, 1},
		{-2, 1, 3, 1},
		{3, 1, 1, 0},
		{0, -1, 0, 0},
	} {
		if got := cycle(tt.at, tt.by, tt.n); got != tt.want {
			t.Errorf("cycle(%d, %d, %d) = %d, want %d", tt.at, tt.by, tt.n, got, tt.want)
		}
	}
}
