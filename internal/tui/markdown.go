package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/table"
	"github.com/charmbracelet/x/ansi"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// The answers of the agent are markdown, as the models write it. The chat
// shows them formatted: headings, emphasis, code, lists, quotes, tables and
// links in the colors of the terminal theme, and without the marks.

// codeColor is the color of code, in a line and in a block.
var codeColor = lipgloss.Color("3")

var markdown = goldmark.New(goldmark.WithExtensions(extension.GFM))

// renderMarkdown is src formatted for the chat, wrapped to width.
func renderMarkdown(src string, width int) string {
	source := []byte(src)
	r := mdRenderer{source: source}
	doc := markdown.Parser().Parse(text.NewReader(source))
	return strings.Join(r.blocks(doc, max(1, width)), "\n")
}

type mdRenderer struct {
	source []byte
}

// blocks are the lines of the children of n, a blank line between two of
// them unless n is a tight list item.
func (r mdRenderer) blocks(n ast.Node, width int) []string {
	var lines []string
	tight := false
	if item, ok := n.Parent().(*ast.List); ok && n.Kind() == ast.KindListItem {
		tight = item.IsTight
	}
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		if len(lines) > 0 && !tight {
			lines = append(lines, "")
		}
		lines = append(lines, r.block(c, width)...)
	}
	return lines
}

// block is the lines of the block n.
func (r mdRenderer) block(n ast.Node, width int) []string {
	switch n := n.(type) {
	case *ast.Heading:
		style := boldStyle
		if n.Level <= 2 {
			style = style.Foreground(accent)
		}
		return r.wrap(r.inlines(n, style), width)
	case *ast.List:
		return r.list(n, width)
	case *ast.Blockquote:
		lines := r.blocks(n, width-2)
		for i, l := range lines {
			lines[i] = mutedStyle.Render("│ ") + l
		}
		return lines
	case *ast.FencedCodeBlock, *ast.CodeBlock:
		return r.code(n, width)
	case *ast.ThematicBreak:
		return []string{mutedStyle.Render(strings.Repeat("─", width))}
	case *ast.HTMLBlock:
		return r.lines(n)
	case *east.Table:
		return r.table(n, width)
	}
	// A paragraph, or a block of text in a tight list.
	return r.wrap(r.inlines(n, lipgloss.NewStyle()), width)
}

// list is the lines of a list: every item with its bullet or number, its
// further lines indented below it.
func (r mdRenderer) list(n *ast.List, width int) []string {
	var lines []string
	number := n.Start
	for item := n.FirstChild(); item != nil; item = item.NextSibling() {
		if len(lines) > 0 && !n.IsTight {
			lines = append(lines, "")
		}
		bullet := "• "
		if n.IsOrdered() {
			bullet = fmt.Sprintf("%d. ", number)
			number++
		}
		pad := strings.Repeat(" ", ansi.StringWidth(bullet))
		item := r.blocks(item, width-len(pad))
		for i, l := range item {
			if i == 0 {
				l = mutedStyle.Render(bullet) + l
			} else if l != "" {
				l = pad + l
			}
			lines = append(lines, l)
		}
	}
	return lines
}

// code is the lines of a block of code, with its language above it.
func (r mdRenderer) code(n ast.Node, width int) []string {
	var lines []string
	if f, ok := n.(*ast.FencedCodeBlock); ok {
		if lang := string(f.Language(r.source)); lang != "" {
			lines = append(lines, mutedStyle.Render(lang))
		}
	}
	style := lipgloss.NewStyle().Foreground(codeColor)
	for _, l := range r.lines(n) {
		for _, l := range strings.Split(ansi.Hardwrap(l, max(1, width-2), true), "\n") {
			lines = append(lines, style.Render(l))
		}
	}
	return indent(lines, "  ")
}

// lines is the raw lines of n, a block of code or of HTML.
func (r mdRenderer) lines(n ast.Node) []string {
	var lines []string
	segments := n.Lines()
	for i := range segments.Len() {
		s := segments.At(i)
		lines = append(lines, strings.TrimRight(string(s.Value(r.source)), "\n"))
	}
	return lines
}

// table is the lines of a table, as wide as its cells or as width.
func (r mdRenderer) table(n *east.Table, width int) []string {
	var head []string
	var rows [][]string
	for row := n.FirstChild(); row != nil; row = row.NextSibling() {
		var cells []string
		for cell := row.FirstChild(); cell != nil; cell = cell.NextSibling() {
			cells = append(cells, r.inlines(cell, lipgloss.NewStyle()))
		}
		if row.Kind() == east.KindTableHeader {
			head = cells
			continue
		}
		rows = append(rows, cells)
	}
	t := table.New().Border(lipgloss.NormalBorder()).BorderStyle(mutedStyle).Headers(head...).Rows(rows...).
		StyleFunc(func(row, _ int) lipgloss.Style {
			if row == table.HeaderRow {
				return boldStyle.Padding(0, 1)
			}
			return lipgloss.NewStyle().Padding(0, 1)
		})
	if lipgloss.Width(t.Render()) > width {
		t.Width(width)
	}
	return strings.Split(t.Render(), "\n")
}

// wrap breaks s into lines of width.
func (r mdRenderer) wrap(s string, width int) []string {
	return strings.Split(lipgloss.Wrap(s, width, ""), "\n")
}

// inlines is the text of the children of n, in style and in the styles of
// the children. Only text is rendered, so that a style inside another does
// not end the outer one.
func (r mdRenderer) inlines(n ast.Node, style lipgloss.Style) string {
	var b strings.Builder
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		b.WriteString(r.inline(c, style))
	}
	return b.String()
}

func (r mdRenderer) inline(n ast.Node, style lipgloss.Style) string {
	switch n := n.(type) {
	case *ast.Text:
		value := n.Value(r.source)
		if !n.IsRaw() {
			value = util.ResolveNumericReferences(util.ResolveEntityNames(util.UnescapePunctuations(value)))
		}
		s := style.Render(string(value))
		switch {
		case n.HardLineBreak():
			s += "\n"
		case n.SoftLineBreak():
			s += " "
		}
		return s
	case *ast.CodeSpan:
		return r.inlines(n, style.Foreground(codeColor))
	case *ast.Emphasis:
		style = style.Italic(n.Level == 1).Bold(n.Level > 1)
	case *east.Strikethrough:
		return r.inlines(n, style.Strikethrough(true))
	case *ast.Link:
		label := r.inlines(n, style.Foreground(accent).Underline(true))
		if url := string(n.Destination); url != ansi.Strip(label) {
			label += mutedStyle.Render(" (" + url + ")")
		}
		return label
	case *ast.AutoLink:
		return style.Foreground(accent).Underline(true).Render(string(n.URL(r.source)))
	case *ast.Image:
		return mutedStyle.Render("[image: "+ansi.Strip(r.inlines(n, style))+"] ") + mutedStyle.Render(string(n.Destination))
	case *ast.RawHTML:
		var b strings.Builder
		for i := range n.Segments.Len() {
			s := n.Segments.At(i)
			b.Write(s.Value(r.source))
		}
		return style.Render(b.String())
	case *east.TaskCheckBox:
		if n.IsChecked {
			return mutedStyle.Render("☑ ")
		}
		return mutedStyle.Render("☐ ")
	}
	// Emphasis, or an inline of no style of its own.
	return r.inlines(n, style)
}

// indent puts prefix before every line but the empty ones.
func indent(lines []string, prefix string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		if l != "" {
			l = prefix + l
		}
		out[i] = l
	}
	return out
}
