package matrix

import (
	"fmt"
	"html"
	"net/url"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	gmtext "github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// markdown parses the answers of the agent, as the TUI reads them. Task lists
// are left out: Matrix has no checkbox to carry them.
var markdownParser = goldmark.New(goldmark.WithExtensions(extension.Table, extension.Strikethrough, extension.Linkify)).Parser()

// formatHTML renders markdown as the subset of HTML that the Matrix
// specification permits in formatted_body, or "" when it holds no text. The
// message keeps its plain body, so a client that renders no HTML shows the
// markdown itself. Raw HTML in the answer is shown as the text it is, never
// interpreted, and only the tags below are ever written.
func formatHTML(text string) string {
	source := []byte(text)
	r := htmlRenderer{source: source}
	r.blocks(markdownParser.Parse(gmtext.NewReader(source)))
	return strings.TrimSpace(r.buf.String())
}

type htmlRenderer struct {
	source []byte
	buf    strings.Builder
}

func (r *htmlRenderer) write(s string) { r.buf.WriteString(s) }

// blocks writes the block children of n, one after the other.
func (r *htmlRenderer) blocks(n ast.Node) {
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		r.block(c)
	}
}

func (r *htmlRenderer) block(n ast.Node) {
	switch n := n.(type) {
	case *ast.Heading:
		tag := "h" + fmt.Sprint(n.Level)
		r.write("<" + tag + ">")
		r.inlines(n)
		r.write("</" + tag + ">")
	case *ast.Paragraph:
		r.write("<p>")
		r.inlines(n)
		r.write("</p>")
	case *ast.TextBlock: // a paragraph in a tight list item
		r.inlines(n)
	case *ast.List:
		r.list(n)
	case *ast.Blockquote:
		r.write("<blockquote>")
		r.blocks(n)
		r.write("</blockquote>")
	case *ast.FencedCodeBlock:
		r.codeBlock(n.Lines(), string(n.Language(r.source)))
	case *ast.CodeBlock:
		r.codeBlock(n.Lines(), "")
	case *ast.ThematicBreak:
		r.write("<hr>")
	case *east.Table:
		r.table(n)
	case *ast.HTMLBlock:
		// Raw HTML is not interpreted: it is shown as the text it is.
		r.write("<p>")
		for i := range n.Lines().Len() {
			if i > 0 {
				r.write("<br>")
			}
			segment := n.Lines().At(i)
			r.write(escape(segment.Value(r.source)))
		}
		r.write("</p>")
	default:
		r.blocks(n)
	}
}

func (r *htmlRenderer) list(n *ast.List) {
	tag := "ul"
	if n.IsOrdered() {
		tag = "ol"
		if n.Start > 1 {
			r.write(fmt.Sprintf(`<ol start="%d">`, n.Start))
		} else {
			r.write("<ol>")
		}
	} else {
		r.write("<ul>")
	}
	for item := n.FirstChild(); item != nil; item = item.NextSibling() {
		r.write("<li>")
		r.blocks(item)
		r.write("</li>")
	}
	r.write("</" + tag + ">")
}

// codeBlock is a block of code, in the language as a class when the fence
// named one, as the specification permits for syntax highlighting.
func (r *htmlRenderer) codeBlock(lines *gmtext.Segments, language string) {
	r.write("<pre><code")
	if language = languageClass(language); language != "" {
		r.write(` class="language-` + language + `"`)
	}
	r.write(">")
	for i := range lines.Len() {
		segment := lines.At(i)
		r.write(escape(segment.Value(r.source)))
	}
	r.write("</code></pre>")
}

// languageClass keeps what a language name may be, as a class value that
// needs no escaping.
func languageClass(language string) string {
	var b strings.Builder
	for _, c := range language {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("+-#_.", c) {
			b.WriteRune(c)
		}
		if b.Len() == 24 {
			break
		}
	}
	return b.String()
}

func (r *htmlRenderer) table(n *east.Table) {
	r.write("<table>")
	body := false
	for row := n.FirstChild(); row != nil; row = row.NextSibling() {
		if row.Kind() == east.KindTableHeader {
			r.write("<thead><tr>")
			r.cells(row, "th")
			r.write("</tr></thead>")
			continue
		}
		if !body {
			r.write("<tbody>")
			body = true
		}
		r.write("<tr>")
		r.cells(row, "td")
		r.write("</tr>")
	}
	if body {
		r.write("</tbody>")
	}
	r.write("</table>")
}

func (r *htmlRenderer) cells(row ast.Node, tag string) {
	for cell := row.FirstChild(); cell != nil; cell = cell.NextSibling() {
		r.write("<" + tag + ">")
		r.inlines(cell)
		r.write("</" + tag + ">")
	}
}

func (r *htmlRenderer) inlines(n ast.Node) {
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		r.inline(c)
	}
}

func (r *htmlRenderer) inline(n ast.Node) {
	switch n := n.(type) {
	case *ast.Text:
		value := n.Segment.Value(r.source)
		if !n.IsRaw() {
			// As goldmark's HTML writer does: references and escapes resolved.
			value = util.ResolveNumericReferences(util.ResolveEntityNames(util.UnescapePunctuations(value)))
		}
		r.write(escape(value))
		if n.HardLineBreak() || n.SoftLineBreak() {
			// A line of the answer stays a line: clients would show a soft
			// break as the space that HTML wraps a newline to, while the body
			// keeps the line the model wrote.
			r.write("<br>")
		}
	case *ast.String:
		r.write(escape(n.Value))
	case *ast.CodeSpan:
		r.write("<code>")
		r.codeText(n)
		r.write("</code>")
	case *ast.Emphasis:
		tag := "em"
		if n.Level >= 2 {
			tag = "strong"
		}
		r.write("<" + tag + ">")
		r.inlines(n)
		r.write("</" + tag + ">")
	case *east.Strikethrough:
		r.write("<del>")
		r.inlines(n)
		r.write("</del>")
	case *ast.Link:
		r.link(string(n.Destination), func() { r.inlines(n) })
	case *ast.AutoLink:
		label := string(n.Label(r.source))
		r.link(string(n.URL(r.source)), func() { r.write(escape([]byte(label))) })
	case *ast.Image:
		alt := string(n.Text(r.source))
		if alt == "" {
			alt = string(n.Destination)
		}
		r.link(string(n.Destination), func() { r.write(escape([]byte(alt))) })
	case *ast.RawHTML:
		// As a block of raw HTML: shown as text, never interpreted.
		for i := range n.Segments.Len() {
			segment := n.Segments.At(i)
			r.write(escape(segment.Value(r.source)))
		}
	default:
		r.inlines(n)
	}
}

// codeText writes the content of a code span: no reference is resolved and no
// escape is taken back, only HTML is escaped.
func (r *htmlRenderer) codeText(n ast.Node) {
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		switch c := c.(type) {
		case *ast.Text:
			r.write(escape(c.Segment.Value(r.source)))
		case *ast.String:
			r.write(escape(c.Value))
		default:
			r.codeText(c)
		}
	}
}

// link writes label as a link to href, or the label alone when href is not a
// link the specification permits: no relative target and only its schemes.
func (r *htmlRenderer) link(href string, label func()) {
	if !safeHref(href) {
		label()
		return
	}
	r.write(`<a href="` + escape([]byte(href)) + `">`)
	label()
	r.write("</a>")
}

func safeHref(href string) bool {
	u, err := url.Parse(href)
	if err != nil || u.Scheme == "" {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "https", "http", "ftp", "mailto", "magnet":
		return true
	}
	return false
}

// escape is text as HTML text; a NUL, which no HTML can carry, becomes the
// replacement character.
func escape(value []byte) string {
	return html.EscapeString(strings.ReplaceAll(string(value), "\x00", "\uFFFD"))
}
