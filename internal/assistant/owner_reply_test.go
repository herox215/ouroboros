package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"ouroboros/internal/agent"
	"ouroboros/internal/config"
	"ouroboros/internal/openai"
)

func TestOwnerReplySourceIsNotAnswer(t *testing.T) {
	for _, source := range []string{"tui", "gui", "unknown"} {
		for _, text := range []string{AllowOnce, AllowAlways, AllowNot, "  here\nunchanged  "} {
			t.Run(source+"/"+text, func(t *testing.T) {
				reply := agent.OwnerReply{Text: text, Source: openai.MessageSource{Interface: source}}
				ctx := agent.WithOwnerReply(t.Context(), func(context.Context, json.RawMessage) (agent.OwnerReply, error) { return reply, nil })
				got, err := agent.AskOwner(ctx, json.RawMessage(`{}`))
				if err != nil || got != text {
					t.Fatalf("raw reply: %q, %v", got, err)
				}
				result, err := userQuestion().Run(ctx, json.RawMessage(`{"question":"Which?","options":["Run it this time","Always allow it","Don't run it"]}`))
				var decoded agent.OwnerReply
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal([]byte(result), &decoded); err != nil || (decoded.Text != reply.Text || decoded.Source != reply.Source || len(decoded.Images) != 0) {
					t.Fatalf("model reply: %s, %v", result, err)
				}
				var kept []config.Call
				tool, runs := gated(t, ToolRebuild, config.Config{}, &kept)
				_, err = tool.Run(ctx, json.RawMessage(`{}`))
				allowed := text == AllowOnce || text == AllowAlways
				if allowed != (err == nil) || allowed != (*runs == 1) {
					t.Fatalf("permission: runs=%d, err=%v", *runs, err)
				}
				if (text == AllowAlways) != (len(kept) == 1) {
					t.Fatalf("kept: %+v", kept)
				}
				if !allowed && text != AllowNot && !strings.HasSuffix(err.Error(), text) {
					t.Fatalf("altered refusal: %v", err)
				}
			})
		}
	}
}

func TestImageReplyCannotApprovePermission(t *testing.T) {
	for _, text := range []string{"", AllowOnce, AllowAlways, "new topic: look at this"} {
		t.Run(text, func(t *testing.T) {
			var kept []config.Call
			tool, runs := gated(t, ToolRebuild, config.Config{}, &kept)
			asked := 0
			ctx := agent.WithOwnerReply(t.Context(), func(context.Context, json.RawMessage) (agent.OwnerReply, error) {
				asked++
				return agent.OwnerReply{Text: text, Images: []openai.Image{{Type: "image/png", Data: []byte("picture")}}}, nil
			})
			_, err := tool.Run(ctx, json.RawMessage(`{}`))
			if err == nil || !strings.Contains(err.Error(), "sent images") || !strings.HasSuffix(err.Error(), text) || *runs != 0 || len(kept) != 0 || asked != 1 {
				t.Fatalf("runs=%d kept=%v asked=%d err=%v", *runs, kept, asked, err)
			}
		})
	}
}

func TestImageReplyCannotApplyConfig(t *testing.T) {
	c := newConfigured(t)
	ctx := agent.WithOwnerReply(t.Context(), func(context.Context, json.RawMessage) (agent.OwnerReply, error) {
		return agent.OwnerReply{Text: "Apply change", Images: []openai.Image{{Type: "image/png", Data: []byte("picture")}}}, nil
	})
	got, err := c.builder.proposeConfig().Run(ctx, json.RawMessage(`{"path":"name","value":"you","reason":"a better name"}`))
	if err != nil || c.config().Name != "me" || len(c.waiting()) != 0 || !strings.Contains(got, "not changed") {
		t.Fatalf("result=%q name=%q err=%v", got, c.config().Name, err)
	}
}

func TestPermissionTopicChangeReturnsToAgent(t *testing.T) {
	llm := &askingLLM{phrased: `{"question":"Allow?","once":"Yes","always":"Always","not":"No"}`, decided: "MESSAGE: Ignore this follow-up"}
	runs := 0
	b := Builder{LLM: func(string) (agent.LLM, error) { return llm, nil }}
	tool := b.permitted(config.Config{}, []agent.Tool{{Name: ToolRebuild, Run: func(context.Context, json.RawMessage) (string, error) { runs++; return "done", nil }}}, false)[0]
	var asked []Question
	_, err := tool.Run(answering("Tell me about cats", &asked), json.RawMessage(`{}`))
	if err == nil || runs != 0 || len(asked) != 1 || !strings.Contains(err.Error(), "Tell me about cats") {
		t.Fatalf("runs=%d asked=%d err=%v", runs, len(asked), err)
	}
}

// Attachments are no leave for a call either, whatever comes with them.
func TestAttachmentReplyCannotApprovePermission(t *testing.T) {
	var kept []config.Call
	tool, runs := gated(t, ToolRebuild, config.Config{}, &kept)
	ctx := agent.WithOwnerReply(t.Context(), func(context.Context, json.RawMessage) (agent.OwnerReply, error) {
		return agent.OwnerReply{Text: AllowOnce, HasAttachments: true}, nil
	})
	if _, err := tool.Run(ctx, json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "sent attachments") || *runs != 0 || len(kept) != 0 {
		t.Fatalf("runs=%d kept=%v err=%v", *runs, kept, err)
	}
}
