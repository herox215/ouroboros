package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// plainMarkdown is src formatted, without the styles and the spaces at the
// ends of the lines.
func plainMarkdown(src string, width int) string {
	lines := strings.Split(ansi.Strip(renderMarkdown(src, width)), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	return strings.Join(lines, "\n")
}

// The marks of markdown are not shown, what they mean is.
func TestMarkdownBlocks(t *testing.T) {
	src := "# Title\n\n### Small\n\nSome **bold** and *italic*, `code`, ~~gone~~,\nsoft  \nhard.\n\n" +
		"- one\n- two\n  - nested\n- [x] done\n- [ ] open\n\n3. three\n4. four\n\n" +
		"- loose\n\n- items\n\n> quoted\n>\n> twice\n\n```go\nfunc main() {}\n```\n\n    indented\n\n---\n\n" +
		"<div>\nhtml\n</div>\n\n| Name | Value |\n|---|---|\n| a | **1** |\n"
	want := strings.Join([]string{
		"Title", "", "Small", "", "Some bold and italic, code, gone, soft", "hard.", "",
		"• one", "• two", "  • nested", "• ☑ done", "• ☐ open", "", "3. three", "4. four", "",
		"• loose", "", "• items", "", "│ quoted", "│", "│ twice", "",
		"  go", "  func main() {}", "", "  indented", "", strings.Repeat("─", 40), "",
		"<div>", "html", "</div>", "",
		"┌──────┬───────┐", "│ Name │ Value │", "├──────┼───────┤", "│ a    │ 1     │", "└──────┴───────┘",
	}, "\n")
	if got := plainMarkdown(src, 40); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	// The styles stand for the marks, also one inside another.
	styled := renderMarkdown("**bold `code` on**", 40)
	if !strings.Contains(styled, "\x1b[1m") || !strings.Contains(styled, "\x1b[1;33m") || !strings.HasSuffix(styled, "\x1b[1m on\x1b[m") {
		t.Fatalf("styles %q", styled)
	}
}

func TestMarkdownLinks(t *testing.T) {
	for src, want := range map[string]string{
		"[docs](https://a.b/c)":      "docs (https://a.b/c)",
		"[https://a.b](https://a.b)": "https://a.b",
		"<https://a.b>":              "https://a.b",
		"see https://a.b":            "see https://a.b",
		"![logo](logo.png)":          "[image: logo] logo.png",
		"a <b>raw</b> tag":           "a <b>raw</b> tag",
		"&copy; \\*":                 "© *",
		"`&copy; \\*`":               "&copy; \\*",
	} {
		if got := plainMarkdown(src, 60); got != want {
			t.Errorf("%q: got %q, want %q", src, got, want)
		}
	}
}

// Text is wrapped to the width, below a bullet indented; a table that is too
// wide is made narrower, and so are long lines of code.
func TestMarkdownWraps(t *testing.T) {
	if got := plainMarkdown("- one two three four", 10); got != "• one two\n  three\n  four" {
		t.Fatalf("list %q", got)
	}
	table := plainMarkdown("| a | b |\n|---|---|\n| one two three four | five six seven |\n", 20)
	for _, l := range strings.Split(table, "\n") {
		if ansi.StringWidth(l) > 20 {
			t.Fatalf("table wider than 20:\n%s", table)
		}
	}
	if got := plainMarkdown("```\n0123456789\n```", 8); got != "  012345\n  6789" {
		t.Fatalf("code %q", got)
	}
	if got := plainMarkdown("", 0); got != "" {
		t.Fatalf("nothing %q", got)
	}
}

// The chat shows the answers of the agent formatted, and what the owner
// wrote as it was written.
func TestChatShowsMarkdown(t *testing.T) {
	m, b := newModel(t)
	b.reply = "**done**: `x`"
	answered(t, m, say(t, m, "**as is**"))
	view := trimmedLines(m)
	if !strings.Contains(view, "**as is**") || !strings.Contains(view, "done: x") || strings.Contains(view, "**done**") {
		t.Fatalf("view\n%s", view)
	}
	m.Update(tea.WindowSizeMsg{Width: 60, Height: 30})
	if !strings.Contains(trimmedLines(m), "done: x") {
		t.Fatal("the answer should stay formatted in another width")
	}
}
