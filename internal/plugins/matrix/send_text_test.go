package matrix

import (
	"context"
	"strings"
	"testing"
)

type recordingSender struct {
	plain, formatted []string
	bodies, html     []string
}

func (s *recordingSender) SendTo(_ context.Context, _, _, text string) error {
	s.plain = append(s.plain, text)
	return nil
}

func (s *recordingSender) SendFormattedTo(_ context.Context, _, _, body, formatted string) error {
	s.bodies = append(s.bodies, body)
	s.html = append(s.html, formatted)
	return nil
}

func TestSendTextRendersMarkdown(t *testing.T) {
	s := &recordingSender{}
	if err := sendText(t.Context(), s, "!room:test", "@alice:test", "**fett**"); err != nil {
		t.Fatal(err)
	}
	if len(s.html) != 1 || s.html[0] != "<p><strong>fett</strong></p>" || s.bodies[0] != "**fett**" {
		t.Fatalf("formatted %q of %q", s.html, s.bodies)
	}
	if len(s.plain) != 0 {
		t.Fatalf("also sent plain: %q", s.plain)
	}
}

func TestSendTextWithoutMarkupOrBeyondTheBudgetIsPlain(t *testing.T) {
	s := &recordingSender{}
	if err := sendText(t.Context(), s, "!room:test", "@alice:test", ""); err != nil {
		t.Fatal(err)
	}
	// Escaping every character expands the HTML far beyond the body budget.
	tooBig := strings.Repeat("&", messageLimit)
	if err := sendText(t.Context(), s, "!room:test", "@alice:test", tooBig); err != nil {
		t.Fatal(err)
	}
	if len(s.plain) != 2 || s.plain[1] != tooBig || len(s.html) != 0 {
		t.Fatalf("plain %d, formatted %d", len(s.plain), len(s.html))
	}
}
