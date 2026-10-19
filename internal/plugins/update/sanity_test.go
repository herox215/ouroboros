package update_test

import (
	"context"
	"encoding/json"
	"ouroboros/internal/cli"
	"ouroboros/internal/cli/clitest"
	"ouroboros/internal/update"
	"ouroboros/plugin"
	"strings"
	"testing"
)

func TestCheckSanityTool(t *testing.T) {
	var ctx *plugin.Context
	app, _ := clitest.NewApp(t, clitest.Settings(t), "", plugin.New("sanity-reader", func(c *plugin.Context, _ struct{}) error { ctx = c; return nil }))
	updater, _ := cli.Service[*update.Updater](app)
	updater.Source = ""
	for _, tool := range plugin.All[plugin.Tool](ctx) {
		if tool.Name != "check_sanity" {
			continue
		}
		if !tool.Trusted || !strings.Contains(tool.Schema, "timeout_minutes") {
			t.Fatalf("tool: %+v", tool)
		}
		if got := plugin.ToolLine(ctx, plugin.Step{Tool: tool.Name, Args: json.RawMessage(`{}`)}); !strings.Contains(got, "checking tests and coverage") {
			t.Fatal(got)
		}
		if _, err := tool.Run(context.Background(), json.RawMessage(`{"gui":"yes"}`)); err == nil {
			t.Fatal("accepted invalid arguments")
		}
		if _, err := tool.Run(context.Background(), json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "source checkout") {
			t.Fatalf("unknown source: %v", err)
		}
		// Without Go nothing is checked.
		empty := t.TempDir()
		t.Setenv("OURO_GO_DIR", empty)
		t.Setenv("PATH", empty)
		if _, err := update.GoTool(); err == nil {
			t.Skip("Go is installed for all users, where the test cannot hide it")
		}
		if _, err := tool.Run(context.Background(), json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "rebuild needs Go") {
			t.Fatalf("without Go: %v", err)
		}
		return
	}
	t.Fatal("check_sanity was not registered")
}
