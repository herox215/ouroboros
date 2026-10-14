package browser

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"ouroboros/internal/a2a"
)

// A conversation opens tabs, switches between them and closes them; open
// goes where a link or an image leads.
func TestTabsOfAConversation(t *testing.T) {
	f := newFake(t)
	b := newBrowser(f)
	ctx := context.Background()
	got, err := call(ctx, b, settings{}, `{"action": "tabs"}`)
	is(t, "none", got, err, "the conversation has no tab")

	if _, err := call(ctx, b, settings{}, `{"action": "open", "url": "https://example.com/a", "new_tab": true}`); err != nil {
		t.Fatal(err)
	}
	f.page["address"] = map[string]any{"address": "https://example.com/snake.jpg"}
	if _, err := call(ctx, b, settings{}, `{"action": "open", "element": 7, "new_tab": true}`); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.last("address"), "address(7)") || f.last("Page.navigate") != `{"url":"https://example.com/snake.jpg"}` {
		t.Fatalf("asked %s, navigated %s", f.last("address"), f.last("Page.navigate"))
	}
	f.on("Target.getTargetInfo", func(json.RawMessage) (any, error) {
		return map[string]any{"targetInfo": map[string]string{"title": "snake.jpg (1200×800)", "url": "https://example.com/snake.jpg"}}, nil
	})
	got, err = call(ctx, b, settings{}, `{"action": "tabs"}`)
	is(t, "two", got, err, "[1] \"snake.jpg (1200×800)\" https://example.com/snake.jpg\n[2] \"snake.jpg (1200×800)\" https://example.com/snake.jpg (current)")

	got, err = call(ctx, b, settings{}, `{"action": "switch", "tab": 1}`)
	is(t, "switch", got, err, `switched to tab 1; now at "Start" https://example.com/`)
	if f.last("Target.activateTarget") != "" {
		t.Fatal("a tab activated without a window")
	}
	_, err = call(ctx, b, settings{}, `{"action": "switch", "tab": 3}`)
	fails(t, "a tab that is not there", err, "there is no tab 3: the conversation has 2")

	// Opened in place, the element leads the current tab on.
	f.page["address"] = map[string]any{"address": "https://example.com/b"}
	if _, err := call(ctx, b, settings{}, `{"action": "open", "element": 2}`); err != nil {
		t.Fatal(err)
	}
	if b.convs["/"].current != 0 || len(b.convs["/"].tabs) != 2 {
		t.Fatalf("tab %d of %d", b.convs["/"].current, len(b.convs["/"].tabs))
	}

	got, err = call(ctx, b, settings{}, `{"action": "close"}`)
	is(t, "close the first", got, err, "closed the tab; tab 1 of 1 is the current one, at https://example.com/")
	got, err = call(ctx, b, settings{}, `{"action": "close"}`)
	is(t, "close the last", got, err, "closed the tab")
	if len(b.convs) != 0 {
		t.Fatal("the conversation is still kept")
	}

	// After the browser went, a tab that is not open again says where it
	// was; one the browser cannot say anything of says that too.
	if _, err := call(ctx, b, settings{}, `{"action": "open", "url": "https://example.com/c"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := call(ctx, b, settings{}, `{"action": "open", "url": "about", "new_tab": true}`); err != nil {
		t.Fatal(err)
	}
	b.proc.end()
	f.fail("Target.getTargetInfo", "refused")
	got, err = call(ctx, b, settings{}, `{"action": "tabs"}`)
	is(t, "after the browser went", got, err, "[1] https://example.com/\n[2] https://example.com/ (current)")
	b.convs["/"].tabs = append(b.convs["/"].tabs, &tab{})
	got, err = call(ctx, b, settings{}, `{"action": "tabs"}`)
	is(t, "a blank tab", got, err, "[1] https://example.com/\n[2] https://example.com/ (current)\n[3] a blank page")
	got, err = call(ctx, b, settings{}, `{"action": "read"}`)
	if err != nil || !strings.HasPrefix(got, `"Start"`) {
		t.Fatalf("read: %q, %v", got, err)
	}
	got, err = call(ctx, b, settings{}, `{"action": "tabs"}`)
	is(t, "refused", got, err, "[1] https://example.com/\n[2] https://example.com/ (current)\n[3] a blank page")
}

// With a window, the tab switched to is shown, and so is the one that is
// current after a close.
func TestTabsInAWindow(t *testing.T) {
	f := newFake(t)
	b := newBrowser(f)
	ctx := context.Background()
	for _, args := range []string{`{"action": "open", "url": "a.org"}`, `{"action": "open", "url": "b.org", "new_tab": true}`, `{"action": "switch", "tab": 1}`} {
		if _, err := call(ctx, b, settings{Visible: true}, args); err != nil {
			t.Fatal(err)
		}
	}
	if f.last("Target.activateTarget") != `{"targetId":"T"}` {
		t.Fatalf("activated %s", f.last("Target.activateTarget"))
	}
	f.fail("Target.activateTarget", "refused")
	_, err := call(ctx, b, settings{}, `{"action": "switch", "tab": 2}`)
	fails(t, "refused", err, "Target.activateTarget: refused")
	n := strings.Count(strings.Join(f.got(), " "), "Target.activateTarget")
	got, err := call(ctx, b, settings{}, `{"action": "close"}`)
	is(t, "close", got, err, "closed the tab; tab 1 of 1 is the current one, at https://example.com/")
	if strings.Count(strings.Join(f.got(), " "), "Target.activateTarget") != n+1 {
		t.Fatal("the tab after is not shown")
	}
}

// The tabs of a conversation of a peer share its browser context, which
// goes with the last.
func TestTabsOfAPeer(t *testing.T) {
	f := newFake(t)
	b := newBrowser(f)
	peer := a2a.WithOrigin(context.Background(), a2a.Origin{PublicKey: "peer"})
	for _, args := range []string{`{"action": "open", "url": "a.org"}`, `{"action": "open", "url": "b.org", "new_tab": true}`} {
		if _, err := call(peer, b, settings{}, args); err != nil {
			t.Fatal(err)
		}
	}
	if n := strings.Count(strings.Join(f.got(), " "), "Target.createBrowserContext"); n != 1 {
		t.Fatalf("%d contexts", n)
	}
	call(peer, b, settings{}, `{"action": "close"}`)
	if f.last("Target.disposeBrowserContext") != "" {
		t.Fatal("disposed with a tab left")
	}
	call(peer, b, settings{}, `{"action": "close"}`)
	if f.last("Target.disposeBrowserContext") != `{"browserContextId":"C"}` {
		t.Fatal("not disposed")
	}

	// A conversation that has no tab left has nothing to close.
	if _, err := call(peer, b, settings{}, `{"action": "tabs"}`); err != nil {
		t.Fatal(err)
	}
	got, err := call(peer, b, settings{}, `{"action": "close"}`)
	is(t, "nothing", got, err, "there is no tab to close")
}

// What keeps open from going where an element leads.
func TestOpenElementFails(t *testing.T) {
	ctx := context.Background()
	f := newFake(t)
	f.fail("Target.createTarget", "refused")
	_, err := call(ctx, newBrowser(f), settings{}, `{"action": "open", "element": 1}`)
	fails(t, "no tab", err, "refused")

	f = newFake(t)
	f.page["address"] = map[string]any{"error": "[1], button \"Go\", is no link or image: click it instead"}
	_, err = call(ctx, newBrowser(f), settings{}, `{"action": "open", "element": 1}`)
	fails(t, "a button", err, "is no link or image")

	f = newFake(t)
	f.page["address"] = map[string]any{"address": "file:///etc/passwd"}
	_, err = call(ctx, newBrowser(f), settings{}, `{"action": "open", "element": 1}`)
	fails(t, "a file", err, "not file")
}
