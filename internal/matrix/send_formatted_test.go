package matrix

import (
	"context"
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
