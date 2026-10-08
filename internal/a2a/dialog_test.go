package a2a

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// duo is two nodes that trust one another: terra asks, luna answers.
func duo(t *testing.T) (terra, luna *node) {
	t.Helper()
	terra, luna = newNode(t, "terra"), newNode(t, "luna")
	luna.trust.AddTrusted(terra.id.Public(), "terra", "")
	terra.trust.AddTrusted(luna.id.Public(), "luna", "")
	return terra, luna
}

// to asks luna as terra does, with the client of terra.
type to struct {
	c    *Client
	luna *node
}

func (p to) ask(ctx context.Context, contextID, text string) (Reply, error) {
	return p.c.Ask(ctx, p.luna.srv.URL, p.luna.id.Public(), contextID, text)
}

func (p to) answer(ctx context.Context, contextID, question, text string) (Reply, error) {
	return p.c.Answer(ctx, p.luna.srv.URL, p.luna.id.Public(), contextID, question, text)
}

// An agent that answers a peer asks it back, as often as it needs, and goes
// on with each answer where it was: the peer gets the question as what its
// request is answered with, and the result as what its last answer is. All
// of it is one task, with one budget.
func TestAskBack(t *testing.T) {
	terra, luna := duo(t)
	var origin Origin
	runs := 0
	luna.respond = func(ctx context.Context, contextID, text string) (string, error) {
		runs++
		origin, _ = OriginFrom(ctx)
		file, err := AskBack(ctx, "which file?")
		if err != nil {
			return "", err
		}
		sure, err := AskBack(ctx, "are you sure?")
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%s in %s: removed %s (%s)", text, contextID, file, sure), nil
	}
	task := NewTask()
	ctx := WithTask(context.Background(), task)
	p := to{terra.client(), luna}

	first, err := p.ask(ctx, "c1", "clean up")
	if err != nil || first.Text != "which file?" || first.Question == "" {
		t.Fatalf("asked back %+v, %v", first, err)
	}
	second, err := p.answer(ctx, "c1", first.Question, "a.txt")
	if err != nil || second.Text != "are you sure?" || second.Question == "" || second.Question == first.Question {
		t.Fatalf("asked back again %+v, %v", second, err)
	}
	result, err := p.answer(ctx, "c1", second.Question, "yes")
	if err != nil || result.Text != "clean up in c1: removed a.txt (yes)" || result.Question != "" {
		t.Fatalf("result %+v, %v", result, err)
	}
	if runs != 1 {
		t.Fatalf("the agent ran %d times for one request", runs)
	}
	// The request, two questions and two answers: five messages of one task.
	if origin.PublicKey != terra.id.Public() || origin.Task.ID() != task.ID() || task.Budget().Left != MaxExchanges-5 || origin.Task.Budget().Left != MaxExchanges-5 {
		t.Fatalf("origin %+v, left %d here and %d there", origin, task.Budget().Left, origin.Task.Budget().Left)
	}
	// A question that is answered is none any more.
	if _, err := p.answer(ctx, "c1", second.Question, "yes"); !IsNoQuestion(err) {
		t.Fatalf("answered twice: %v", err)
	}
	if _, err := p.answer(ctx, "c1", "no-such-question", "yes"); !IsNoQuestion(err) || IsQuestionOpen(err) {
		t.Fatalf("no such question: %v", err)
	}
	// Only the peer that was asked answers.
	luna.respond = func(ctx context.Context, _, _ string) (string, error) { return AskBack(ctx, "who are you?") }
	asked, _ := p.ask(ctx, "c2", "hi")
	if _, err := luna.client().Answer(ctx, luna.srv.URL, luna.id.Public(), "c2", asked.Question, "me"); !IsNoQuestion(err) {
		t.Fatalf("another peer answered: %v", err)
	}
	if got, err := p.answer(ctx, "c2", asked.Question, "terra"); err != nil || got.Text != "terra" {
		t.Fatalf("the peer that was asked: %+v, %v", got, err)
	}
	if _, err := AskBack(ctx, "anyone?"); !errors.Is(err, ErrNoAsker) {
		t.Fatalf("nobody to ask back: %v", err)
	}
	// What ended runs in no conversation, and waits for no answer.
	luna.server.dialogs.mu.Lock()
	running, asking := len(luna.server.dialogs.byContext), len(luna.server.dialogs.byQuestion)
	luna.server.dialogs.mu.Unlock()
	if running != 0 || asking != 0 {
		t.Fatalf("%d dialogs run, %d ask", running, asking)
	}
}

// A message that is delivered twice is answered once: the agent does not
// start anew for it, and an answer is not taken twice. Another message is
// another one, whatever it says.
func TestMessagesAreNotHandledTwice(t *testing.T) {
	terra, luna := duo(t)
	var runs atomic.Int32
	luna.respond = func(ctx context.Context, _, text string) (string, error) {
		n := runs.Add(1)
		got, err := AskBack(ctx, fmt.Sprintf("what about %s?", text))
		return fmt.Sprintf("run %d: %s", n, got), err
	}
	ctx := context.Background()
	c := terra.client()
	send := func(msg Message) (*Message, error) {
		res, err := c.call(ctx, luna.srv.URL, luna.id.Public(), MethodSend, sendParams{msg, Peer{}, 0})
		if err != nil {
			return nil, err
		}
		return res.Result, nil
	}
	request := NewMessage("user", "c1", "tea")
	first, err := send(request)
	again, err2 := send(request)
	if err != nil || err2 != nil || first.meta().Question == "" || again.meta().Question != first.meta().Question || runs.Load() != 1 {
		t.Fatalf("delivered twice: %+v, %v and %+v, %v, %d runs", first, err, again, err2, runs.Load())
	}
	answer := NewMessage("user", "c1", "with milk")
	answer.Metadata = &Meta{Kind: KindAnswer, Question: first.meta().Question}
	result, err := send(answer)
	twice, err2 := send(answer)
	if err != nil || err2 != nil || result.Text() != "run 1: with milk" || twice.Text() != result.Text() || result.meta().Kind != "" || result.meta().Task == nil {
		t.Fatalf("answered twice: %+v, %v and %+v, %v", result, err, twice, err2)
	}
	// The request once more, after its end: still the question it was
	// answered with first.
	if late, err := send(request); err != nil || late.meta().Question != first.meta().Question || runs.Load() != 1 {
		t.Fatalf("delivered again: %+v, %v", late, err)
	}
	// The same words in a new message are a new request, and so is a message
	// that has no ID to know it by.
	if _, err := send(NewMessage("user", "c2", "tea")); err != nil || runs.Load() != 2 {
		t.Fatalf("a new request: %v, %d runs", err, runs.Load())
	}
	luna.respond = func(context.Context, string, string) (string, error) { runs.Add(1); return "ok", nil }
	nameless := NewMessage("user", "c3", "tea")
	nameless.MessageID = ""
	for range 2 {
		if got, err := send(nameless); err != nil || got.Text() != "ok" {
			t.Fatalf("a message without an ID: %+v, %v", got, err)
		}
	}
	if runs.Load() != 4 {
		t.Fatalf("%d runs", runs.Load())
	}
	// What ended a while ago is forgotten: its message is a new one then.
	kept := dialogKept
	defer func() { dialogKept = kept }()
	dialogKept = 0
	send(NewMessage("user", "c4", "tea"))
	if got, err := send(nameless); err != nil || got.Text() != "ok" {
		t.Fatal(err)
	}
	luna.server.dialogs.mu.Lock()
	left := len(luna.server.dialogs.byMessage)
	luna.server.dialogs.mu.Unlock()
	if left != 1 { // the request of c2, whose agent still waits for its answer
		t.Fatalf("%d messages are remembered", left)
	}
}

// While an agent waits for the answer to its question, a new request in the
// same conversation is told so instead of waiting behind it; other
// conversations go on.
func TestQuestionComesFirst(t *testing.T) {
	terra, luna := duo(t)
	luna.respond = func(ctx context.Context, _, text string) (string, error) {
		if text == "plain" {
			return "ok", nil
		}
		return AskBack(ctx, "which?")
	}
	ctx := context.Background()
	p := to{terra.client(), luna}
	asked, err := p.ask(ctx, "c1", "do it")
	if err != nil || asked.Question == "" {
		t.Fatalf("%+v, %v", asked, err)
	}
	_, err = p.ask(ctx, "c1", "do it")
	if !IsQuestionOpen(err) || IsNoQuestion(err) || !strings.Contains(err.Error(), "answer the question "+asked.Question+" first") {
		t.Fatalf("a request while a question is open: %v", err)
	}
	if got, err := p.ask(ctx, "c2", "plain"); err != nil || got.Text != "ok" {
		t.Fatalf("another conversation: %+v, %v", got, err)
	}
	if got, err := p.answer(ctx, "c1", asked.Question, "that"); err != nil || got.Text != "that" {
		t.Fatalf("answered: %+v, %v", got, err)
	}
	if got, err := p.ask(ctx, "c1", "plain"); err != nil || got.Text != "ok" {
		t.Fatalf("after the answer: %+v, %v", got, err)
	}
}

// A note is kept and starts nothing: no agent runs for it, whatever it says.
func TestNotesStartNothing(t *testing.T) {
	terra, luna := duo(t)
	runs := 0
	luna.respond = func(context.Context, string, string) (string, error) { runs++; return "you are welcome", nil }
	ctx := context.Background()
	c := terra.client()
	tell := func(contextID, text string) error {
		return c.Tell(ctx, luna.srv.URL, luna.id.Public(), contextID, text)
	}
	if err := tell("c1", "thanks"); !code(err, CodeMethodNotFound) {
		t.Fatalf("a node that takes no notes: %v", err)
	}
	var notes []string
	var failing error
	luna.server.Note = func(ctx context.Context, contextID, text string) error {
		if origin, _ := OriginFrom(ctx); origin.PublicKey != terra.id.Public() {
			t.Errorf("a note of %q", origin.PublicKey)
		}
		notes = append(notes, contextID+": "+text)
		return failing
	}
	task := NewTask()
	if err := c.Tell(WithTask(ctx, task), luna.srv.URL, luna.id.Public(), "c1", "thanks"); err != nil {
		t.Fatal(err)
	}
	if runs != 0 || len(notes) != 1 || notes[0] != "c1: thanks" || task.Budget().Left != MaxExchanges-1 {
		t.Fatalf("%d runs, notes %q, %d left", runs, notes, task.Budget().Left)
	}
	failing = errors.New("disk full")
	if err := tell("c1", "thanks again"); !code(err, CodeInternalError) || strings.Contains(err.Error(), "disk full") {
		t.Fatalf("a note that cannot be kept: %v", err)
	}
	if len(luna.log) != 1 || !strings.Contains(luna.log[0], "was not kept: disk full") {
		t.Fatalf("log %q", luna.log)
	}
	if err := tell("c1", ""); err == nil {
		t.Fatal("a note of nothing: want error")
	}
	// A stranger leaves no note, but a connection request.
	stranger := newNode(t, "sol")
	if err := stranger.client().Tell(ctx, luna.srv.URL, luna.id.Public(), "c1", "hi"); !IsPending(err) || len(notes) != 2 {
		t.Fatalf("a stranger: %v, notes %q", err, notes)
	}
}

// Two agents that ask one another without end stop when the budget of the
// task is used up, and are told so. A peer cannot fill a budget, and what an
// agent asks of others on the way counts against the same one.
func TestBudgetEndsALoop(t *testing.T) {
	terra, luna := duo(t)
	sol := newNode(t, "sol")
	sol.trust.AddTrusted(luna.id.Public(), "luna", "")
	var inner Origin
	sol.respond = func(ctx context.Context, _, _ string) (string, error) {
		inner, _ = OriginFrom(ctx)
		return "sol", nil
	}
	var questions atomic.Int32
	ended := make(chan error, 1)
	luna.respond = func(ctx context.Context, _, text string) (string, error) {
		if text == "ask sol" {
			return luna.client().Send(ctx, sol.srv.URL, sol.id.Public(), "c9", "hi")
		}
		if text == "ask back" {
			return AskBack(ctx, "what?")
		}
		for {
			if _, err := AskBack(ctx, "and then?"); err != nil {
				ended <- err
				return "that is all: " + err.Error(), nil
			}
			questions.Add(1)
		}
	}
	task := NewTask()
	ctx := WithTask(context.Background(), task)
	p := to{terra.client(), luna}
	r, err := p.ask(ctx, "c1", "go on")
	sent := 1
	open := ""
	for err == nil && r.Question != "" {
		open = r.Question
		r, err = p.answer(ctx, "c1", open, "more")
		sent++
		if sent > 100 {
			t.Fatal("the loop does not end")
		}
	}
	// 16 messages: the request, eight questions and seven answers; the
	// eighth answer is not sent any more.
	if !errors.Is(err, ErrBudget) || !IsBudget(err) || sent != 9 || questions.Load() != 7 || task.Budget().Left != 0 {
		t.Fatalf("ended with %v after %d messages of terra and %d answers taken, %d left", err, sent, questions.Load(), task.Budget().Left)
	}
	// The last question is given up, which costs nothing, and the agent
	// that asked it stops.
	if err := p.c.GiveUp(ctx, luna.srv.URL, luna.id.Public(), "c1", open); err != nil {
		t.Fatal(err)
	}
	if err := <-ended; !errors.Is(err, ErrGaveUp) {
		t.Fatalf("the agent that asked was told %v", err)
	}
	if _, err := p.ask(ctx, "c2", "once more"); !errors.Is(err, ErrBudget) {
		t.Fatalf("a request of a task that is used up: %v", err)
	}
	if err := p.c.Tell(ctx, luna.srv.URL, luna.id.Public(), "c2", "bye"); !errors.Is(err, ErrBudget) {
		t.Fatalf("a note of a task that is used up: %v", err)
	}

	// The node refuses a message of a task that is used up or over, also
	// when the peer sends it all the same, and one that says more is left
	// than a task has is taken for what a task has.
	text := "ask sol"
	send := func(b *Budget) (*response, error) {
		msg := NewMessage("user", "c3", text)
		msg.Metadata = &Meta{Task: b}
		return p.c.call(context.Background(), luna.srv.URL, luna.id.Public(), MethodSend, sendParams{msg, Peer{}, 0})
	}
	far := time.Now().Add(time.Hour)
	if _, err := send(&Budget{ID: "t", Left: -1, Until: far}); !code(err, CodeBudget) || !IsBudget(err) {
		t.Fatalf("no message left: %v", err)
	}
	if _, err := send(&Budget{ID: "t", Left: 5, Until: time.Now().Add(-time.Second)}); !code(err, CodeBudget) {
		t.Fatalf("no time left: %v", err)
	}
	res, err := send(&Budget{ID: "t7", Left: 1000, Until: far.Add(24 * time.Hour)})
	if err != nil || res.Result.Text() != "sol" {
		t.Fatalf("%+v, %v", res, err)
	}
	// Luna took one for what it asked sol.
	left := res.Result.meta().Task
	if inner.Task.ID() != "t7" || inner.Hops != 1 || left.ID != "t7" || left.Left != MaxExchanges-1 || !left.Until.Before(far) {
		t.Fatalf("the task at sol %+v, left %+v", inner.Task.Budget(), left)
	}
	if res, err := send(&Budget{ID: "t8", Left: 1, Until: far}); err != nil || res.Result.meta().Task.Left != 0 {
		t.Fatalf("one message left: %+v, %v", res, err)
	}
	// The request was the last message of its task: the agent answers it,
	// but asks nobody for it.
	if _, err := send(&Budget{ID: "t9", Left: 0, Until: far}); !code(err, CodeInternalError) || len(luna.failures()) != 1 || !errors.Is(luna.failures()[0].Err, ErrBudget) {
		t.Fatalf("the last message: %v, failed %+v", err, luna.failures())
	}
	text = "ask back"
	if _, err := send(&Budget{ID: "t10", Left: 0, Until: far}); !code(err, CodeInternalError) || len(luna.failures()) != 2 || !errors.Is(luna.failures()[1].Err, ErrBudget) {
		t.Fatalf("the last message, and a question back: %v, failed %+v", err, luna.failures())
	}
	text = "ask sol"
	// A request that names no task starts one, and is its first message.
	plain := NewMessage("user", "c4", "ask sol")
	if res, err := p.c.call(context.Background(), luna.srv.URL, luna.id.Public(), MethodSend, sendParams{plain, Peer{}, 0}); err != nil || res.Result.meta().Task.Left != MaxExchanges-2 {
		t.Fatalf("a request without a task: %+v, %v", res, err)
	}
}

// What a task has left is the lower of what its agents know.
func TestTask(t *testing.T) {
	task := NewTask()
	b := task.Budget()
	if b.ID != task.ID() || b.Left != MaxExchanges || time.Until(b.Until) > TaskTime || time.Until(b.Until) < TaskTime-time.Minute {
		t.Fatalf("a new task: %+v", b)
	}
	task.heard(nil)
	task.heard(&Budget{Left: 20, Until: b.Until.Add(time.Hour)})
	if got := task.Budget(); got.Left != MaxExchanges || !got.Until.Equal(b.Until) {
		t.Fatalf("a budget is not filled: %+v", got)
	}
	task.heard(&Budget{Left: 2, Until: b.Until.Add(-time.Minute)})
	if got := task.Budget(); got.Left != 2 || !got.Until.Equal(b.Until.Add(-time.Minute)) {
		t.Fatalf("heard %+v", got)
	}
	first, second, third := task.spend(), task.spend(), task.spend()
	if first != nil || second != nil || !errors.Is(third, ErrBudget) || task.Budget().Left != 0 {
		t.Fatalf("spent: %+v", task.Budget())
	}
	task.heard(&Budget{Left: -3, Until: b.Until})
	if task.Budget().Left != 0 {
		t.Fatalf("less than nothing: %+v", task.Budget())
	}
	over, room := taskOf(&Budget{ID: "x", Left: 3, Until: time.Now()})
	if over.ID() != "x" || room || !errors.Is(over.spend(), ErrBudget) || over.Budget().Left != 3 {
		t.Fatalf("a task whose time is over: %+v", over.Budget())
	}
	if last, room := taskOf(&Budget{ID: "x", Left: 0, Until: time.Now().Add(time.Minute)}); !room || !errors.Is(last.spend(), ErrBudget) {
		t.Fatal("the last message of a task is taken, and no more is sent for it")
	}
	if fresh, room := taskOf(nil); fresh.ID() == "" || !room || fresh.Budget().Left != MaxExchanges-1 {
		t.Fatalf("a message without a task: %+v", fresh.Budget())
	}
	// The task of what is sent: that of the message that is answered before
	// one of the answer to the owner.
	ctx := context.Background()
	if TaskFrom(ctx) != nil {
		t.Fatal("no task")
	}
	mine, theirs := NewTask(), NewTask()
	ctx = WithTask(ctx, mine)
	if TaskFrom(ctx) != mine || TaskFrom(WithOrigin(ctx, Origin{})) != mine || TaskFrom(WithOrigin(ctx, Origin{Task: theirs})) != theirs {
		t.Fatal("the task of a context")
	}
}

// When the peer gives up, the agent that works for it is stopped: while it
// works, and while it waits for an answer. A question nobody answers ends by
// itself.
func TestGivingUpStopsTheAgent(t *testing.T) {
	terra, luna := duo(t)
	var mu sync.Mutex
	stopped := map[string]error{}
	done := make(chan string, 8)
	started := make(chan struct{}, 8)
	luna.respond = func(ctx context.Context, contextID, text string) (string, error) {
		var err error
		if text == "work" {
			started <- struct{}{}
			<-ctx.Done()
			err = ctx.Err()
		} else {
			_, err = AskBack(ctx, "which?")
			<-ctx.Done() // given up, the request is too
		}
		mu.Lock()
		stopped[contextID] = err
		mu.Unlock()
		done <- contextID
		return "", err
	}
	wait := func(contextID string) error {
		t.Helper()
		select {
		case got := <-done:
			if got != contextID {
				t.Fatalf("%s ended, not %s", got, contextID)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("the agent of %s was not stopped", contextID)
		}
		mu.Lock()
		defer mu.Unlock()
		return stopped[contextID]
	}
	p := to{terra.client(), luna}

	// The peer stops waiting for the result.
	ctx, stop := context.WithCancel(context.Background())
	failed := make(chan error, 1)
	go func() {
		_, err := p.ask(ctx, "working", "work")
		failed <- err
	}()
	<-started
	stop()
	if err := wait("working"); !errors.Is(err, context.Canceled) || !errors.Is(<-failed, context.Canceled) {
		t.Fatalf("stopped with %v", err)
	}

	// The peer stops waiting for what follows its answer.
	asked, err := p.ask(context.Background(), "answering", "ask")
	if err != nil || asked.Question == "" {
		t.Fatalf("%+v, %v", asked, err)
	}
	ctx, stop = context.WithCancel(context.Background())
	go func() {
		_, err := p.answer(ctx, "answering", asked.Question, "this")
		failed <- err
	}()
	for answered := false; !answered; time.Sleep(time.Millisecond) {
		luna.server.dialogs.mu.Lock()
		d := luna.server.dialogs.byContext[key(terra.id.Public(), "answering")]
		luna.server.dialogs.mu.Unlock()
		d.mu.Lock()
		answered = d.open == nil && d.waiting == 1
		d.mu.Unlock()
	}
	stop()
	if err := wait("answering"); err != nil || !errors.Is(<-failed, context.Canceled) {
		t.Fatalf("stopped after its answer with %v", err)
	}

	// The peer gives up the question.
	asked, err = p.ask(context.Background(), "asking", "ask")
	if err != nil || asked.Question == "" {
		t.Fatalf("%+v, %v", asked, err)
	}
	task := NewTask()
	if err := p.c.GiveUp(WithTask(context.Background(), task), luna.srv.URL, luna.id.Public(), "asking", asked.Question); err != nil {
		t.Fatal(err)
	}
	if err := wait("asking"); !errors.Is(err, ErrGaveUp) || task.Budget().Left != MaxExchanges {
		t.Fatalf("given up: %v, %d left", err, task.Budget().Left)
	}
	if err := p.c.GiveUp(context.Background(), luna.srv.URL, luna.id.Public(), "asking", asked.Question); !IsNoQuestion(err) {
		t.Fatalf("given up twice: %v", err)
	}

	// Who cannot answer a question gives it up, and is told what was asked.
	got, err := p.c.Send(context.Background(), luna.srv.URL, luna.id.Public(), "plain", "ask")
	if err != nil || !strings.HasPrefix(got, "which?\n\n[This is a question back") {
		t.Fatalf("%q, %v", got, err)
	}
	if err := wait("plain"); !errors.Is(err, ErrGaveUp) {
		t.Fatalf("given up: %v", err)
	}

	// Nobody answers: the agent stops waiting, and an answer that comes
	// later finds no question.
	was := QuestionTime
	defer func() { QuestionTime = was }()
	QuestionTime = 20 * time.Millisecond
	luna.respond = func(ctx context.Context, _, _ string) (string, error) {
		_, err := AskBack(ctx, "which?")
		done <- "late"
		return "nobody answered", err
	}
	asked, _ = p.ask(context.Background(), "late", "ask")
	<-done
	if _, err := p.answer(context.Background(), "late", asked.Question, "this"); !IsNoQuestion(err) {
		t.Fatalf("an answer that is late: %v", err)
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(time.Millisecond) {
		if failed := luna.failures(); len(failed) > 0 && errors.Is(failed[len(failed)-1].Err, ErrNoAnswer) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("failed %+v", luna.failures())
		}
	}

	// The node stops: so do the agents that work for peers, and those that
	// wait for an answer.
	QuestionTime = was
	asking := make(chan error, 1)
	luna.respond = func(ctx context.Context, _, text string) (string, error) {
		if text == "ask" {
			_, err := AskBack(ctx, "which?")
			asking <- err
			return "", err
		}
		started <- struct{}{}
		<-ctx.Done()
		done <- "closing"
		return "", ctx.Err()
	}
	go p.ask(context.Background(), "closing", "work")
	<-started
	if asked, err := p.ask(context.Background(), "waiting", "ask"); err != nil || asked.Question == "" {
		t.Fatalf("%+v, %v", asked, err)
	}
	luna.server.Close()
	if got := <-done; got != "closing" {
		t.Fatalf("%s ended", got)
	}
	if err := <-asking; !errors.Is(err, context.Canceled) {
		t.Fatalf("the agent that waited was told %v", err)
	}
}

// An answer that came just before the agent stopped waiting is the answer.
func TestAnswerJustBeforeTheWaitEnds(t *testing.T) {
	a := &dialogs{byQuestion: map[string]*dialog{}}
	d := &dialog{all: a, peer: "p"}
	q := &question{id: "q", answer: make(chan reply, 1)}
	q.answer <- reply{text: "in time"}
	a.unanswered(d, q, ErrNoAnswer)
	if got := <-q.answer; got.text != "in time" || got.err != nil {
		t.Fatalf("%+v", got)
	}
	d.open, a.byQuestion[key("p", "q")] = q, d
	a.unanswered(d, q, ErrNoAnswer)
	if got := <-q.answer; got.err != ErrNoAnswer || d.open != nil || len(a.byQuestion) != 0 {
		t.Fatalf("%+v", got)
	}
}

// A request that is delivered twice has two that wait for its answer: one
// of them leaving does not stop the agent the other waits for.
func TestOneOfTwoWaitersLeaves(t *testing.T) {
	terra, luna := duo(t)
	gate, started := make(chan struct{}), make(chan struct{}, 1)
	luna.respond = func(ctx context.Context, _, _ string) (string, error) {
		started <- struct{}{}
		select {
		case <-gate:
			return "done", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	c := terra.client()
	request := NewMessage("user", "c1", "work")
	send := func(ctx context.Context) (string, error) {
		res, err := c.call(ctx, luna.srv.URL, luna.id.Public(), MethodSend, sendParams{request, Peer{}, 0})
		if err != nil {
			return "", err
		}
		return res.Result.Text(), nil
	}
	answers := make(chan string, 1)
	go func() {
		got, _ := send(context.Background())
		answers <- got
	}()
	<-started
	leaving, leave := context.WithCancel(context.Background())
	left := make(chan error, 1)
	go func() {
		_, err := send(leaving)
		left <- err
	}()
	// Both wait, then one leaves.
	for waiting := 0; waiting < 2; {
		luna.server.dialogs.mu.Lock()
		d := luna.server.dialogs.byContext[key(terra.id.Public(), "c1")]
		luna.server.dialogs.mu.Unlock()
		d.mu.Lock()
		waiting = d.waiting
		d.mu.Unlock()
		time.Sleep(time.Millisecond)
	}
	leave()
	if err := <-left; !errors.Is(err, context.Canceled) {
		t.Fatalf("left with %v", err)
	}
	time.Sleep(20 * time.Millisecond) // the node notices that it left
	close(gate)
	if got := <-answers; got != "done" {
		t.Fatalf("the one that stayed got %q", got)
	}

	// When both leave, one after the other, nobody waits: the agent stops.
	stopped := make(chan struct{})
	luna.respond = func(ctx context.Context, _, _ string) (string, error) {
		started <- struct{}{}
		<-ctx.Done()
		close(stopped)
		return "", ctx.Err()
	}
	request = NewMessage("user", "c2", "work")
	var leaves []context.CancelFunc
	for range 2 {
		ctx, leave := context.WithCancel(context.Background())
		leaves = append(leaves, leave)
		go send(ctx)
	}
	<-started
	for waiting := 0; waiting < 2; {
		luna.server.dialogs.mu.Lock()
		d := luna.server.dialogs.byContext[key(terra.id.Public(), "c2")]
		luna.server.dialogs.mu.Unlock()
		d.mu.Lock()
		waiting = d.waiting
		d.mu.Unlock()
		time.Sleep(time.Millisecond)
	}
	leaves[0]()
	select {
	case <-stopped:
		t.Fatal("the agent was stopped while one still waited")
	case <-time.After(30 * time.Millisecond):
	}
	leaves[1]()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("nobody waits, and the agent goes on")
	}
}
