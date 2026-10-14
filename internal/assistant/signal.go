package assistant

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"ouroboros/internal/agent"
	"ouroboros/internal/script"
)

// Signal is a sign, measured on the way to an answer, that ouro could have
// done it better: a tool that was slow, calls that failed or were repeated,
// many calls of the LLM. It is no judgement, only what the numbers say; what
// would be better is for the agent to tell.
type Signal struct {
	Kind SignalKind
	Text string // what was measured, like "bash took 42s"
}

// SignalKind is what a Signal is of.
type SignalKind string

const (
	SignalSlowTool    SignalKind = "slow tool"
	SignalSlowAnswer  SignalKind = "slow answer"
	SignalFailedTools SignalKind = "failed tools"
	SignalRepeated    SignalKind = "repeated call"
	SignalManyCalls   SignalKind = "many calls"
	SignalRetried     SignalKind = "retried"
	SignalFallback    SignalKind = "fallback"
	SignalLowCache    SignalKind = "low cache"
	SignalGaveUp      SignalKind = "gave up"
	SignalEmpty       SignalKind = "empty answer"
	SignalCut         SignalKind = "cut"
	// SignalFollowUp is of the message after the answer, see followed.
	SignalFollowUp SignalKind = "follow-up"
)

// What makes a Signal.
const (
	slowTool      = 30 * time.Second // a call of one tool
	slowAnswer    = 3 * time.Minute  // an answer, without the waits of waiting
	failedTools   = 3                // tool calls of an answer that failed
	repeatedCalls = 3                // calls of a tool with the same arguments
	manyCalls     = 15               // calls of the LLM for an answer
	lowCache      = 50               // percent of the tokens cached, below which it is low
	cacheCalls    = 3                // calls of the LLM before the cache is judged
	cacheTokens   = 20000            // tokens sent before the cache is judged: a provider caches from a size on
)

// waiting are the tools that wait for someone else, not for ouro: how long
// they take says nothing of ouro, and is not counted to the answer.
var waiting = []string{ToolUserQuestion, toolAskPeer, toolAskBack}

// measures is what the record of an answer keeps for its signals.
type measures struct {
	slowest  map[string]time.Duration // of each tool slower than slowTool
	waited   time.Duration            // in the tools of waiting
	failed   int                      // tool calls
	scripts  []int                    // failed when each script under way started
	cut      int                      // results of scripts that left something out
	leftOut  int                      // bytes they left out
	same     map[string]int           // calls by tool and arguments
	retried  int                      // calls of the LLM asked again
	fallback int                      // calls of the LLM a fallback answered
}

// result notes what a script gave back: what it left out of it, see
// script.LeftOut, which the agent has to go and get with another step.
func (m *measures) result(text string) {
	if n := script.LeftOut(text); n > 0 {
		m.cut++
		m.leftOut += n
	}
}

// start notes a tool call that starts.
func (m *measures) start(tool string) {
	if tool == toolRun {
		m.scripts = append(m.scripts, m.failed)
	}
}

// step notes a tool call that ended. A script is noted by the tools it
// calls: it is slow, failed or repeated as they are, and failed by itself
// only when none of them did, as when it could not be run.
func (m *measures) step(tool, args string, took time.Duration, failed bool) {
	if tool == toolRun {
		before := m.failed
		if n := len(m.scripts); n > 0 {
			before, m.scripts = m.scripts[n-1], m.scripts[:n-1]
		}
		if failed && m.failed == before {
			m.failed++
		}
		return
	}
	if slices.Contains(waiting, tool) {
		m.waited += took
	} else if took >= slowTool {
		if m.slowest == nil {
			m.slowest = map[string]time.Duration{}
		}
		m.slowest[tool] = max(m.slowest[tool], took)
	}
	if failed {
		m.failed++
	}
	if m.same == nil {
		m.same = map[string]int{}
	}
	m.same[tool+" "+args]++
}

// signals are what the measures of an answer say, together with how long it
// took, the calls of the LLM, the tokens sent and cached, the reply, and err,
// if it failed: in the order of the kinds, each in the order of its subject. Of the
// errors only giving up after too many calls is one: the others are of the
// provider or of the network, or the answer was stopped.
func (m *measures) signals(took time.Duration, llm, tokens, cached int, reply string, err error) []Signal {
	var signals []Signal
	add := func(kind SignalKind, format string, args ...any) {
		signals = append(signals, Signal{kind, fmt.Sprintf(format, args...)})
	}
	for _, tool := range slices.Sorted(maps.Keys(m.slowest)) {
		add(SignalSlowTool, "%s took %s", tool, roughly(m.slowest[tool]))
	}
	if worked := took - m.waited; worked >= slowAnswer {
		add(SignalSlowAnswer, "the answer took %s", roughly(worked))
	}
	if m.failed >= failedTools {
		add(SignalFailedTools, "%d tool calls failed", m.failed)
	}
	for _, call := range slices.Sorted(maps.Keys(m.same)) {
		if n := m.same[call]; n >= repeatedCalls {
			tool, args, _ := strings.Cut(call, " ")
			add(SignalRepeated, "%s ran %d times with %s", tool, n, args)
		}
	}
	if m.cut > 0 {
		results := "1 result of a script was"
		if m.cut > 1 {
			results = fmt.Sprintf("%d results of scripts were", m.cut)
		}
		add(SignalCut, "%s cut: %.1f KiB left out", results, float64(m.leftOut)/1024)
	}
	if llm >= manyCalls {
		add(SignalManyCalls, "%d calls of the LLM", llm)
	}
	if m.retried > 0 {
		add(SignalRetried, "%s of the LLM broke off and asked again", calls(m.retried))
	}
	if m.fallback > 0 {
		add(SignalFallback, "%s of the LLM answered by a fallback", calls(m.fallback))
	}
	if llm >= cacheCalls && tokens >= cacheTokens && cached*100/tokens < lowCache {
		add(SignalLowCache, "%d%% of the tokens were cached", cached*100/tokens)
	}
	if errors.Is(err, agent.ErrTooManyToolCalls) {
		add(SignalGaveUp, "the answer gave up after too many calls")
	}
	if err == nil && strings.TrimSpace(reply) == "" {
		add(SignalEmpty, "the answer was empty")
	}
	return signals
}

// roughly is d to the second, as a signal tells it.
func roughly(d time.Duration) string { return d.Round(time.Second).String() }

// calls is "1 call" or "n calls".
func calls(n int) string {
	if n == 1 {
		return "1 call"
	}
	return fmt.Sprintf("%d calls", n)
}
