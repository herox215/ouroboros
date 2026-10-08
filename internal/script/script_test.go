package script

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
	"time"
)

var errTest = errors.New("test error")

// recorder is a set of tools that record how they were called.
type recorder struct {
	calls []string
	ctx   context.Context // of the last call
}

func (r *recorder) names() []string { return []string{"echo", "fail", "delete", "list_peers"} }

func (r *recorder) call(ctx context.Context, name string, args json.RawMessage) (string, error) {
	r.calls = append(r.calls, name+" "+string(args))
	r.ctx = ctx
	if name == "fail" {
		return "", errTest
	}
	return name + ":" + string(args), nil
}

// run runs source with the tools of a recorder.
func run(t *testing.T, source string) (string, *recorder, error) {
	t.Helper()
	r := &recorder{}
	got, err := Run(context.Background(), source, r.names(), r.call)
	return got, r, err
}

// Of a value beyond MaxResult, its start is handed on, and the value is kept
// in Returned, as it is.
func TestResultBeyondTheLimit(t *testing.T) {
	s, r := NewSession(), &recorder{}
	got, err := s.Run(context.Background(), `var big = {a: "x" + "ä".repeat(`+fmt.Sprint(MaxResult)+`)}; big`, r.names(), r.call)
	if err != nil {
		t.Fatal(err)
	}
	encoded := `{"a":"x` + strings.Repeat("ä", MaxResult) + `"}`
	shown := encoded[:MaxResult-1] // ä has two bytes: a character begins there
	want := shown + "\n(the value has " + fmt.Sprint(len(encoded)) + " bytes, of which the first " + fmt.Sprint(len(shown)) +
		" came back. It is kept in returned: look into it and give back only what you need)\n" +
		"[kept for the next script: big (object with 1 keys), returned (object with 1 keys)]"
	if got != want {
		t.Fatalf("got %q…%q", got[:20], got[len(got)-200:])
	}
	if got, err := s.Run(context.Background(), "returned === big", r.names(), r.call); err != nil || got != "true\n[kept for the next script: big (object with 1 keys), returned (object with 1 keys)]" {
		t.Fatalf("kept: %q", got)
	}
	// Under a tightened context, less is handed on.
	got, err = s.Run(Tighten(context.Background()), `return "x".repeat(`+fmt.Sprint(Tight+1)+`)`, r.names(), r.call)
	if err != nil || !strings.HasPrefix(got, strings.Repeat("x", Tight)+"\n(the value has 4097 bytes, of which the first 4096 came back.") {
		t.Fatalf("tight: %.80q, %v", got, err)
	}
}

// What a script returns goes to the LLM: a string as it is, everything else
// as JSON.
func TestResult(t *testing.T) {
	for source, want := range map[string]string{
		`return "hello"`:                         "hello",
		`return ""`:                              "",
		`return 1 + 1`:                           "2",
		`return {a: [1, "two", null], b: true}`:  `{"a":[1,"two",null],"b":true}`,
		`return [1, 2].map(x => x * 2)`:          "[2,4]",
		`return null`:                            "null",
		`return`:                                 "(the script returned nothing)",
		``:                                       "(the script returned nothing)",
		`var a = 1 // a comment at the end`:      "(the script returned nothing)\n[kept for the next script: a (number)]",
		"const s = `a${1}`; let n = 2\nreturn s": "a1",
		`x = 3; return x`:                        "3\n[kept for the next script: x (number)]", // no strict mode: an LLM forgets a var
		`return new Date(0).toISOString()`:       "1970-01-01T00:00:00.000Z",
		`return JSON.stringify({a: 1})`:          `{"a":1}`,
		`return "x".repeat(` + fmt.Sprint(MaxResult) + `)`: strings.Repeat("x", MaxResult),
	} {
		if got, _, err := run(t, source); err != nil || got != want {
			t.Errorf("%q = %q, %v; want %q", source, got, err, want)
		}
	}
	for source, want := range map[string]string{
		`return function() {}`: "what the script returned is no data: return a string, or what JSON can hold",
		`return Symbol("x")`:   "what the script returned is no data: return a string, or what JSON can hold",
		`return 1n`:            "what the script returned cannot be written as JSON: TypeError: Do not know how to serialize a BigInt",
	} {
		if got, _, err := run(t, source); err == nil || got != "" || !strings.HasPrefix(err.Error(), want) {
			t.Errorf("%q = %q, %v; want the error %q", source, got, err, want)
		}
	}
}

// A tool is a function of its name that takes one object and returns the
// text the LLM would get.
func TestTools(t *testing.T) {
	got, r, err := run(t, `
		var a = echo({text: "one", n: 1})
		var b = tools.echo({text: "two"})
		var c = list_peers()
		var d = tools["delete"]({path: "~/x"})
		return [a, b, c, d, typeof tools.fail, Object.keys(tools).join()]`)
	want := `["echo:{\"text\":\"one\",\"n\":1}","echo:{\"text\":\"two\"}","list_peers:{}","delete:{\"path\":\"~/x\"}","function","echo,fail,delete,list_peers"]`
	if err != nil || got != want {
		t.Fatalf("got %s, %v\nwant %s", got, err, want)
	}
	if calls := strings.Join(r.calls, "\n"); calls != "echo {\"text\":\"one\",\"n\":1}\necho {\"text\":\"two\"}\nlist_peers {}\ndelete {\"path\":\"~/x\"}" {
		t.Fatalf("calls:\n%s", calls)
	}

	// What is no data cannot be handed to a tool.
	for _, source := range []string{`echo(function() {})`, `echo(1n)`, `var a = {}; a.a = a; echo(a)`} {
		got, r, err := run(t, source)
		if err == nil || got != "" || !strings.HasPrefix(err.Error(), "the script failed: Error: echo takes one object with its arguments\n") || len(r.calls) != 0 {
			t.Errorf("%q = %q, %v, calls %v", source, got, err, r.calls)
		}
	}
	// Whatever else it is handed is for the tool to refuse.
	if got, _, err := run(t, `return echo("text") + echo(null)`); err != nil || got != `echo:"text"echo:null` {
		t.Errorf("got %q, %v", got, err)
	}
}

// Nothing but the language and the tools: no way out of the interpreter.
func TestEnvironment(t *testing.T) {
	absent := []string{"require", "module", "process", "setTimeout", "setInterval", "setImmediate", "fetch", "XMLHttpRequest",
		"Deno", "Bun", "global", "window", "load", "readFile", "exports", "importScripts", "WebAssembly"}
	names, _ := json.Marshal(absent)
	got, _, err := run(t, `return `+string(names)+`.filter(function(name) { return typeof globalThis[name] !== "undefined" })`)
	if err != nil || got != "[]" {
		t.Errorf("reachable from a script: %s, %v", got, err)
	}
	got, _, err = run(t, `return Object.getOwnPropertyNames(globalThis).filter(function(n) { return typeof globalThis[n] === "function" && !/^[A-Z]/.test(n) }).sort()`)
	want := `["decodeURI","decodeURIComponent","delete","echo","encodeURI","encodeURIComponent","escape","eval","fail","isFinite","isNaN","list_peers","parseFloat","parseInt","print","unescape"]`
	if err != nil || got != want {
		t.Errorf("the functions of a script: %s, %v", got, err)
	}
	for _, source := range []string{`import("fs")`, `return require("fs")`} {
		if got, _, err := run(t, source); err == nil || got != "" {
			t.Errorf("%q = %q", source, got)
		}
	}

	// Every script starts anew, also when one before it broke what it found.
	if _, _, err := run(t, `globalThis.kept = 1; JSON.stringify = function() { return "broken" }; Error = null; echo = null; tools.echo = null`); err != nil {
		t.Fatal(err)
	}
	if got, _, err := run(t, `return [typeof kept, JSON.stringify(1), typeof Error, echo({})]`); err != nil || got != `["undefined","1","function","echo:{}"]` {
		t.Errorf("the next script: %s, %v", got, err)
	}
	// What a script breaks for itself does not reach the tools.
	got, r, err := run(t, `JSON.stringify = function() { return '{"path":"/"}' }; Error = function() { return "no" }
		try { fail({a: 1}) } catch (e) { return e.message + " " + echo({a: 1}) }`)
	if err != nil || got != `test error echo:{"a":1}` || r.calls[1] != `echo {"a":1}` {
		t.Errorf("got %q, %v, calls %v", got, err, r.calls)
	}
}

// A tool that fails throws: not caught it ends the script, and the error
// names the line; caught, the script goes on.
func TestErrors(t *testing.T) {
	got, r, err := run(t, "var a = echo({})\nfail({why: 1})\nreturn echo({})")
	if err == nil || got != "" || err.Error() != "the script failed: Error: test error\n\tat script:2:5\n\tat script:4:3" {
		t.Errorf("not caught: %q, %v", got, err)
	}
	if len(r.calls) != 2 {
		t.Errorf("the script went on after the error: %v", r.calls)
	}
	got, r, err = run(t, `try { fail({}) } catch (e) { return [e instanceof Error, e.message, echo({})] }`)
	if err != nil || got != `[true,"test error","echo:{}"]` || len(r.calls) != 2 {
		t.Errorf("caught: %q, %v, calls %v", got, err, r.calls)
	}

	for source, want := range map[string]string{
		"\n\nthrow new Error('boom')": "the script failed: Error: boom\n\tat script:3:7",
		"throw 'text'":                "the script failed: text\n\tat script:1:1",
		"return nothing.there":        "the script failed: ReferenceError: nothing is not defined\n\tat script:1:20\n\tat script:2:3",
		"nothing.there":               "the script failed: ReferenceError: nothing is not defined\n\tat script:1:1",
		"var x = ;":                   "the script is not valid JavaScript: SyntaxError: script: Line 1:9 Unexpected token ;",
		"}":                           "the script is not valid JavaScript: SyntaxError: script: Line 1:1 Unexpected token }",
		"return 1 +":                  "the script is not valid JavaScript: SyntaxError: script: Line 2:1 Unexpected token }",
		`const home = await list({path: "~"})`: "the script is not valid JavaScript: SyntaxError: script: Line 1:20 Unexpected identifier: " +
			"the tools return their result right away, so call them without await",
		"function f() { return f() }\nreturn f()": "the script was stopped: more than 1024 calls within each other, at f (script:1:",
		strings.Repeat(" ", MaxSource) + ";":      "the script has 65537 bytes: at most 65536 are run",
	} {
		got, _, err := run(t, source)
		if err == nil || got != "" || !strings.HasPrefix(err.Error(), want) {
			t.Errorf("%.40q = %q, %v; want the error %q", source, got, err, want)
		}
		// A line of the script, and nothing of the interpreter.
		if err != nil && strings.HasPrefix(want, "the script failed") && err.Error() != want {
			t.Errorf("%.40q: %q; want the error %q", source, err, want)
		}
	}
	if got, _, err := run(t, strings.Repeat(" ", MaxSource-9)+`return 1;`); err != nil || got != "1" {
		t.Errorf("a script of exactly the limit: %q, %v", got, err)
	}
	if got, _, err := run(t, "function f(n) { return n ? f(n - 1) : 0 }\nreturn f(1000)"); err != nil || got != "0" {
		t.Errorf("1000 calls within each other: %q, %v", got, err)
	}
}

// A tool whose name is a word of the language is called through "tools": as
// a function of its name it would be the operator, which does nothing.
func TestReservedNames(t *testing.T) {
	for _, source := range []string{`delete({path: "~/x"})`, "var a = 1;delete ( {path: a})", `return [1].map(function(p) { return delete({path: p}) })`, "delete\n({})"} {
		got, r, err := run(t, source)
		if err == nil || got != "" || len(r.calls) != 0 || err.Error() != "delete is a word of JavaScript, so delete({…}) does not call the tool: write tools.delete({…})" {
			t.Errorf("%q = %q, %v", source, got, err)
		}
	}
	for source, want := range map[string]string{
		`return tools.delete({path: "~/x"})`:       `delete:{"path":"~/x"}`,
		`return tools .delete({})`:                 `delete:{}`,
		`var o = {a: 1}; delete o.a; return o`:     `{}`,
		`var o = {a: 1}; delete(o.a); return o`:    `{}`,
		`return globalThis.delete({})`:             `delete:{}`,
		`var undelete = echo; return undelete({})`: `echo:{}`,
		`var $delete = echo; return $delete({})`:   `echo:{}`,
		`return echo({text: "delete({"}).length`:   `echo:{"text":"delete({"}`[:0] + "24",
	} {
		if strings.Contains(source, `"delete({"`) {
			// Seen in a text, the call is taken for one: better than a tool that silently does nothing.
			if _, _, err := run(t, source); err == nil {
				t.Errorf("%q was run", source)
			}
			continue
		}
		if got, _, err := run(t, source); err != nil || got != want {
			t.Errorf("%q = %q, %v; want %q", source, got, err, want)
		}
	}
	if reserved("echo") || reserved("list_peers") || !reserved("delete") || !reserved("new") || !reserved("my-tool") {
		t.Error("the words of the language are not told from names")
	}
	// A name that is no word of the language is not looked for.
	r := &recorder{}
	if got, err := Run(context.Background(), `return echo({a: 1})`, []string{"echo", "my-tool"}, r.call); err != nil || got != `echo:{"a":1}` {
		t.Errorf("got %q, %v", got, err)
	}
	if got, err := Run(context.Background(), `return tools["my-tool"]({a: 1})`, []string{"my-tool"}, r.call); err != nil || got != `my-tool:{"a":1}` {
		t.Errorf("got %q, %v", got, err)
	}
}

// A script calls 64 tools and is stopped at the next one, caught or not.
func TestLimitOfCalls(t *testing.T) {
	got, r, err := run(t, fmt.Sprintf(`for (var i = 0; i < %d; i++) echo({i: i}); return i`, MaxCalls))
	if err != nil || got != "64" || len(r.calls) != MaxCalls {
		t.Fatalf("as many as allowed: %q, %v, %d calls", got, err, len(r.calls))
	}
	got, r, err = run(t, `for (var i = 0; ; i++) { try { echo({i: i}) } catch (e) {} }`)
	if err == nil || got != "" || err.Error() != "the script was stopped: it called more than 64 tools" {
		t.Fatalf("one more: %q, %v", got, err)
	}
	if len(r.calls) != MaxCalls || r.calls[MaxCalls-1] != `echo {"i":63}` {
		t.Fatalf("%d calls, the last %s", len(r.calls), r.calls[len(r.calls)-1])
	}
}

// shorten sets the limits that take long to reach until the test ends.
func shorten(t *testing.T, limit, wait time.Duration, heap uint64) {
	t.Helper()
	oldTime, oldGrace, oldHeap := maxTime, grace, maxHeap
	maxTime, grace, maxHeap = limit, wait, heap
	t.Cleanup(func() { maxTime, grace, maxHeap = oldTime, oldGrace, oldHeap })
}

// A script that does not end is stopped, also within a try.
func TestLimitOfTime(t *testing.T) {
	shorten(t, 200*time.Millisecond, time.Minute, maxHeap)
	start := time.Now()
	got, _, err := run(t, `while (true) { try { while (true) {} } catch (e) {} }`)
	if err == nil || got != "" || err.Error() != "the script was stopped: it ran for more than 200ms" {
		t.Fatalf("got %q, %v", got, err)
	}
	if took := time.Since(start); took < 200*time.Millisecond || took > 20*time.Second {
		t.Fatalf("stopped after %s", took)
	}

	// The tools are told to stop with it.
	var told error
	got, err = Run(context.Background(), `return wait({})`, []string{"wait"}, func(ctx context.Context, _ string, _ json.RawMessage) (string, error) {
		<-ctx.Done()
		told = ctx.Err()
		return "late", nil
	})
	if err == nil || got != "" || err.Error() != "the script was stopped: it ran for more than 200ms" || !errors.Is(told, context.DeadlineExceeded) {
		t.Fatalf("got %q, %v; the tool was told %v", got, err, told)
	}
	if MaxTime != 10*time.Minute {
		t.Fatalf("the limit is %s", MaxTime)
	}
}

// A tool that does not stop when it is told to is not waited for.
func TestToolThatDoesNotEnd(t *testing.T) {
	shorten(t, 100*time.Millisecond, 100*time.Millisecond, maxHeap)
	release := make(chan struct{})
	after := make(chan string, 1)
	start := time.Now()
	got, err := Run(context.Background(), `stuck({}); stuck({})`, []string{"stuck"}, func(context.Context, string, json.RawMessage) (string, error) {
		<-release
		after <- "returned"
		return "", nil
	})
	if err == nil || got != "" || err.Error() != "the script was stopped: it ran for more than 100ms, and a tool it called did not end: what the scripts before it kept is gone" {
		t.Fatalf("got %q, %v", got, err)
	}
	if took := time.Since(start); took < 200*time.Millisecond || took > 20*time.Second {
		t.Fatalf("given up after %s", took)
	}
	// When the tool does return, the script is over: it calls nothing more.
	close(release)
	<-after
	select {
	case <-after:
		t.Fatal("the script went on after it was stopped")
	case <-time.After(300 * time.Millisecond):
	}
}

// A conversation that is cancelled stops its script.
func TestCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	go func() {
		<-started
		cancel()
	}()
	got, err := Run(ctx, `begin({}); while (true) {}`, []string{"begin"}, func(context.Context, string, json.RawMessage) (string, error) {
		close(started)
		return "", nil
	})
	if err == nil || got != "" || err.Error() != "the script was stopped: the conversation was cancelled" {
		t.Fatalf("got %q, %v", got, err)
	}
	// One that was cancelled before runs nothing.
	r := &recorder{}
	if got, err := Run(ctx, `while (true) { echo({}) }`, r.names(), r.call); err == nil || got != "" || len(r.calls) > MaxCalls {
		t.Fatalf("got %q, %v", got, err)
	}
}

// A script that fills the memory is stopped.
func TestLimitOfMemory(t *testing.T) {
	shorten(t, time.Minute, time.Minute, 32<<20)
	got, _, err := run(t, `var all = []; while (true) { all.push(new Array(1000).fill(all.length)) }`)
	if err == nil || got != "" || err.Error() != "the script was stopped: it used more than 32 MiB of memory: what the scripts before it kept is gone" {
		t.Fatalf("got %q, %v", got, err)
	}
	// What stays below the limit runs to its end.
	if got, _, err := run(t, `var all = []; for (var i = 0; i < 1000; i++) all.push(new Array(100).fill(i)); return all.length`); err != nil || got != "1000" {
		t.Fatalf("got %q, %v", got, err)
	}
	if MaxHeap != 256<<20 || MaxDepth != 1024 {
		t.Fatalf("the limits are %d bytes and %d calls", MaxHeap, MaxDepth)
	}
}

// What counts is how far the heap grows while the script runs, not how
// large it already was.
func TestLimitOfMemoryCountsTheGrowth(t *testing.T) {
	shorten(t, time.Minute, time.Minute, 32<<20)
	held := make([]byte, 64<<20)
	got, _, err := run(t, `var t = Date.now(), n = 0; while (Date.now() - t < 100) n++; return "done"`)
	runtime.KeepAlive(held)
	if err != nil || got != "done" {
		t.Fatalf("got %q, %v", got, err)
	}

	// A heap that shrinks while the script runs is no growth either. The
	// garbage stays until the tool collects it.
	defer debug.SetGCPercent(debug.SetGCPercent(-1))
	garbage := make([]byte, 64<<20)
	runtime.KeepAlive(garbage) // from here on it is garbage
	got, err = Run(context.Background(), `collect({}); var t = Date.now(), n = 0; while (Date.now() - t < 100) n++; return "done"`,
		[]string{"collect"}, func(context.Context, string, json.RawMessage) (string, error) {
			runtime.GC()
			return "", nil
		})
	if err != nil || got != "done" {
		t.Fatalf("after a collection: got %q, %v", got, err)
	}
}
