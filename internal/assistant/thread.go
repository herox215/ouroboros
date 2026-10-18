package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"ouroboros/internal/agent"
	"ouroboros/internal/config"
	"ouroboros/internal/workdir"
)

// The thread is the one conversation in which the owner talks with the
// agent about everything, while behind it each topic is a session of its
// own, and several of them work at once. Its agent does none of the work: it
// hands each message to the session of its topic with ToolSessionSend, or
// starts a session for a new one with ToolSessionNew, and ends its answer
// with that, without a word. What the session answers is shown to the owner
// as the answer of the thread, so that the owner talks with one agent alone.
// The sessions run it, see WithThread.

// The tools of the agent of the thread, which no other agent has.
const (
	ToolSessionSend = "session_send"
	ToolSessionNew  = "session_new"
)

// threadPrompt is what the agent of the thread is; %q is the name of the
// node. It names the tools of the thread, so keep it in step with them.
const threadPrompt = `You are %q, the personal agent of the owner of this Ouroboros node, and this is the one conversation the owner has with you, about everything. ` +
	`Behind it, each topic is worked on in a session of its own, which does the work with all your tools, at the same time as the sessions of the other topics. ` +
	`You do none of the work yourself: you hand what the owner says to the session of its topic with ` + ToolSessionSend + `, or start a session for a new topic with ` + ToolSessionNew + `. ` +
	`What a session answers is shown to the owner as your answer, and what it asks as your question, so that the owner talks with you alone. ` +
	`The owner is never to notice the sessions: never mention them, nor handing on, starting or asking one, nor their IDs, and write nothing along with handing on. ` +
	`Handing on ends your answer: the session answers. ` +
	`Before the messages of the owner comes the list of the sessions of the last 24 hours whenever it changed, the latest first, and the last one you were told holds: their handles, what each is about, its project where it is not that of this conversation, and whether it is working or asks the owner something. ` +
	`Decide for every message which topic it continues, or whether it is a new one; a message about several topics hands each its part. ` +
	`A session remembers its own conversation, but sees nothing of this one: it is to get the owner's words as they are, in the owner's voice, as if they had written them there themselves. ` +
	`So leave out text, and what the owner said since your last answer goes to it word for word: that is right for most messages. ` +
	`Write text only for the part of a message that belongs to the topic, or where the owner's words make sense only with this conversation, like the answer to a question you asked: then write what they mean, as they would say it, in their language. ` +
	`Never write about the owner, this conversation or other topics, never repeat or sum up what the session said itself, and add no instructions of your own. ` +
	`When you cannot tell which topic is meant, ask the owner in a short, natural question about the topic, as anyone would in a conversation. ` +
	`Of what a session answered you are shown only the start and the end: what the owner asks about an answer goes to the session of its topic, which has all of it. ` +
	`Answer yourself only what this conversation answers already, like what was said before. ` +
	`Messages in brackets are from ouro, not from the owner: the list of the sessions, and what the sessions answered and asked, which the owner was shown as yours. `

// handedOn is what the log tells of an answer of the thread that answered
// nothing, as it does when it hands on.
const handedOn = "[nothing: the thread hands on, and the session answers]"

// threadDirectoryHint tells the agent of the thread where a session it
// starts works; %s is the project of the thread, quoted.
const threadDirectoryHint = `The project of the thread, where ` + ToolSessionNew + ` starts a session that names no directory, is %s. `

// Thread is what the agent of the thread hands messages on with: the
// sessions of the owner.
type Thread interface {
	// Send hands text to the session id as a message of the owner, "" for
	// what the owner said since the last answer of the thread, and returns
	// the title of the session and what became of the message.
	Send(ctx context.Context, id, text string) (title, outcome string, err error)
	// Start starts a session with text as its first message, "" for what
	// the owner said since the last answer of the thread, in the project
	// directory, "" for that of the thread, and returns its ID.
	Start(ctx context.Context, text, directory string) (string, error)
}

type threadKey struct{}

// WithThread returns ctx under which the owner's agent answers as the agent
// of the thread, which hands messages on with thread.
func WithThread(ctx context.Context, thread Thread) context.Context {
	return context.WithValue(ctx, threadKey{}, thread)
}

// ThreadOf returns what the agent of the thread hands messages on with
// under ctx, nil outside the thread.
func ThreadOf(ctx context.Context) Thread {
	t, _ := ctx.Value(threadKey{}).(Thread)
	return t
}

// sending is what a call of ToolSessionSend is asked to do.
type sending struct {
	Session string `json:"session"`
	Text    string `json:"text"`
}

// starting is what a call of ToolSessionNew is asked to do.
type starting struct {
	Text      string `json:"text"`
	Directory string `json:"directory"`
}

// threadTools are the tools of the agent of the thread.
func threadTools() []agent.Tool {
	return []agent.Tool{{
		Name: ToolSessionSend,
		Description: "Hand what the owner said to a session from the list of the sessions of the last 24 hours, as their message there: " +
			"an answer to it starts there, the answer on its way takes it, or it answers the question the session waits for. " +
			"Leave out text to hand on what the owner said since your last answer, word for word. Returns what became of the message.",
		Schema: `{"type":"object","properties":{` +
			`"session":{"type":"string","description":"the handle of the session, as the list names it"},` +
			`"text":{"type":"string","description":"only for the part of the owner's message about this topic, or for what their words mean where only this conversation explains them: in their voice and language"}` +
			`},"required":["session"]}`,
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			thread, err := threadFor(ctx, ToolSessionSend)
			if err != nil {
				return "", err
			}
			var s sending
			if err := agent.Decode(ToolSessionSend, raw, &s); err != nil {
				return "", err
			}
			title, outcome, err := thread.Send(ctx, s.Session, s.Text)
			if err != nil {
				return "", err
			}
			agent.End(ctx) // the session answers
			return fmt.Sprintf("Handed to %q: %s.", title, outcome), nil
		},
	}, {
		Name: ToolSessionNew,
		Description: "Start a new session for a new topic with what the owner said as its first message, and return its ID: " +
			"it works on it in the background with all the tools of the owner's agent, and sees nothing of this thread. " +
			"Leave out text to start it with what the owner said since your last answer, word for word.",
		Schema: `{"type":"object","properties":{` +
			`"text":{"type":"string","description":"only for the part of the owner's message about the new topic, or for what their words mean where only this conversation explains them: in their voice and language"},` +
			`"directory":{"type":"string","description":"the absolute project directory of the session; leave it out for the project of the thread"}` +
			`},"required":[]}`,
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			thread, err := threadFor(ctx, ToolSessionNew)
			if err != nil {
				return "", err
			}
			var s starting
			if err := agent.Decode(ToolSessionNew, raw, &s); err != nil {
				return "", err
			}
			if _, err := thread.Start(ctx, s.Text, s.Directory); err != nil {
				return "", err
			}
			agent.End(ctx) // the session answers
			return "Started a session with it: it answers it now.", nil
		},
	}}
}

// threadFor returns what tool hands messages on with under ctx, and fails
// outside the thread.
func threadFor(ctx context.Context, tool string) (Thread, error) {
	thread := ThreadOf(ctx)
	if thread == nil {
		return nil, fmt.Errorf("%s is for the thread alone", tool)
	}
	return thread, nil
}

// inThread returns the agent of the thread for the owner's agent bot of
// cfg, where ctx is of the thread: its prompt and its tools are those of the
// thread alone. Elsewhere it returns bot.
func inThread(ctx context.Context, bot *agent.Agent, cfg config.Config) *agent.Agent {
	if ThreadOf(ctx) == nil {
		return bot
	}
	project := "your workspace"
	if directory := workdir.Of(ctx); directory != "" {
		project = strconv.Quote(directory)
	}
	prompt := fmt.Sprintf(threadPrompt, cfg.Name) + fmt.Sprintf(threadDirectoryHint, project) + ownerStyle + machineHint(today())
	return bot.WithSystem(prompt).WithTools(threadTools()...)
}
