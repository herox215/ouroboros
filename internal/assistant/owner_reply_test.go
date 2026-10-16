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
				if err := json.Unmarshal([]byte(result), &decoded); err != nil || decoded != reply {
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
