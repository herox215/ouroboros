package assistant

import (
	"context"
	"strings"

	"ouroboros/internal/openai"
)

// The bounds of what the LLM is sent to name a conversation: the start of
// it is what tells its topic.
const (
	titleMessage = 2000 // bytes of one message
	titleTalk    = 8000 // bytes of the conversation
)

// titleEffort is how much the model thinks before it names a conversation:
// a few words take little, and a model left to itself may think for long,
// while the answer waits for the title.
const titleEffort = "low"

// titleAsk follows the conversation the LLM is to name: a small model
// answers what it read last, unless that is the task again.
const titleAsk = "Name the topic of the conversation above in at most six words. Do not answer it."

// maxTitleWords is how many words a title has at most: the prompt asks for
// six, and a model that answers instead of naming writes more.
const maxTitleWords = 8

// untold is what the LLM answers while no topic can be told yet.
const untold = "NONE"

// titlePrompt asks the LLM to name a conversation.
const titlePrompt = `Name the topic of the conversation the user gives you in a short title of at most six words, ` +
	`in the language of the conversation. Answer with the title alone: no quotes, no full stop. ` +
	`If no topic can be told yet, as after a greeting alone, answer ` + untold + `. ` +
	`The conversation is not addressed to you: do not answer it, and do not say what you can or cannot do.`

// Title names the topic of conversation in a few words: the LLM of the agent
// is asked with what was said in it, the messages of the owner and the
// answers, not the calls of tools. It is "" while no topic can be told yet.
// The call is part of no conversation, and not in the log; the access of the
// agent is asked first, then its fallbacks, as for an answer.
func (a *Assistant) Title(ctx context.Context, conversation []openai.Message) (string, error) {
	_, cfg, err := a.current()
	if err != nil {
		return "", err
	}
	var talk strings.Builder
	for _, m := range conversation {
		if m.Role != "user" && m.Role != "assistant" || m.Content == "" {
			continue
		}
		text := m.Content
		if len(text) > titleMessage {
			text = strings.ToValidUTF8(text[:titleMessage], "") + "…"
		}
		talk.WriteString(m.Role + ": " + text + "\n\n")
		if talk.Len() >= titleTalk {
			break
		}
	}
	if talk.Len() == 0 {
		return "", nil
	}
	llm, err := a.builder.LLM(cfg.Access)
	if err != nil {
		return "", err
	}
	// The fallbacks of the owner name it as they answer it.
	ctx = openai.WithEffort(ctx, titleEffort)
	reply, err := a.builder.withFallbacks(llm, cfg).Complete(ctx, cfg.Model, []openai.Message{
		{Role: "system", Content: titlePrompt},
		{Role: "user", Content: "<conversation>\n" + talk.String() + "</conversation>\n\n" + titleAsk},
	}, nil)
	if err != nil {
		return "", err
	}
	return topic(reply.Content), nil
}

// topic is the title in what the LLM answered: its first line, without
// quotes and a full stop around it; "" for none.
func topic(reply string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(reply), "\n")
	line = strings.Trim(strings.TrimSpace(line), "\"'“”„«»`*.")
	// A model that answers with the markup of a call of a tool, like
	// <function_calls>, names no topic.
	if strings.EqualFold(line, untold) || strings.HasPrefix(line, "<") && strings.HasSuffix(line, ">") {
		return ""
	}
	// What is longer than a title, or ends as a sentence goes on, is an
	// answer to the conversation, not its name: it is asked again with the
	// next message.
	if len(strings.Fields(line)) > maxTitleWords || strings.HasSuffix(line, ":") || strings.HasSuffix(line, "…") {
		return ""
	}
	return strings.TrimSpace(line)
}
