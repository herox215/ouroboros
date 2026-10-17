package tui

import (
	"encoding/json"
	"fmt"
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
	lines := strings.Split(renderMessage(Message{"user", "hi\nthere"}, 0, 30, false), "\n")
	want := []string{" user  #1 " + strings.Repeat("─", 12) + " 8 bytes", "▎ hi", "▎ there"}
	if got := stripped(lines); !slices.Equal(got, want) {
		t.Fatalf("%q, want %q", got, want)
	}
	badge := lipgloss.NewStyle().Foreground(lipgloss.Color("0")).Background(accent).Bold(true).Padding(0, 1).Render("user")
	if !strings.HasPrefix(lines[0], badge) || !strings.HasPrefix(lines[1], lipgloss.NewStyle().Foreground(accent).Render(bar)) {
		t.Fatalf("the badge and the bar should have the color of the role: %q", lines[:2])
	}
	if got := stripped(strings.Split(renderMessage(Message{"tool", "aaa bbb ccc"}, 0, 9, true), "\n")); !slices.Equal(got[1:], []string{"▎ aaa bbb", "▎ ccc"}) {
		t.Fatalf("the lines should be wrapped two cells narrower: %q", got)
	}
	if got := ansi.Strip(renderMessage(Message{"assistant", "x"}, 9, 10, false)); got != " assistant  #10  1 byte\n▎ x" {
		t.Fatalf("on a narrow screen: %q", got)
	}
}

// The user and the agent have the colors of the chat; every other role has
// its own, and one that is not known is muted.
func TestRoleColor(t *testing.T) {
	for role, want := range map[string]color.Color{
		"user": accent, "assistant": peer, "system": lipgloss.Color("7"), "tool": good,
		"tool read": lipgloss.Color("4"), "plugin diagnostic": bad, "developer": muted,
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
		{Message{"tool read", "- f(): y"}, []string{"- f(): y"}},
	} {
		if got := stripped(messageLines(tc.c, 20)); !slices.Equal(got, tc.want) {
			t.Errorf("%v: %q, want %q", tc.c, got, tc.want)
		}
	}
	if got := messageLines(Message{"tool read", "- f(): y"}, 20); !strings.HasPrefix(got[0], "- "+boldStyle.Render("f")) {
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
		if got := stripped(callLines(tc.text, tc.width)); !slices.Equal(got, tc.want) {
			t.Errorf("%q: %q,\nwant %q", tc.text, got, tc.want)
		}
	}
	lines := callLines(`→ read {"path":"/a","limit":20}`, 40)
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

// Direct calls and archived calls are displayed without interpreter state.
func TestRenderContext(t *testing.T) {
	msgs := []Message{
		{"tool read", "Read a file.\nparameters: {}"},
		{"assistant", `→ read {"path":"/a"}`},
		{"tool", "contents"},
		{"assistant", `→ run {"script":"const a = 1; print(a)"}`},
		{"tool", "1\n[kept for the next script: a (number)]"},
		{"assistant", `→ unknown {"script":"print(a)"}`},
	}
	blocks := renderContext(msgs, 80, false)
	if len(blocks) != len(msgs) {
		t.Fatalf("want one block per message, got %d", len(blocks))
	}
	for i, msg := range msgs {
		if want := fmt.Sprintf(" %s  #%d ", msg.Role, i+1); !strings.HasPrefix(ansi.Strip(blocks[i]), want) {
			t.Errorf("message %d: %q", i, blocks[i])
		}
	}
	for i, want := range map[int]string{
		1: "▎   path  /a",
		3: "▎   script  const a = 1; print(a)",
		4: "▎ [kept for the next script: a (number)]",
		5: "▎   script  print(a)",
	} {
		if !strings.Contains(ansi.Strip(blocks[i]), want) {
			t.Errorf("message %d lost its text: %q", i, blocks[i])
		}
	}
	// 'script' is an ordinary string argument, regardless of the tool name.
	run := callLines(`→ run {"script":"print(a)"}`, 80)
	unknown := callLines(`→ unknown {"script":"print(a)"}`, 80)
	if !slices.Equal(run[1:], unknown[1:]) || len(run) != 2 {
		t.Fatalf("archived run has special rendering: %q vs %q", run, unknown)
	}
}
