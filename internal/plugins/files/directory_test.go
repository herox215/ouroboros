package files

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"ouroboros/internal/workdir"
)

func TestFileToolUsesConversationDirectory(t *testing.T) {
	w := newWorld(t)
	w.file("~/project/notes.md", "project\n")
	w.file("~/workspace/notes.md", "workspace\n")
	w.guard.Workspace = w.home + "/workspace"
	args, _ := json.Marshal(map[string]string{"path": "notes.md"})
	for _, tool := range tools(w.guard) {
		if tool.Name != Read {
			continue
		}
		run, err := tool.Configure(json.RawMessage("{}"))
		if err != nil {
			t.Fatal(err)
		}
		for _, directory := range []string{w.home + "/project", ""} {
			got, err := run.Run(workdir.With(context.Background(), directory), args)
			want := "project\n"
			if directory == "" {
				want = "workspace\n"
			}
			if err != nil || got != want {
				t.Fatalf("%q: %q %v", directory, got, err)
			}
		}
		w.protected = append(w.protected, w.home+"/project")
		if _, err := run.Run(workdir.With(context.Background(), w.home+"/project"), args); err == nil || !strings.Contains(err.Error(), "protected") {
			t.Fatalf("project bypassed protection: %v", err)
		}
		return
	}
	t.Fatal("read not registered")
}
