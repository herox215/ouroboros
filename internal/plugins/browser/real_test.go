package browser

import (
	"context"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// site is a small web site to browse: a page with a form, the page the form
// sends to, and one more.
func site(t *testing.T) *httptest.Server {
	t.Helper()
	pages := map[string]string{
		"/": `<title>Start</title><h1>Welcome</h1><p>Some   text
			here.</p><a href="/next">Next page</a>
			<div style="display:none"><a href="/hidden">Hidden</a></div>
			<form action="/result"><label>Search <input name="q" value="old"></label>
			<select name="color"><option>Red</option><option value="b">Blue</option></select>
			<label><input type="checkbox" name="ok"> Agree</label>
			<button>Send</button></form>
			<button id="covered" onclick="document.title='clicked'">Under</button>
			<div style="position:fixed;inset:0;opacity:0.01;background:white" onmousedown="event.stopPropagation()">cover</div>
			<ul><li>one</li><li>two</li></ul><img alt="a cat" src="data:,"><a href="/"><img alt="Home" src="data:,"></a>
			<div style="height:3000px"></div>`,
		"/next": `<title>Next</title><p>Second page</p>`,
		"/gallery": `<title>Gallery</title><a href="/next"><img alt="A snake" src="/snake.png" width="100" height="80"></a>
			<img alt="Another" src="/snake.png"><img alt="icon" src="/snake.png" width="16" height="16">`,
		"/ask": `<title>Ask</title>
			<button onclick="console.log('asking', {n: 1}); document.title = confirm('Sure?') ? 'yes' : 'no'">Confirm</button>
			<button onclick="document.title = 'name ' + prompt('Name?', 'you')">Prompt</button>
			<button onclick="setTimeout(() => { throw new Error('boom') })">Fail</button>
			<img src="/missing.png">`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if r.URL.Path == "/snake.png" {
			w.Header().Set("Content-Type", "image/png")
			png.Encode(w, image.NewRGBA(image.Rect(0, 0, 100, 80)))
			return
		}
		if r.URL.Path == "/result" {
			fmt.Fprintf(w, "<title>Result</title><p>q=%s color=%s ok=%s</p>", r.FormValue("q"), r.FormValue("color"), r.FormValue("ok"))
			return
		}
		page, ok := pages[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, page)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A real browser, where there is one: the page script reads what a page
// shows, and the actions act on it.
func TestRealBrowser(t *testing.T) {
	if _, err := find(); err != nil || testing.Short() {
		t.Skip("no browser:", err)
	}
	srv := site(t)
	b := &browser{profile: t.TempDir(), convs: map[string]*conversation{}}
	t.Cleanup(b.close)
	ctx := context.Background()
	do := func(a args) string {
		t.Helper()
		began := time.Now()
		out, err := b.run(ctx, settings{}, a)
		t.Logf("%s took %v", a.Action, time.Since(began))
		if err != nil {
			t.Fatalf("%s: %v", a.Action, err)
		}
		return out
	}

	page := do(args{Action: "open", URL: srv.URL})
	// What a page sees of the browser: one a person uses.
	var looks struct {
		Result struct {
			Value struct {
				Agent     string `json:"agent"`
				Webdriver bool   `json:"webdriver"`
				Brands    int    `json:"brands"`
			} `json:"value"`
		} `json:"result"`
	}
	tb := current(b)
	if err := tb.do(ctx, "Runtime.evaluate", map[string]any{"returnByValue": true,
		"expression": "({agent: navigator.userAgent, webdriver: navigator.webdriver, brands: navigator.userAgentData.brands.length})"}, &looks); err != nil {
		t.Fatal(err)
	}
	if v := looks.Result.Value; strings.Contains(v.Agent, "Headless") || !strings.Contains(v.Agent, "Chrome/") || v.Webdriver || v.Brands == 0 {
		t.Errorf("the page sees %+v", v)
	}
	t.Log(page)
	for _, want := range []string{`"Start" ` + srv.URL + "/", "# Welcome", "Some text here.", `[1] link "Next page" → /next`,
		`input "Search" = "old"`, `select "color" = "Red" (options: Red | Blue)`, `checkbox "Agree" not checked`, `button "Send"`,
		"- one\n- two", `[image "a cat"]`, `[7] link "Home" → /`} {
		if !strings.Contains(page, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	if strings.Contains(page, "Hidden") {
		t.Error("a hidden link is shown")
	}

	if got := do(args{Action: "type", Element: 2, Text: "ouro"}); !strings.HasPrefix(got, `typed into [2] input "Search" = "old"; now at "Start"`) {
		t.Errorf("type: %q", got)
	}
	if got := do(args{Action: "type", Element: 3, Text: "blue"}); !strings.HasPrefix(got, `chose "blue" in [3]`) {
		t.Errorf("select: %q", got)
	}
	if got := do(args{Action: "click", Element: 4}); !strings.Contains(got, "clicked [4] checkbox") {
		t.Errorf("checkbox: %q", got)
	}
	if got := do(args{Action: "read"}); !strings.Contains(got, `input "Search" = "ouro"`) || !strings.Contains(got, `= "Blue"`) || !strings.Contains(got, "Agree\" checked") {
		t.Errorf("after typing: %q", got)
	}
	if got := do(args{Action: "click", Element: 6}); !strings.Contains(got, "clicked by script") || !strings.Contains(got, `now at "clicked"`) {
		t.Errorf("a covered button: %q", got)
	}
	if got := do(args{Action: "scroll"}); !strings.HasPrefix(got, "the screen shows ") {
		t.Errorf("scroll: %q", got)
	}
	if got := do(args{Action: "click", Element: 5}); !strings.Contains(got, `now at "Result" `+srv.URL+"/result?q=ouro&color=b&ok=on") {
		t.Errorf("send: %q", got)
	}
	if got := do(args{Action: "read"}); !strings.Contains(got, "q=ouro color=b ok=on") {
		t.Errorf("the result: %q", got)
	}
	if got := do(args{Action: "back"}); !strings.Contains(got, `now at "`) {
		t.Errorf("back: %q", got)
	}
	if _, err := b.run(ctx, settings{}, args{Action: "click", Element: 40}); err == nil || !strings.Contains(err.Error(), "no element [40]") {
		t.Errorf("an element that is not there: %v", err)
	}
	if got := do(args{Action: "open", URL: srv.URL + "/next"}); !strings.Contains(got, "Second page") {
		t.Errorf("next: %q", got)
	}
	if got := do(args{Action: "key", Key: "End"}); !strings.HasPrefix(got, "pressed End; now at \"Next\"") {
		t.Errorf("key: %q", got)
	}
	gone := httptest.NewServer(nil)
	gone.Close()
	if _, err := b.run(ctx, settings{}, args{Action: "open", URL: gone.URL}); err == nil || !strings.Contains(err.Error(), "ERR_CONNECTION_REFUSED") {
		t.Errorf("a page that is not there: %v", err)
	}

	// Dialogs, and the console.
	do(args{Action: "open", URL: srv.URL + "/ask"})
	if got := do(args{Action: "click", Element: 1}); got != `click: the page shows a confirm "Sure?" and waits for an answer: answer it with the action dialog, accept true for OK or false for Cancel` {
		t.Errorf("confirm: %q", got)
	}
	if got := do(args{Action: "read"}); !strings.HasPrefix(got, `read: the page shows a confirm "Sure?"`) {
		t.Errorf("read while it asks: %q", got)
	}
	no := false
	if got := do(args{Action: "dialog", Accept: &no}); got != `answered the confirm "Sure?" with Cancel; now at "no" `+srv.URL+"/ask" {
		t.Errorf("cancel: %q", got)
	}
	do(args{Action: "click", Element: 2})
	yes := true
	if got := do(args{Action: "dialog", Accept: &yes, Text: "ouro"}); got != `answered the prompt "Name?" with "ouro"; now at "name ouro" `+srv.URL+"/ask" {
		t.Errorf("prompt: %q", got)
	}
	do(args{Action: "click", Element: 3})
	got := do(args{Action: "console"})
	for _, want := range []string{"[error] Failed to load resource: the server responded with a status of 404 (Not Found) (" + srv.URL + "/missing.png)",
		"[log] asking {n: 1}", "[exception] Error: boom at " + srv.URL + "/ask:"} {
		if !strings.Contains(got, want) {
			t.Errorf("the console lacks %q:\n%s", want, got)
		}
	}
	if got := do(args{Action: "console"}); got != "the page wrote nothing to its console since the last call" {
		t.Errorf("the console again: %q", got)
	}

	// Pictures, opened in a tab of their own.
	page = do(args{Action: "open", URL: srv.URL + "/gallery"})
	for _, want := range []string{`[1] link "A snake" → /next [2] image "A snake" 100×80 → ` + srv.URL + "/snake.png",
		`[3] image "Another" 100×80 → ` + srv.URL + "/snake.png"} {
		if !strings.Contains(page, want) {
			t.Errorf("the gallery lacks %q:\n%s", want, page)
		}
	}
	if !strings.Contains(page, `[image "icon"]`) || strings.Contains(page, "[4]") {
		t.Errorf("an icon is shown:\n%s", page)
	}
	if got := do(args{Action: "open", Element: 2, NewTab: true}); !strings.HasPrefix(got, `"snake.png (100×80)" `+srv.URL+"/snake.png") {
		t.Errorf("the picture: %q", got)
	}
	if got := do(args{Action: "tabs"}); got != `[1] "Gallery" `+srv.URL+"/gallery\n"+`[2] "snake.png (100×80)" `+srv.URL+"/snake.png (current)" {
		t.Errorf("tabs: %q", got)
	}
	if got := do(args{Action: "switch", Tab: 1}); got != `switched to tab 1; now at "Gallery" `+srv.URL+"/gallery" {
		t.Errorf("switch: %q", got)
	}
	if got := do(args{Action: "close"}); got != "closed the tab; tab 1 of 1 is the current one, at "+srv.URL+"/snake.png" {
		t.Errorf("close the gallery: %q", got)
	}
	if got := do(args{Action: "close"}); got != "closed the tab" {
		t.Errorf("close: %q", got)
	}
}
