package script

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// runIn runs source in s with the tools of r, and returns what it gave back
// without the line of what is kept.
func runIn(t *testing.T, s *Session, r *recorder, source string) string {
	t.Helper()
	got, err := s.Run(context.Background(), source, r.names(), r.call)
	if err != nil {
		t.Fatalf("%q: %v", source, err)
	}
	got, _, _ = strings.Cut(got, "\n[kept for the next script: ")
	return got
}

// What a script declares at its top level is there for the next one, also
// when it declares it again; the value of its last statement is its result.
func TestSessionKeeps(t *testing.T) {
	s, r := NewSession(), &recorder{}
	for source, want := range map[string]string{
		"let a = 1; const b = 2; var c = 3; function f() { return 4 }; a + b": "3",
	} {
		if got := runIn(t, s, r, source); got != want {
			t.Fatalf("%q = %q", source, got)
		}
	}
	for _, step := range [][2]string{
		{"[a, b, c, f()]", "[1,2,3,4]"},
		{"let a = 5; const b = 6; a + b", "11"},
		{"b = 7; b", "7"}, // a const that was declared at the top level is a var
		{"globalThis.d = echo({x: 1}); 'done'", "done"},
		{"d", `echo:{"x":1}`},
		{"const r = [1, 2]\nr.length", "2"},
		{"for (const x of r) { x * 10 }", "20"},
		{"if (true) { 'yes' }", "yes"},
		{"var e = 1", "(the script returned nothing)"},
		{"const é = 'ü'; let ß = é + 'x'; ß", "üx"},
		{"é", "ü"},
		{"class K {}; typeof K", "function"},
		// A script that returns is the body of a function: what it declares ends with it.
		{"var inside = 1; outside = 2; return a", "5"},
		{"[typeof inside, outside]", `["undefined",2]`},
	} {
		if got := runIn(t, s, r, step[0]); got != step[1] {
			t.Fatalf("%q = %q; want %q", step[0], got, step[1])
		}
	}
}

// print and console.log add lines to what a script gives back, before its value.
func TestPrint(t *testing.T) {
	for source, want := range map[string]string{
		`print("a", 1, {b: [2]}, null); print(); console.log("c"); 42`: "a 1 {\"b\":[2]} null\n\nc\n42",
		`print("only")`:                                  "only",
		`console.error("x"); console.warn("y")`:          "x\ny",
		`print(undefined, function f() {}, Symbol("s"))`: "undefined function f() {} s",
		`print("x"); return "y"`:                         "x\ny",
		`print("x"); return`:                             "x",
	} {
		if got, _, err := run(t, source); err != nil || got != want {
			t.Errorf("%q = %q, %v; want %q", source, got, err, want)
		}
	}
	// What is printed beyond the limit is left out, said so, and kept in
	// Printed: whole lines, or what fits of a line that is too long.
	const note = " more bytes were printed and left out. All of it is kept in printed, a string: " +
		"look into it, like printed.indexOf(…) or printed.slice(…), and print only what you need)"
	line := strings.Repeat("x", 1000)
	s, r := NewSession(), &recorder{}
	got, err := s.Run(context.Background(), fmt.Sprintf(`[1, 2].forEach(function() { for (var i = 0; i < 100; i++) print(%q) })`, line), r.names(), r.call)
	if want := "\n(184184" + note + "\n[kept for the next script: printed (string of 200200 bytes)]"; !strings.HasSuffix(got, want) || strings.Count(got, line) != 16 {
		t.Errorf("too much: %d bytes, %v: %q", len(got), err, got[len(got)-100:])
	}
	if got := runIn(t, s, r, "printed.length"); got != "200200" {
		t.Errorf("kept: %q", got)
	}
	if got, _, err := run(t, `print("x".repeat(`+fmt.Sprint(MaxPrint)+`)); 1`); err != nil ||
		got != strings.Repeat("x", MaxPrint)+"\n(1"+note+"\n1\n[kept for the next script: printed (string of 16385 bytes)]" {
		t.Errorf("one line too long: %.40q, %v", got[MaxPrint-10:], err)
	}
	// Of very much, the start is kept; under a tightened context less is
	// handed on.
	got, err = s.Run(context.Background(), `print("x".repeat(5 * 1024 * 1024))`, r.names(), r.call)
	if err != nil || !strings.Contains(got, " more bytes were printed and left out. The first 4 MiB of it is kept in printed, a string: ") ||
		!strings.HasSuffix(got, ")\n[kept for the next script: printed (string of 4194304 bytes)]") {
		t.Errorf("very much: %q, %v", got[MaxPrint:], err)
	}
	got, err = s.Run(Tighten(context.Background()), fmt.Sprintf(`for (var i = 0; i < 10; i++) print(%q)`, line), r.names(), r.call)
	if err != nil || strings.Count(got, line) != 4 || !strings.Contains(got, "\n(6006"+note) {
		t.Errorf("tight: %d lines, %v", strings.Count(got, line), err)
	}
	// A script that fails says what it printed before.
	if _, _, err := run(t, "print('so far')\nfail({})"); err == nil || err.Error() != "the script failed: Error: test error\n\tat script:2:5\nIt printed before:\nso far" {
		t.Errorf("failed: %v", err)
	}
	if _, _, err := run(t, "print('so far'); Symbol()"); err == nil || err.Error() != "what the script returned is no data: return a string, or what JSON can hold\nIt printed before:\nso far" {
		t.Errorf("no data: %v", err)
	}
}

// Every script gets the tools it is run with, and only those, whatever the
// script before made of them.
func TestSessionTools(t *testing.T) {
	s, r := NewSession(), &recorder{}
	runIn(t, s, r, `echo = null; tools = null; print = null; console = null; JSON.stringify = function() { return '"x"' }; Error = null`)
	// What else it broke stays broken for the scripts after it, but not for the tools.
	if got := runIn(t, s, r, `print("p"); [echo({a: 1}), tools.echo({}), typeof Error].join()`); got != `p
echo:{"a":1},echo:{},object` {
		t.Fatalf("restored: %q", got)
	}
	got, err := s.Run(context.Background(), `[typeof echo, typeof list_peers, Object.keys(tools)]`, []string{"list_peers"}, r.call)
	if err != nil || got != `["undefined","function",["list_peers"]]` {
		t.Fatalf("fewer tools: %q, %v", got, err)
	}
	// The limit of calls is one of each script.
	for range 2 {
		runIn(t, s, r, fmt.Sprintf(`for (var i = 0; i < %d; i++) echo({})`, MaxCalls))
	}
}

// A script that was stopped leaves the session to the next, which keeps
// what was there unless it was memory that stopped it.
func TestSessionAfterAStop(t *testing.T) {
	shorten(t, 100*time.Millisecond, time.Minute, 32<<20)
	s, r := NewSession(), &recorder{}
	runIn(t, s, r, "var kept = 1")
	if _, err := s.Run(context.Background(), "while (true) {}", r.names(), r.call); err == nil {
		t.Fatal("not stopped")
	}
	if got := runIn(t, s, r, "kept"); got != "1" {
		t.Fatalf("after the time: %q", got)
	}
	shorten(t, time.Minute, time.Minute, 32<<20)
	if _, err := s.Run(context.Background(), "var all = []; while (true) all.push(new Array(1000).fill(1))", r.names(), r.call); err == nil || !strings.Contains(err.Error(), "memory") {
		t.Fatalf("not stopped for the memory: %v", err)
	}
	if got := runIn(t, s, r, "typeof kept + typeof all"); got != "undefinedundefined" {
		t.Fatalf("after the memory: %q", got)
	}

	// A tool that does not end takes the interpreter with it.
	shorten(t, 100*time.Millisecond, 100*time.Millisecond, 32<<20)
	runIn(t, s, r, "var kept = 2")
	release := make(chan struct{})
	defer close(release)
	_, err := s.Run(context.Background(), "stuck({})", []string{"stuck"}, func(context.Context, string, json.RawMessage) (string, error) {
		<-release
		return "", nil
	})
	if err == nil {
		t.Fatal("not stopped")
	}
	if got := runIn(t, s, r, "typeof kept"); got != "undefined" {
		t.Fatalf("after a tool that did not end: %q", got)
	}
}

// The scripts of a session run one after the other; one that waits for its
// turn can be cancelled.
func TestSessionTurns(t *testing.T) {
	s := NewSession()
	entered, release := make(chan struct{}), make(chan struct{})
	block := func(context.Context, string, json.RawMessage) (string, error) {
		close(entered)
		<-release
		return "", nil
	}
	var wg sync.WaitGroup
	wg.Go(func() {
		if _, err := s.Run(context.Background(), "var n = 1; block({})", []string{"block"}, block); err != nil {
			t.Error(err)
		}
	})
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := &recorder{}
	if got, err := s.Run(ctx, "1", r.names(), r.call); err == nil || got != "" || err.Error() != "the script was not run: the conversation was cancelled" {
		t.Fatalf("cancelled while waiting: %q, %v", got, err)
	}
	second := make(chan string)
	go func() {
		got, _ := s.Run(context.Background(), "n + 1", r.names(), r.call)
		second <- got
	}()
	select {
	case <-second:
		t.Fatal("ran at the same time")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	wg.Wait()
	if got := <-second; got != "2\n[kept for the next script: n (number)]" {
		t.Fatalf("after its turn: %q", got)
	}
}

// Sessions keeps one session per conversation, and forgets the one used the
// longest ago beyond MaxSessions.
func TestSessions(t *testing.T) {
	all := NewSessions()
	first := all.Of("0")
	if all.Of("0") != first || all.Of("1") == first {
		t.Fatal("not one per conversation")
	}
	for i := 2; i < MaxSessions; i++ {
		all.Of(fmt.Sprint(i))
	}
	all.Of("0") // used again: "1" is the oldest now
	all.Of("new")
	if all.Of("0") != first || len(all.sessions) != MaxSessions {
		t.Fatalf("%d sessions", len(all.sessions))
	}
	if _, ok := all.sessions["1"]; ok {
		t.Fatal("the oldest was kept")
	}
	if MaxSessions != 16 {
		t.Fatal("the README names the limit")
	}
}

// What a script gives back ends with what the interpreter keeps for the next
// one: the names it put on globalThis, with what they are.
func TestKept(t *testing.T) {
	s, r := NewSession(), &recorder{}
	got, err := s.Run(context.Background(), `
		let text = "äb"; const list = [1, 2, 3]; var obj = {a: 1, b: 2}; function f() {}
		var n = 1.5, yes = true, none = null, missing = undefined, sym = Symbol("s"), big = 1n
		Object.defineProperty(globalThis, "lazy", {get() { while (true) {} }})
		var arrayProxy = new Proxy([], {get() { throw new Error("no") }})
		var objectProxy = new Proxy({}, {ownKeys() { throw new Error("no") }})
		var revoked = Proxy.revocable([], {}); revoked.revoke(); revoked = revoked.proxy
		echo = 1; globalThis.JSON = JSON
		"done"`, r.names(), r.call)
	// Declarations first, as the language makes them, then what was put there.
	want := "done\n[kept for the next script: f (function), text (string of 3 bytes), list (array of 3), obj (object with 2 keys), " +
		"n (number), yes (boolean), none (null), missing (undefined), sym (symbol), big (bigint), " +
		"arrayProxy (array), objectProxy (object), revoked (object), lazy (getter)]"
	if err != nil || got != want {
		t.Fatalf("got %q, %v\nwant %q", got, err, want)
	}
	// Of many, the first are listed.
	got, _, err = run(t, `for (var i = 0; i < 40; i++) globalThis["v" + i] = i; "x"`)
	if err != nil || !strings.HasPrefix(got, "x\n[kept for the next script: i (number), v0 (number), ") || !strings.HasSuffix(got, ", v28 (number), and 11 more]") {
		t.Fatalf("many: %q, %v", got, err)
	}
	// What looks at a Proxy runs under the limits of the script.
	shorten(t, 200*time.Millisecond, time.Minute, maxHeap)
	_, _, err = run(t, `var p = new Proxy({}, {ownKeys() { while (true) {} }}); 1`)
	if err != nil {
		t.Fatalf("a Proxy that does not end: %v", err)
	}
	// So does what a script gives back, and memory that stops it there takes the interpreter.
	shorten(t, time.Minute, time.Minute, 32<<20)
	s = NewSession()
	runIn(t, s, r, "var before = 1")
	_, err = s.Run(context.Background(), `var all = []; ({toJSON() { while (true) all.push(new Array(1000).fill(1)) }})`, r.names(), r.call)
	if err == nil || !strings.Contains(err.Error(), "memory") {
		t.Fatalf("given back: %v", err)
	}
	if got := runIn(t, s, r, "typeof before"); got != "undefined" {
		t.Fatalf("after the memory: %q", got)
	}
	if MaxPrint != 16<<10 {
		t.Fatal("the README names the limit")
	}
}

// What is printed at the bounds of the limits: exactly the limit is handed on
// whole; whole lines are kept when the last of them ends at half the limit;
// exactly maxPrinted bytes are kept all, of more lines as of one.
func TestOutputAtItsBounds(t *testing.T) {
	o := &output{limit: 10}
	o.add("123456789")
	if got := o.String(); got != "123456789" {
		t.Errorf("the limit: %q", got)
	}
	o = &output{limit: 10}
	o.add("12345")
	o.add("abcdefgh")
	if got := o.String(); !strings.HasPrefix(got, "12345\n(9 more bytes") {
		t.Errorf("a line that ends at half the limit: %q", got)
	}
	o = &output{limit: 10}
	o.add(strings.Repeat("x", maxPrinted-1))
	if got := o.String(); o.all.Len() != maxPrinted || !strings.Contains(got, "All of it is kept") {
		t.Errorf("maxPrinted bytes: %d kept, %q", o.all.Len(), got[10:40])
	}
	o = &output{limit: 10}
	o.add(strings.Repeat("x", 3<<20))
	o.add(strings.Repeat("y", 2<<20))
	if o.all.Len() != maxPrinted || !strings.HasSuffix(o.all.String(), "y") {
		t.Errorf("more lines: %d kept", o.all.Len())
	}
}

// The line of what is kept lists maxKept names, and says how many more there
// are only when there are.
func TestKeptAtItsBound(t *testing.T) {
	s, r := NewSession(), &recorder{}
	got, err := s.Run(context.Background(), fmt.Sprintf("for (var i = 1; i < %d; i++) globalThis['v' + i] = i", maxKept), r.names(), r.call)
	if err != nil || !strings.HasSuffix(got, fmt.Sprintf("v%d (number)]", maxKept-1)) || strings.Contains(got, "more") {
		t.Fatalf("maxKept names: %q, %v", got, err)
	}
}
