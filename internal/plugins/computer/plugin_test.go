package computer_test

import (
	"strings"
	"testing"

	"ouroboros/internal/cli/clitest"
	"ouroboros/internal/config"
)

func TestMain(m *testing.M) { clitest.Main(m) }

// The agent of the owner has the tool computer without app settings.
// An invalid action checks registration without needing a desktop driver.
func TestOwnerHasTheComputer(t *testing.T) {
	s := clitest.Settings(t)
	if err := config.Save(s.Home, clitest.NodeConfig(s)); err != nil {
		t.Fatal(err)
	}
	out, err := clitest.Run(t, s, "", "chat", `!computer {"action": "jump"}`)
	if err != nil || !strings.Contains(out, `there is no action "jump"`) {
		t.Fatalf("computer: %q, %v", out, err)
	}
}
