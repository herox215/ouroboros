package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"ouroboros/internal/a2a"
	"ouroboros/internal/agent"
	"ouroboros/internal/openai"
	"ouroboros/internal/peers"
)

// steps is an LLM that calls a tool with every step, which is told what the
// tool before it returned, "" at first, and answers with what the last tool
// returned once step returns no tool.
type steps struct {
	step    func(n int, last string) (tool string, args any)
	n       int
	results []string // of the tools, in order
}

func (l *steps) Complete(_ context.Context, _ string, msgs []openai.Message, _ []openai.Tool) (openai.Message, error) {
	last := ""
	if m := msgs[len(msgs)-1]; m.Role == "tool" {
		last = m.Content
		l.results = append(l.results, last)
	}
	tool, args := l.step(l.n, last)
	l.n++
	if tool == "" {
		return openai.Message{Content: "done: " + last}, nil
	}
	raw, _ := json.Marshal(args)
	return openai.Message{ToolCalls: []openai.ToolCall{{ID: fmt.Sprint("c", l.n), Type: "function", Function: openai.FunctionCall{Name: tool, Arguments: string(raw)}}}}, nil
}

// asked is the ID of the question a result of ask_peer or answer_peer names.
func asked(t *testing.T, result string) string {
	t.Helper()
	m := regexp.MustCompile(`\(question ([0-9a-f-]{36})\)`).FindStringSubmatch(result)
	if m == nil {
		t.Fatalf("no question in %q", result)
	}
	return m[1]
}

// The agent of a peer asks back what it needs, the agent that asked answers
// it and gets the result of its request, and thanks are a note that starts
// nothing: all in one task.
func TestPeerAsksBackAndIsAnswered(t *testing.T) {
	luna := serveNode(t, "luna")
	s := newSelf(t, luna.Node)
	s.connect(t, luna)
	var tasks []string
	luna.answers(func(ctx context.Context, _, text string) (string, error) {
		origin, _ := a2a.OriginFrom(ctx)
		tasks = append(tasks, origin.Task.ID())
		file, err := a2a.AskBack(ctx, "which file?")
		if err != nil {
			return "", err
		}
		sure, err := a2a.AskBack(ctx, "sure?")
		return text + ": removed " + file + " (" + sure + ")", err
	})
	llm := &steps{step: func(n int, last string) (string, any) {
		switch n {
		case 0:
			return toolAskPeer, askPeerArgs{Peer: "luna", Message: "clean up"}
		case 1:
			return toolAnswer, answerPeerArgs{Peer: "luna", Question: asked(t, last), Answer: "a.txt"}
		case 2:
			return toolAnswer, answerPeerArgs{Peer: luna.ID, Question: asked(t, last), Answer: "yes"}
		case 3:
			return toolTellPeer, tellPeerArgs{Peer: "luna", Message: "thanks"}
		case 4:
			// A question it was never asked: of another answer, or made up.
			return toolAnswer, answerPeerArgs{Peer: "luna", Question: "none", Answer: "what?"}
		case 5:
			return "probe", struct{}{}
		}
		return "", nil
	}}
	probe := agent.Tool{Name: "probe", Run: func(ctx context.Context, _ json.RawMessage) (string, error) {
		first, again := a2a.TaskFrom(inTask(ctx)), a2a.TaskFrom(inTask(ctx))
		if first != again {
			t.Error("an answer has one task")
		}
		return fmt.Sprint(first.ID(), " ", first.Budget().Left), nil
	}}
	bot := agent.New(llm, "m", "sys", append(PeerTools(s.network, s.id.ID), probe)...)
	got, err := bot.Respond(context.Background(), "c1", "ask luna to clean up")
	if err != nil {
		t.Fatal(err)
	}
	if len(llm.results) != 6 || len(tasks) != 1 || !strings.HasPrefix(llm.results[4], "error: luna never asked this answer a question") {
		t.Fatalf("results %q, %d runs at luna", llm.results, len(tasks))
	}
	first := llm.results[0]
	for _, want := range []string{"luna has not answered yet. It asks back", "\n\nwhich file?\n\n", "Answer with answer_peer({peer, question: \"" + asked(t, first) + "\", answer})"} {
		if !strings.Contains(first, want) {
			t.Errorf("no %q in %q", want, first)
		}
	}
	if !strings.Contains(llm.results[1], "\n\nsure?\n\n") || llm.results[2] != "clean up: removed a.txt (yes)" || llm.results[3] != "luna was told. Nothing answers a note." {
		t.Fatalf("results %q", llm.results)
	}
	// The request, two questions, three answers and the note are one task; the question
	// that was no question was never sent and costs nothing.
	if want := tasks[0] + " " + fmt.Sprint(a2a.MaxExchanges-6); llm.results[5] != want || got != "done: "+want {
		t.Fatalf("the task: %q, want %q", llm.results[5], want)
	}
	if got, want := luna.messages(), "c1/"+luna.ID+": clean up"; got != want {
		t.Fatalf("luna was asked %q, want %q", got, want)
	}
	if got, want := luna.notes(), "c1/"+luna.ID+": thanks"; got != want {
		t.Fatalf("luna was told %q, want %q", got, want)
	}
}

// A question the agent does not answer is given up when its answer ends: the
// peer that asked it does not wait for nothing.
func TestUnansweredQuestionIsGivenUp(t *testing.T) {
	luna := serveNode(t, "luna")
	s := newSelf(t, luna.Node)
	s.connect(t, luna)
	ended := make(chan error, 2)
	luna.answers(func(ctx context.Context, _, _ string) (string, error) {
		_, err := a2a.AskBack(ctx, "which file?")
		ended <- err
		return "", err
	})
	llm := &steps{step: func(n int, _ string) (string, any) {
		if n == 0 {
			return toolAskPeer, askPeerArgs{Peer: "luna", Message: "clean up"}
		}
		return "", nil
	}}
	bot := agent.New(llm, "m", "sys", PeerTools(s.network, s.id.ID)...)
	if got, err := bot.Respond(context.Background(), "c1", "go"); err != nil || !strings.Contains(got, "which file?") {
		t.Fatalf("%q, %v", got, err)
	}
	select {
	case err := <-ended:
		if !errors.Is(err, a2a.ErrGaveUp) {
			t.Fatalf("luna was told %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the question was not given up")
	}
	// Outside of an answer nothing ends, and nothing is given up: the
	// question waits for its time.
	if got, err := s.ask(context.Background(), "luna", "hello"); err != nil || !strings.Contains(got, "which file?") {
		t.Fatalf("%q, %v", got, err)
	}
	select {
	case err := <-ended:
		t.Fatalf("given up without an answer that ended: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
}

// A note to a peer that asked nothing of this answer is sent all the same,
// but the agent is told that it has no message of it to go on, so that it
// does not tell the peer, or its owner, of a question the peer never asked.
func TestANoteToAPeerThatAskedNothing(t *testing.T) {
	luna := serveNode(t, "luna")
	s := newSelf(t, luna.Node)
	s.connect(t, luna)
	luna.answers(func(_ context.Context, _, text string) (string, error) { return "ok: " + text, nil })
	var note string
	llm := &steps{step: func(n int, last string) (string, any) {
		if n > 0 {
			note = last
			return "", nil
		}
		return toolTellPeer, tellPeerArgs{Peer: "luna", Message: "thanks"}
	}}
	if _, err := agent.New(llm, "m", "sys", PeerTools(s.network, s.id.ID)...).Respond(context.Background(), "c1", "thank luna"); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(note, "luna was told. Nothing answers a note.") || !strings.Contains(note, "asked you nothing") {
		t.Fatalf("a note nobody asked for: %q", note)
	}
}

// What a peer does not take of a message is told as what the agent can do
// about it: a budget that is used up, a question that comes first, and one
// that is not open any more.
func TestWhatAPeerDoesNotTake(t *testing.T) {
	luna := serveNode(t, "luna")
	s := newSelf(t, luna.Node)
	s.connect(t, luna)
	luna.answers(func(ctx context.Context, _, _ string) (string, error) { return a2a.AskBack(ctx, "which?") })
	tools := s.tools()
	call := func(ctx context.Context, tool string, args any) (string, error) {
		raw, _ := json.Marshal(args)
		return tools[tool].Run(ctx, raw)
	}
	ctx := context.Background()

	first, err := s.ask(ctx, "luna", "hello")
	if err != nil || !strings.Contains(first, "It asks back") {
		t.Fatalf("%q, %v", first, err)
	}
	got, err := s.ask(ctx, "luna", "hello again")
	if err != nil || !strings.HasPrefix(got, "Nothing was started at luna: its agent asked back in this conversation and waits for the answer. Answer its question with answer_peer first. (") || !strings.Contains(got, asked(t, first)[:8]) {
		t.Fatalf("a request while a question is open: %q, %v", got, err)
	}
	if got, err := call(ctx, toolAnswer, answerPeerArgs{Peer: "luna", Question: asked(t, first), Answer: "this"}); err != nil || got != "this" {
		t.Fatalf("answered: %q, %v", got, err)
	}
	got, err = call(ctx, toolAnswer, answerPeerArgs{Peer: "luna", Question: asked(t, first), Answer: "this"})
	if err != nil || got != "luna has no such question open: it was answered or given up, or its agent stopped waiting. Ask it anew with ask_peer if you still need its answer." {
		t.Fatalf("answered twice: %q, %v", got, err)
	}

	// The budget of the task is used up: nothing is sent, by any of them.
	used := a2a.NewTask()
	luna.answers(nil)
	spent := a2a.WithTask(ctx, used)
	for used.Budget().Left > 0 {
		if _, err := s.ask(spent, "luna", "hello"); err != nil {
			t.Fatal(err)
		}
	}
	heard := luna.messages()
	want := fmt.Sprintf("Nothing was sent to luna: the budget of this task for messages between peers is used up (%d messages or %s, whichever comes first). Finish with what you have, and say what is still open.", a2a.MaxExchanges, a2a.TaskTime)
	for tool, args := range map[string]any{
		toolAskPeer:  askPeerArgs{Peer: "luna", Message: "hello"},
		toolAnswer:   answerPeerArgs{Peer: "luna", Question: "q", Answer: "a"},
		toolTellPeer: tellPeerArgs{Peer: "luna", Message: "hello"},
	} {
		if got, err := call(spent, tool, args); err != nil || got != want {
			t.Errorf("%s with no budget: %q, %v", tool, got, err)
		}
	}
	if luna.messages() != heard || luna.notes() != "" || strings.Count(heard, "hello") != a2a.MaxExchanges+1 {
		t.Fatalf("luna heard %q, was told %q", luna.messages(), luna.notes())
	}
	if refused(errors.New("anything else"), "luna") != "" || refused(nil, "luna") != "" {
		t.Fatal("any other error is told as it is")
	}
}

// The tools that answer and tell need what they send, and a peer that is
// connected: a note asks nobody to connect.
func TestAnswerAndTellNeedAPeer(t *testing.T) {
	luna, sol := serveNode(t, "luna"), serveNode(t, "sol")
	s := newSelf(t, luna.Node, sol.Node)
	s.connect(t, luna)
	tools := s.tools()
	ctx := context.Background()
	for tool, bad := range map[string][]string{
		toolAnswer:   {`{}`, `{"peer":"luna","question":"q"}`, `{"peer":"luna","answer":"a"}`, `{"question":"q","answer":"a"}`},
		toolTellPeer: {`{}`, `{"peer":"luna"}`, `{"message":"hi"}`},
		toolAskBack:  {`{}`},
	} {
		for _, args := range bad {
			if _, err := tools[tool].Run(ctx, json.RawMessage(args)); err == nil || !strings.HasPrefix(err.Error(), tool+" needs ") {
				t.Errorf("%s(%s): %v", tool, args, err)
			}
		}
	}
	for tool, args := range map[string]string{
		toolAnswer:   `{"peer":"%s","question":"q","answer":"a"}`,
		toolTellPeer: `{"peer":"%s","message":"hi"}`,
	} {
		if _, err := tools[tool].Run(ctx, json.RawMessage(fmt.Sprintf(args, "nobody"))); err == nil || !strings.Contains(err.Error(), `no node "nobody" found`) {
			t.Errorf("%s to nobody: %v", tool, err)
		}
		if _, err := tools[tool].Run(ctx, json.RawMessage(fmt.Sprintf(args, "sol"))); !errors.Is(err, peers.ErrNotConnected) {
			t.Errorf("%s to a node that is not connected: %v", tool, err)
		}
	}
	if theirs, _ := sol.contacts.Snapshot(); len(theirs.Incoming) != 0 || sol.notes() != "" {
		t.Fatalf("sol was asked to connect, or told: %+v, %q", theirs.Incoming, sol.notes())
	}
	if ours, _ := s.contacts.Snapshot(); len(ours.Incoming) != 0 {
		t.Fatalf("a note proposed a connection: %+v", ours.Incoming)
	}
}

// ask_back asks the peer that is answered, and says what the agent can do
// when it gets no answer. It is no tool for an answer to the owner.
func TestAskBack(t *testing.T) {
	luna := serveNode(t, "luna")
	s := newSelf(t, luna.Node)
	s.connect(t, luna)
	back := askBack()
	said := make(chan string, 1)
	luna.answers(func(ctx context.Context, _, text string) (string, error) {
		got, err := back.Run(ctx, json.RawMessage(`{"question":"`+text+`?"}`))
		if err != nil {
			got = "error: " + err.Error()
		}
		said <- got
		return got, nil
	})
	ctx := context.Background()
	tools := s.tools()
	first, err := s.ask(ctx, "luna", "which")
	if err != nil || !strings.Contains(first, "\n\nwhich?\n\n") {
		t.Fatalf("%q, %v", first, err)
	}
	args, _ := json.Marshal(answerPeerArgs{Peer: "luna", Question: asked(t, first), Answer: "that one"})
	if got, err := tools[toolAnswer].Run(ctx, args); err != nil || got != "that one" || <-said != "that one" {
		t.Fatalf("answered: %q, %v", got, err)
	}

	// The request was the last message of its task.
	last := a2a.NewTask()
	for last.Budget().Left > 1 {
		s.network.Tell(a2a.WithTask(ctx, last), luna.Node, "c", "x")
	}
	if _, err := s.ask(a2a.WithTask(ctx, last), "luna", "which"); err != nil {
		t.Fatal(err)
	}
	if got := <-said; got != "The question was not asked: the budget of this task for messages between peers is used up. Answer with what you have, and say what you could not find out." {
		t.Fatalf("no budget: %q", got)
	}
	// Nobody answers in time.
	was := a2a.QuestionTime
	defer func() { a2a.QuestionTime = was }()
	a2a.QuestionTime = 10 * time.Millisecond
	if _, err := s.ask(ctx, "luna", "who"); err != nil {
		t.Fatal(err)
	}
	if got := <-said; got != "The peer did not answer the question in time. Answer with what you have, and say what you could not find out." {
		t.Fatalf("no answer: %q", got)
	}
	a2a.QuestionTime = was
	// The peer gave up: the request is over, which is an error.
	late, err := s.ask(ctx, "luna", "why")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.network.GiveUp(ctx, luna.Node, "/"+luna.ID, asked(t, late)); err != nil {
		t.Fatal(err)
	}
	if got := <-said; got != "error: "+a2a.ErrGaveUp.Error() {
		t.Fatalf("given up: %q", got)
	}
	if _, err := back.Run(ctx, json.RawMessage(`{"question":"anyone?"}`)); err == nil || !strings.HasPrefix(err.Error(), "ask_back is for the request of a peer: here you are answering your owner") {
		t.Fatalf("for the owner: %v", err)
	}
}
