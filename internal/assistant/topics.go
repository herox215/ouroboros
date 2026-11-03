package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"ouroboros/internal/agent"
	"ouroboros/internal/memory"
)

// A topic is a conversation of the owner. Beside the core memories its
// prompt is given the topics of the memories when it starts, and how many
// memories each has, see withTopic: the agent loads those of one with the
// memory tools. What it is given stays the same from then on, so that the
// provider can cache the prompt. Once the owner left it for idleTime, it is
// looked back on, see finish.

// The bounds of what a topic is given and kept of.
const (
	topicMapBytes = 1000 // of the list of the topics of the memories
	topicMessages = 40   // messages of the owner its review reads
)

// When a topic is looked back on: a topic the owner left for idleTime, with
// finishTurns answers or signals.
const (
	idleTime    = 2 * time.Hour
	finishTurns = 3
)

// The tools of the memories whose topic a conversation is known to have
// loaded, see loadedTopic: those of the plugin memory.
const (
	toolMemoryLoad = "memory_load"
	toolMemoryAdd  = "memory_add"
)

// topicState is what the assistant keeps of a topic.
type topicState struct {
	listed   bool      // topics is given
	topics   string    // the topics of the memories its prompt is given, "" for none
	loaded   []string  // the topics of the memories it loaded or added to
	turns    int       // answers reviewed
	signaled bool      // one of them had signals
	messages []string  // what the log says of the messages of the owner
	last     time.Time // of the last answer
	to       review    // of the last answer
}

// topicIntro starts what the prompt of a topic says of the topics of the
// memories.
const topicIntro = "Your memories are kept by named topic; " + toolMemoryLoad + " loads those of one. " +
	"The topics, with how many memories each has:"

// listing is the topics as a topic is told them, "name (count)" each, as many
// as fit in topicMapBytes: without core, whose memories are in every prompt,
// and "" if there are no others.
func listing(topics []memory.Topic) string {
	var names []string
	size := 0
	for _, t := range topics {
		if strings.EqualFold(t.Name, memory.Core) {
			continue
		}
		name := t.Name + " (" + strconv.Itoa(t.Count) + ")"
		if size+len(name) > topicMapBytes {
			break
		}
		names, size = append(names, name), size+len(name)+len(" · ")
	}
	return strings.Join(names, " · ")
}

// remembered are memories as the review of an answer is told them.
func remembered(entries []memory.Entry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Key + ": " + e.Content
	}
	return out
}

// withTopic gives bot the topics of the memories in its prompt, as the
// conversation chat was told them when it started. The thread is told them
// too.
func (a *Assistant) withTopic(ctx context.Context, chat string, bot *agent.Agent) *agent.Agent {
	if a.builder.memories == nil {
		return bot
	}
	a.learning.Lock()
	t := a.topics[chat]
	if t == nil {
		t = &topicState{}
		a.topics[chat] = t
	}
	t.last = time.Now() // let go of once left, see finish, also where no answer is tracked, as in the thread
	listed, topics := t.listed, t.topics
	a.learning.Unlock()
	if !listed {
		store, err := a.builder.memories()
		var all []memory.Topic
		if err == nil {
			all, err = store.Topics(context.WithoutCancel(ctx)) // a stopped answer says so itself
		}
		if err != nil {
			a.logf("chat %s: the topics of the memories cannot be read: %v", chat, err)
		}
		topics = listing(all)
		a.learning.Lock()
		t.listed, t.topics = true, topics
		a.learning.Unlock()
	}
	if topics == "" {
		return bot
	}
	system, _ := bot.Prompt()
	return bot.WithSystem(system + "\n\n" + topicIntro + " " + topics)
}

// loadedTopic keeps that the conversation chat loaded the memories of a
// topic, or added one to it, which s is the call of a tool of: nothing for
// another tool, or one that failed.
func (a *Assistant) loadedTopic(chat string, s agent.Step) {
	if !s.Done || s.Err != nil || (s.Tool != toolMemoryLoad && s.Tool != toolMemoryAdd) {
		return
	}
	var args struct {
		Topic string `json:"topic"`
	}
	if json.Unmarshal(s.Args, &args) != nil {
		return
	}
	topic, err := memory.CleanTopic(args.Topic)
	if err != nil {
		return
	}
	a.learning.Lock()
	defer a.learning.Unlock()
	t := a.topics[chat]
	if t == nil {
		t = &topicState{}
		a.topics[chat] = t
	}
	if !slices.ContainsFunc(t.loaded, func(name string) bool { return strings.EqualFold(name, topic) }) {
		t.loaded = append(t.loaded, topic)
	}
}

// topicsOf is what the review of an answer in the conversation chat is told
// of the topics: those it was given and those it loaded.
func (a *Assistant) topicsOf(chat string) (string, []string) {
	a.learning.Lock()
	defer a.learning.Unlock()
	t := a.topics[chat]
	if t == nil {
		return "", nil
	}
	return t.topics, slices.Clone(t.loaded)
}

// track keeps what the answer to message in the topic chat leaves for the
// look back on it, and waits anew for the owner to leave.
func (a *Assistant) track(chat, message string, signals []Signal, to review) {
	a.learning.Lock()
	defer a.learning.Unlock()
	t := a.topics[chat]
	if t == nil {
		t = &topicState{}
		a.topics[chat] = t
	}
	t.turns++
	t.signaled = t.signaled || len(signals) > 0
	t.messages = append(t.messages, message)
	t.messages = t.messages[max(0, len(t.messages)-topicMessages):]
	t.last, t.to = time.Now(), to
	if a.stopped {
		return
	}
	if a.idle == nil {
		a.idle = time.AfterFunc(a.idleAfter, a.sweep)
	} else {
		a.idle.Reset(a.idleAfter)
	}
}

// sweep is what happens once the owner left: the topics they left are looked
// back on.
func (a *Assistant) sweep() { a.finish(time.Now()) }

// finish looks back on the topics the owner left at now, see topic, and
// lets go of them: one with enough answers or signals is reviewed with the
// messages of the owner in it.
func (a *Assistant) finish(now time.Time) {
	type left struct {
		chat string
		t    *topicState
	}
	var due []left
	a.learning.Lock()
	for chat, t := range a.topics {
		if now.Sub(t.last) < a.idleAfter {
			continue
		}
		delete(a.topics, chat)
		if t.turns >= finishTurns || t.signaled {
			due = append(due, left{chat, t})
		}
	}
	a.learning.Unlock()
	if len(due) == 0 {
		return
	}
	slices.SortFunc(due, func(x, y left) int { return cmp.Compare(x.chat, y.chat) })
	_, cfg, err := a.current(false, "")
	if err != nil {
		a.logf("the topics left cannot be reviewed: %v", err)
		return
	}
	tools := a.reviewTools(cfg)
	for _, d := range due {
		signal := Signal{SignalFinished, fmt.Sprintf("the owner left the topic after %d answers", d.t.turns)}
		a.reviewed(d.chat, []Signal{signal}, d.t.messages, tools, d.t.to)
	}
}

// logf writes to the log, if there is one.
func (a *Assistant) logf(format string, args ...any) {
	if a.log != nil {
		a.log(format, args...)
	}
}
