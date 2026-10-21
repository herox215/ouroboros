package assistant

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"ouroboros/internal/agent"
	"ouroboros/internal/config"
	"ouroboros/internal/openai"
)

// What a decision on a call can be, see decide: the owner allows it once or
// for good, refuses it, wants to see the call in full first, asked a
// question about it that is answered in words, or said something that is
// none of these.
const (
	decidedOnce    = "ONCE"
	decidedAlways  = "ALWAYS"
	decidedNot     = "NOT"
	decidedDetails = "DETAILS"
	decidedReply   = "REPLY"
	decidedUnclear = "UNCLEAR"
	decidedMessage = "MESSAGE" // conversational input, not a permission decision
)

// The bounds of what the LLM is sent to ask the owner: the end of the
// conversation tells its language and what the call is for.
const (
	askingTalk = 3000 // bytes of the conversation
	askingCall = 4000 // bytes of the call
)

// askingEffort is how much the model thinks to phrase or read a question:
// the owner waits for it.
const askingEffort = "low"

// phrasePrompt asks the LLM to tell the owner what a call does.
const phrasePrompt = `You write the question with which an assistant asks its owner whether it may run a tool call. ` +
	`Say in one short, plain sentence what the call does and what it changes, and ask whether it may run: ` +
	`no code, no command, no paths unless they matter, nothing technical the owner would have to read closely. ` +
	`Write in the language of the conversation you are shown, as a person writes in a chat. ` +
	`Answer with JSON alone: {"question": "...", "once": "...", "always": "...", "not": "...", "yes": "...", "no": "..."}, ` +
	`where once, always and not are labels of two or three words for: run it this time, always allow this call, do not run it; ` +
	`yes and no are what the assistant says back in a few words once the owner allowed it or did not, as a person in a chat who goes on, ` +
	`like "Alles klar, ich lösche sie." or "Okay, dann lasse ich sie."`

// decidePrompt asks the LLM what the owner's answer to such a question means.
const decidePrompt = `An assistant asked its owner whether it may run a tool call, and the owner answered in their own words. ` +
	`Tell what the answer means. Answer with one word alone: ` +
	decidedOnce + ` if it may run this time, ` + decidedAlways + ` if it may always run this call, ` +
	decidedNot + ` if it must not run, ` + decidedDetails + ` if the owner asks to see the command or the call itself, ` +
	`or ` + decidedUnclear + ` for anything else, like a condition or a change of the call. ` +
	`A topic change or a new unrelated request is ` + decidedMessage + ` with no follow-up question: return control to the main agent. ` +
	`When in doubt, answer ` + decidedUnclear + `: only a clear yes lets the call run. ` +
	`After ` + decidedUnclear + ` add a colon and one short question back to them in their language, as a person asks in a chat, ` +
	`that they can answer with yes or no, like "` + decidedUnclear + `: Soll ich sie nun löschen oder nicht?" ` +
	`After ` + decidedOnce + `, ` + decidedAlways + ` or ` + decidedNot + ` you may add a colon and a few words the assistant says back to them, ` +
	`in their language, as a person in a chat who goes on, like "` + decidedNot + `: Okay, then I leave it." ` +
	`If the owner asks a question about the call instead, like what or which file it is about or why, answer ` +
	decidedReply + `: followed by a short, plain answer to them in their language, as a person says it in a chat, ` +
	`from what the call and the conversation tell: no code or JSON, a path or a name only where it is what they asked for.`

// asking is how a call that needs the owner's leave is put to them: by the
// LLM of the owner's agent, nil where there is none, as in tests.
type asking struct {
	llm   agent.LLM
	model string
}

// askingOf is the asking of cfg: its LLM with the fallbacks of the owner.
func (b Builder) askingOf(cfg config.Config) asking {
	if b.LLM == nil {
		return asking{}
	}
	llm, err := b.LLM(cfg.Access)
	if err != nil {
		return asking{}
	}
	return asking{b.withFallbacks(llm, cfg), cfg.Model}
}

// permission is the question whether the call of tool may run, as the
// owner reads it, and the labels its answers have.
type permission struct {
	Question string `json:"question"`
	Once     string `json:"once"`
	Always   string `json:"always"`
	Not      string `json:"not"`
	// Yes and No are what is said back to the owner once they allowed
	// the call or did not, "" for nothing.
	Yes string `json:"yes"`
	No  string `json:"no"`
}

// plainPermission is the question without the LLM: the call itself.
func plainPermission(tool, call string) permission {
	return permission{Question: "Allow " + tool + ": " + flat(call), Once: AllowOnce, Always: AllowAlways, Not: AllowNot}
}

// phrase is the question whether the call of tool may run, in the words of
// the conversation of ctx; plainPermission where the LLM cannot tell it.
func (a asking) phrase(ctx context.Context, tool, call string) permission {
	plain := plainPermission(tool, call)
	if a.llm == nil {
		return plain
	}
	reply, err := a.complete(ctx, phrasePrompt, talkOf(ctx)+"<call tool=\""+tool+"\">\n"+clip(call, askingCall)+"\n</call>")
	if err != nil {
		return plain
	}
	var p permission
	text := strings.TrimSpace(reply)
	if i, j := strings.Index(text, "{"), strings.LastIndex(text, "}"); i >= 0 && j > i {
		text = text[i : j+1]
	}
	if json.Unmarshal([]byte(text), &p) != nil {
		return plain
	}
	p = permission{strings.TrimSpace(p.Question), flat(p.Once), flat(p.Always), flat(p.Not), flat(p.Yes), flat(p.No)}
	if p.Question == "" || p.Once == "" || p.Always == "" || p.Not == "" || p.Once == p.Always || p.Once == p.Not || p.Always == p.Not {
		return plain
	}
	return p
}

// decide is what the owner's answer to p means, one of the decided
// constants: the LLM reads every answer, a chosen option too; without it
// only a label of p is understood. To a question of the owner,
// decidedReply, it gives the answer too, to an unclear answer the question
// back to them, and to a decision what is said back, "" for what p says.
func (a asking) decide(ctx context.Context, p permission, tool, call, answer string) (string, string) {
	if a.llm == nil {
		said := flat(answer)
		is := func(labels ...string) bool {
			return slices.ContainsFunc(labels, func(l string) bool { return strings.EqualFold(said, l) })
		}
		switch {
		case is(p.Once, AllowOnce):
			return decidedOnce, ""
		case is(p.Always, AllowAlways):
			return decidedAlways, ""
		case is(p.Not, AllowNot):
			return decidedNot, ""
		}
		return decidedUnclear, ""
	}
	reply, err := a.complete(ctx, decidePrompt, talkOf(ctx)+"<call tool=\""+tool+"\">\n"+clip(call, askingCall)+"\n</call>\n"+
		"<question>\n"+p.Question+"\n</question>\n<answer>\n"+answer+"\n</answer>")
	if err != nil {
		return decidedUnclear, ""
	}
	text := strings.TrimSpace(reply)
	if head, said, ok := strings.Cut(text, ":"); ok && strings.ToUpper(strings.Trim(strings.TrimSpace(head), "\"'`*")) == decidedReply {
		if said = strings.TrimSpace(said); said != "" {
			return decidedReply, said
		}
		return decidedUnclear, ""
	}
	word, back, _ := strings.Cut(text, ":")
	word = strings.ToUpper(strings.Trim(strings.TrimSpace(word), "\"'`*.!"))
	switch word {
	case decidedOnce, decidedAlways, decidedNot:
		return word, flat(back)
	case decidedDetails, decidedMessage:
		return word, ""
	case decidedUnclear:
		return word, flat(back)
	}
	return decidedUnclear, ""
}

// ackPrompt asks the LLM what the agent says back once the owner answered
// a question of its own.
const ackPrompt = `An assistant asked its owner a question and the owner answered. ` +
	`Write what the assistant says back at once, before it goes on: a few words in the language of the answer, ` +
	`as a person in a chat who heard it and goes on, like "Alles klar, dann nehme ich SQLite." or "Got it, I'll leave it then." ` +
	`No question, no promise of what comes later beyond going on, no code. Answer with the words alone.`

// acknowledge says back to the owner that their answer to q arrived, in a
// few words of the conversation; nothing without the LLM, or where it
// cannot tell them.
func (a asking) acknowledge(ctx context.Context, q Question, answer string) {
	if a.llm == nil || strings.TrimSpace(answer) == "" {
		return
	}
	text := talkOf(ctx) + "<question>\n" + q.Question
	if len(q.Options) > 0 {
		text += "\n(" + strings.Join(q.Options, " / ") + ")"
	}
	reply, err := a.complete(ctx, ackPrompt, text+"\n</question>\n<answer>\n"+answer+"\n</answer>")
	if err != nil || ctx.Err() != nil {
		return
	}
	agent.Say(ctx, clip(flat(strings.Trim(strings.TrimSpace(reply), "\"")), 300))
}

// complete asks the LLM with prompt about text, outside of any conversation.
func (a asking) complete(ctx context.Context, prompt, text string) (string, error) {
	ctx = openai.WithEffort(ctx, askingEffort)
	reply, err := a.llm.Complete(ctx, a.model, []openai.Message{
		{Role: "system", Content: prompt},
		{Role: "user", Content: text},
	}, nil)
	return reply.Content, err
}

// talkOf is the end of the conversation of ctx, what was said in it, as the
// LLM is shown it: "" outside of an answer.
func talkOf(ctx context.Context) string {
	var said []string
	size := 0
	talk := agent.Talk(ctx)
	for i := len(talk) - 1; i >= 0 && size < askingTalk; i-- {
		m := talk[i]
		if m.Role != "user" && m.Role != "assistant" || strings.TrimSpace(m.Content) == "" {
			continue
		}
		line := m.Role + ": " + clip(m.Content, askingTalk-size)
		said = append([]string{line}, said...)
		size += len(line)
	}
	if len(said) == 0 {
		return ""
	}
	return "<conversation>\n" + strings.Join(said, "\n\n") + "\n</conversation>\n\n"
}

// clip is text cut to at most n bytes, with … where it is cut.
func clip(text string, n int) string {
	if len(text) <= n {
		return text
	}
	return strings.ToValidUTF8(text[:max(0, n)], "") + "…"
}
