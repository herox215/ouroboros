package skills_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ouroboros/internal/cli/clitest"
	"ouroboros/internal/config"
)

func TestMain(m *testing.M) { clitest.Main(m) }

// The agent of the owner loads a skill of the node; the profiles share them.
func TestOwnerLoadsASkill(t *testing.T) {
	s := clitest.Settings(t)
	if err := config.Save(s.Home, clitest.NodeConfig(s)); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(s.Home, "skills", "review")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: review\ndescription: Review a change.\n---\nRead the diff first.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]string{"name": "review"})
	out, err := clitest.Run(t, s, "", "chat", "!skill "+string(args))
	if err != nil || !strings.Contains(out, "The skill review:") || !strings.Contains(out, "Read the diff first.") {
		t.Fatalf("skill: %q, %v", out, err)
	}
}
