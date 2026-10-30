package matrix

import (
	"strings"
	"testing"
)

func TestFormatHTML(t *testing.T) {
	for _, tc := range []struct {
		name     string
		markdown string
		want     string
	}{
		{"empty", "", ""},
		{"text", "Hallo\n", "<p>Hallo</p>"},
		{"emphasis", "**fett** und *kursiv*", "<p><strong>fett</strong> und <em>kursiv</em></p>"},
		{"strikethrough", "~~weg~~", "<p><del>weg</del></p>"},
		{"code span", "`a < b`", "<p><code>a &lt; b</code></p>"},
		{"code block", "```go\nx := 1 < 2\n```\n", `<pre><code class="language-go">x := 1 &lt; 2` + "\n</code></pre>"},
		{"heading", "# Titel", "<h1>Titel</h1>"},
		{"unordered list", "- a\n- b\n", "<ul><li>a</li><li>b</li></ul>"},
		{"ordered list", "1. a\n2. b\n", "<ol><li>a</li><li>b</li></ol>"},
		{"quote", "> zitat\n", "<blockquote><p>zitat</p></blockquote>"},
		{"link", "[Beispiel](https://example.org)\n", `<p><a href="https://example.org">Beispiel</a></p>`},
		{"relative link", "[relativ](/pfad)\n", "<p>relativ</p>"},
		{"script link", "[böse](javascript:alert(1))\n", "<p>böse</p>"},
		{"escaped text", "a & b < c\n", "<p>a &amp; b &lt; c</p>"},
		{"raw html", "<b>roh</b>\n", "<p>&lt;b&gt;roh&lt;/b&gt;</p>"},
		{"table", "| a | b |\n|---|---|\n| 1 | 2 |\n", "<table><thead><tr><th>a</th><th>b</th></tr></thead><tbody><tr><td>1</td><td>2</td></tr></tbody></table>"},
		{"soft break", "a\nb\n", "<p>a\nb</p>"},
		{"hard break", "a  \nb\n", "<p>a<br>b</p>"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatHTML(tc.markdown); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// An answer's HTML stays within what Matrix permits clients to render: no tag
// outside the subset, and no attribute that could carry script.
func TestFormatHTMLStaysInThePermittedSubset(t *testing.T) {
	for _, markdown := range []string{
		"<script>alert(1)</script>",
		"<img src=x onerror=alert(1)>",
		"[x](javascript:alert(1))",
		"<a href=\"javascript:alert(1)\">x</a>",
		"<div style=\"width:100%\">x</div>",
	} {
		got := formatHTML(markdown)
		for _, forbidden := range []string{"<script", "<img", "<a ", "<div ", `<a href="javascript`, `<img src=`} {
			if strings.Contains(got, forbidden) {
				t.Errorf("%q: %q carries %q", markdown, got, forbidden)
			}
		}
	}
}
