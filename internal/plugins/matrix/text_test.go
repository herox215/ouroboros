package matrix

import (
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	transport "ouroboros/internal/matrix"
)

func TestMessagePartsPreserveUnicodeAndEventSize(t *testing.T) {
	text := strings.Repeat("A🌓ü\n", 10000)
	parts := messageParts(text)
	if len(parts) < 2 || strings.Join(parts, "") != text {
		t.Fatal("split lost text")
	}
	for _, part := range parts {
		if transport.EncodedLen(part) > messageLimit || !utf8.ValidString(part) {
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

func TestMessagePartsCountInvalidBytesAsTheReplacementTheyBecome(t *testing.T) {
	// An invalid byte reaches the receiver as U+FFFD, three bytes of JSON,
	// where the text holds one: the splitter counts it that way and so cuts
	// sooner than it must for a run that becomes one U+FFFD.
	text := strings.Repeat("a\x80", 9000)
	parts := messageParts(text)
	if len(parts) != 3 || len(parts[0]) != 6000 || strings.Join(parts, "") != text {
		t.Fatalf("split into %d parts", len(parts))
	}
	for _, part := range parts {
		if got := transport.EncodedLen(part); got > messageLimit {
			t.Fatalf("a part takes %d bytes of the event", got)
		}
	}
}

func TestMessagePartsCountMarkupAsTheByteItIs(t *testing.T) {
	// The canonical JSON that Megolm encrypts keeps &, < and > as the one
	// byte they are, so a part holds 12000 of them where counting the escapes
	// of encoding/json would let 2000 through.
	for _, text := range []string{strings.Repeat("&", 12001), strings.Repeat("<", 12001)} {
		parts := messageParts(text)
		if len(parts) != 2 || len(parts[0]) != 12000 || strings.Join(parts, "") != text {
			t.Fatalf("%q split into %d parts", text[:1], len(parts))
		}
	}
}

// The splitter counts the bytes of a rune itself; it must agree with what the
// canonical JSON of an event makes of it, which EncodedLen measures.
func TestEncodedRuneAgreesWithTheEventJSON(t *testing.T) {
	check := func(r rune) {
		decoded, size := utf8.DecodeRuneInString(string(r))
		if got, want := encodedRune(decoded, size), transport.EncodedLen(string(r)); got != want {
			t.Fatalf("U+%04X takes %d bytes, the event carries %d", r, got, want)
		}
	}
	for r := rune(0); r <= 0x2FFF; r++ {
		check(r)
	}
	for r := rune(0x3000); r <= 0x10FFFF; r += 127 {
		check(r)
	}
	for _, r := range []rune{0xD800, 0xDFFF, 0x2028, 0x2029, 0xFFFD, 0xFFFE, 0xFFFF, 0x10000, 0x10FFFF} {
		check(r)
	}
}
