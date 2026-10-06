// Package agent turns an LLM into a conversational agent with per-context
// memory that can use tools on the way to its answer.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ouroboros/internal/openai"
)

const (
	maxContexts = 256               // conversations kept in memory
	maxRounds   = 100               // LLM calls per answer; stops an agent that keeps calling tools
	warnRounds  = maxRounds * 4 / 5 // LLM calls after which the LLM is told how many it has left
	// askRounds are the LLM calls after which the LLM is told to ask the
	// user rather than search on, if it has not found what it looks for:
	// what it is to look for may not be clear, and the user knows.
	askRounds = 15
	// maxRepeats is how often in a row the LLM may ask for the same call of
	// a tool: one more gives the same result and is not run.
	maxRepeats = 2

	// A conversation is cut once it has more than maxHistory messages or
	// maxHistoryBytes, and then down to keptHistory messages and
	// keptHistoryBytes. Cut in steps, its start stays the same from one
	// answer to the next until the next cut, and so does what the provider
	// cached of it. As a cut costs that cache, it is only made when it
	// leaves at most half, or beyond hugeHistoryBytes.
	maxHistory       = 80
	maxHistoryBytes  = 256 << 10
	keptHistory      = 40
	keptHistoryBytes = 128 << 10
	hugeHistoryBytes = 1 << 20

	// Budget is how many bytes the results of tools of one answer may have
	// before the LLM is told with every call how much it gathered, and the
	// tools are to give back less: see Spent.
	Budget = 64 << 10

	// minRepeated is how many bytes of lines a result of a tool has to repeat
	// of an earlier result of the same answer before they are left out of it.
	minRepeated = 1 << 10

	// abandoned ends an answer that failed or was stopped in the history, so
	// that the next message does not follow tool results.
	abandoned = "[This answer broke off here, before it was given. " +
		"The work above is done: go on from it when you are asked again.]"
)

// ErrTooManyToolCalls is the error of an answer that was given up because the
// LLM kept asking for tools.
var ErrTooManyToolCalls = errors.New("agent: gave up after too many tool calls")

// LLM generates a reply for a conversation or asks for tools to be run.
// openai.Client implements it; another provider has to translate to and from
// the OpenAI message types.
type LLM interface {
	Complete(ctx context.Context, model string, msgs []openai.Message, tools []openai.Tool) (openai.Message, error)
}

// Endpoint is where the LLM of a node is reached: all that whoever provides
// the LLM gets to know of the configuration of the node.
type Endpoint struct {
	BaseURL string
	APIKey  string
}

// Step reports a tool call: once when it starts and again, with Done set,
// when its result is in, together with how long the call took.
type Step struct {
	Tool   string
	Args   json.RawMessage
	Done   bool
	Result string
	Err    error
	Took   time.Duration
}

// Call reports a call of the LLM: once when it starts and again, with Done
// set, when it has ended, with how long it took and whether it ended in a
// text, in calls of tools or in an error. Content is the text the LLM sent,
// also along with calls of tools. Sent is how many bytes of messages and
// tools the call sent the LLM, as JSON: what an answer costs grows with it.
// Tokens and Cached are what the API said of the call that has ended: the
// tokens it was sent, and how many of them it had cached. 0 if it did not say.
// Thought is how many bytes the LLM thought before it wrote, which go back
// with the calls after it.
type Call struct {
	Sent      int
	Tokens    int
	Cached    int
	Thought   int
	Done      bool
	Took      time.Duration
	ToolCalls int
	Content   string
	Err       error
}

// Observer is told what an agent does on the way to an answer. Either field
// may be nil.
type Observer struct {
	Step func(Step) // every tool call, also those of a script
	Call func(Call) // every call of the LLM
}

type (
	contextKey  struct{}
	observerKey struct{}
	spentKey    struct{}
)

// Spent returns how many bytes the results of tools of the answer that ctx
// belongs to have so far. A tool that gives back much, like a script, gives
// back less once it is beyond Budget.
func Spent(ctx context.Context) int {
	if spent, ok := ctx.Value(spentKey{}).(*atomic.Int64); ok {
		return int(spent.Load())
	}
	return 0
}

// ContextID returns the conversation a tool is run for.
func ContextID(ctx context.Context) string {
	id, _ := ctx.Value(contextKey{}).(string)
	return id
}

// Agent answers messages and remembers each conversation by context ID.
type Agent struct {
	llm    LLM
	model  string
	system openai.Message
	tools  map[string]Tool
	specs  []openai.Tool
	sizeOf int // of specs, as JSON
	memory *Memory
}

// Memory keeps conversations by context ID. It may outlive the agent that
// has it: an agent built anew with the same memory goes on with them.
type Memory struct {
	mu            sync.Mutex
	clock         uint64
	conversations map[string]*conversation
}

// NewMemory returns a memory that knows no conversation yet.
func NewMemory() *Memory {
	return &Memory{conversations: map[string]*conversation{}}
}

type conversation struct {
	lastUsed uint64
	msgs     []openai.Message
}

// New returns an agent that answers with model as told by the system prompt
// and may use tools.
func New(llm LLM, model, system string, tools ...Tool) *Agent {
	a := &Agent{
		llm:    llm,
		model:  model,
		system: openai.Message{Role: "system", Content: system},
		tools:  map[string]Tool{},
		memory: NewMemory(),
	}
	for _, t := range tools {
		a.tools[t.Name] = t
		a.specs = append(a.specs, openai.NewTool(t.Name, t.Description, t.Schema))
	}
	specs, _ := json.Marshal(a.specs) // texts and JSON: cannot fail
	a.sizeOf = len(specs)
	return a
}

// WithMemory makes the agent remember its conversations in m instead of a
// memory of its own, and returns it. It is for an agent that replaces
// another: given the memory of that one, it knows what it was told.
func (a *Agent) WithMemory(m *Memory) *Agent {
	a.memory = m
	return a
}

// Prompt returns what the agent sends with every call of the LLM before the
// conversation: its system prompt and its tools.
func (a *Agent) Prompt() (system string, tools []openai.Tool) {
	return a.system.Content, slices.Clone(a.specs)
}

// Respond answers text within the conversation contextID.
func (a *Agent) Respond(ctx context.Context, contextID, text string) (string, error) {
	return a.Run(ctx, contextID, text, Observer{})
}

// Run is like Respond and reports every call of the LLM and every tool call
// to observe.
func (a *Agent) Run(ctx context.Context, contextID, text string, observe Observer) (string, error) {
	ctx = openai.WithSession(context.WithValue(ctx, contextKey{}, contextID), contextID)
	ctx = context.WithValue(ctx, observerKey{}, observe)
	spent := &atomic.Int64{}
	ctx = context.WithValue(ctx, spentKey{}, spent)
	history := append(a.memory.history(contextID), openai.Message{Role: "user", Content: text})
	asked := len(history) - 1 // where this answer starts
	// An answer that fails is remembered as far as it got: asked again, the
	// LLM goes on from there instead of starting over. Why it failed is not
	// part of it: a peer must not learn the cause from a later answer.
	abandon := func(err error) (string, error) {
		a.memory.remember(contextID, append(history, openai.Message{Role: "assistant", Content: abandoned}))
		return "", err
	}
	for round := range maxRounds {
		// An answer that was stopped while a tool ran asks the LLM no more.
		if err := ctx.Err(); err != nil {
			return abandon(err)
		}
		msgs := append([]openai.Message{a.system}, history...)
		if n := int(spent.Load()); n > Budget && msgs[len(msgs)-1].Role == "tool" {
			msgs[len(msgs)-1].Content += gathered(n)
		}
		if round >= askRounds && round < warnRounds && msgs[len(msgs)-1].Role == "tool" {
			msgs[len(msgs)-1].Content += searching(round)
		}
		if round >= warnRounds {
			msgs[len(msgs)-1].Content += remaining(round)
		}
		sent := 0
		if observe.Call != nil {
			b, _ := json.Marshal(msgs) // texts and JSON: cannot fail
			sent = len(b) + a.sizeOf
			observe.Call(Call{Sent: sent})
		}
		start := time.Now()
		msg, err := a.llm.Complete(ctx, a.model, msgs, a.specs)
		if observe.Call != nil {
			observe.Call(Call{Sent: sent, Tokens: msg.Usage.Prompt, Cached: msg.Usage.Cached, Thought: len(msg.Reasoning), Done: true, Took: time.Since(start),
				ToolCalls: len(msg.ToolCalls), Content: msg.Content, Err: err})
		}
		if err != nil {
			return abandon(err)
		}
		msg.Role = "assistant"
		history = append(history, msg)
		if len(msg.ToolCalls) == 0 {
			a.memory.remember(contextID, history)
			return msg.Content, nil
		}
		before := history[asked : len(history)-1]
		for _, call := range msg.ToolCalls {
			result := repeated(call, before)
			if result.Content == "" {
				result = a.call(ctx, call)
			}
			result.Content = unrepeated(result.Content, history[asked:])
			spent.Add(int64(len(result.Content)))
			history = append(history, result)
		}
	}
	return abandon(ErrTooManyToolCalls)
}

// remaining is what the LLM is told after the last result of a tool when
// round, counted from 0, is one of the last calls of the LLM for an answer.
// It is only sent, not remembered: the next call tells the next number.
func remaining(round int) string {
	return fmt.Sprintf("\n\n[Not from the tool: this is call %d of at most %d of the LLM for this answer. "+
		"Without an answer by then, it breaks off. Finish the work that is begun, "+
		"and answer with what is done and what is still open: asked again, you go on from there.]",
		round+1, maxRounds)
}

// searching is what the LLM is told after the last result of a tool when
// round, counted from 0, is askRounds or later. Like remaining, it is only
// sent.
func searching(round int) string {
	return fmt.Sprintf("\n\n[Not from the tool: this is call %d of the LLM for this answer. "+
		"If you have not yet found what you are looking for, stop searching: "+
		"tell the user in a few words what you checked and what you found, and ask what exactly they mean. "+
		"If you have found it, go on and finish.]", round+1)
}

// repeated returns the result of call when it is the same as each of the
// last maxRepeats calls in msgs, which the LLM asked for in a row: it is not
// run again. Otherwise it returns a message without content.
func repeated(call openai.ToolCall, msgs []openai.Message) openai.Message {
	same := 0
	for _, m := range slices.Backward(msgs) {
		if m.Role == "tool" {
			continue
		}
		if m.Role != "assistant" || len(m.ToolCalls) == 0 {
			break
		}
		last := m.ToolCalls[len(m.ToolCalls)-1]
		if last.Function.Name != call.Function.Name || last.Function.Arguments != call.Function.Arguments {
			break
		}
		if same++; same == maxRepeats {
			return openai.Message{Role: "tool", ToolCallID: call.ID, Content: fmt.Sprintf(
				"error: this is the call of %s you asked for %d times in a row, with the same arguments: "+
					"it was not run again, as it would give the same. Do something else, or answer with what you have.",
				call.Function.Name, maxRepeats+1)}
		}
	}
	return openai.Message{}
}

// gathered is what the LLM is told after the last result of a tool once the
// results of the answer have n bytes, beyond Budget. Like remaining, it is
// only sent.
func gathered(n int) string {
	return fmt.Sprintf("\n\n[Not from the tool: the results of tools of this answer have %d KiB, "+
		"and all of it is sent again with every call. Tools give back less from now on: "+
		"keep data in variables and print only the little you need for the next step.]", n>>10)
}

// unrepeated is result without the runs of its lines, of minRepeated bytes
// or more, that earlier results of tools in msgs have too: the LLM still has
// those, and the answer sends them again with every call. In their place a
// line says what was left out. Empty lines go along with a run. Only the
// results of the same answer count: what came before it may be cut from the
// conversation, while the answer is kept whole.
func unrepeated(result string, msgs []openai.Message) string {
	if len(result) < minRepeated {
		return result
	}
	seen := map[string]bool{}
	for _, m := range msgs {
		if m.Role == "tool" {
			for line := range strings.Lines(m.Content) {
				if line = strings.TrimRight(line, "\r\n"); strings.TrimSpace(line) != "" {
					seen[line] = true
				}
			}
		}
	}
	if len(seen) == 0 {
		return result
	}
	lines := strings.SplitAfter(result, "\n")
	var out strings.Builder
	for i := 0; i < len(lines); {
		j, repeated := i, i
		for ; j < len(lines); j++ {
			line := strings.TrimRight(lines[j], "\r\n")
			blank := strings.TrimSpace(line) == ""
			if !blank && !seen[line] {
				break
			}
			if !blank {
				repeated = j + 1 // a run ends with a line that was seen
			}
		}
		if repeated == i {
			out.WriteString(lines[i])
			i++
			continue
		}
		// A shorter run that starts within this one ends with it: none is
		// left out if this one is not.
		run := strings.Join(lines[i:repeated], "")
		if len(run) >= minRepeated {
			fmt.Fprintf(&out, "[%d lines, %d bytes, left out: an earlier result of this answer has them]\n", repeated-i, len(run))
		} else {
			out.WriteString(run)
		}
		i = repeated
	}
	return out.String()
}

// call runs one tool call and returns its result as a message for the LLM.
func (a *Agent) call(ctx context.Context, call openai.ToolCall) openai.Message {
	tool, ok := a.tools[call.Function.Name]
	if !ok {
		tool = Tool{Name: call.Function.Name, Run: func(context.Context, json.RawMessage) (string, error) {
			return "", errors.New("unknown tool " + call.Function.Name)
		}}
	}
	content, err := Invoke(ctx, tool, json.RawMessage(call.Function.Arguments))
	if err != nil {
		content = "error: " + err.Error()
	}
	return openai.Message{Role: "tool", ToolCallID: call.ID, Content: content}
}

// Invoke runs tool with args within the conversation of ctx and reports the
// call to whoever observes that conversation, like a call the LLM asked for.
// It is for a tool that runs other tools of its agent, such as a script.
func Invoke(ctx context.Context, tool Tool, args json.RawMessage) (string, error) {
	observe, _ := ctx.Value(observerKey{}).(Observer)
	step := Step{Tool: tool.Name, Args: args}
	if observe.Step != nil {
		observe.Step(step)
	}
	start := time.Now()
	step.Result, step.Err = tool.Run(ctx, args)
	step.Done, step.Took = true, time.Since(start)
	if observe.Step != nil {
		observe.Step(step)
	}
	return step.Result, step.Err
}

// Conversation returns what m remembers of the conversation contextID, to go
// on with in another memory: see Restore.
func (m *Memory) Conversation(contextID string) []openai.Message { return m.history(contextID) }

// Restore makes m remember msgs as the conversation contextID, as far as it
// keeps of one.
func (m *Memory) Restore(contextID string, msgs []openai.Message) {
	if len(msgs) > 0 {
		m.remember(contextID, slices.Clone(msgs))
	}
}

func (m *Memory) history(contextID string) []openai.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c, ok := m.conversations[contextID]; ok {
		return slices.Clone(c.msgs)
	}
	return nil
}

func (m *Memory) remember(contextID string, msgs []openai.Message) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.clock++
	msgs = trim(msgs)
	m.conversations[contextID] = &conversation{lastUsed: m.clock, msgs: msgs}
	if len(m.conversations) > maxContexts {
		m.evictOldest()
	}
}

// trim returns the conversation msgs as it is remembered. It starts at a
// user message: a tool result without its call is rejected by the API.
//
// Beyond maxHistory messages or maxHistoryBytes it is cut: it starts at the
// first user message after which there are at most keptHistory messages and
// keptHistoryBytes, and what the LLM thought is left out of every message,
// as the LLM only needs it within an answer. The last user message stays,
// with all that followed it, even beyond those: an answer that took many tool
// calls is not forgotten as a whole. A cut that would keep more than half is
// not made below hugeHistoryBytes: it would cost the cache of all that is
// kept, to save little.
//
// Without a cut, what the LLM thought is left out once it is at least half of
// the conversation from the first message that has it on: the cache of that
// part is lost, but every later call sends less.
func trim(msgs []openai.Message) []openai.Message {
	size := sizes(msgs)
	start := cut(msgs, size, len(msgs), size[0])
	if len(msgs) <= maxHistory && size[0] <= maxHistoryBytes {
		first, thought := -1, 0
		for i, m := range msgs[start:] {
			if m.Reasoning != "" && first < 0 {
				first = start + i
			}
			thought += len(m.Reasoning)
		}
		if first >= 0 && 2*thought >= size[first] {
			return thoughtless(msgs[start:])
		}
		return msgs[start:]
	}
	bare := thoughtless(msgs)
	less := sizes(bare)
	at := cut(bare, less, keptHistory, keptHistoryBytes)
	if less[at] > size[0]/2 && len(msgs)-at > len(msgs)/2 && size[0] <= hugeHistoryBytes {
		return msgs[start:]
	}
	return bare[at:]
}

// thoughtless returns msgs without what the LLM thought.
func thoughtless(msgs []openai.Message) []openai.Message {
	msgs = slices.Clone(msgs)
	for i := range msgs {
		msgs[i].Reasoning, msgs[i].ReasoningField = "", ""
	}
	return msgs
}

// sizes returns the sizes of msgs as JSON: sizes[i] is that of msgs[i:].
func sizes(msgs []openai.Message) []int {
	sizes := make([]int, len(msgs)+1)
	for i := len(msgs) - 1; i >= 0; i-- {
		b, _ := json.Marshal(msgs[i]) // texts and JSON: cannot fail
		sizes[i] = sizes[i+1] + len(b)
	}
	return sizes
}

// cut returns the first user message of msgs after which there are at most
// keep messages and keepBytes, by sizes; the last one if there is none, and
// len(msgs) if there is no user message at all.
func cut(msgs []openai.Message, sizes []int, keep, keepBytes int) int {
	last := len(msgs)
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != "user" {
			continue
		}
		if last < len(msgs) && (len(msgs)-i > keep || sizes[i] > keepBytes) {
			break
		}
		last = i
	}
	return last
}

func (m *Memory) evictOldest() {
	var oldest string
	for id, c := range m.conversations {
		if oldest == "" || c.lastUsed < m.conversations[oldest].lastUsed {
			oldest = id
		}
	}
	delete(m.conversations, oldest)
}
