package matrix

import "unicode/utf8"

// Leave room for encrypted event overhead below Matrix's event-size limit.
func messageParts(text string) []string {
	const limit = 12000
	var parts []string
	for len(text) > limit {
		n := limit
		for n > 0 && !utf8.RuneStart(text[n]) {
			n--
		}
		if n == 0 { // no rune starts before the limit: no UTF-8, split at it
			n = limit
		}
		parts = append(parts, text[:n])
		text = text[n:]
	}
	if text != "" {
		parts = append(parts, text)
	}
	return parts
}
