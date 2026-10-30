package matrix

import (
	"strings"
	"unicode/utf8"
)

// Leave room for encrypted event overhead below Matrix's event-size limit.
const messageLimit = 12000

// messageParts splits text into parts that fit an encrypted Matrix event. A
// part ends at a line break where it can, so that no word and no line is cut.
// A fenced code block that a boundary falls into is closed in the part before
// and opened again in the next, so that both parts render as code.
func messageParts(text string) []string {
	var parts []string
	var b strings.Builder
	fence := "" // the opening fence line of the code block the part is in

	flush := func() {
		if b.Len() == 0 {
			return
		}
		part := b.String()
		if fence != "" {
			if !strings.HasSuffix(part, "\n") {
				part += "\n"
			}
			part += fenceClose(fence) + "\n"
		}
		parts = append(parts, part)
		b.Reset()
		if fence != "" {
			b.WriteString(fence + "\n")
		}
	}
	for text != "" {
		line, rest := cutLine(text)
		text = rest
		room := messageLimit - b.Len() - fenceRoom(fence)
		if room < 1 {
			flush()
			room = messageLimit - b.Len() - fenceRoom(fence)
		}
		// A line longer than a part is cut, as no event can hold it.
		split := false
		for len(line) > room {
			n := room
			for n > 0 && !utf8.RuneStart(line[n]) {
				n--
			}
			if n == 0 { // no rune starts before the limit: no UTF-8, split at it
				n = room
			}
			b.WriteString(line[:n])
			line = line[n:]
			split = true
			flush()
			room = max(1, messageLimit-b.Len()-fenceRoom(fence))
		}
		b.WriteString(line)
		if split {
			// A cut line is no fence line, whatever its end looks like.
			continue
		}
		if fence == "" {
			fence = fenceOpen(line)
		} else if fenceEnds(line, fence) {
			fence = ""
		}
	}
	if b.Len() > 0 {
		parts = append(parts, b.String())
	}
	return parts
}

// cutLine takes the first line of text with its line break, and the rest.
func cutLine(text string) (string, string) {
	if i := strings.IndexByte(text, '\n'); i >= 0 {
		return text[:i+1], text[i+1:]
	}
	return text, ""
}

// fenceOpen is line without its indentation when it opens a fenced code block,
// and "" otherwise. A fence line is short; a long run of backticks is text.
func fenceOpen(line string) string {
	s := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	trimmed := strings.TrimLeft(s, " ")
	if len(s)-len(trimmed) > 3 || trimmed == "" || len(trimmed) > 100 {
		return ""
	}
	c := trimmed[0]
	if c != '`' && c != '~' {
		return ""
	}
	n := 0
	for n < len(trimmed) && trimmed[n] == c {
		n++
	}
	if n < 3 || (c == '`' && strings.IndexByte(trimmed[n:], '`') >= 0) {
		return ""
	}
	return trimmed
}

// fenceEnds reports whether line closes the code block that fence opened.
func fenceEnds(line, fence string) bool {
	s := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	trimmed := strings.TrimLeft(s, " ")
	if len(s)-len(trimmed) > 3 {
		return false
	}
	n := 0
	for n < len(trimmed) && trimmed[n] == fence[0] {
		n++
	}
	return n >= fenceLen(fence) && strings.TrimSpace(trimmed[n:]) == ""
}

// fenceLen is the length of the run of markers that opens fence.
func fenceLen(fence string) int {
	n := 0
	for n < len(fence) && fence[n] == fence[0] {
		n++
	}
	return n
}

// fenceClose is the line that closes the code block that fence opened.
func fenceClose(fence string) string { return strings.Repeat(fence[:1], fenceLen(fence)) }

// fenceRoom is the space a part has to leave to close the code block it is in.
func fenceRoom(fence string) int {
	if fence == "" {
		return 0
	}
	return len(fenceClose(fence)) + 2 // the line breaks around it
}
