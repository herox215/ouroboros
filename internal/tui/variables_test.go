package tui

import (
	"slices"
	"testing"
)

// A script refers to names, declares names, also as parameters and in
// patterns, and assigns to names. A name after a dot, a private name, a key
// that is not computed and a label are none of these; a name written with
// escapes is not where its letters are, so it is left out of where names
// are.
func TestScriptNames(t *testing.T) {
	for _, tc := range []struct {
		script             string
		refs               []nameAt
		declared, assigned []string
	}{
		{"const t = read({path: \"/x\", cfg}); print(t.length, cfg?.a, `${l}`);",
			[]nameAt{{"read", 10}, {"cfg", 28}, {"print", 35}, {"t", 41}, {"cfg", 51}, {"l", 62}}, []string{"t"}, nil},
		{"class A { #p = 1; m() { return this.#p } }; lbl: for (;;) break lbl; function f(q) { return new.target ?? q + z }",
			[]nameAt{{"q", 106}, {"z", 110}}, []string{"A", "f", "q"}, nil},
		{"x = y + 1; a.b = c; [d, e] = f; ({g} = h); k[0] = 1; o += 2; x = 3",
			[]nameAt{{"y", 4}, {"a", 11}, {"c", 17}, {"f", 29}, {"h", 39}, {"k", 43}}, []string{"d", "e", "g"}, []string{"x", "o"}},
		{"({[key]: val}); class B extends Base { [m2]() {}; [f2] = 1; static s = w }",
			[]nameAt{{"key", 3}, {"val", 9}, {"Base", 32}, {"m2", 40}, {"f2", 51}, {"w", 71}}, []string{"B"}, nil},
		{"for (const f of files) g(f)", []nameAt{{"files", 16}, {"g", 23}, {"f", 25}}, []string{"f"}, nil},
		{"var v = w; (() => { var u = y })()", []nameAt{{"w", 8}, {"y", 28}}, []string{"v", "u"}, nil},                       // each once
		{"for (let i = 0; i < n; i += step) {}", []nameAt{{"i", 16}, {"n", 20}, {"step", 28}}, []string{"i"}, []string{"i"}}, // in order
		{"var \\u0061b = 1; \\u0061b + cd", []nameAt{{"cd", 27}}, []string{"ab"}, nil},
		{"{", nil, nil, nil},
	} {
		refs, declared, assigned := scriptNames(tc.script)
		if !slices.Equal(refs, tc.refs) || !slices.Equal(declared, tc.declared) || !slices.Equal(assigned, tc.assigned) {
			t.Errorf("%q: %v %q %q,\nwant %v %q %q", tc.script, refs, declared, assigned, tc.refs, tc.declared, tc.assigned)
		}
	}
}

// The line of what is kept is the last of a result: it is taken off, and
// what it lists taken apart.
func TestParseKept(t *testing.T) {
	text, kept, more, ok := parseKept("out\n[kept for the next script: a (string of 3 bytes), b2$ (array of 2), and 3 more]")
	if want := []keptVar{{"a", "string of 3 bytes"}, {"b2$", "array of 2"}}; !ok || text != "out" || !slices.Equal(kept, want) || more != "and 3 more" {
		t.Fatalf("%q %v %q %v", text, kept, more, ok)
	}
	if text, kept, more, ok := parseKept("[kept for the next script: x (number)]"); !ok || text != "" || !slices.Equal(kept, []keptVar{{"x", "number"}}) || more != "" {
		t.Fatalf("a result of nothing but the line: %q %v %q %v", text, kept, more, ok)
	}
	for _, result := range []string{"plain", "[kept for the next script: x (number)]\nmore"} {
		if text, kept, _, ok := parseKept(result); ok || text != result || kept != nil {
			t.Errorf("%q has no line of what is kept: %q %v", result, text, kept)
		}
	}
}

// Only a call of run with a script of text has a script.
func TestRunScript(t *testing.T) {
	for call, want := range map[string]string{`run {"script":"a()"}`: "a()", `run {"other":1,"script":"b"}`: "b"} {
		if got, ok := runScript(call); !ok || got != want {
			t.Errorf("%q: %q, %v", call, got, ok)
		}
	}
	for _, call := range []string{`bash {"script":"x"}`, "run not json", `run {"script":1}`, "run {}"} {
		if got, ok := runScript(call); ok {
			t.Errorf("%q has no script: %q", call, got)
		}
	}
}

func TestSplitCalls(t *testing.T) {
	for text, want := range map[string][]string{"text\n→ a {}\n→ b": {"a {}", "b"}, "→ a": {"a"}, "no calls → x": {}} {
		if got := splitCalls(text); !slices.Equal(got, want) {
			t.Errorf("%q: %q, want %q", text, got, want)
		}
	}
}

// traceContext is a context in which scripts keep, use, set again and lose
// variables.
var traceContext = []Message{
	{"system", "You are alpha."},
	{"user", "go"},
	{"assistant", `→ run {"script":"const a = 1; var b = 'x'"}`}, // #3
	{"tool", "1\n[kept for the next script: a (number), b (string of 1 bytes)]"},
	{"assistant", "Let me look.\n→ bash {\"command\":\"ls\"}\n→ run {\"script\":\"print(a + b.length); b = 'yy'\"}"}, // #5
	{"tool", "file"}, // of bash
	{"tool", "2\n[kept for the next script: a (number), b (string of 2 bytes)]"},
	{"assistant", `→ run {"script":"const a = 2; print(a)"}`}, // #8
	{"tool", "2\n[kept for the next script: a (number), b (string of 2 bytes), c (function)]"},
	{"assistant", `→ run {"script":"delete globalThis.b; c()"}`}, // #10
	{"tool", "[kept for the next script: a (number), c (function)]"},
	{"assistant", `→ run {"script":"x"}`}, // #12
	{"tool", "error: the script was stopped: what the scripts before it kept is gone"},
	{"tool", "a result of no call"},
	{"assistant", `→ run {"script":"syntax error ("}`}, // #15
	{"tool", "error: the script is not valid JavaScript"},
	{"assistant", `→ run {"script":"z = 1"}`}, // #17
	{"tool", "[kept for the next script: z (number)]"},
	{"assistant", `→ run {"script":"1"}`}, // #19
	{"tool", "1\n[kept for the next script: and 41 more]"},
}

// The trace follows every variable: which scripts set it, which used it,
// what it was last, and after which script it was gone. A name a script
// declares is its own and no use; one it assigns to after using it is used.
// A result without a line of what is kept changes nothing, unless the
// interpreter was lost; and a line that does not list everything tells
// nothing of what is gone.
func TestTraceScripts(t *testing.T) {
	tr := traceScripts(traceContext)
	want := []variable{
		{"a", "number", []int{3, 8}, []int{5}, 12},
		{"b", "string of 2 bytes", []int{3, 5}, []int{5}, 10},
		{"c", "function", []int{8}, []int{10}, 12},
		{"z", "number", []int{17}, nil, 0},
	}
	if len(tr.variables) != len(want) {
		t.Fatalf("%d variables, want %d", len(tr.variables), len(want))
	}
	for i, v := range tr.variables {
		if w := want[i]; v.name != w.name || v.what != w.what || !slices.Equal(v.set, w.set) || !slices.Equal(v.used, w.used) || v.gone != w.gone {
			t.Errorf("%+v, want %+v", *v, w)
		}
	}
	if calls := tr.calls[4]; len(calls) != 2 || calls[0] != nil || !slices.Equal(calls[1].uses, []string{"a", "b"}) ||
		!slices.Equal(calls[1].at, []nameAt{{"a", 6}, {"b", 10}}) || calls[1].number != 5 {
		t.Fatalf("the calls of #5: %v", calls)
	}
	if uses := tr.calls[7][0].uses; len(uses) != 0 {
		t.Fatalf("a declared name is no use: %v", uses)
	}
	r := tr.results[8]
	if r.text != "2" || !slices.Equal(r.added, []keptVar{{"c", "function"}}) || !slices.Equal(r.changed, []keptChange{{"a", "number", "number"}}) ||
		!slices.Equal(r.same, []string{"b"}) || len(r.gone) != 0 {
		t.Fatalf("the result of #8: %+v", *r)
	}
	if r := tr.results[6]; !slices.Equal(r.changed, []keptChange{{"b", "string of 1 bytes", "string of 2 bytes"}}) || !slices.Equal(r.same, []string{"a"}) {
		t.Fatalf("the result of #5: %+v", *r)
	}
	if r := tr.results[12]; !slices.Equal(r.gone, []string{"a", "c"}) || r.text != traceContext[12].Text {
		t.Fatalf("a lost interpreter keeps nothing: %+v", *r)
	}
	if r := tr.results[19]; r.more != "and 41 more" || len(r.gone) != 0 {
		t.Fatalf("a line that does not list everything: %+v", *r)
	}
	for _, i := range []int{5, 13, 15} {
		if r, ok := tr.results[i]; ok {
			t.Errorf("%d changed nothing: %+v", i, r)
		}
	}
}
