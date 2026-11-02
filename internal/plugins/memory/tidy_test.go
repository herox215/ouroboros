package memory

import (
	"context"
	"strconv"
	"strings"
	"testing"

	store "ouroboros/internal/memory"
	"ouroboros/internal/workdir"
)

// The tidy tools change a memory through memory.Apply: every change needs its
// reason, a change over a text that changed since is refused when the tool was
// given the version it read, and a scope move goes to the project of the
// conversation, not to a path the tool names.
func TestTidyTools(t *testing.T) {
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := workdir.With(context.Background(), "/project/a")
	e, err := s.Create(ctx, "", "drink", "oolong", "owner")
	if err != nil {
		t.Fatal(err)
	}
	if out := call(t, s, ctx, "owner", Rewrite, `{"id":"`+e.ID+`","content":"tea","reason":""}`); !strings.HasPrefix(out, "error:") {
		t.Fatalf("a change without a reason went through: %q", out)
	}
	if _, err := s.Update(ctx, "", e.ID, e.Version, "drink", "oolong tea", "owner"); err != nil {
		t.Fatal(err)
	}
	out := call(t, s, ctx, "owner", Rewrite, `{"id":"`+e.ID+`","content":"tea","version":`+strconv.FormatInt(e.Version, 10)+`,"reason":"shorter"}`)
	if !strings.HasPrefix(out, "error:") {
		t.Fatalf("a rewrite over a newer text went through: %q", out)
	}
	got, err := s.Read(ctx, "", e.ID)
	if err != nil || got.Content != "oolong tea" {
		t.Fatalf("the newer text is gone: %+v, %v", got, err)
	}
	if out := call(t, s, ctx, "owner", Rewrite, `{"id":"`+e.ID+`","content":"tea","version":`+strconv.FormatInt(got.Version, 10)+`,"reason":"shorter"}`); strings.HasPrefix(out, "error:") {
		t.Fatalf("a rewrite over the version read failed: %q", out)
	}
	if out := call(t, s, ctx, "owner", Scope, `{"id":"`+e.ID+`","scope":"project","reason":"belongs to the project"}`); strings.HasPrefix(out, "error:") {
		t.Fatalf("a move to the project failed: %q", out)
	}
	if got, err := s.Read(ctx, "/project/a", e.ID); err != nil || got.Project != "/project/a" {
		t.Fatalf("not in the project of the conversation: %+v, %v", got, err)
	}
	if out := call(t, s, ctx, "owner", Core, `{"id":"`+e.ID+`","core":true,"reason":"who the owner is"}`); strings.HasPrefix(out, "error:") {
		t.Fatalf("a core change failed: %q", out)
	}
	if got, err := s.Read(ctx, "", e.ID); err != nil || !got.Core || got.Project != "" {
		t.Fatalf("core is not global: %+v, %v", got, err)
	}
}
