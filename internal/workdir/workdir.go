// Package workdir carries the owner's project directory with an answer.
// It never changes the process directory or the profile's private workspace.
package workdir

import "context"

type key struct{}

// With sets the project directory for this answer and the tasks it starts.
// An empty directory keeps the profile workspace as the default.
func With(ctx context.Context, directory string) context.Context {
	return context.WithValue(ctx, key{}, directory)
}

// Of returns the project directory, or empty for the profile workspace.
func Of(ctx context.Context) string {
	directory, _ := ctx.Value(key{}).(string)
	return directory
}
