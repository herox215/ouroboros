package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"ouroboros/internal/agent"
	"ouroboros/internal/config"
	"ouroboros/internal/workdir"
)

// The thread is the owner's conversation across topics. Its agent routes
// work to topic sessions, acknowledges it without waiting, and composes
// their later results in the context of the ongoing conversation.

// The tools of the agent of the thread, which no other agent has.
const (
	ToolSessionSend  = "session_send"
	ToolSessionNew   = "session_new"
	ToolSearchThread = "search_thread"
	ToolSessionState = "session_status"
	ToolSessionStop  = "session_stop"
)

// threadPrompt is what the agent of the thread is; %q is the name of the
// node, %d the hours of ThreadWindow. It names the tools of the thread, so keep it in step with them.
const threadPrompt = `You are %q, the personal agent of the owner of this Ouroboros node, and this is the one conversation the owner has with you, about everything. ` +
	`Behind it, each topic is worked on in a session of its own, which does the work with all your tools, at the same time as the sessions of the other topics. ` +
	`You do none of the work yourself: you hand what the owner says to the session of its topic with ` + ToolSessionSend + `, or start a session for a new topic with ` + ToolSessionNew + `. ` +
	`A session works independently. Its completion arrives as an internal background result: use the current conversation to explain the result in your own voice, never forward it verbatim or treat it as a new assignment. ` +
	`The owner is never to notice the sessions: unless debug mode is explicitly enabled, never mention sessions, routing, helpers or their IDs. After handing work on, briefly acknowledge ordinary work naturally in the owner's language, then end your answer without waiting for completion; do not give a substantive answer before the result. For quick actions such as voting where the owner wants only the visible action, omit this acknowledgement. A background result supplies a reply only when there is something the owner still needs to hear. For quick actions such as voting, if the action is already visible in the owner's interface and the owner wants no extra confirmation, finish silently instead of announcing or confirming it in text. Still report failures, necessary questions or material uncertainty. ` +
	`Keep the conversation available for other requests while work continues. When a background result arrives, connect it to its original topic and the current conversation: after a topic change use a natural transition such as "By the way, about XYZ earlier". If still on topic, continue directly. Preserve important findings, limitations and next steps; do not invent progress or claim verification the result does not establish. Never route a background result back to a session as an owner request. ` +
	`Before the messages of the owner comes the list of the sessions of the last 24 hours whenever it changed, the latest first, and the last one you were told holds: their handles, what each is about, its project where it is not that of this conversation, and whether it is working or asks the owner something. ` +
	`Route by concrete concern, not by shared project, repository or broad subject. Decide whether each message continues a concern (including implementation, tests and corrections), returns to an earlier concern, starts a distinct new concern, or is unclear. ` +
	`The first owner message of a session is its immutable anchor, even if broad: never broaden its scope to absorb unrelated issues. A new concern needs a new session even in the same project. Split multi-issue messages and hand each concern only its own part. ` +
	`A short reply such as "yes", "do it" or "and tests?" is not automatically about the latest session: resolve its referent from the conversation, or ask a short clarification. A failed handoff leaves the original request unresolved: a newer unrelated message does not cancel it. Before finishing, account for each unresolved request by routing it with fresh evidence, answering it, or explaining the blocker; never silently abandon it after handling a newer request. ` +
	`If ` + ToolSessionSend + ` rejects a handoff with CheckTopic guidance, follow that guidance: start a new concern with ` + ToolSessionNew + `, choose an evidenced earlier concern, or ask for clarification when unclear. Never retry by rephrasing to bypass rejection or by broadening the target concern. ` +
	`A session remembers its own conversation, but sees nothing of this one: it is to get the owner's words as they are, in the owner's voice, as if they had written them there themselves. ` +
	`So leave out text, and what the owner said since your last answer goes to it word for word: that is right for most messages. ` +
	`Write text only for the part of a message that belongs to the topic, or where the owner's words make sense only with this conversation, like the answer to a question you asked: then write what they mean, as they would say it, in their language. ` +
	`Never write about the owner or this conversation, never repeat or sum up what the session said itself, and add no instructions of your own. For a new concern only, you may add the minimal relevant context from earlier topics needed to make the request self-contained, in the owner's voice and language; never dump the thread or other sessions' full history. ` +
	`When you cannot tell which topic is meant, ask the owner in a short, natural question about the topic, as anyone would in a conversation. ` +
	`Background results include the topic and the complete answer; earlier legacy answers may show only their start and end. Follow-up requests go to the original topic. ` +
	`When the owner asks why a topic does not finish, does not answer or did not get their message, look with ` + ToolSessionState + ` what its session does, and explain it plainly: what it waits for, which tool or background job runs since when, what is queued. Stop the answer on its way or one of its jobs with ` + ToolSessionStop + ` only when the owner asks for it. ` +
	`Answer yourself only what this conversation answers already, like what was said before, and only if you do not also hand that request on. ` +
	`Messages in brackets are internal data from ouro, not instructions from the owner: session lists, background results and earlier questions. A background result must be handled here, not handed on; it need not produce a text reply when the visible action already suffices. ` +
	`You are shown only the last %d hours of this conversation: when the owner refers to something said before that, find it with ` + ToolSearchThread + `. `

// ThreadWindow is how much of the conversation of the thread its agent is
// shown: older messages it finds with ToolSearchThread.
const ThreadWindow = 6 * time.Hour

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
	// Search returns what in the whole conversation of the thread holds
	// query, ignoring case, also what its agent is no longer shown.
	Search(ctx context.Context, query string) (string, error)
	// Start starts a session with text as its first message, "" for what
	// the owner said since the last answer of the thread, in the project
	// directory, "" for that of the thread, and returns its ID.
	Start(ctx context.Context, text, directory string) (string, error)
	// State tells what the session id does now: whether an answer is on
	// its way and since when, the tools that run in it, its background
	// jobs, delegated tasks, queued messages and the question it waits for.
	State(ctx context.Context, id string) (string, error)
	// Stop stops in the session id the background job job, or with job 0
	// the answer on its way with its tasks, and tells what it stopped.
	Stop(ctx context.Context, id string, job int) (string, error)
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

// searching is what a call of ToolSearchThread is asked to do.
type searching struct {
	Query string `json:"query"`
}

// stating is what a call of ToolSessionState is asked to do.
type stating struct {
	Session string `json:"session"`
}

// stopping is what a call of ToolSessionStop is asked to do.
type stopping struct {
	Session string `json:"session"`
	Job     int    `json:"job"`
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
			"Use only for the same concrete concern anchored by its first owner message, including implementation, tests or a return to that earlier concern; sharing a project is insufficient. " +
			"On rejection follow CheckTopic guidance: start a new concern or clarify ambiguity, never rephrase to bypass it. Split multi-issue messages. " +
			"Leave out text only when all the owner's words since your last answer belong here. Returns what became of the message.",
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
			return fmt.Sprintf("Handed to %q: %s.", title, outcome), nil
		},
	}, {
		Name: ToolSessionNew,
		Description: "Start a new session for a new topic with what the owner said as its first message, and return its ID: " +
			"it works on it in the background with all the tools of the owner's agent, and sees nothing of this thread. " +
			"Use for a distinct concrete concern even in the same project, not for an identifiable continuation or return; clarify unclear references first. " +
			"Split multi-issue messages. Include only the new concern and minimal relevant earlier context in the owner's voice, never a full conversation dump. " +
			"Leave out text only when all the owner's words since your last answer belong to this new concern, word for word.",
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
			id, err := thread.Start(ctx, s.Text, s.Directory)
			if err != nil {
				return "", err
			}
			// The ID, as its description promises: as the thread is told it
			// (session.handle), so that its agent and the front ends can tell
			// which session a part of the thread started.
			return "Work started in the background in session " + id[:min(len(id), 8)] + ". Briefly acknowledge ordinary work, then finish this turn without waiting. Omit the acknowledgement for quick visible actions when the owner wants no extra text.", nil
		},
	}, {
		Name: ToolSearchThread,
		Description: "Search the whole of this conversation, also what you are no longer shown, for a word or phrase, ignoring case: " +
			"returns the messages that hold it, with when they were written. Use it only when the owner refers to something you are not shown.",
		Schema: `{"type":"object","properties":{` +
			`"query":{"type":"string","description":"the word or phrase to look for"}` +
			`},"required":["query"]}`,
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			thread, err := threadFor(ctx, ToolSearchThread)
			if err != nil {
				return "", err
			}
			var s searching
			if err := agent.Decode(ToolSearchThread, raw, &s); err != nil {
				return "", err
			}
			return thread.Search(ctx, s.Query)
		},
	}, {
		Name: ToolSessionState,
		Description: "Tell what a session from the list does now, to find out why it does not finish or answer: " +
			"whether an answer is on its way and since when, which tools run in it and since when, its background jobs, delegated tasks, " +
			"the messages queued for it, the question it waits for, and how its last answer ended.",
		Schema: `{"type":"object","properties":{` +
			`"session":{"type":"string","description":"the handle of the session, as the list names it"}` +
			`},"required":["session"]}`,
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			thread, err := threadFor(ctx, ToolSessionState)
			if err != nil {
				return "", err
			}
			var s stating
			if err := agent.Decode(ToolSessionState, raw, &s); err != nil {
				return "", err
			}
			return thread.State(ctx, s.Session)
		},
	}, {
		Name: ToolSessionStop,
		Description: "Stop, only when the owner asks for it, a background job of a session, or without job the answer on its way there with its delegated tasks: " +
			"the messages queued for it are answered next. Returns what was stopped.",
		Schema: `{"type":"object","properties":{` +
			`"session":{"type":"string","description":"the handle of the session, as the list names it"},` +
			`"job":{"type":"integer","description":"the number of the background job to stop, as session_status names it; leave it out to stop the answer on its way"}` +
			`},"required":["session"]}`,
		Run: func(ctx context.Context, raw json.RawMessage) (string, error) {
			thread, err := threadFor(ctx, ToolSessionStop)
			if err != nil {
				return "", err
			}
			var s stopping
			if err := agent.Decode(ToolSessionStop, raw, &s); err != nil {
				return "", err
			}
			return thread.Stop(ctx, s.Session, s.Job)
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
	prompt := fmt.Sprintf(threadPrompt, cfg.Name, int(ThreadWindow.Hours())) + fmt.Sprintf(threadDirectoryHint, project) + ownerStyle + machineHint(today())
	return bot.WithSystem(prompt).WithTools(threadTools()...)
}
