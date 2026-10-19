package agent

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"ouroboros/internal/openai"
)

func TestInputImagesHistoryAndIsolation(t *testing.T) {
	var got []openai.Image
	var captured context.Context
	read := Tool{Name: "read", Run: func(ctx context.Context, _ json.RawMessage) (string, error) {
		got = InputImages(ctx)
		captured = ctx
		Show(ctx, image(90))
		return "read pictures", nil
	}}
	a := New(&fakeLLM{}, "m", "s", read)
	respond := func(ctx context.Context, id, text string, want ...openai.Image) {
		t.Helper()
		got = nil
		if _, err := a.Respond(ctx, id, text); err != nil {
			t.Fatal(err)
		}
		if !slices.EqualFunc(got, want, sameImage) {
			t.Fatalf("%s: pictures = %v, want %v", id, got, want)
		}
	}
	input := func(images ...openai.Image) context.Context {
		return WithInputMessages(t.Context(), []openai.Message{{Role: "user", Content: "call read", Images: images}})
	}
	respond(input(image(1), image(2)), "one", "ignored", image(1), image(2))
	respond(t.Context(), "two", "call read") // no cross-conversation or tool output leakage
	respond(t.Context(), "one", "call read", image(1), image(2))
	respond(t.Context(), "one", "a text-only turn")
	respond(t.Context(), "one", "call read", image(1), image(2))
	respond(input(image(3)), "one", "ignored", image(3))
	parent := WithInputMessages(context.WithoutCancel(captured), []openai.Message{{Role: "user", Content: "call read"}})
	respond(parent, "nested", "call read") // a parent tool context must be shadowed
	respond(t.Context(), "one", "call read", image(3))
	a.memory.Forget("one")
	respond(t.Context(), "one", "call read")
	if got := InputImages(t.Context()); got != nil {
		t.Fatalf("outside a tool: %v", got)
	}
}

func TestInputImagesCopiesSnapshotAndBytes(t *testing.T) {
	history := []openai.Message{{Role: "user", Images: []openai.Image{image(1), image(2)}}}
	ctx := withInputImages(t.Context(), history)
	history[0].Images[0].Data[0] = 9
	history[0].Images[1] = image(8)
	got := InputImages(ctx)
	if !slices.EqualFunc(got, []openai.Image{image(1), image(2)}, sameImage) {
		t.Fatalf("snapshot aliases history: %v", got)
	}
	got[0].Data[0] = 7
	got[1] = image(6)
	if got := InputImages(ctx); !slices.EqualFunc(got, []openai.Image{image(1), image(2)}, sameImage) {
		t.Fatalf("accessor exposes snapshot: %v", got)
	}
	if history[0].Images[0].Data[0] != 9 || history[0].Images[1].Data[0] != 8 {
		t.Fatal("accessor modified history")
	}
}

func TestInputImagesBoundedAndUserOnly(t *testing.T) {
	images := make([]openai.Image, maxImages+2)
	for i := range images {
		images[i] = image(byte(i))
	}
	for _, tt := range []struct {
		name    string
		history []openai.Message
		want    []openai.Image
	}{
		{"latest user with images", []openai.Message{
			{Role: "user", Images: []openai.Image{image(20)}},
			{Role: "user", Images: []openai.Image{image(21), image(22)}},
			{Role: "user", Content: "text"},
			{Role: "tool", Images: []openai.Image{image(23)}},
			{Role: "assistant", Images: []openai.Image{image(24)}},
		}, []openai.Image{image(21), image(22)}},
		{"oversized input", []openai.Message{{Role: "user", Images: images}}, images[2:]},
		{"tool output consumes budget", []openai.Message{
			{Role: "user", Images: images[:maxImages]},
			{Role: "tool", Images: []openai.Image{image(30), image(31)}},
		}, images[2:maxImages]},
		{"evicted user images", []openai.Message{
			{Role: "user", Images: []openai.Image{image(20)}},
			{Role: "tool", Images: images[:maxImages]},
		}, nil},
		{"tool output only", []openai.Message{{Role: "tool", Images: []openai.Image{image(20)}}}, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := InputImages(withInputImages(t.Context(), tt.history))
			if !slices.EqualFunc(got, tt.want, sameImage) {
				t.Fatalf("pictures = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestInputImagesThroughMultipleDirectCalls(t *testing.T) {
	calls := 0
	inner := Tool{Name: "read", Run: func(ctx context.Context, _ json.RawMessage) (string, error) {
		calls++
		got := InputImages(ctx)
		if !slices.EqualFunc(got, []openai.Image{image(4)}, sameImage) {
			t.Errorf("direct tool pictures = %v", got)
			return "missing pictures", nil
		}
		got[0].Data[0] = 99
		return "read", nil
	}}
	rounds := 0
	llm := llmFunc(func([]openai.Message) openai.Message {
		rounds++
		if rounds > 1 {
			return openai.Message{Content: "done"}
		}
		return openai.Message{ToolCalls: []openai.ToolCall{
			{ID: "one", Type: "function", Function: openai.FunctionCall{Name: "read", Arguments: "{}"}},
			{ID: "two", Type: "function", Function: openai.FunctionCall{Name: "read", Arguments: "{}"}},
		}}
	})
	a := New(llm, "m", "s", inner)
	ctx := WithInputMessages(t.Context(), []openai.Message{{Role: "user", Content: "call read", Images: []openai.Image{image(4)}}})
	if _, err := a.Respond(ctx, "one", "ignored"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("directly invoked tool %d times, want 2", calls)
	}
	if got := a.memory.Conversation("one")[0].Images; !slices.EqualFunc(got, []openai.Image{image(4)}, sameImage) {
		t.Fatalf("tool modified conversation images: %v", got)
	}
}

func TestInputImagesRefreshAfterSteering(t *testing.T) {
	calls := 0
	llm := llmFunc(func(messages []openai.Message) openai.Message {
		if calls == 2 {
			return openai.Message{Content: "done"}
		}
		return openai.Message{ToolCalls: []openai.ToolCall{{ID: string(rune('a' + calls)), Type: "function", Function: openai.FunctionCall{Name: "read", Arguments: "{}"}}}}
	})
	read := Tool{Name: "read", Run: func(ctx context.Context, _ json.RawMessage) (string, error) {
		calls++
		got := InputImages(ctx)
		if !slices.EqualFunc(got, []openai.Image{image(byte(calls))}, sameImage) {
			t.Errorf("round %d pictures = %v", calls, got)
		}
		return "read", nil
	}}
	ctx := WithInputMessages(t.Context(), []openai.Message{{Role: "user", Content: "read", Images: []openai.Image{image(1)}}})
	steered := false
	ctx = WithMessageSteering(ctx, func() []openai.Message {
		if steered {
			return nil
		}
		steered = true
		return []openai.Message{{Role: "user", Content: "new picture", Images: []openai.Image{image(2)}}}
	})
	if _, err := New(llm, "m", "s", read).Respond(ctx, "one", "ignored"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}
