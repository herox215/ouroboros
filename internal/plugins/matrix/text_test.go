package matrix

import (
	"slices"
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

func TestMessagePartsEndBeforeASplitRune(t *testing.T) {
	// The limit falls on the second byte of a ü: the first part ends before it.
	text := "x" + strings.Repeat("ü", 7000)
	parts := messageParts(text)
	if len(parts) != 2 || len(parts[0]) != 11999 || parts[0]+parts[1] != text {
		t.Fatalf("split into %d parts, the first of %d bytes", len(parts), len(parts[0]))
	}
	for size, want := range map[int][]int{12000: {12000}, 12001: {12000, 1}, 24001: {12000, 12000, 1}} {
		var got []int
		for _, part := range messageParts(strings.Repeat("a", size)) {
			got = append(got, len(part))
		}
		if !slices.Equal(got, want) {
			t.Errorf("%d bytes: parts of %v, want %v", size, got, want)
		}
	}
}

func TestMessagePartsOfTextThatIsNoUTF8(t *testing.T) {
	// Without a rune start before the limit there is nowhere better to split.
	text := strings.Repeat("\x80", 12001)
	parts := messageParts(text)
	if len(parts) != 2 || len(parts[0]) != 12000 || parts[0]+parts[1] != text {
		t.Fatalf("split into %d parts", len(parts))
	}
}
