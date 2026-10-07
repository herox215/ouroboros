package plugin_test

import (
	"testing"

	"ouroboros/plugin"
)

// The error of a front end that is to be started anew names what starts.
func TestRestartingSaysFromWhat(t *testing.T) {
	if got := (&plugin.Restarting{Exe: "/bin/ouro"}).Error(); got != "ouro is to be started anew from /bin/ouro" {
		t.Fatalf("got %q", got)
	}
}
