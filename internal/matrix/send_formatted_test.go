package matrix

import (
	"context"
	"encoding/json"
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

// The budget counts what the canonical JSON of the event carries, not the
// text: the escapes canonicaljson keeps, and no escape for markup.
func TestTheBudgetCountsTheCanonicalJSON(t *testing.T) {
	for text, want := range map[string]int{
		"&":        1, // encoding/json would say 6
		"<p>x</p>": 8,
		"\x01":     6, // a control character stays escaped
		"\n":       2, // the five short escapes take two bytes
		`"`:        2,
		"ü":        2,
		"ab":       2,
	} {
		if got := EncodedLen(text); got != want {
			t.Errorf("%q takes %d bytes, want %d", text, got, want)
		}
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

// A full part of prose keeps its HTML: 11960 bytes of body and 18727 of the
// markup rendered from it fit the budget once <, > and & count as the bytes
// they are. Counted with the escapes of encoding/json the pair takes 41107
// bytes, so such a part used to lose its formatting.
func TestFormattedProsePartKeepsItsHTML(t *testing.T) {
	h := newHomeserver(t)
	c := testClient(t, h.server.URL)
	crypt := &fakeCrypto{}
	c.running, c.crypto = true, crypt
	defer func() { c.running, c.crypto = false, nil }()

	body := strings.Repeat("Die Antwort ist **rot** und `klein`, sagt er.\n", 260)
	formatted := "<p>" + strings.Repeat("Die Antwort ist <strong>rot</strong> und <code>klein</code>, sagt er.\n", 260) + "</p>"
	if got := EncodedLen(body) + EncodedLen(formatted); got > textBudget {
		t.Fatalf("the sample takes %d bytes, more than the budget of %d", got, textBudget)
	}
	if got := htmlEscapedLen(t, body) + htmlEscapedLen(t, formatted); got <= textBudget {
		t.Fatalf("the sample takes %d bytes even with the HTML escapes, so it shows nothing", got)
	}
	if err := c.SendFormattedTo(context.Background(), "!dm:test", "@alice:test", body, formatted); err != nil {
		t.Fatal(err)
	}
	if got := crypt.contents[0]; got.Format != event.FormatHTML {
		t.Fatalf("sent the part without its HTML: %+v", got)
	}
}

// htmlEscapedLen is EncodedLen as encoding/json measures it, which escapes <,
// > and & as six bytes each although the encrypted event carries one.
func htmlEscapedLen(t *testing.T, text string) int {
	b, err := json.Marshal(text)
	if err != nil {
		t.Fatal(err)
	}
	return len(b) - 2
}

// A byte that is no UTF-8 would fail the encryption of the whole message; it
// reaches the receiver as U+FFFD instead, in the body and in the HTML.
func TestSendTextReplacesInvalidUTF8(t *testing.T) {
	h := newHomeserver(t)
	c := testClient(t, h.server.URL)
	crypt := &fakeCrypto{}
	c.running, c.crypto = true, crypt
	defer func() { c.running, c.crypto = false, nil }()

	if err := c.Send(context.Background(), "!dm:test", "a\x80b"); err != nil {
		t.Fatal(err)
	}
	if got := crypt.contents[0].Body; got != "a\uFFFDb" {
		t.Fatalf("body %q", got)
	}
	if err := c.SendFormattedTo(context.Background(), "!dm:test", "@alice:test", "a\x80b", "<p>a\x80b</p>"); err != nil {
		t.Fatal(err)
	}
	if got := crypt.contents[1]; got.Body != "a\uFFFDb" || got.FormattedBody != "<p>a\uFFFDb</p>" {
		t.Fatalf("content %+v", got)
	}
}
