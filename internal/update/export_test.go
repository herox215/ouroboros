package update

import "testing"

// WithoutSandbox makes Rebuild find no sandbox, for why, until the test ends.
func WithoutSandbox(t *testing.T, why error) {
	old := sandboxAvailable
	sandboxAvailable = func() error { return why }
	t.Cleanup(func() { sandboxAvailable = old })
}

// SystemGo makes Rebuild look for the Go of all users in dir until the test
// ends.
func SystemGo(t *testing.T, dir string) {
	old := systemGo
	systemGo = dir
	t.Cleanup(func() { systemGo = old })
}
