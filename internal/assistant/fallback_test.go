package assistant

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"ouroboros/internal/agent"
	"ouroboros/internal/config"
	"ouroboros/internal/identity"
	"ouroboros/internal/openai"
)

// accessLLM is the LLM of an access of a test: it fails with err while it has
// one, and else answers "MODEL of NAME" with a thought.
type accessLLM struct {
	name  string
	err   error
	asked int
	sent  []openai.Message // with the last call
}

func (l *accessLLM) Complete(_ context.Context, model string, msgs []openai.Message, _ []openai.Tool) (openai.Message, error) {
	l.asked++
	l.sent = msgs
	if l.err != nil {
		return openai.Message{}, l.err
	}
	return openai.Message{Content: model + " of " + l.name, Reasoning: "hm", ReasoningField: "reasoning"}, nil
}

var (
	errQuota = &openai.APIError{Status: 429, Message: "quota"}
	errDown  = &openai.APIError{Status: 503, Message: "down"}
	errAsked = &openai.APIError{Status: 400, Message: "too long"}
)

// chain is the LLM of an agent that answers with "big" of a, then "mid" of
// b, then "small" of c, at the time *now.
func chain(now *time.Time, a, b, c *accessLLM) *fallbacks {
	return &fallbacks{links: []link{{a, "big", "A"}, {b, "mid", "B"}, {c, "small", "C"}}, now: func() time.Time { return *now }}
}

// A call the access cannot answer for now is answered through the first of
// the fallbacks that can, which says so, and that one goes on answering for a
// while: then the access before it is asked again.
func TestFallbacksAnswerInTheirOrder(t *testing.T) {
	now := time.Unix(1000, 0)
	a, b, c := &accessLLM{name: "A", err: errQuota}, &accessLLM{name: "B", err: errDown}, &accessLLM{name: "C"}
	f := chain(&now, a, b, c)
	thought := []openai.Message{{Role: "user", Content: "hi"}, {Role: "assistant", Content: "ho", Reasoning: "of A"}, {Role: "user", Content: "go on"}}
	ask := func() openai.Message {
		t.Helper()
		msg, err := f.Complete(context.Background(), "big", thought, nil)
		if err != nil {
			t.Fatal(err)
		}
		return msg
	}
	msg := ask()
	if msg.Content != "small of C" || msg.Via != "small of C" || msg.Instead != errQuota.Error() || msg.Reasoning != "" || msg.ReasoningField != "" {
		t.Fatalf("answered %+v", msg)
	}
	if a.asked != 1 || b.asked != 1 || c.asked != 1 || a.sent[1].Reasoning != "of A" || b.sent[1].Reasoning != "" || c.sent[1].Reasoning != "" || thought[1].Reasoning != "of A" {
		t.Fatalf("asked %d %d %d, sent %+v and %+v", a.asked, b.asked, c.asked, a.sent, c.sent)
	}
	// It goes on answering, and the others are left alone.
	now = now.Add(retryFirst - time.Second)
	if msg := ask(); msg.Via != "small of C" || msg.Instead != errQuota.Error() || a.asked != 1 || b.asked != 1 || c.asked != 2 {
		t.Fatalf("answered %+v, asked %d %d %d", msg, a.asked, b.asked, c.asked)
	}
	// Then they are asked again, and as they still cannot, left alone again:
	// from now on, not from when they first could not.
	now = now.Add(time.Second)
	b.err = errQuota
	if msg := ask(); msg.Via != "small of C" || a.asked != 2 || b.asked != 2 || c.asked != 3 {
		t.Fatalf("answered %+v, asked %d %d %d", msg, a.asked, b.asked, c.asked)
	}
	now = now.Add(retryFirst - time.Second)
	if ask(); a.asked != 2 || c.asked != 4 {
		t.Fatalf("asked %d %d %d", a.asked, b.asked, c.asked)
	}
	// One before the fallback can again: it answers, and says why the first
	// cannot.
	now = now.Add(time.Second)
	b.err = nil
	if msg := ask(); msg.Content != "mid of B" || msg.Via != "mid of B" || msg.Instead != errQuota.Error() || c.asked != 4 {
		t.Fatalf("answered %+v, asked %d %d %d", msg, a.asked, b.asked, c.asked)
	}
	// The access itself can again: it answers as if nothing was, with what
	// it thinks, and at once the next time too.
	now = now.Add(retryFirst)
	a.err = nil
	for range 2 {
		if msg := ask(); msg.Content != "big of A" || msg.Via != "" || msg.Instead != "" || msg.Reasoning != "hm" || b.asked != 3 {
			t.Fatalf("answered %+v, asked %d %d %d", msg, a.asked, b.asked, c.asked)
		}
	}
	if a.asked != 5 {
		t.Fatalf("asked %d", a.asked)
	}
}

// What no fallback helps with is told as it is: an error of what was asked,
// a call that was stopped, and fallbacks that cannot answer either.
func TestFallbacksThatDoNotAnswer(t *testing.T) {
	now := time.Unix(1000, 0)
	ctx := context.Background()
	a, b, c := &accessLLM{name: "A", err: errAsked}, &accessLLM{name: "B"}, &accessLLM{name: "C"}
	f := chain(&now, a, b, c)
	if _, err := f.Complete(ctx, "big", nil, nil); err != errAsked || b.asked != 0 {
		t.Fatalf("an error of what was asked: %v, asked %d", err, b.asked)
	}
	a.err = errQuota
	stopped, stop := context.WithCancel(ctx)
	stop()
	if _, err := f.Complete(stopped, "big", nil, nil); err != errQuota || b.asked != 0 {
		t.Fatalf("a call that was stopped: %v, asked %d", err, b.asked)
	}
	// The fallback fails with what was asked: the next is not tried.
	b.err = errAsked
	_, err := f.Complete(ctx, "big", nil, nil)
	if !errors.Is(err, errQuota) || err.Error() != "openai: HTTP 429: quota; the fallback mid of B did not answer either: openai: HTTP 400: too long" || c.asked != 0 {
		t.Fatalf("a fallback that fails: %v, asked %d", err, c.asked)
	}
	// None can for now: the first says why, and the last.
	b.err, c.err = errDown, errDown
	_, err = f.Complete(ctx, "big", nil, nil)
	if !errors.Is(err, errQuota) || !strings.HasSuffix(err.Error(), "; the fallback small of C did not answer either: openai: HTTP 503: down") {
		t.Fatalf("no fallback answers: %v", err)
	}
	// A fallback that answered and cannot any more hands on, and when none
	// is left the access itself is asked first again.
	b.err, c.err = nil, nil
	if msg, err := f.Complete(ctx, "big", nil, nil); err != nil || msg.Via != "mid of B" {
		t.Fatalf("answered %+v, %v", msg, err)
	}
	b.err, c.err = errDown, errQuota
	asked := a.asked
	_, err = f.Complete(ctx, "big", nil, nil)
	if !errors.Is(err, errDown) || !strings.HasSuffix(err.Error(), "the fallback small of C did not answer either: openai: HTTP 429: quota") || a.asked != asked {
		t.Fatalf("the fallback that answered fails: %v, asked %d", err, a.asked)
	}
	b.err = nil
	if msg, err := f.Complete(ctx, "big", nil, nil); err != nil || msg.Via != "mid of B" || a.asked != asked+1 {
		t.Fatalf("answered %+v, %v, asked %d", msg, err, a.asked)
	}
}

// The agent of the owner answers through the fallbacks the configuration
// lists, as far as their accesses are there, and is built anew when they
// change. The log and the session say that a fallback answered.
func TestOwnerFallsBack(t *testing.T) {
	llms := map[string]*accessLLM{"a": {name: "A", err: errQuota}, "b": {name: "B"}}
	built := 0
	cfg := config.Config{Access: "a", Model: "big", Name: "me", PrivateKey: identity.Generate().Private(),
		Fallbacks: []config.Fallback{{Access: "gone", Model: "x"}, {Access: "b", Model: "mid"}}}
	log := &logged{}
	b := Builder{
		LLM: func(access string) (agent.LLM, error) {
			built++
			if l, ok := llms[access]; ok {
				return l, nil
			}
			return nil, errors.New("access " + access + ": gone")
		},
		Accesses: &accesses{label: "L"},
		Tools:    func() []agent.Tool { return nil },
	}
	a := New(func() (config.Config, error) { return cfg, nil }, b, log.log)
	ctx := context.Background()
	if got, err := a.Ask(ctx, "c1", "hi", nil); err != nil || got != "mid of B" || built != 3 {
		t.Fatalf("got %q, %v, built %d", got, err, built)
	}
	equal(t, "a fallback answered", log.take(), []string{
		`chat c1: message from the owner: "hi"`,
		`chat c1: llm call 1 took D: text [by the fallback mid of L, as the one before it said "openai: HTTP 429: quota"]`,
		`chat c1: signals: 1 call of the LLM answered by a fallback`,
		`chat c1: answered after D (llm calls: 1, tool calls: 0, sent N): "mid of B"`,
	})
	if via := a.Cache("c1").Metrics.Via; via != "mid of L" {
		t.Fatalf("the session says %q", via)
	}
	if kept := a.memory.Conversation("c1"); len(kept) != 2 || kept[1].Reasoning != "" {
		t.Fatalf("what a fallback thought is not kept: %+v", kept)
	}
	// The same fallbacks leave the agent, others build it anew.
	if a.Ask(ctx, "c1", "hi", nil); built != 3 {
		t.Fatalf("built %d", built)
	}
	cfg.Fallbacks = cfg.Fallbacks[:1]
	if _, err := a.Ask(ctx, "c1", "hi", nil); err != errQuota || built != 5 {
		t.Fatalf("without a fallback that is there: %v, built %d", err, built)
	}
	// Peers are not answered through the fallbacks of the owner.
	cfg.Fallbacks = []config.Fallback{{Access: "b", Model: "mid"}}
	bot, err := b.ForPeers(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bot.Respond(from("luna"), "p1", "hi"); err != errQuota || llms["b"].asked != 2 {
		t.Fatalf("a peer: %v, the fallback was asked %d times", err, llms["b"].asked)
	}
}

// The fallbacks are told apart by their accesses, as they are signed in to,
// and their models; an access is called by its ID where nothing names it.
func TestFallbacksOf(t *testing.T) {
	cfg := config.Config{Fallbacks: []config.Fallback{{Access: "a", Model: "m"}, {Access: "b", Model: "n"}}}
	if got := (Builder{Accesses: &accesses{rev: "7"}}).fallbacksOf(cfg); got != `[["a","7","m"],["b","7","n"]]` {
		t.Fatalf("with accesses: %s", got)
	}
	if got := (Builder{}).fallbacksOf(config.Config{}); got != "null" {
		t.Fatalf("without fallbacks: %s", got)
	}
	if got := (Builder{}).label("a1"); got != "a1" {
		t.Fatalf("label %q", got)
	}
	first := &accessLLM{}
	b := Builder{LLM: func(string) (agent.LLM, error) { return nil, errors.New("gone") }}
	// The access asks again when a call broke off, see retrying.
	if got, ok := b.withFallbacks(first, cfg).(retrying); !ok || got.LLM != agent.LLM(first) {
		t.Fatalf("without a fallback that is there: %T", got)
	}
	b.LLM = func(access string) (agent.LLM, error) { return &accessLLM{name: access}, nil }
	f := b.withFallbacks(first, config.Config{Access: "z", Model: "big", Fallbacks: cfg.Fallbacks}).(*fallbacks)
	var links []string
	for _, l := range f.links {
		links = append(links, fmt.Sprint(l.model, " of ", l.label))
	}
	if !slices.Equal(links, []string{"big of z", "m of a", "n of b"}) || f.now().IsZero() {
		t.Fatalf("links %v", links)
	}
	for _, l := range f.links {
		if _, ok := l.llm.(retrying); !ok {
			t.Errorf("%s of %s does not ask again: %T", l.model, l.label, l.llm)
		}
	}
}

// A conversation is named through the fallbacks too, when the access cannot
// answer for now.
func TestTitleFallsBack(t *testing.T) {
	llms := map[string]*accessLLM{"a": {name: "A", err: errDown}, "b": {name: "B"}}
	cfg := config.Config{Access: "a", Model: "big", Name: "me", Fallbacks: []config.Fallback{{Access: "b", Model: "mid"}}}
	// An access that is down is not asked again here.
	old := retryAfter
	retryAfter = nil
	defer func() { retryAfter = old }()
	a := New(func() (config.Config, error) { return cfg, nil }, Builder{
		LLM:   func(access string) (agent.LLM, error) { return llms[access], nil },
		Tools: func() []agent.Tool { return nil },
	}, discard)
	hi := []openai.Message{{Role: "user", Content: "plan the garden"}}
	if got, err := a.Title(context.Background(), hi); err != nil || got != "mid of B" {
		t.Fatalf("title %q, %v", got, err)
	}
	// One that is asked wrong is told as it is.
	llms["a"].err = errAsked
	if _, err := a.Title(context.Background(), hi); err != errAsked {
		t.Fatalf("asked wrong: %v", err)
	}
}

// A call an adapter gave up on for a stall is handed to the fallback: the
// caller's context is still live. A call the caller itself stopped is not.
func TestFallbacksOnAStall(t *testing.T) {
	now := time.Unix(1000, 0)
	stall := fmt.Errorf("openai: no data from the API: %w", context.DeadlineExceeded)
	a, b, c := &accessLLM{name: "A", err: stall}, &accessLLM{name: "B"}, &accessLLM{name: "C"}
	if msg, err := chain(&now, a, b, c).Complete(context.Background(), "big", nil, nil); err != nil || msg.Content != "mid of B" {
		t.Fatalf("a stall answered %+v, %v", msg, err)
	}
	stopped, cancel := context.WithCancel(context.Background())
	cancel()
	a, b, c = &accessLLM{name: "A", err: stall}, &accessLLM{name: "B"}, &accessLLM{name: "C"}
	if msg, err := chain(&now, a, b, c).Complete(stopped, "big", nil, nil); !errors.Is(err, stall) || b.asked != 0 {
		t.Fatalf("a stopped call answered %+v, %v; B asked %d times", msg, err, b.asked)
	}
}
