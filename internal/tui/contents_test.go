package tui

import (
	"encoding/json"
	"image/color"
	"slices"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// stripped is lines without colors.
func stripped(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = ansi.Strip(l)
	}
	return out
}

// A message has a head as wide as the screen: the badge of its role, a rule
// and its size. Its lines hang on a bar, two cells narrower; on a narrow
// screen the rule goes.
func TestRenderMessage(t *testing.T) {
	lines := strings.Split(renderMessage(Message{"user", "hi\nthere"}, 0, &scriptTrace{}, 30, false), "\n")
	want := []string{" user  #1 " + strings.Repeat("─", 12) + " 8 bytes", "▎ hi", "▎ there"}
	if got := stripped(lines); !slices.Equal(got, want) {
		t.Fatalf("%q, want %q", got, want)
	}
	badge := lipgloss.NewStyle().Foreground(lipgloss.Color("0")).Background(accent).Bold(true).Padding(0, 1).Render("user")
	if !strings.HasPrefix(lines[0], badge) || !strings.HasPrefix(lines[1], lipgloss.NewStyle().Foreground(accent).Render(bar)) {
		t.Fatalf("the badge and the bar should have the color of the role: %q", lines[:2])
	}
	if got := stripped(strings.Split(renderMessage(Message{"tool", "aaa bbb ccc"}, 0, &scriptTrace{}, 9, true), "\n")); !slices.Equal(got[1:], []string{"▎ aaa bbb", "▎ ccc"}) {
		t.Fatalf("the lines should be wrapped two cells narrower: %q", got)
	}
	if got := ansi.Strip(renderMessage(Message{"assistant", "x"}, 9, &scriptTrace{}, 10, false)); got != " assistant  #10  1 byte\n▎ x" {
		t.Fatalf("on a narrow screen: %q", got)
	}
}

// The user and the agent have the colors of the chat; every other role has
// its own, and one that is not known is muted.
func TestRoleColor(t *testing.T) {
	for role, want := range map[string]color.Color{
		"user": accent, "assistant": peer, "system": lipgloss.Color("7"), "tool": good,
		"tool run": lipgloss.Color("4"), "plugin diagnostic": bad, "developer": muted,
	} {
		if got := roleColor(role); got != want {
			t.Errorf("%s: %v, want %v", role, got, want)
		}
	}
}

func TestByteSize(t *testing.T) {
	for n, want := range map[int]string{0: "0 bytes", 1: "1 byte", 2: "2 bytes", 1023: "1023 bytes", 1024: "1.0 KiB", 2560: "2.5 KiB"} {
		if got := byteSize(n); got != want {
			t.Errorf("%d: %q, want %q", n, got, want)
		}
	}
}

// Every role has its layout; a tab is as wide as four spaces.
func TestMessageLines(t *testing.T) {
	for _, tc := range []struct {
		c    Message
		want []string
	}{
		{Message{"tool", "\tx"}, []string{"    x"}},
		{Message{"assistant", "→ f {}"}, []string{"→ f"}},
		{Message{"tool run", "- f(): y"}, []string{"- f(): y"}},
	} {
		if got := stripped(messageLines(tc.c, 0, &scriptTrace{}, 20)); !slices.Equal(got, tc.want) {
			t.Errorf("%v: %q, want %q", tc.c, got, tc.want)
		}
	}
	if got := messageLines(Message{"tool run", "- f(): y"}, 0, &scriptTrace{}, 20); !strings.HasPrefix(got[0], "- "+boldStyle.Render("f")) {
		t.Fatalf("a tool should have its functions stand out: %q", got)
	}
}

// A line that is a note in brackets recedes; a line is wrapped and keeps
// how it is indented.
func TestTextLines(t *testing.T) {
	lines := textLines("[a note]\n[x] y\ny [x]\n\n  indented\none two three", 12)
	want := []string{"[a note]", "[x] y", "y [x]", "", "  indented", "one two", "three"}
	if got := stripped(lines); !slices.Equal(got, want) {
		t.Fatalf("%q, want %q", got, want)
	}
	if lines[0] != noteStyle.Render("[a note]") || lines[1] != "[x] y" || lines[2] != "y [x]" {
		t.Fatalf("only the note should recede: %q", lines[:3])
	}
}

// The lines after the first hang after rest.
func TestHang(t *testing.T) {
	if got := hang("> ", "  ", "aaa bbb ccc", 9); !slices.Equal(got, []string{"> aaa bbb", "  ccc"}) {
		t.Fatalf("%q", got)
	}
}

// A tool shows its description with the functions and their parameters
// standing out, and the schema of its parameters as indented JSON, a long
// line of it hanging below its key.
func TestToolLines(t *testing.T) {
	text := "Does things.\n- f({a: string}): Does f and more.\n    a: the thing it is\n- g(): Nothing.\n" +
		`parameters: {"type":"object","properties":{"a":{"description":"what a is, at some length"}},"n":1.5}`
	lines := toolLines(text, 30)
	want := []string{
		"Does things.",
		"- f({a: string}): Does f and",
		"  more.",
		"    a: the thing it is",
		"- g(): Nothing.",
		"parameters",
		"{",
		`  "type": "object",`,
		`  "properties": {`,
		`    "a": {`,
		`      "description": "what a`,
		`        is, at some length"`,
		"    }",
		"  },",
		`  "n": 1.5`,
		"}",
	}
	if got := stripped(lines); !slices.Equal(got, want) {
		t.Fatalf("%q,\nwant %q", got, want)
	}
	if !strings.Contains(lines[1], boldStyle.Render("f")+mutedStyle.Render("({a: string})")) ||
		!strings.Contains(lines[3], keyStyle.Render("a")+": the thing") || !strings.Contains(lines[7], keyStyle.Render(`"type"`)+":") {
		t.Fatalf("the functions, parameters and keys should stand out: %q", lines)
	}
	for text, want := range map[string][]string{
		"\nparameters: {}":    {"", "parameters", "{}"}, // a tool without a description
		"Just text.":          {"Just text."},
		"x\nparameters: ":     {"x"},
		"x\nparameters: {bad": {"x", "parameters", "{bad"},
	} {
		if got := stripped(toolLines(text, 30)); !slices.Equal(got, want) {
			t.Errorf("%q: %q, want %q", text, got, want)
		}
	}
}

// Keys, strings and the other values of JSON have colors of their own.
func TestColorJSON(t *testing.T) {
	key := func(s string) string { return keyStyle.Render(s) + ":" }
	want := "{" + key(`"a"`) + stringStyle.Render(`"b:"`) + "," + key(`"q"`) + stringStyle.Render(`"x\"y"`) + "," +
		key(`"n"`) + literalStyle.Render("-1.5e3") + "," + key(`"t"`) + literalStyle.Render("true") + "," +
		key(`"f"`) + literalStyle.Render("false") + "," + key(`"z"`) + literalStyle.Render("null") + "}"
	if got := colorJSON(`{"a":"b:","q":"x\"y","n":-1.5e3,"t":true,"f":false,"z":null}`); got != want {
		t.Fatalf("%q,\nwant %q", got, want)
	}
}

// What the agent says comes before its calls. A call has its arguments
// below it, their values in a column: a text as the lines it has, hanging in
// that column, an empty text and the other values as JSON. Arguments that
// are no object of JSON stay as they are.
func TestCallLines(t *testing.T) {
	for _, tc := range []struct {
		text  string
		width int
		want  []string
	}{
		{"Let me look.\n→ read {\"path\":\"/a\",\"limit\":20}\n→ run {\"script\":\"a()\\n\\tb()\"}", 40, []string{
			"Let me look.", "→ read", "  path   /a", "  limit  20", "→ run", "  script  a()", "              b()"}},
		{`→ ask {"m":"","n":[1, 2],"o":{"p":null}}`, 40, []string{"→ ask", `  m  ""`, "  n  [1,2]", `  o  {"p":null}`}},
		{`→ w {"c":"aaa bbb ccc"}`, 12, []string{"→ w", "  c  aaa bbb", "     ccc"}},
		{"→ bash not json", 40, []string{"→ bash not json"}},
		{"→ list", 40, []string{"→ list"}},
	} {
		if got := stripped(callLines(tc.text, nil, tc.width)); !slices.Equal(got, tc.want) {
			t.Errorf("%q: %q,\nwant %q", tc.text, got, tc.want)
		}
	}
	lines := callLines(`→ read {"path":"/a","limit":20}`, nil, 40)
	if !strings.HasPrefix(lines[0], lipgloss.NewStyle().Foreground(peer).Render("→ ")+boldStyle.Render("read")) ||
		!strings.HasPrefix(lines[1], "  "+keyStyle.Render("path")) || !strings.HasSuffix(lines[2], literalStyle.Render("20")) {
		t.Fatalf("the call, its keys and its values should stand out: %q", lines)
	}
}

// An object of JSON comes apart into its fields in their order; anything
// else is no object.
func TestObjectFields(t *testing.T) {
	fields, ok := objectFields(`{"b":1,"a":"x"} `)
	if want := []field{{"b", json.RawMessage("1")}, {"a", json.RawMessage(`"x"`)}}; !ok ||
		!slices.EqualFunc(fields, want, func(a, b field) bool { return a.key == b.key && string(a.value) == string(b.value) }) {
		t.Fatalf("%v, %v", fields, ok)
	}
	if fields, ok := objectFields("{}"); !ok || len(fields) != 0 {
		t.Fatalf("an empty object: %v, %v", fields, ok)
	}
	for _, s := range []string{"", `["a",1]`, "{1:2}", `{"a":}`, `{"a":1`, "{} x", `{"a":1}{}`} {
		if fields, ok := objectFields(s); ok {
			t.Errorf("%q is no object: %v", s, fields)
		}
	}
}

// A context is a block a message, and a block of the variables its scripts
// kept, when they kept any; the LLM is not sent that block.
func TestRenderContext(t *testing.T) {
	blocks := renderContext(traceContext, 60, false)
	if len(blocks) != len(traceContext)+1 {
		t.Fatalf("%d blocks", len(blocks))
	}
	want := []string{
		" variables  4 " + strings.Repeat("─", 26) + " not sent to the LLM",
		"▎ a  number             set #3 #8 · used #5 · gone #12",
		"▎ b  string of 2 bytes  set #3 #5 · used #5 · gone #10",
		"▎ c  function           set #8 · used #10 · gone #12",
		"▎ z  number             set #17",
	}
	if got := stripped(strings.Split(blocks[len(blocks)-1], "\n")); !slices.Equal(got, want) {
		t.Fatalf("%q,\nwant %q", got, want)
	}
	if !strings.HasPrefix(ansi.Strip(blocks[4]), " assistant  #5 ") || !strings.Contains(ansi.Strip(blocks[8]), "kept for the next script") {
		t.Fatalf("the messages should come first, with what their scripts did: %q", stripped(blocks[4:9]))
	}
	if blocks := renderContext([]Message{{"user", "hi"}}, 40, false); len(blocks) != 1 {
		t.Fatalf("without variables there is no block of them: %q", blocks)
	}
}

// The script of a call of run has the variables it uses marked, and a line
// below that names them; a script that uses none has no such line.
func TestCallLinesMarkUses(t *testing.T) {
	tr := traceScripts(traceContext)
	lines := callLines(traceContext[4].Text, tr.calls[4], 60)
	want := []string{"Let me look.", "→ bash", "  command  ls", "→ run", "  script  print(a + b.length); b = 'yy'", "  uses a, b"}
	if got := stripped(lines); !slices.Equal(got, want) {
		t.Fatalf("%q,\nwant %q", got, want)
	}
	if !strings.Contains(lines[4], "print("+varStyle.Render("a")+" + "+varStyle.Render("b")+".length); b = 'yy'") ||
		lines[5] != "  "+mutedStyle.Render("uses ")+varStyle.Render("a")+mutedStyle.Render(", ")+varStyle.Render("b") {
		t.Fatalf("the variables should be marked: %q", lines[4:])
	}
	if got := stripped(callLines(traceContext[7].Text, tr.calls[7], 60)); !slices.Equal(got, []string{"→ run", "  script  const a = 2; print(a)"}) {
		t.Fatalf("a script that uses nothing: %q", got)
	}
}

// The result of a script has below it what is kept, as it changed: what
// the script set with what it is, what is gone, and the names that stayed.
func TestResultLines(t *testing.T) {
	tr := traceScripts(traceContext)
	for i, want := range map[int][]string{
		8:  {"2", "kept for the next script", "  + c  function", "  ~ a  number, set again", "  · b as they were"},
		6:  {"2", "kept for the next script", "  ~ b  string of 2 bytes, was string of 1 bytes", "  · a as they were"},
		12: {"error: the script was stopped: what the scripts before it", "kept is gone", "kept for the next script", "  − a  gone", "  − c  gone"},
		17: {"kept for the next script", "  + z  number"},
		19: {"1", "kept for the next script", "  and 41 more"},
	} {
		if got := stripped(resultLines(tr.results[i], 60)); !slices.Equal(got, want) {
			t.Errorf("%d: %q,\nwant %q", i, got, want)
		}
	}
	r := &resultTrace{text: "\tx", added: []keptVar{{"long", "aaa bbb ccc"}}, changed: []keptChange{{"ch", "a", "b"}}, gone: []string{"s"}}
	want := []string{"    x", "kept for the next script", "  + long  aaa bbb", "          ccc", "  ~ ch    b, was a", "  − s     gone"}
	if got := stripped(resultLines(r, 18)); !slices.Equal(got, want) {
		t.Fatalf("%q,\nwant %q", got, want)
	}
	if lines := resultLines(r, 18); !strings.HasPrefix(lines[2], "  "+lipgloss.NewStyle().Foreground(good).Render("+")+" "+varStyle.Render("long")) ||
		!strings.HasPrefix(lines[4], "  "+literalStyle.Render("~")) || lines[5] != "  "+errStyle.Render("−")+" "+varStyle.Render("s")+"     "+errStyle.Render("gone") {
		t.Fatalf("what was set, set again and is gone should stand out: %q", lines)
	}
}

// A variable whose places do not fit on its line goes on below them.
func TestRenderVariablesWraps(t *testing.T) {
	vars := []*variable{{name: "long", what: "x", set: []int{1, 2, 3}, used: []int{4}, gone: 5}, {name: "s", what: "number", set: []int{9}}}
	got := stripped(strings.Split(renderVariables(vars, 44, true), "\n"))[1:]
	want := []string{"▎ long  x       set #1 #2 #3 · used #4 ·", "▎               gone #5", "▎ s     number  set #9"}
	if !slices.Equal(got, want) {
		t.Fatalf("%q,\nwant %q", got, want)
	}
}
