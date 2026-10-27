package agent

import (
	"context"
	"slices"

	"ouroboros/internal/chat"
)

type inputImagesKey struct{}

// InputImages returns the pictures from the most recent user message with
// pictures in the current conversation, looking past text-only turns. Only
// pictures still retained by lastImages are available; tool output is excluded.
// The slice and image bytes belong to the caller. Outside a tool's conversation
// context it returns nil.
func InputImages(ctx context.Context) []chat.Image {
	images, _ := ctx.Value(inputImagesKey{}).([]chat.Image)
	return copyImages(images)
}

// withInputImages takes a bounded snapshot before tools run. Even an empty
// snapshot shadows a parent conversation's pictures in a nested agent run.
func withInputImages(ctx context.Context, history []chat.Message) context.Context {
	var images []chat.Image
	history = lastImages(history)
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == "user" && len(history[i].Images) > 0 {
			images = copyImages(history[i].Images)
			break
		}
	}
	return context.WithValue(ctx, inputImagesKey{}, images)
}

func copyImages(images []chat.Image) []chat.Image {
	out := slices.Clone(images)
	for i := range out {
		out[i].Data = slices.Clone(out[i].Data)
	}
	return out
}
