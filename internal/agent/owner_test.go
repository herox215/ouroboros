package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"ouroboros/internal/openai"
)

// A tool asks the owner through the context: the question goes to whoever
// answers as the tool wrote it, and the answer, or why there is none, comes
// back.
func TestAskOwner(t *testing.T) {
	if _, err := AskOwner(context.Background(), json.RawMessage(`"may I?"`)); !errors.Is(err, ErrNoOwner) {
		t.Fatalf("nobody to ask: %v", err)
	}
	var asked string
	ctx := WithOwner(context.Background(), func(_ context.Context, question json.RawMessage) (string, error) {
		asked = string(question)
		return "yes", nil
	})
	if got, err := AskOwner(ctx, json.RawMessage(`"may I?"`)); err != nil || got != "yes" || asked != `"may I?"` {
		t.Fatalf("got %q, %v; asked %s", got, err, asked)
	}
	if reply, err := AskOwnerReply(ctx, nil); err != nil || (reply.Text != "yes" || reply.Source != (openai.MessageSource{}) || len(reply.Images) != 0) {
		t.Fatalf("an answer through WithOwner has no source: %+v, %v", reply, err)
	}
	gone := errors.New("gone")
	failing := WithOwner(context.Background(), func(context.Context, json.RawMessage) (string, error) { return "", gone })
	if _, err := AskOwner(failing, nil); !errors.Is(err, gone) {
		t.Fatalf("failing owner: %v", err)
	}
	if _, err := AskOwner(WithoutOwner(ctx), nil); !errors.Is(err, ErrNoOwner) {
		t.Fatalf("without owner: %v", err)
	}
	if !CanAskOwner(ctx) || CanAskOwner(context.Background()) || CanAskOwner(WithoutOwner(ctx)) || CanAskOwner(WithOwnerReply(ctx, nil)) {
		t.Fatal("CanAskOwner does not tell where a question reaches someone")
	}
}

// An answer of the owner keeps where it came from apart from what it says.
func TestAskOwnerReply(t *testing.T) {
	from := openai.MessageSource{Interface: "tui"}
	ctx := WithOwnerReply(context.Background(), func(context.Context, json.RawMessage) (OwnerReply, error) {
		return OwnerReply{Text: "yes", Source: from}, nil
	})
	if reply, err := AskOwnerReply(ctx, nil); err != nil || (reply.Text != "yes" || reply.Source != from || len(reply.Images) != 0) {
		t.Fatalf("got %+v, %v", reply, err)
	}
	if got, err := AskOwner(ctx, nil); err != nil || got != "yes" {
		t.Fatalf("the text alone: %q, %v", got, err)
	}
	if _, err := AskOwnerReply(WithOwnerReply(ctx, nil), nil); !errors.Is(err, ErrNoOwner) {
		t.Fatalf("an owner that is nil: %v", err)
	}
}

func TestOwnerReplyImagesReachNextModelRound(t *testing.T) {
	calls := 0
	llm := llmFunc(func(messages []openai.Message) openai.Message {
		calls++
		if calls == 1 {
			return openai.Message{ToolCalls: []openai.ToolCall{{ID: "q", Function: openai.FunctionCall{Name: "question", Arguments: "{}"}}}}
		}
		last := messages[len(messages)-1]
		if last.Role != "tool" || len(last.Images) != 1 || string(last.Images[0].Data) != "picture" {
			t.Errorf("model did not receive owner picture: %+v", last)
		}
		return openai.Message{Content: "seen"}
	})
	tool := Tool{Name: "question", Run: func(ctx context.Context, _ json.RawMessage) (string, error) {
		reply, err := AskOwnerReply(ctx, json.RawMessage(`{"question":"Which?"}`))
		return reply.Text, err
	}}
	ctx := WithOwnerReply(t.Context(), func(context.Context, json.RawMessage) (OwnerReply, error) {
		return OwnerReply{Images: []openai.Image{{Type: "image/png", Data: []byte("picture")}}}, nil
	})
	result, err := New(llm, "m", "system", tool).Run(ctx, "s1", "go", Observer{})
	if err != nil || result != "seen" || calls != 2 {
		t.Fatalf("result=%q calls=%d err=%v", result, calls, err)
	}
}
