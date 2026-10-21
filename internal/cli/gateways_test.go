package cli_test

import (
	"testing"

	"ouroboros/internal/assistant"
	"ouroboros/internal/cli"
	"ouroboros/internal/cli/clitest"
	"ouroboros/internal/config"
)

// The standard application exposes neutral output tools, independently of the
// Matrix backend's settings or connection state.
func TestStandardGatewayTools(t *testing.T) {
	s := clitest.Settings(t)
	cfg := clitest.NodeConfig(s)
	cfg.Tools = &config.Tools{H2A: config.Named("gateway_list", "gateway_send_image", "gateway_set_avatar")}
	if err := config.Save(s.Home, cfg); err != nil {
		t.Fatal(err)
	}
	app, _ := clitest.NewApp(t, s, "")
	bot, _ := cli.Service[*assistant.Assistant](app)
	if err := bot.Ready(); err != nil {
		t.Fatal(err)
	}
}
