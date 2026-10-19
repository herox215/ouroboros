package workdir

import (
	"context"
	"testing"
)

// A context carries the project directory it was given, and none without
// one: then the profile workspace applies.
func TestWithAndOf(t *testing.T) {
	ctx := context.Background()
	if got := Of(ctx); got != "" {
		t.Fatalf("without a directory: %q", got)
	}
	project := With(ctx, "/home/owner/project")
	if got := Of(project); got != "/home/owner/project" {
		t.Fatalf("the directory %q", got)
	}
	if got := Of(ctx); got != "" {
		t.Fatalf("the parent changed: %q", got)
	}
	// An answer within an answer goes on in the directory set last; an empty
	// one goes back to the workspace.
	if got := Of(With(project, "/srv/other")); got != "/srv/other" {
		t.Fatalf("the inner directory %q", got)
	}
	if got := Of(With(project, "")); got != "" {
		t.Fatalf("an empty directory %q", got)
	}
	// A value of another type under another key is no directory.
	type other struct{}
	if got := Of(context.WithValue(ctx, other{}, "/elsewhere")); got != "" {
		t.Fatalf("another key %q", got)
	}
}
