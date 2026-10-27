package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"ouroboros/internal/agent"
	"ouroboros/internal/chat"
	"ouroboros/internal/config"
)

// The owner may list fallbacks: accesses of the node, each with a model, that
// the agent goes on with, in their order, when the access it answers with
// cannot answer for now, see chat.IsUnavailable. Nothing is switched that
// is not listed, and only for the owner: a peer is answered through the
// access chosen for peers or not at all.
//
// A fallback is not sent what the model before it thought, and what it
// thought is not kept: every provider has a form of its own for that. The
// call says that a fallback answered, and why, see agent.Call.

// retryFirst is how long the agent goes on with a fallback that answered
// before it asks the access before it again.
const retryFirst = 5 * time.Minute

// link is one of the accesses a call may be answered through, with its
// model and what it is called.
type link struct {
	llm          agent.LLM
	model, label string
}

// fallbacks is the LLM of an agent with fallbacks: links[0] is what it
// answers with, the others what it goes on with.
type fallbacks struct {
	links []link
	now   func() time.Time

	mu    sync.Mutex
	from  int       // the link that answers until until
	until time.Time // when the links before it are asked again
	why   string    // why they could not answer
}

// Complete answers through the first link that can: one that cannot answer
// for now hands on to the next. A fallback that answered goes on answering
// for retryFirst, so that not every call waits for an access that is down.
func (f *fallbacks) Complete(ctx context.Context, _ string, msgs []chat.Message, tools []chat.Tool) (chat.Message, error) {
	from, why := f.start()
	var first error
	for i := from; ; i++ {
		l := f.links[i]
		sent := msgs
		if i > 0 {
			sent = agent.Thoughtless(msgs)
		}
		msg, err := l.llm.Complete(ctx, l.model, sent, tools)
		if err == nil {
			if i > 0 {
				if first != nil {
					why = first.Error()
				}
				msg.Reasoning, msg.ReasoningField = "", ""
				msg.Via, msg.Instead = l.model+" of "+l.label, why
			}
			f.stick(i, why)
			return msg, nil
		}
		first = cmp.Or(first, err)
		if i == len(f.links)-1 || ctx.Err() != nil || !chat.IsUnavailable(err) {
			f.stick(0, "")
			if i > from {
				return msg, fmt.Errorf("%w; the fallback %s of %s did not answer either: %v", first, l.model, l.label, err)
			}
			return msg, err
		}
	}
}

// start returns the link the next call starts with, and why not with one
// before it.
func (f *fallbacks) start() (int, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.now().Before(f.until) {
		return f.from, f.why
	}
	return 0, ""
}

// stick makes the link i answer the next calls, for retryFirst if it is a
// fallback, as the links before it could not for why.
func (f *fallbacks) stick(i int, why string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if now := f.now(); i == 0 {
		f.from, f.why, f.until = 0, "", time.Time{}
	} else if i != f.from || !now.Before(f.until) {
		f.from, f.why, f.until = i, why, now.Add(retryFirst)
	}
}

// withFallbacks returns first, the LLM of the access of cfg, with the
// fallbacks cfg lists behind it. A fallback whose access is gone, or whose
// provider is not part of this ouro, is left out.
//
// Every access asks again when a call broke off on its way, before the next
// one is asked: see retrying.
func (b Builder) withFallbacks(first agent.LLM, cfg config.Config) agent.LLM {
	first = retrying{first, wait}
	links := []link{{first, cfg.Model, b.label(cfg.Access)}}
	for _, f := range cfg.Fallbacks {
		if llm, err := b.LLM(f.Access); err == nil {
			links = append(links, link{retrying{llm, wait}, f.Model, b.label(f.Access)})
		}
	}
	if len(links) == 1 {
		return first
	}
	return &fallbacks{links: links, now: time.Now}
}

// label is what the access id is called where it is shown: its ID without
// Accesses.
func (b Builder) label(id string) string {
	if b.Accesses == nil {
		return id
	}
	return b.Accesses.Label(id)
}

// fallbacksOf tells the fallbacks of cfg apart from what they were: the
// accesses, as they are signed in to, and their models.
func (b Builder) fallbacksOf(cfg config.Config) string {
	var list [][3]string
	for _, f := range cfg.Fallbacks {
		list = append(list, [3]string{f.Access, b.revision(f.Access), f.Model})
	}
	told, _ := json.Marshal(list) // texts: cannot fail
	return string(told)
}

// retryAfter are how long an access waits before it asks again, once for
// each: a call that broke off on its way is likely answered when it is
// asked again, see chat.IsTransient. A connection a gateway closed is
// open again at once; one that is overloaded needs a moment.
var retryAfter = []time.Duration{time.Second, 4 * time.Second}

// retrying is an LLM that asks again when a call broke off on its way,
// after each of retryAfter, as long as ctx goes on. An answer says why it
// was asked again, see chat.Message; the error of a call that was never
// answered says how often it was asked.
type retrying struct {
	agent.LLM
	wait func(ctx context.Context, d time.Duration) error
}

func (r retrying) Complete(ctx context.Context, model string, msgs []chat.Message, tools []chat.Tool) (chat.Message, error) {
	msg, err := r.LLM.Complete(ctx, model, msgs, tools)
	var why []string
	for _, d := range retryAfter {
		if !chat.IsTransient(err) || r.wait(ctx, d) != nil {
			break
		}
		why = append(why, err.Error())
		msg, err = r.LLM.Complete(ctx, model, msgs, tools)
	}
	if err != nil && len(why) > 0 {
		return msg, fmt.Errorf("%w (asked %d times, as it broke off)", err, len(why)+1)
	}
	msg.Retried = append(why, msg.Retried...)
	return msg, err
}

// wait waits for d, or until ctx ends, with its error.
func wait(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
