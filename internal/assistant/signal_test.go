package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"ouroboros/internal/agent"
)

// signalled is what the record of an answer logged as its signals, after
// steps and calls were noted and it ended with err: "" without any.
func signalled(t *testing.T, err error, note func(r *record)) string {
	t.Helper()
	var lines []string
	r := newRecord(context.Background(), func(f string, a ...any) { lines = append(lines, fmt.Sprintf(f, a...)) }, "c", []string{"sk-secret"}, "hi", func() {})
	note(r)
	r.end("ok", err)
	for _, line := range lines {
		if s, ok := strings.CutPrefix(line, "chat c: signals: "); ok {
			return s
		}
	}
	return ""
}

// done notes a call of tool with args that ended after took.
func done(r *record, tool, args string, took time.Duration, err error) {
	r.step(agent.Step{Tool: tool, Args: json.RawMessage(args)})
	r.step(agent.Step{Tool: tool, Args: json.RawMessage(args), Done: true, Took: took, Err: err})
}

// An answer that went well has no signals, and the log no line for them.
func TestNoSignals(t *testing.T) {
	got := signalled(t, nil, func(r *record) {
		r.call(agent.Call{})
		r.call(agent.Call{Done: true, ToolCalls: 1})
		done(r, "bash", `{"command":"ls"}`, time.Second, nil)
		done(r, "bash", `{"command":"ls"}`, time.Second, errors.New("no"))
		r.call(agent.Call{})
		r.call(agent.Call{Done: true})
	})
	if got != "" {
		t.Fatalf("signals of an answer that went well: %s", got)
	}
}

// Each signal is logged in one line before the end, in the order of the
// kinds, without secrets.
func TestSignalsAreLogged(t *testing.T) {
	got := signalled(t, agent.ErrTooManyToolCalls, func(r *record) {
		for range manyCalls {
			r.call(agent.Call{})
			r.call(agent.Call{Done: true, ToolCalls: 1, Tokens: cacheTokens, Cached: cacheTokens / 10})
		}
		r.call(agent.Call{})
		r.call(agent.Call{Done: true, Retried: "EOF; EOF", Via: "other", Instead: "down"})
		r.call(agent.Call{})
		r.call(agent.Call{Done: true, Retried: "EOF"})
		done(r, "read", `{"path":"a"}`, slowTool, nil)
		done(r, "read", `{"path":"b"}`, 2*slowTool, nil)
		done(r, "bash", `{"command":"echo sk-secret"}`, time.Second, errors.New("x"))
		done(r, "bash", `{"command":"echo sk-secret"}`, time.Second, errors.New("x"))
		done(r, "bash", `{"command":"echo sk-secret"}`, time.Second, errors.New("x"))
	})
	want := `read took 1m0s; 3 tool calls failed; bash ran 3 times with {"command":"echo [redacted]"}; ` +
		`17 calls of the LLM; 2 calls of the LLM broke off and asked again; 1 call of the LLM answered by a fallback; ` +
		`10% of the tokens were cached; the answer gave up after too many calls`
	if got != want {
		t.Fatalf("signals:\n%s\nwant:\n%s", got, want)
	}
}

// A tool that waits for someone else is not slow, and what it waits is not
// counted to the answer; a script is as slow as the tools it calls.
func TestWaitsAreNoSignals(t *testing.T) {
	for _, tool := range waiting {
		if got := signalled(t, nil, func(r *record) { done(r, tool, `{}`, time.Hour, nil) }); got != "" {
			t.Errorf("%s: %s", tool, got)
		}
	}
	if got := signalled(t, nil, func(r *record) { done(r, toolRun, `{}`, time.Hour, nil) }); got != "" {
		t.Errorf("%s: %s", toolRun, got)
	}
}

// A script is counted by the tools it calls: a tool that failed in it is
// one failure, and the script failed only by itself when none of them did.
func TestScriptsAreCountedByTheirTools(t *testing.T) {
	inScript := func(r *record, tool, args string, err error) {
		script := `{"script":"` + tool + `()"}`
		r.step(agent.Step{Tool: toolRun, Args: json.RawMessage(script)})
		if tool != "" {
			done(r, tool, args, time.Second, err)
		}
		r.step(agent.Step{Tool: toolRun, Args: json.RawMessage(script), Done: true, Took: time.Second, Err: err})
	}
	got := signalled(t, nil, func(r *record) {
		inScript(r, "read", `{"path":"a"}`, errors.New("no"))
		inScript(r, "read", `{"path":"b"}`, errors.New("no"))
		inScript(r, "read", `{"path":"a"}`, nil)
	})
	if got != "" {
		t.Fatalf("two tools failed in scripts: %s", got)
	}
	got = signalled(t, nil, func(r *record) {
		inScript(r, "read", `{"path":"a"}`, errors.New("no"))
		inScript(r, "", "", errors.New("syntax"))
		inScript(r, "read", `{"path":"a"}`, errors.New("no"))
	})
	if got != "3 tool calls failed" {
		t.Fatalf("a script that failed by itself: %s", got)
	}
}

// A script that left out of its result what the agent has to get with
// another step is a signal, with how much it left out.
func TestCutIsASignal(t *testing.T) {
	got := signalled(t, nil, func(r *record) {
		for _, result := range []string{"x\n(2048 more bytes were printed and left out. All of it is kept", "fine",
			"y\n(the value has 5120 bytes, of which the first 4096 came back. It is kept"} {
			r.step(agent.Step{Tool: toolRun, Args: json.RawMessage(`{}`)})
			r.step(agent.Step{Tool: toolRun, Args: json.RawMessage(`{}`), Done: true, Result: result})
		}
	})
	if got != "2 results of scripts were cut: 3.0 KiB left out" {
		t.Fatalf("signals: %s", got)
	}
	got = signalled(t, nil, func(r *record) {
		r.step(agent.Step{Tool: toolRun, Args: json.RawMessage(`{}`)})
		r.step(agent.Step{Tool: toolRun, Args: json.RawMessage(`{}`), Done: true, Result: "(512 more bytes were printed and left out. "})
	})
	if got != "1 result of a script was cut: 0.5 KiB left out" {
		t.Fatalf("signals: %s", got)
	}
}

// An answer that failed for the provider or was stopped is no signal.
func TestOtherErrorsAreNoSignals(t *testing.T) {
	for _, err := range []error{errors.New("HTTP 500"), context.Canceled} {
		if got := signalled(t, err, func(*record) {}); got != "" {
			t.Errorf("%v: %s", err, got)
		}
	}
}

// An answer without words is a signal, one that failed has none to have.
func TestEmptyAnswer(t *testing.T) {
	var m measures
	if got := m.signals(0, 1, 0, 0, " \n", nil); len(got) != 1 || got[0].Kind != SignalEmpty {
		t.Fatalf("empty: %v", got)
	}
	if got := m.signals(0, 1, 0, 0, "", errors.New("HTTP 500")); len(got) != 0 {
		t.Fatalf("failed: %v", got)
	}
}

// What the answer worked, without the waits, makes it slow.
func TestSlowAnswer(t *testing.T) {
	m := measures{waited: time.Minute}
	got := m.signals(slowAnswer+time.Minute, 1, 0, 0, "ok", nil)
	if len(got) != 1 || got[0].Kind != SignalSlowAnswer || got[0].Text != "the answer took 3m0s" {
		t.Fatalf("slow answer: %v", got)
	}
	if got := m.signals(slowAnswer+time.Minute-time.Second, 1, 0, 0, "ok", nil); len(got) != 0 {
		t.Fatalf("an answer that waited: %v", got)
	}
}

// The cache is judged only after a few calls, as the first finds nothing
// cached.
func TestLowCacheNeedsCalls(t *testing.T) {
	var m measures
	if got := m.signals(0, cacheCalls-1, cacheTokens, 0, "ok", nil); len(got) != 0 {
		t.Fatalf("too few calls: %v", got)
	}
	if got := m.signals(0, cacheCalls, cacheTokens-1, 0, "ok", nil); len(got) != 0 {
		t.Fatalf("too few tokens: %v", got)
	}
	if got := m.signals(0, cacheCalls, cacheTokens, cacheTokens*lowCache/100, "ok", nil); len(got) != 0 {
		t.Fatalf("cached enough: %v", got)
	}
	if got := m.signals(0, cacheCalls, cacheTokens, 0, "ok", nil); len(got) != 1 || got[0].Kind != SignalLowCache {
		t.Fatalf("cached nothing: %v", got)
	}
	if got := m.signals(0, cacheCalls, 0, 0, "ok", nil); len(got) != 0 {
		t.Fatalf("no tokens told: %v", got)
	}
}
