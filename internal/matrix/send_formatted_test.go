package matrix

import (
	"context"
	"strings"
	"testing"

	"maunium.net/go/mautrix/event"
)

func TestSendFormattedToCarriesHTMLNextToTheBody(t *testing.T) {
	h := newHomeserver(t)
	c := testClient(t, h.server.URL)
	crypt := &fakeCrypto{}
	c.running, c.crypto = true, crypt
	defer func() { c.running, c.crypto = false, nil }()

	if err := c.SendFormattedTo(context.Background(), "!dm:test", "@alice:test", "**fett**", "<p><strong>fett</strong></p>"); err != nil {
		t.Fatal(err)
	}
	got := crypt.contents[0]
	if got.Format != event.FormatHTML || got.FormattedBody != "<p><strong>fett</strong></p>" || got.Body != "**fett**" {
		t.Fatalf("content %+v", got)
	}
	// Without HTML the send is the plain text it always was.
	if err := c.SendFormattedTo(context.Background(), "!dm:test", "@alice:test", "nur Text", ""); err != nil {
		t.Fatal(err)
	}
	if got := crypt.contents[1]; got.Format != "" || got.FormattedBody != "" || got.Body != "nur Text" {
		t.Fatalf("content %+v", got)
	}
	// A reply is still bound to the peer it names.
	if err := c.SendFormattedTo(context.Background(), "!dm:test", "", "x", "<p>x</p>"); err == nil {
		t.Fatal("sent a formatted reply without a peer")
	}
}

// A formatted body that would make the encrypted event exceed the size the
// specification allows is left out; the plain body still carries the text.
func TestSendFormattedToDropsHTMLBeyondTheEventLimit(t *testing.T) {
	h := newHomeserver(t)
	c := testClient(t, h.server.URL)
	crypt := &fakeCrypto{}
	c.running, c.crypto = true, crypt
	defer func() { c.running, c.crypto = false, nil }()

	// Escaped, the formatted body takes the budget alone, so the two together
	// would not fit one event.
	body := "answer"
	formatted := strings.Repeat("\x01", textBudget/6)
	if err := c.SendFormattedTo(context.Background(), "!dm:test", "@alice:test", body, formatted); err != nil {
		t.Fatal(err)
	}
	if got := crypt.contents[0]; got.Format != "" || got.FormattedBody != "" || got.Body != body {
		t.Fatalf("content %+v", got)
	}
}

// The budget counts the JSON escapes an event carries, not the text.
func TestTheBudgetCountsTheJSONEscapes(t *testing.T) {
	if got := EncodedLen("&"); got != 6 {
		t.Fatalf("an ampersand takes %d bytes", got)
	}
	if got := EncodedLen("\x01"); got != 6 {
		t.Fatalf("a control character takes %d bytes", got)
	}
	if got := EncodedLen("ab"); got != 2 {
		t.Fatalf("plain text takes %d bytes", got)
	}
	if !fitsOneEvent(strings.Repeat("x", textBudget), "") {
		t.Fatal("text at the budget does not fit")
	}
	if fitsOneEvent(strings.Repeat("x", textBudget+1), "") {
		t.Fatal("text beyond the budget fits")
	}
	if !fitsOneEvent(strings.Repeat("\x01", textBudget/6), "") {
		t.Fatal("escaped text at the budget does not fit")
	}
	if fitsOneEvent(strings.Repeat("\x01", textBudget/6+1), "") {
		t.Fatal("escaped text beyond the budget fits")
	}
}
