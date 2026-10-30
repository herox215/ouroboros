package matrix

import (
	"strings"
	"unicode/utf8"

	transport "ouroboros/internal/matrix"
)

// messageLimit is the JSON size of the text one encrypted event may carry: the
// event limit is 65536 bytes, and Megolm encryption, its base64 and the
// envelope add about a third. Escapes count, not the text, so a part of
// ampersands holds fewer of them than the same run of plain letters.
const messageLimit = 12000

// messageParts splits text into parts that fit an encrypted Matrix event. A
// part ends at a line break where it can, so that no word and no line is cut.
// A fenced code block that a boundary falls into is closed in the part before
// and opened again in the next, so that both parts render as code.
func messageParts(text string) []string {
	var parts []string
	var b strings.Builder
	fence := "" // the opening fence line of the code block the part is in
	used := 0   // how many bytes of the event's JSON the text in b takes
	room := func() int { return messageLimit - used - fenceRoom(fence) }

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
		used = 0
		if fence != "" {
			b.WriteString(fence + "\n")
			used = transport.EncodedLen(fence + "\n")
		}
	}
	for text != "" {
		line, rest := cutLine(text)
		text = rest
		if transport.EncodedLen(line) > room() {
			flush()
		}
		// A line longer than a part is cut, as no event can hold it.
		split := false
		for transport.EncodedLen(line) > room() {
			n := cutEncoded(line, room())
			b.WriteString(line[:n])
			used += transport.EncodedLen(line[:n])
			line = line[n:]
			split = true
			flush()
		}
		b.WriteString(line)
		used += transport.EncodedLen(line)
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

// cutEncoded is how many bytes of line fit into room as the JSON an event
// carries, ending at a rune boundary, or its first rune when not even that fits.
func cutEncoded(line string, room int) int {
	used, n := 0, 0
	for n < len(line) {
		_, size := utf8.DecodeRuneInString(line[n:])
		cost := transport.EncodedLen(line[n : n+size])
		if used+cost > room && n > 0 {
			break
		}
		used += cost
		n += size
		if used > room {
			break
		}
	}
	return n
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
