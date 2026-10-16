package matrix

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestMessagePartsPreserveUnicodeAndEventSize(t *testing.T) {
	text := strings.Repeat("A🌓ü\n", 10000)
	parts := messageParts(text)
	if len(parts) < 2 || strings.Join(parts, "") != text {
		t.Fatal("split lost text")
	}
	for _, part := range parts {
		if len(part) > 12000 || !utf8.ValidString(part) {
			t.Fatal("invalid encrypted message part")
		}
	}
	if len(messageParts("")) != 0 {
		t.Fatal("empty event")
	}
}
