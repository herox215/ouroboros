package files_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ouroboros/internal/cli/clitest"
	"ouroboros/internal/config"
)

// Run the documented patterns through JavaScript and the real search tool,
// not just through RE2: JS parsing is where an ordinary escape can be lost.
func TestSearchDocumentedJavaScriptPatterns(t *testing.T) {
	home := user(t)
	if err := os.WriteFile(filepath.Join(home, "sample.go"), []byte("func main() {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s := clitest.Settings(t)
	if err := config.Save(s.Home, clitest.NodeConfig(s)); err != nil {
		t.Fatal(err)
	}
	for _, pattern := range []string{`"func main[(][)]"`, "String.raw`\\bmain\\s*\\(`"} {
		out, err := clitest.Run(t, s, "", "chat", call("run", map[string]any{"script": `return search({path: "~/sample.go", pattern: ` + pattern + `})`}))
		if err != nil || !strings.Contains(out, "sample.go:1: func main() {}") {
			t.Errorf("pattern %s: %s, %v", pattern, out, err)
		}
	}
}
