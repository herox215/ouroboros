package tui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image/color"
	"regexp"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// How the context screen shows a context: not as the chat shows a
// conversation, but as what the LLM is sent. Every message has a head: a
// badge of its role in the color of the role, a rule, and its size at the
// end. Its lines hang on a bar of that color. The text stays what it is;
// only how it is laid out and colored differs. The functions a tool offers
// and their parameters stand out, its schema is indented JSON, and the
// arguments of a call are taken apart: a text is shown as the lines it has,
// not as a string of JSON. A note in brackets, like the one where an answer
// broke off, recedes.

// bar is what the lines of a message hang on. It is no part of what is
// searched.
const bar = "▎"

var (
	keyStyle     = lipgloss.NewStyle().Foreground(accent) // a parameter, an argument, a key of JSON
	stringStyle  = lipgloss.NewStyle().Foreground(good)   // a text in JSON
	literalStyle = lipgloss.NewStyle().Foreground(warn)   // a number, true, false or null in JSON
	noteStyle    = mutedStyle.Italic(true)                // a note in brackets
)

// roleColors are the colors of the roles of messages, in the palette of the
// GUI: what a tool returned in the muted color. Set by applyPalette.
var roleColors map[string]color.Color

// roleColor is the color of the messages of role: a tool the LLM is offered
// stands out, a role that is not known is muted.
func roleColor(role string) color.Color {
	if c, ok := roleColors[role]; ok {
		return c
	}
	if strings.HasPrefix(role, "tool ") {
		return warn
	}
	return muted
}

// renderContext lays out the messages of a context width cells wide, a
// block each.
func renderContext(msgs []Message, width int, light bool) []string {
	var blocks []string
	for i, c := range msgs {
		blocks = append(blocks, renderMessage(c, i, width, light))
	}
	return blocks
}

// renderMessage lays out the message i of a context width cells wide: its
// head, with its number and its size, and its lines on the bar.
func renderMessage(c Message, i int, width int, light bool) string {
	return block(c.Role, roleColor(c.Role), fmt.Sprintf("#%d", i+1), byteSize(len(c.Text)), messageLines(c, width-2), width, light)
}

// block lays out a block of the context screen width cells wide: a head of a
// badge with label in col, a note after it, a rule and end, and lines that
// hang on a bar of col, two cells narrower.
func block(label string, col color.Color, note, end string, lines []string, width int, light bool) string {
	badge := lipgloss.NewStyle().Foreground(ui.background).Background(col).Bold(true).Padding(0, 1).Render(label)
	note, end = " "+note, " "+end
	rule := strings.Repeat("─", max(0, width-lipgloss.Width(badge)-lipgloss.Width(note)-lipgloss.Width(end)-1))
	out := []string{badge + mutedStyle.Render(note) + lipgloss.NewStyle().Foreground(borderMuted(light)).Render(" "+rule) + mutedStyle.Render(end)}
	edge := lipgloss.NewStyle().Foreground(col).Render(bar) + " "
	for _, l := range lines {
		out = append(out, edge+l)
	}
	return strings.Join(out, "\n")
}

// byteSize is how long a message is: in bytes below a KiB, in KiB from there
// on.
func byteSize(n int) string {
	if n == 1 {
		return "1 byte"
	}
	if n < 1024 {
		return fmt.Sprintf("%d bytes", n)
	}
	return fmt.Sprintf("%.1f KiB", float64(n)/1024)
}

// messageLines lays out text width cells wide, with tool definitions and
// the assistant's calls formatted for readability.
func messageLines(c Message, width int) []string {
	text := strings.ReplaceAll(c.Text, "\t", "    ")
	if c.Role == "assistant" {
		return callLines(text, width)
	}
	if strings.HasPrefix(c.Role, "tool ") {
		return toolLines(text, width)
	}
	return textLines(text, width)
}

// textLines wraps text; a line that is a note in brackets recedes.
func textLines(text string, width int) []string {
	var lines []string
	for _, l := range strings.Split(text, "\n") {
		if strings.HasPrefix(l, "[") && strings.HasSuffix(l, "]") {
			l = noteStyle.Render(l)
		}
		lines = append(lines, hang("", "", l, width)...)
	}
	return lines
}

// hang wraps text width cells wide: its first line goes after first, the
// others after rest, which is as wide.
func hang(first, rest, text string, width int) []string {
	lines := strings.Split(lipgloss.Wrap(text, max(1, width-lipgloss.Width(first)), ""), "\n")
	for i := range lines {
		prefix := rest
		if i == 0 {
			prefix = first
		}
		lines[i] = prefix + lines[i]
	}
	return lines
}

var (
	// function is a line of the description of a tool that names a function
	// it offers: "- name(signature): what it does".
	function = regexp.MustCompile(`^- ([\w.]+)(\(.*?\))(.*)$`)
	// parameter is an indented line that names a parameter: "name: what it is".
	parameter = regexp.MustCompile(`^(\s+)([\w.?]+)(: .*)$`)
)

// toolLines lays out what a tool the LLM is offered is: its description,
// the functions it names and their parameters standing out, and the schema
// of its parameters as indented JSON.
func toolLines(text string, width int) []string {
	description, schema := text, ""
	const label = "\nparameters: "
	if i := strings.LastIndex(text, label); i >= 0 {
		description, schema = text[:i], text[i+len(label):]
	}
	var lines []string
	for _, l := range strings.Split(description, "\n") {
		if f := function.FindStringSubmatch(l); f != nil {
			lines = append(lines, hang("- ", "  ", boldStyle.Render(f[1])+mutedStyle.Render(f[2])+f[3], width)...)
		} else if p := parameter.FindStringSubmatch(l); p != nil {
			lines = append(lines, hang(p[1], p[1]+"  ", keyStyle.Render(p[2])+p[3], width)...)
		} else {
			lines = append(lines, hang("", "", l, width)...)
		}
	}
	if schema != "" {
		lines = append(lines, mutedStyle.Render("parameters"))
		lines = append(lines, jsonLines(schema, width)...)
	}
	return lines
}

// jsonLines lays out JSON indented and colored, a line that is too long
// hanging below its key; what is no JSON as it is.
func jsonLines(raw string, width int) []string {
	var b bytes.Buffer
	if json.Indent(&b, []byte(raw), "", "  ") != nil {
		return hang("", "", raw, width)
	}
	var lines []string
	for _, l := range strings.Split(b.String(), "\n") {
		indent := strings.Repeat(" ", len(l)-len(strings.TrimLeft(l, " "))+2)
		lines = append(lines, hang("", indent, colorJSON(l), width)...)
	}
	return lines
}

// jsonToken is a string of JSON, with the colon after it when it is a key,
// or a number, true, false or null.
var jsonToken = regexp.MustCompile(`"(?:[^"\\]|\\.)*":?|true|false|null|-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?`)

// colorJSON colors the keys, the strings and the other values of JSON
// without white space between a key and its colon.
func colorJSON(s string) string {
	return jsonToken.ReplaceAllStringFunc(s, func(t string) string {
		if key, ok := strings.CutSuffix(t, ":"); ok {
			return keyStyle.Render(key) + ":"
		}
		if strings.HasPrefix(t, `"`) {
			return stringStyle.Render(t)
		}
		return literalStyle.Render(t)
	})
}

// callLines lays out what the agent says and the calls of tools it makes,
// each a line "→ name arguments".
func callLines(text string, width int) []string {
	parts := strings.Split("\n"+text, "\n→ ")
	var lines []string
	if words := strings.TrimPrefix(parts[0], "\n"); words != "" {
		lines = textLines(words, width)
	}
	for _, call := range parts[1:] {
		lines = append(lines, callLine(call, width)...)
	}
	return lines
}

// callLine lays out a call of a tool: "→ name", and its arguments below it,
// one a line and their values in a column. A text is shown as the lines it
// has; other values, and arguments that are no object of JSON, as JSON.
func callLine(call string, width int) []string {
	name, args, _ := strings.Cut(call, " ")
	head := lipgloss.NewStyle().Foreground(peer).Render("→ ") + boldStyle.Render(name)
	fields, ok := objectFields(args)
	if !ok {
		return hang("", "  ", strings.TrimRight(head+" "+args, " "), width)
	}
	lines := hang("", "  ", head, width)
	column := 0
	for _, f := range fields {
		column = max(column, ansi.StringWidth(f.key))
	}
	for _, f := range fields {
		first := "  " + keyStyle.Render(f.key) + strings.Repeat(" ", column-ansi.StringWidth(f.key)+2)
		rest := strings.Repeat(" ", column+4)
		var s string
		if json.Unmarshal(f.value, &s) == nil && s != "" {
			lines = append(lines, hang(first, rest, strings.ReplaceAll(s, "\t", "    "), width)...)
			continue
		}
		var b bytes.Buffer
		_ = json.Compact(&b, f.value) // it was decoded: it is JSON
		lines = append(lines, hang(first, rest, colorJSON(b.String()), width)...)
	}
	return lines
}

// field is a field of an object of JSON.
type field struct {
	key   string
	value json.RawMessage
}

// objectFields takes an object of JSON apart into its fields, in the order
// they have; false when s is no object of JSON, or has more after it.
func objectFields(s string) ([]field, bool) {
	dec := json.NewDecoder(strings.NewReader(s))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, false
	}
	var fields []field
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, false
		}
		key, _ := t.(string) // where a key is, a token is a string or an error
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, false
		}
		fields = append(fields, field{key, value})
	}
	if _, err := dec.Token(); err != nil || dec.More() {
		return nil, false
	}
	return fields, true
}
