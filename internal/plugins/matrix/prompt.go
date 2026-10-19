package matrix

import (
	"context"

	"ouroboros/internal/openai"
	"ouroboros/plugin"
)

const outputInstructions = "Output for Matrix: Keep replies short and to the point. Write answers and commentary as plain text. Do not use Markdown formatting for emphasis, headings, tables, links or code fences. Use paragraphs and simple lists when helpful. Write URLs directly. Preserve literal code, commands, paths and other exact text; do not remove characters that belong to their content. These instructions concern presentation only, not the language of the reply."

// outputHook contributes once per answer, not once per message or tool round.
// The context is ephemeral: it never becomes conversation history. Inspect the
// latest input, not an older Matrix turn, so switching interfaces drops the rule.
func outputHook() plugin.AnswerHook {
	return plugin.AnswerHook{Name: "matrix-output", Before: func(_ context.Context, p plugin.Preparation) ([]openai.Message, error) {
		if p.Conversation.Peer {
			return nil, nil
		}
		for i := len(p.Messages) - 1; i >= 0; i-- {
			m := p.Messages[i]
			if m.Role != "user" {
				continue
			}
			source := m.Source.Normalized()
			if source.Interface == "api" && source.Gateway == gatewayName {
				return []openai.Message{{Role: "system", Content: outputInstructions}}, nil
			}
			break
		}
		return nil, nil
	}}
}
