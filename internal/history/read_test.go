package history

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"ouroboros/internal/openai"
)

// Following every continuation reproduces all formatted records byte for byte,
// even with multibyte characters at the page boundaries and embedded newlines.
func TestReadContinuation(t *testing.T) {
	for _, who := range []Who{owner, peerA} {
		t.Run(who.Session, func(t *testing.T) {
			a := at(t)
			a.Append(who, []openai.Message{
				{Role: "user", Content: strings.Repeat("ä🙂\n", 15000)},
				{Role: "assistant", Content: "the next record"},
			}, nil)
			var want strings.Builder
			if err := a.records(a.file(who, Name(who.Session)), func(r Record) bool {
				want.WriteString(show(r))
				return true
			}); err != nil {
				t.Fatal(err)
			}
			var got strings.Builder
			from, offset, pages := 1, 0, 0
			for {
				out, err := a.Read(who, "", from, 200, offset)
				if err != nil {
					t.Fatal(err)
				}
				if len(out) > MaxReadBytes || !utf8.ValidString(out) {
					t.Fatalf("invalid page of %d bytes", len(out))
				}
				note := strings.LastIndex(out, "[records ")
				if note < 0 {
					t.Fatalf("missing continuation note: %q", out)
				}
				text := out[:note]
				var first, last, total, next, skip int
				n, _ := fmt.Sscanf(out[note:], "[records %d to %d of %d: continue with from %d, offset %d]", &first, &last, &total, &next, &skip)
				if n == 5 {
					text = strings.TrimSuffix(text, "\n") // separator before a partial-record note
				}
				got.WriteString(text)
				pages++
				if n < 4 {
					break
				}
				if next < from || next == from && skip <= offset {
					t.Fatal("continuation made no progress")
				}
				from, offset = next, skip
				if pages > 100 {
					t.Fatal("too many pages")
				}
			}
			if pages < 3 || got.String() != want.String() {
				t.Fatalf("%d pages: reconstructed %d bytes, want %d", pages, got.Len(), want.Len())
			}
		})
	}
}

func TestReadOffset(t *testing.T) {
	a := at(t)
	a.Append(owner, []openai.Message{{Role: "user", Content: "ä final"}}, nil)
	out, err := a.Read(owner, "", 1, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	pos := strings.Index(out, "ä final")
	for _, offset := range []int{-1, pos + 1, len(out), int(^uint(0) >> 1)} {
		if _, err := a.Read(owner, "", 1, 1, offset); err == nil {
			t.Errorf("offset %d should fail", offset)
		}
	}
	if out, err := a.Read(owner, "", 1, 1, pos); err != nil || !strings.HasPrefix(out, "ä final\n[records") {
		t.Fatalf("resumed: %q, %v", out, err)
	}
	if _, err := a.Read(peerA, Name(owner.Session), 1, 1, pos); !errors.Is(err, ErrScope) {
		t.Fatalf("offset bypassed scope: %v", err)
	}
}
