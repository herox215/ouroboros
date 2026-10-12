// Package printable makes what other nodes say of themselves fit to be shown:
// a name or a description that came over the network is shown in terminals,
// in the log and to the LLM, and a control character in it would move the
// cursor, set the title of a terminal or turn text around.
package printable

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// zwj joins emoji into one, and zwnj keeps letters apart that would join,
// as Persian writes them: they are format characters all the same.
const (
	zwj  = '\u200d'
	zwnj = '\u200c'
)

// Line returns s as one line of at most max characters: without control
// characters, a line break and a tab becoming a space, and without the
// format characters that hide text or turn it around; what is not UTF-8 is
// left out.
func Line(s string, max int) string {
	s = strings.ToValidUTF8(s, "")
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			return ' '
		case unicode.IsControl(r) || unicode.Is(unicode.Cf, r) && r != zwj && r != zwnj:
			return -1
		}
		return r
	}, s)
	if utf8.RuneCountInString(s) > max {
		s = string([]rune(s)[:max])
	}
	return s
}
