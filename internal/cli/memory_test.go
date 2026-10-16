package cli_test

import (
	"slices"
	"testing"

	"ouroboros/internal/assistant"
	"ouroboros/internal/cli"
	"ouroboros/internal/cli/clitest"
	"ouroboros/internal/config"
)

// The standard application exposes every memory operation without a custom
// plugin list, and the optional plugin can still be disabled.
func TestStandardMemoryTools(t *testing.T) {
	s := clitest.Settings(t)
	cfg := clitest.NodeConfig(s)
	cfg.Tools = &config.Tools{H2A: config.Named("memory_add", "memory_update", "memory_forget", "memory_search", "memory_read")}
	if err := config.Save(s.Home, cfg); err != nil {
		t.Fatal(err)
	}
	app, _ := clitest.NewApp(t, s, "")
	bot, _ := cli.Service[*assistant.Assistant](app)
	if err := bot.Ready(); err != nil {
		t.Fatal(err)
	}
	cfg.Plugins = &config.Plugins{Off: []string{"memory"}}
	if err := config.Save(s.Home, cfg); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range cli.Plugins(s, cli.Terminal{}) {
		names = append(names, p.Name)
	}
	if slices.Contains(names, "memory") {
		t.Fatal("disabled memory plugin is still active")
	}
}
