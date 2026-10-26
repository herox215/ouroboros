package cli_test

import (
	"slices"
	"testing"

	"ouroboros/internal/assistant"
	"ouroboros/internal/cli"
	"ouroboros/internal/cli/clitest"
	"ouroboros/internal/config"
)

// Ability tools must participate in normal configuration selection even though
// they are contributed through a catalog rather than as standalone tools.
func TestStandardAbilityTools(t *testing.T) {
	s := clitest.Settings(t)
	cfg := clitest.NodeConfig(s)
	cfg.Tools = &config.Tools{H2A: config.Named("ability_load", "ability_inspect", "whatsapp_status")}
	if err := config.Save(s.Home, cfg); err != nil {
		t.Fatal(err)
	}
	app, _ := clitest.NewApp(t, s, "")
	bot, _ := cli.Service[*assistant.Assistant](app)
	if err := bot.Ready(); err != nil {
		t.Fatal(err)
	}
	cfg.Plugins = &config.Plugins{Off: []string{"abilities", "whatsapp"}}
	if err := config.Save(s.Home, cfg); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range cli.Plugins(s, cli.Terminal{}) {
		names = append(names, p.Name)
	}
	for _, name := range []string{"abilities", "whatsapp"} {
		if slices.Contains(names, name) {
			t.Fatalf("disabled %s plugin is still active", name)
		}
	}
}
