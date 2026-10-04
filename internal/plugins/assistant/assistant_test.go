package assistant_test

import (
	"strings"
	"testing"

	"ouroboros/internal/cli/clitest"
	"ouroboros/internal/config"
)

// The owner can take tools away from the own agent, too.
func TestOwnerToolsCanBeRestricted(t *testing.T) {
	s := clitest.Settings(t)
	cfg := clitest.NodeConfig(s)
	cfg.Tools = &config.Tools{H2A: config.Named()}
	config.Save(s.Home, cfg)
	out, err := clitest.Run(t, s, "", "chat", "@beta hello")
	if err != nil || out != "[gpt-4o] turns=1 last=@beta hello\n" {
		t.Fatalf("without tools the message is just a message: %q, %v", out, err)
	}

	cfg.Tools = &config.Tools{H2A: config.Named("ask_peers")}
	config.Save(s.Home, cfg)
	if _, err := clitest.Run(t, s, "", "chat", "hi"); err == nil || !strings.Contains(err.Error(), "tools.h2a") {
		t.Fatalf("a typo should be reported: %v", err)
	}
	cfg.Tools = &config.Tools{A2A: config.Named("ask_peers")}
	config.Save(s.Home, cfg)
	if _, err := clitest.Run(t, s, "", "serve"); err == nil || !strings.Contains(err.Error(), "tools.a2a") {
		t.Fatalf("a node with a typo should not start: %v", err)
	}
}
