package browser_test

import (
	"strings"
	"testing"

	"ouroboros/internal/cli/clitest"
	"ouroboros/internal/config"
)

func TestMain(m *testing.M) { clitest.Main(m) }

// The agent of the owner has the tool browser.
func TestOwnerHasTheBrowser(t *testing.T) {
	s := clitest.Settings(t)
	if err := config.Save(s.Home, clitest.NodeConfig(s)); err != nil {
		t.Fatal(err)
	}
	out, err := clitest.Run(t, s, "", "chat", `!browser {"action": "close"}`)
	if err != nil || !strings.Contains(out, "there is no tab to close") {
		t.Fatalf("browser: %q, %v", out, err)
	}
}
