package gui

import (
	"testing"

	"ouroboros/internal/config"
	"ouroboros/internal/home"
	"ouroboros/internal/identity"
	"ouroboros/plugin"
)

// memoryWindow is a window over one profile whose owner keeps its
// configuration and memories in a temporary home.
func memoryWindow(t *testing.T) (*Window, home.Dir) {
	t.Helper()
	dir := home.Dir(t.TempDir())
	if err := dir.SaveConfig(config.Config{Access: "access", Model: "model", Name: "node", PrivateKey: identity.Generate().Private()}); err != nil {
		t.Fatal(err)
	}
	w := newWindow(page{choices: []choice{{"node", "id"}}, profiles: []*Profile{{owner: plugin.OwnerOf(dir, nil)}}})
	t.Cleanup(w.Close)
	return w, dir
}
