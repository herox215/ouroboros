package matrix

import (
	"strings"
	"testing"
)

func TestMessagePartsEndAtLineBreaks(t *testing.T) {
	text := strings.Repeat(strings.Repeat("x", 99)+"\n", 200)
	parts := messageParts(text)
	if len(parts) < 2 || strings.Join(parts, "") != text {
		t.Fatal("split lost text")
	}
	for _, part := range parts {
		if len(part) > messageLimit || !strings.HasSuffix(part, "\n") {
			t.Fatalf("part of %d bytes does not end at a line", len(part))
		}
	}
}

func TestMessagePartsCloseAndReopenCode(t *testing.T) {
	line := strings.Repeat("y", 99) + "\n"
	text := "```go\n" + strings.Repeat(line, 200) + "```\n"
	parts := messageParts(text)
	if len(parts) < 2 {
		t.Fatalf("code block was not split: %d parts", len(parts))
	}
	// Without the fences the parts are the text, in order.
	if got := strings.Join(parts, ""); got == text {
		t.Fatal("split inside a code block without closing it")
	}
	if !strings.HasSuffix(parts[0], "```\n") || !strings.HasPrefix(parts[0], "```go\n") {
		t.Fatalf("first part starts %q and ends %q", head(parts[0], 10), tail(parts[0], 10))
	}
	for _, part := range parts[1:] {
		if !strings.HasPrefix(part, "```go\n") {
			t.Fatalf("part does not reopen the block: %q", head(part, 20))
		}
	}
	if last := parts[len(parts)-1]; !strings.HasSuffix(last, "```\n") {
		t.Fatalf("last part ends %q", tail(last, 20))
	}
	for _, part := range parts {
		if len(part) > messageLimit {
			t.Fatalf("part of %d bytes exceeds the limit", len(part))
		}
	}
}

func TestMessagePartsKeepATildeFence(t *testing.T) {
	line := strings.Repeat("z", 99) + "\n"
	text := "~~~\n" + strings.Repeat(line, 200) + "~~~\n"
	parts := messageParts(text)
	if len(parts) < 2 || !strings.HasPrefix(parts[1], "~~~\n") || !strings.HasSuffix(parts[0], "~~~\n") {
		t.Fatalf("tilde fence not closed and reopened: %d parts", len(parts))
	}
}

func head(s string, n int) string {
	if len(s) < n {
		return s
	}
	return s[:n]
}

func tail(s string, n int) string {
	if len(s) < n {
		return s
	}
	return s[len(s)-n:]
}
