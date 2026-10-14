package browser

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"ouroboros/internal/a2a"
	"ouroboros/internal/agent"
	"ouroboros/internal/openai"
)

// newBrowser is a browser of the tool, whose browsers are those of f.
func newBrowser(f *fake) *browser {
	b := &browser{profile: f.t.TempDir(), convs: map[string]*conversation{}}
	f.t.Cleanup(b.close)
	return b
}

// call runs the tool with the settings s and the arguments a, as JSON, in
// the conversation of ctx.
func call(ctx context.Context, b *browser, s settings, a string) (string, error) {
	tool := b.tool()
	cs, _ := json.Marshal(s)
	run, err := tool.Configure(cs)
	if err != nil {
		return "", err
	}
	return run.Run(ctx, json.RawMessage(a))
}

func is(t *testing.T, what string, got string, err error, want string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
	if got != want {
		t.Fatalf("%s:\n got %.300q\nwant %.300q", what, got, want)
	}
}

func fails(t *testing.T, what string, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("%s: got %v, want an error with %q", what, err, want)
	}
}

// The tool opens a page in a tab of the conversation, and acts on it.
func TestBrowse(t *testing.T) {
	f := newFake(t)
	b := newBrowser(f)
	ctx := context.Background()
	got, err := call(ctx, b, settings{}, `{"action": "open", "url": "example.com"}`)
	is(t, "open", got, err, "\"Start\" https://example.com/\n\nHello")
	if want := []string{"Browser.setDownloadBehavior", "Target.createTarget", "Target.attachToTarget", "Page.enable", "Runtime.enable", "Log.enable", "Page.navigate", "read"}; !slices.Equal(f.got(), want) {
		t.Fatalf("sent %v, want %v", f.got(), want)
	}
	if f.last("Page.navigate") != `{"url":"https://example.com"}` || !slices.Equal(f.started, []bool{false}) {
		t.Fatalf("navigated with %s, started %v", f.last("Page.navigate"), f.started)
	}

	f.page["point"] = map[string]any{"x": 10.5, "y": 20, "what": `link "Next"`}
	got, err = call(ctx, b, settings{}, `{"action": "click", "element": "3"}`)
	is(t, "click", got, err, `clicked [3] link "Next"; now at "Start" https://example.com/`)
	if p := f.last("Input.dispatchMouseEvent"); p != `{"button":"left","clickCount":1,"type":"mouseReleased","x":10.5,"y":20}` {
		t.Fatalf("clicked with %s", p)
	}
	if f.last("point") == "" || !strings.HasSuffix(f.last("point"), `point(3)","returnByValue":true}`) {
		t.Fatalf("pointed with %s", f.last("point"))
	}

	f.page["point"] = map[string]any{"x": 1, "y": 1, "what": `button "Buy"`, "covered": "Accept cookies"}
	got, err = call(ctx, b, settings{}, `{"action": "click", "element": 4}`)
	is(t, "a covered element", got, err, `clicked [4] button "Buy" (it is covered by "Accept cookies", so it was clicked by script); now at "Start" https://example.com/`)

	f.page["focus"] = map[string]any{"what": `input "Search"`}
	got, err = call(ctx, b, settings{}, `{"action": "type", "element": 2, "text": "ouro \"boros\"", "enter": true}`)
	is(t, "type", got, err, `typed into [2] input "Search" and pressed Enter; now at "Start" https://example.com/`)
	if p := f.last("Input.insertText"); p != `{"text":"ouro \"boros\""}` {
		t.Fatalf("typed %s", p)
	}
	if p := f.last("focus"); !strings.HasSuffix(p, `focus(2, \"ouro \\\"boros\\\"\")","returnByValue":true}`) {
		t.Fatalf("focused with %s", p)
	}
	if p := f.last("Input.dispatchKeyEvent"); p != `{"code":"Enter","key":"Enter","type":"keyUp","windowsVirtualKeyCode":13}` {
		t.Fatalf("pressed %s", p)
	}

	f.page["focus"] = map[string]any{"what": `select "Color"`, "chosen": true}
	got, err = call(ctx, b, settings{}, `{"action": "type", "element": 5, "text": "Blue"}`)
	is(t, "choose", got, err, `chose "Blue" in [5] select "Color"; now at "Start" https://example.com/`)

	got, err = call(ctx, b, settings{}, `{"action": "key", "key": "Space"}`)
	is(t, "key", got, err, `pressed Space; now at "Start" https://example.com/`)
	if p := f.params["Input.dispatchKeyEvent"]; string(p[len(p)-2]) != `{"code":"Space","key":" ","text":" ","type":"keyDown","windowsVirtualKeyCode":32}` {
		t.Fatalf("pressed %s", p[len(p)-2])
	}

	f.page["scroll"] = map[string]any{"y": 720, "height": 2000, "screen": 900}
	got, err = call(ctx, b, settings{}, `{"action": "scroll"}`)
	is(t, "scroll", got, err, "the screen shows 720 to 1620 of 2000 pixels of the page")
	got, err = call(ctx, b, settings{}, `{"action": "scroll", "direction": "up"}`)
	is(t, "scroll up", got, err, "the screen shows 720 to 1620 of 2000 pixels of the page")
	if !strings.Contains(f.last("scroll"), "scroll(-1)") {
		t.Fatalf("scrolled with %s", f.last("scroll"))
	}

	got, err = call(ctx, b, settings{}, `{"action": "back"}`)
	is(t, "back", got, err, `went back; now at "Start" https://example.com/`)
	if p := f.last("Page.navigateToHistoryEntry"); p != `{"entryId":2}` {
		t.Fatalf("went back to %s", p)
	}

	got, err = call(ctx, b, settings{}, `{"action": "read"}`)
	is(t, "read", got, err, "\"Start\" https://example.com/\n\nHello")

	// All of it in the one tab of the conversation.
	if n := strings.Count(strings.Join(f.got(), " "), "Target.createTarget"); n != 1 {
		t.Fatalf("%d tabs", n)
	}
	got, err = call(ctx, b, settings{}, `{"action": "close"}`)
	is(t, "close", got, err, "closed the tab")
	if f.last("Target.closeTarget") != `{"targetId":"T"}` {
		t.Fatalf("closed %s", f.last("Target.closeTarget"))
	}
	got, err = call(ctx, b, settings{}, `{"action": "close"}`)
	is(t, "close again", got, err, "there is no tab to close")
}

// What the tool is shown of its calls.
func TestShow(t *testing.T) {
	for args, want := range map[string]string{
		`{"action": "open", "url": "example.com"}`:          "browsing example.com",
		`{"action": "read"}`:                                "reading the page",
		`{"action": "open", "element": 7, "new_tab": true}`: "opening [7] in a new tab",
		`{"action": "tabs"}`:                                "looking at the tabs",
		`{"action": "switch", "tab": 2}`:                    "switching to tab 2",
		`{"action": "click", "element": 3}`:                 "clicking [3]",
		`{"action": "type", "element": 2}`:                  "typing into [2]",
		`{"action": "key", "key": "Enter"}`:                 "pressing Enter",
		`{"action": "scroll"}`:                              "scrolling",
		`{"action": "back"}`:                                "going back",
		`{"action": "screenshot"}`:                          "looking at the page",
		`{"action": "dialog", "accept": true}`:              "answering the dialog",
		`{"action": "console"}`:                             "reading the console",
		`{"action": "close"}`:                               "closing the tab",
		`{"action": "dance"}`:                               "",
		`not json`:                                          "",
	} {
		if got := Show(json.RawMessage(args)); got != want {
			t.Errorf("%s: %q, want %q", args, got, want)
		}
	}
}

// What the tool refuses before it starts a browser.
func TestRefused(t *testing.T) {
	f := newFake(t)
	b := newBrowser(f)
	ctx := context.Background()
	for args, want := range map[string]string{
		`{"action": "dance"}`:                         `there is no action "dance": open, read, click`,
		`{"action": "open"}`:                          "open needs the url of a page",
		`{"action": "open", "url": "file:///etc"}`:    "not file: read files with read",
		`{"action": "open", "url": "http://[::1"}`:    "missing ']'",
		`{"action": "read", "page": 2}`:               `"page"`,
		`{"action": "open", "url": "ftp://x.org/a"}`:  "not ftp",
		`{"action": "open", "url": "javascript:x()"}`: "invalid port",
	} {
		_, err := call(ctx, b, settings{}, args)
		fails(t, args, err, want)
	}
	peer := a2a.WithOrigin(ctx, a2a.Origin{PublicKey: "peer"})
	_, err := call(peer, b, settings{}, `{"action": "open", "url": "example.com", "visible": true}`)
	fails(t, "a window for a peer", err, "only the owner decides")
	if len(f.started) != 0 {
		t.Fatalf("started %d browsers", len(f.started))
	}
}

// Every conversation has a tab of its own; those of peers are in a browser
// context of their own each, which goes with the tab.
func TestTabs(t *testing.T) {
	f := newFake(t)
	b := newBrowser(f)
	owner := context.Background()
	peer := a2a.WithOrigin(owner, a2a.Origin{PublicKey: "peer"})
	for _, ctx := range []context.Context{owner, peer} {
		if _, err := call(ctx, b, settings{}, `{"action": "read"}`); err != nil {
			t.Fatal(err)
		}
	}
	if len(b.convs) != 2 || len(f.started) != 1 {
		t.Fatalf("%d tabs in %d browsers", len(b.convs), len(f.started))
	}
	if p := f.last("Target.createTarget"); p != `{"browserContextId":"C","url":"about:blank"}` {
		t.Fatalf("the tab of the peer: %s", p)
	}
	if p := f.last("Browser.setDownloadBehavior"); p != `{"behavior":"deny","browserContextId":"C"}` {
		t.Fatalf("downloads of the peer: %s", p)
	}
	got, err := call(peer, b, settings{}, `{"action": "close"}`)
	is(t, "close", got, err, "closed the tab")
	if p := f.last("Target.disposeBrowserContext"); p != `{"browserContextId":"C"}` {
		t.Fatalf("the context of the peer: %s", p)
	}

	// The tab of a closed browser is not closed again.
	if _, err := call(peer, b, settings{}, `{"action": "read"}`); err != nil {
		t.Fatal(err)
	}
	b.proc.end()
	sent := len(f.got())
	got, err = call(peer, b, settings{}, `{"action": "close"}`)
	is(t, "close in a closed browser", got, err, "closed the tab")
	if len(f.got()) != sent {
		t.Fatalf("sent %v", f.got()[sent:])
	}
}

// A browser that went is started again, and the tab opened again where it
// was; so is a tab that went, as one the owner closed.
func TestTabsComeBack(t *testing.T) {
	f := newFake(t)
	b := newBrowser(f)
	ctx := context.Background()
	if _, err := call(ctx, b, settings{}, `{"action": "open", "url": "https://example.com/a"}`); err != nil {
		t.Fatal(err)
	}
	f.page["view"] = map[string]any{"title": "B", "url": "https://example.com/b"}
	if _, err := call(ctx, b, settings{}, `{"action": "key", "key": "Enter"}`); err != nil {
		t.Fatal(err)
	}
	b.proc.end()
	if _, err := call(ctx, b, settings{}, `{"action": "read"}`); err != nil {
		t.Fatal(err)
	}
	if len(f.started) != 2 || f.last("Page.navigate") != `{"url":"https://example.com/b"}` {
		t.Fatalf("started %v, navigated %s", f.started, f.last("Page.navigate"))
	}

	// The owner closes the tab in the window.
	session := current(b).session
	b.mu.Lock()
	p := b.proc
	b.mu.Unlock()
	p.mu.Lock()
	p.detached[session] = true
	p.mu.Unlock()
	if _, err := call(ctx, b, settings{}, `{"action": "read"}`); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(strings.Join(f.got(), " "), "Target.createTarget"); n != 3 || len(f.started) != 2 {
		t.Fatalf("%d tabs in %d browsers", n, len(f.started))
	}
}

// A call of the owner says whether the browser shows its window; the
// settings say it when no call did.
func TestVisible(t *testing.T) {
	f := newFake(t)
	b := newBrowser(f)
	ctx := context.Background()
	if _, err := call(ctx, b, settings{Visible: true}, `{"action": "read"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := call(ctx, b, settings{}, `{"action": "read"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := call(ctx, b, settings{}, `{"action": "open", "url": "example.com", "visible": "true"}`); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.started, []bool{true}) {
		t.Fatalf("started %v", f.started)
	}
	if _, err := call(ctx, b, settings{Visible: true}, `{"action": "open", "url": "example.com", "visible": false}`); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.started, []bool{true, false}) {
		t.Fatalf("started %v", f.started)
	}
}

// A browser without a window is ended once it was not used for a while; one
// with a window is not, as the owner may use it.
func TestIdle(t *testing.T) {
	f := newFake(t)
	b := newBrowser(f)
	idle = 20 * time.Millisecond
	ctx := context.Background()
	if _, err := call(ctx, b, settings{}, `{"action": "read"}`); err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	p := b.proc
	b.mu.Unlock()
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the browser was not ended")
	}
	b.mu.Lock()
	ended := b.proc == nil
	b.mu.Unlock()
	if !ended {
		t.Fatal("the browser is still kept")
	}

	if _, err := call(ctx, b, settings{Visible: true}, `{"action": "read"}`); err != nil {
		t.Fatal(err)
	}
	time.Sleep(4 * idle)
	b.mu.Lock()
	p = b.proc
	b.mu.Unlock()
	if p.closed() {
		t.Fatal("a browser with a window was ended")
	}

	// A browser that was replaced is not ended by the timer of the one
	// before.
	b.mu.Lock()
	b.wake()
	b.proc = nil
	b.mu.Unlock()
	time.Sleep(4 * idle)
	b.close()
}

// What fails on the way to a tab.
func TestTabFails(t *testing.T) {
	ctx := context.Background()
	f := newFake(t)
	f.failing = errors.New("no browser found")
	_, err := call(ctx, newBrowser(f), settings{}, `{"action": "read"}`)
	fails(t, "start", err, "no browser found")

	for _, method := range []string{"Browser.setDownloadBehavior", "Target.createTarget", "Target.attachToTarget", "Page.enable", "Runtime.enable", "Log.enable"} {
		f := newFake(t)
		f.fail(method, "refused")
		_, err := call(ctx, newBrowser(f), settings{}, `{"action": "read"}`)
		fails(t, method, err, method+": refused")
	}
	peer := a2a.WithOrigin(ctx, a2a.Origin{PublicKey: "peer"})
	for _, method := range []string{"Target.createBrowserContext"} {
		f := newFake(t)
		f.fail(method, "refused")
		_, err := call(peer, newBrowser(f), settings{}, `{"action": "read"}`)
		fails(t, method, err, method+": refused")
	}
	// The downloads of the context of a peer.
	f = newFake(t)
	b := newBrowser(f)
	if _, err := call(ctx, b, settings{}, `{"action": "read"}`); err != nil {
		t.Fatal(err)
	}
	f.fail("Browser.setDownloadBehavior", "refused")
	_, err = call(peer, b, settings{}, `{"action": "read"}`)
	fails(t, "downloads of a peer", err, "refused")

	// A tab that cannot go back to its page.
	f = newFake(t)
	b = newBrowser(f)
	if _, err := call(ctx, b, settings{}, `{"action": "open", "url": "example.com"}`); err != nil {
		t.Fatal(err)
	}
	b.proc.end()
	f.fail("Page.navigate", "refused")
	_, err = call(ctx, b, settings{}, `{"action": "read"}`)
	fails(t, "open again", err, "refused")
}

// What fails in a tab.
func TestActionsFail(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		args, method, want string
	}{
		{`{"action": "open", "url": "example.com"}`, "Page.navigate", "Page.navigate: refused"},
		{`{"action": "open", "url": "example.com"}`, "read", "refused"},
		{`{"action": "read"}`, "read", "refused"},
		{`{"action": "click", "element": 1}`, "point", "refused"},
		{`{"action": "click", "element": 1}`, "Input.dispatchMouseEvent", "refused"},
		{`{"action": "click", "element": 1}`, "view", "refused"},
		{`{"action": "type", "element": 1, "text": "a"}`, "focus", "refused"},
		{`{"action": "type", "element": 1, "text": "a"}`, "Input.insertText", "refused"},
		{`{"action": "type", "element": 1, "text": "a", "enter": true}`, "Input.dispatchKeyEvent", "refused"},
		{`{"action": "key", "key": "Tab"}`, "Input.dispatchKeyEvent", "refused"},
		{`{"action": "scroll"}`, "scroll", "refused"},
		{`{"action": "back"}`, "Page.getNavigationHistory", "refused"},
		{`{"action": "back"}`, "Page.navigateToHistoryEntry", "refused"},
		{`{"action": "screenshot"}`, "view", "refused"},
		{`{"action": "screenshot"}`, "Page.captureScreenshot", "refused"},
	} {
		f := newFake(t)
		f.page["point"] = map[string]any{"x": 1, "y": 1, "what": "it"}
		f.page["focus"] = map[string]any{"what": "it"}
		f.fail(c.method, "refused")
		_, err := call(ctx, newBrowser(f), settings{}, c.args)
		fails(t, c.args+" with "+c.method, err, c.want)
	}

	f := newFake(t)
	f.page["point"] = map[string]any{"x": 1, "y": 1, "what": "it", "covered": "a banner"}
	f.fail("click", "refused")
	_, err := call(ctx, newBrowser(f), settings{}, `{"action": "click", "element": 1}`)
	fails(t, "a covered element", err, "refused")

	// The key up fails.
	f = newFake(t)
	keys := 0
	f.on("Input.dispatchKeyEvent", func(json.RawMessage) (any, error) {
		if keys++; keys == 2 {
			return nil, &cdpError{Message: "refused"}
		}
		return map[string]any{}, nil
	})
	_, err = call(ctx, newBrowser(f), settings{}, `{"action": "key", "key": "Tab"}`)
	fails(t, "key up", err, "refused")

	for args, want := range map[string]string{
		`{"action": "key", "key": "F13"}`:           `there is no key "F13"`,
		`{"action": "scroll", "direction": "left"}`: `not "left"`,
		`{"action": "read", "offset": 6}`:           "the text of the page has 5 bytes: offset 6 is beyond it",
		`{"action": "read", "offset": -1}`:          "offset -1 is beyond it",
	} {
		_, err := call(ctx, newBrowser(newFake(t)), settings{}, args)
		fails(t, args, err, want)
	}

	// No page to go back to.
	for _, history := range []string{`{"currentIndex": 1, "entries": [{"id": 1}, {"id": 2}]}`, `{"currentIndex": 3, "entries": [{"id": 1}]}`} {
		f := newFake(t)
		f.on("Page.getNavigationHistory", func(json.RawMessage) (any, error) { return json.RawMessage(history), nil })
		_, err := call(ctx, newBrowser(f), settings{}, `{"action": "back"}`)
		fails(t, history, err, "there is no page before this one")
	}

	// A page that cannot be opened.
	f = newFake(t)
	f.on("Page.navigate", func(json.RawMessage) (any, error) {
		return map[string]string{"errorText": "net::ERR_NAME_NOT_RESOLVED"}, nil
	})
	_, err = call(ctx, newBrowser(f), settings{}, `{"action": "open", "url": "nowhere.invalid"}`)
	fails(t, "a host that is not there", err, "https://nowhere.invalid could not be opened: net::ERR_NAME_NOT_RESOLVED")

	// A screenshot that is no base64.
	f = newFake(t)
	f.on("Page.captureScreenshot", func(json.RawMessage) (any, error) { return map[string]string{"data": "!"}, nil })
	_, err = call(ctx, newBrowser(f), settings{}, `{"action": "screenshot"}`)
	fails(t, "a broken screenshot", err, "illegal base64")

	// Outside of an answer a screenshot goes nowhere.
	_, err = call(ctx, newBrowser(newFake(t)), settings{}, `{"action": "screenshot"}`)
	fails(t, "a screenshot outside of an answer", err, "only to an agent that answers")
}

// What page.js reports.
func TestPageScript(t *testing.T) {
	ctx := context.Background()
	f := newFake(t)
	b := newBrowser(f)
	f.page["point"] = map[string]any{"error": "there is no element [9] on the page now: read it again"}
	_, err := call(ctx, b, settings{}, `{"action": "click", "element": 9}`)
	fails(t, "an error of the script", err, "there is no element [9]")

	f.on("read", func(json.RawMessage) (any, error) {
		return map[string]any{"exceptionDetails": map[string]any{"text": "Uncaught", "exception": map[string]any{"description": "TypeError: x is not a function"}}}, nil
	})
	_, err = call(ctx, b, settings{}, `{"action": "read"}`)
	fails(t, "an exception", err, "the page failed: TypeError: x is not a function")
	f.on("read", func(json.RawMessage) (any, error) {
		return map[string]any{"exceptionDetails": map[string]any{"text": "Uncaught"}}, nil
	})
	_, err = call(ctx, b, settings{}, `{"action": "read"}`)
	fails(t, "an exception without description", err, "the page failed: Uncaught")
	if firstOf() != "" {
		t.Fatal("first of nothing")
	}
}

// read returns the text of a long page in pieces, which do not split a
// character.
func TestReadInPieces(t *testing.T) {
	ctx := context.Background()
	f := newFake(t)
	b := newBrowser(f)
	text := strings.Repeat("a", maxPage-1) + "äbc"
	f.page["read"] = map[string]any{"title": "Long", "url": "https://example.com/", "text": text}
	got, err := call(ctx, b, settings{}, `{"action": "read"}`)
	is(t, "the first piece", got, err, "\"Long\" https://example.com/\n\n"+strings.Repeat("a", maxPage-1)+
		"\n[bytes 0 to 24575 of 24579: continue with offset 24575]")
	got, err = call(ctx, b, settings{}, `{"action": "read", "offset": 24575}`)
	is(t, "the last piece", got, err, "\"Long\" https://example.com/\n\näbc\n[bytes 24575 to 24579 of 24579, the end of the page]")
}

// A page that does not finish loading is read as far as it came.
func TestStillLoading(t *testing.T) {
	ctx := context.Background()
	f := newFake(t)
	b := newBrowser(f)
	f.on("Page.navigate", func(json.RawMessage) (any, error) { return map[string]string{"frameId": "T"}, nil })
	got, err := call(ctx, b, settings{}, `{"action": "open", "url": "example.com"}`)
	is(t, "not loading", got, err, "\"Start\" https://example.com/\n\nHello")

	// The fake starts loading and never stops, for the actions after.
	tb := current(b)
	f.on("Input.dispatchKeyEvent", func(json.RawMessage) (any, error) {
		b.proc.mu.Lock()
		b.proc.sessions[tb.session].events <- event{"Page.frameStartedLoading", json.RawMessage(`{"frameId":"T"}`)}
		b.proc.sessions[tb.session].events <- event{"Page.loadEventFired", json.RawMessage(`{}`)}
		b.proc.mu.Unlock()
		return map[string]any{}, nil
	})
	got, err = call(ctx, b, settings{}, `{"action": "key", "key": "Enter"}`)
	is(t, "loading", got, err, `pressed Enter; now at "Start" https://example.com/, which is still loading`)

	f.on("Page.navigate", func(json.RawMessage) (any, error) {
		b.proc.mu.Lock()
		b.proc.sessions[tb.session].events <- event{"Page.frameStartedLoading", json.RawMessage(`{"frameId":"T"}`)}
		b.proc.mu.Unlock()
		return map[string]string{"frameId": "T"}, nil
	})
	got, err = call(ctx, b, settings{}, `{"action": "open", "url": "example.com"}`)
	is(t, "open loading", got, err, "[the page has not finished loading: read it again later for more]\n\"Start\" https://example.com/\n\nHello")

	// The call ends while the page loads: the tool does not wait on.
	loadTimeout = time.Minute
	cut, cancel := context.WithCancel(ctx)
	defer cancel()
	f.on("Page.navigate", func(json.RawMessage) (any, error) {
		b.proc.mu.Lock()
		b.proc.sessions[tb.session].events <- event{"Page.frameStartedLoading", json.RawMessage(`{"frameId":"T"}`)}
		b.proc.mu.Unlock()
		time.AfterFunc(10*time.Millisecond, cancel)
		return map[string]string{"frameId": "T"}, nil
	})
	began := time.Now()
	call(cut, b, settings{}, `{"action": "open", "url": "example.com"}`)
	if took := time.Since(began); took > 10*time.Second {
		t.Fatalf("waited %v", took)
	}
}

// A screenshot is shown to the LLM, with what it shows in words.
func TestScreenshot(t *testing.T) {
	f := newFake(t)
	b := newBrowser(f)
	run, _ := b.tool().Configure(json.RawMessage(`{}`))
	llm := &looking{call: `{"action": "screenshot"}`}
	if _, err := agent.New(llm, "m", "sys", run).Run(context.Background(), "c", "look", agent.Observer{}); err != nil {
		t.Fatal(err)
	}
	if llm.result.Content != `a screenshot of "Start" https://example.com/, 1280×900 pixels, showing 0 to 900 of 2000 pixels of the page` {
		t.Fatalf("got %q", llm.result.Content)
	}
	if len(llm.result.Images) != 1 || llm.result.Images[0].Type != "image/jpeg" || string(llm.result.Images[0].Data) != "JPEG" {
		t.Fatalf("shown %v", llm.result.Images)
	}
	if p := f.last("Page.captureScreenshot"); p != `{"format":"jpeg","quality":80}` {
		t.Fatalf("captured with %s", p)
	}
}

// looking is an LLM that calls the tool once with call, and keeps the
// result it gets.
type looking struct {
	call   string
	result openai.Message
}

func (l *looking) Complete(_ context.Context, _ string, msgs []openai.Message, _ []openai.Tool) (openai.Message, error) {
	last := msgs[len(msgs)-1]
	if last.Role == "user" {
		return openai.Message{ToolCalls: []openai.ToolCall{{ID: "c1", Type: "function", Function: openai.FunctionCall{Name: Tool, Arguments: l.call}}}}, nil
	}
	l.result = last
	return openai.Message{Content: "seen"}, nil
}

// current is the current tab of the conversation of the owner outside of an
// answer.
func current(b *browser) *tab {
	b.mu.Lock()
	defer b.mu.Unlock()
	c := b.convs["/"]
	return c.tabs[c.current]
}
