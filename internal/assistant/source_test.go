package assistant

import (
	"context"
	"strings"
	"testing"

	"ouroboros/internal/agent"
	"ouroboros/internal/openai"
)

func TestHelperDoesNotInheritOwnerMessageEnvelope(t *testing.T) {
	ctx := openai.WithMessageSource(context.Background(), openai.MessageSource{Interface: "gui"})
	ctx = agent.WithInputMessages(ctx, []openai.Message{{Role: "user", Content: "owner input", Source: openai.MessageSource{Interface: "gui"}}})
	ctx = agent.WithMessageSteering(ctx, func() []openai.Message {
		t.Error("helper consumed owner steering")
		return nil
	})
	llm := newHelpers(1)
	result, err := delegating(llm).Run(ctx, tasks("helper task"))
	if err != nil || !strings.Contains(result, "done: helper task") {
		t.Fatalf("helper response %q, %v", result, err)
	}
	if source := openai.MessageSourceFrom(helping(ctx)); source != (openai.MessageSource{}) {
		t.Fatalf("helper inherited owner source: %+v", source)
	}
	if notes := agent.SteeredMessages(helping(ctx)); len(notes) != 0 {
		t.Fatalf("helper inherited steering: %+v", notes)
	}
}
