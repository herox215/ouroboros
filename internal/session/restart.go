package session

import (
	"context"
	"slices"
	"time"

	"ouroboros/internal/presentation"
)

// processInstance is initialized once per executable image, including exec of
// the same binary with the same PID. All Services/profiles in this image share it.
var processInstance = presentation.NewID()

type restartKey struct{}

// WithRestart returns ctx in which a tool can ask for ouro to start anew
// from a new build: restart is told the binary to start, see AskRestart. The
// Service hands such a ctx to the answers of a node that can take its own
// place.
func WithRestart(ctx context.Context, restart func(exe string)) context.Context {
	return context.WithValue(ctx, restartKey{}, restart)
}

type goOnKey struct{}

// WithGoOn returns ctx in which a tool can ask for the conversation to go on
// by itself once ouro started anew: goOn is called, see AskGoOn.
func WithGoOn(ctx context.Context, goOn func()) context.Context {
	return context.WithValue(ctx, goOnKey{}, goOn)
}

// AskGoOn asks whoever holds the conversation of ctx to go on with it by
// itself once ouro started anew from the build AskRestart asked for, as if
// the owner said to go on. It reports false if nobody does that.
func AskGoOn(ctx context.Context) bool {
	goOn, ok := ctx.Value(goOnKey{}).(func())
	if ok {
		goOn()
	}
	return ok
}

// AskRestart asks whoever holds the conversation of ctx to start exe in place of
// the running ouro once the answer is given, and to go on with the
// conversation there. It reports false if nobody does that, like a peer.
func AskRestart(ctx context.Context, exe string) bool {
	restart, ok := ctx.Value(restartKey{}).(func(string))
	if ok {
		restart(exe)
	}
	return ok
}

// restartGrace lets answers finish and pending questions receive replies before
// cancelling work that would otherwise keep the new build waiting indefinitely.
const restartGrace = time.Minute

const restartQuestionHead = "[Not from the owner: a restart interrupted this unanswered question. Ask it again with user_question before continuing; no permission was granted.]\n"

const restartThreadQuestionHead = "[Resume this pending question in the shared thread.]\n" + restartQuestionHead

// scheduleRestart is called once, with p.mu held. The timer never takes a
// service lock while holding p.mu: the normal order is service, then process.
func (p *process) scheduleRestart() {
	wait := p.restartWait
	if wait == 0 {
		wait = restartGrace
	}
	p.restartTimer = time.AfterFunc(wait, func() {
		for _, s := range p.services {
			s.mu.Lock()
			p.mu.Lock()
			pending := p.successor != "" && p.replace != nil
			p.mu.Unlock()
			if pending && !s.closed {
				for _, se := range s.sessions {
					// A task keeps the node waiting as an answer does, and ends
					// with this image anyway: stop it now, so a long task does
					// not hold the new build back until it is done.
					for _, t := range se.tasks {
						stopTask(t)
					}
					if se.cancel == nil {
						continue
					}
					if se.asked != nil && !se.backendQuestion {
						for i := len(se.events) - 1; i >= 0; i-- {
							if e := se.events[i]; e.Kind == Asked && !e.Done {
								se.parked = slices.Clone(e.Args)
								se.parkedRouted = forThread(se)
								break
							}
						}
					}
					se.cancel()
				}
			}
			s.mu.Unlock()
		}
	})
}
