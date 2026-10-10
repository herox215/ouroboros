package update

import "testing"

// SystemGo makes Rebuild look for the Go of all users in dir until the test
// ends.
func SystemGo(t *testing.T, dir string) {
	old := systemGo
	systemGo = dir
	t.Cleanup(func() { systemGo = old })
}
