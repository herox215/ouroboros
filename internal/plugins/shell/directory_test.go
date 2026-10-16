package shell

import (
	"context"
	"os"
	"strings"
	"testing"

	"ouroboros/internal/workdir"
)

func TestProjectDirectoryForCommands(t *testing.T) {
	w := newWorld(t)
	w.file("~/project/sub/keep", "x")
	w.guard.Workspace = w.home + "/share"
	ctx := workdir.With(context.Background(), w.home+"/project")
	for input, want := range map[string]string{"": w.home + "/project", "sub": w.home + "/project/sub", "~/share": w.home + "/share"} {
		got, err := prepare(ctx, "bash", w.guard, input)
		if err != nil || got != want {
			t.Fatalf("%q: %q %v, want %q", input, got, err, want)
		}
	}
	if got, err := prepare(context.Background(), "bash", w.guard, ""); err != nil || got != w.guard.Workspace {
		t.Fatalf("workspace: %q %v", got, err)
	}
	w.protected = append(w.protected, w.home+"/project")
	if _, err := prepare(ctx, "bash", w.guard, ""); err == nil || !strings.Contains(err.Error(), "protected") {
		t.Fatalf("protected: %v", err)
	}
	w.protected = nil
	os.RemoveAll(w.home + "/project")
	if _, err := prepare(ctx, "bash", w.guard, ""); err == nil {
		t.Fatal("missing project silently fell back")
	}
}
