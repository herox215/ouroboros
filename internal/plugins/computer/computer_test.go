package computer

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"ouroboros/internal/a2a"
	"ouroboros/internal/agent"
	"ouroboros/internal/openai"
)

// fake is cua-driver for the tests: an MCP server on pipes that answers the
// tools the tool calls, the way the driver does as far as it needs, and
// keeps what it was asked.
type fake struct {
	t *testing.T

	mu      sync.Mutex
	calls   []string                            // the tools, in the order they were called
	args    map[string][]map[string]any         // the arguments of every call of a tool
	answers map[string]func(map[string]any) any // a result of a tool, or an error
	started []string                            // the programs started
	failing error                               // what starting fails with
	drivers []*driver
}

func newFake(t *testing.T) *fake {
	f := &fake{t: t, args: map[string][]map[string]any{}, answers: map[string]func(map[string]any) any{}}
	f.windows(map[string]any{"window_id": 7, "pid": 70, "app_name": "firefox", "title": "Snakes", "is_on_screen": true, "z_index": 1,
		"bounds": map[string]any{"x": 0, "y": 0, "width": 1280, "height": 900}})
	f.on("get_window_state", func(map[string]any) any {
		return answer(`window state`, map[string]any{
			"window_title": "Snakes", "total_element_count": 3, "snapshot_id": "s00000001",
			"screenshot_width": 1280, "screenshot_height": 900,
			"elements": []any{
				map[string]any{"element_index": 1, "element_token": "t1", "role": "frame", "label": "Snakes", "depth": 2},
				map[string]any{"element_index": 2, "element_token": "t2", "role": "push button", "label": "Back", "depth": 3, "enabled": false},
				map[string]any{"element_index": 3, "element_token": "t3", "role": "entry", "label": "Search", "value": "boa", "depth": 3, "selected": true},
			},
		}, image("PNG"))
	})
	for _, tool := range []string{"bring_to_front", "click", "type_text", "press_key", "hotkey", "scroll", "launch_app"} {
		f.on(tool, func(map[string]any) any { return answer(tool+" done", nil) })
	}
	old := []any{start, idle, closeTimeout}
	start = f.start
	idle, closeTimeout = time.Hour, time.Second
	t.Cleanup(func() {
		start = old[0].(func(context.Context, string) (*driver, error))
		idle, closeTimeout = old[1].(time.Duration), old[2].(time.Duration)
		for _, d := range f.drivers {
			d.end()
		}
	})
	return f
}

// on makes the fake answer the tool with answer: a result, or an error for
// the call to fail with.
func (f *fake) on(tool string, answer func(args map[string]any) any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answers[tool] = answer
}

// windows makes the fake list ws.
func (f *fake) windows(ws ...map[string]any) {
	f.on("list_windows", func(map[string]any) any {
		return answer("windows", map[string]any{"windows": ws})
	})
}

// answer is a result of a tool of the driver.
func answer(text string, structured map[string]any, more ...map[string]any) map[string]any {
	content := append([]map[string]any{{"type": "text", "text": text}}, more...)
	r := map[string]any{"content": content}
	if structured != nil {
		r["structuredContent"] = structured
	}
	return r
}

// image is the content of a result that is a PNG with the bytes data.
func image(data string) map[string]any {
	return map[string]any{"type": "image", "mimeType": "image/png", "data": base64.StdEncoding.EncodeToString([]byte(data))}
}

// start starts the fake as the program path.
func (f *fake) start(ctx context.Context, path string) (*driver, error) {
	f.mu.Lock()
	f.started = append(f.started, path)
	failing := f.failing
	f.mu.Unlock()
	if failing != nil {
		return nil, failing
	}
	toDriver, fromFake := io.Pipe()
	fromDriver, toFake := io.Pipe()
	go f.serve(fromDriver, fromFake)
	d := connect(path, toDriver, toFake, func() { toFake.Close(); fromFake.Close() }, func() string { return "" })
	if err := d.greet(ctx); err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.drivers = append(f.drivers, d)
	f.mu.Unlock()
	return d, nil
}

// serve answers the requests that come on in on out, until in closes.
func (f *fake) serve(in io.Reader, out io.WriteCloser) {
	defer out.Close()
	r := bufio.NewReader(in)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return
		}
		var m struct {
			ID     *int64 `json:"id"`
			Method string `json:"method"`
			Params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		if err := json.Unmarshal(line, &m); err != nil {
			f.t.Errorf("not JSON: %s", line)
			return
		}
		if m.ID == nil {
			continue
		}
		answer := map[string]any{"jsonrpc": "2.0", "id": *m.ID}
		switch m.Method {
		case "initialize":
			answer["result"] = map[string]any{"serverInfo": map[string]any{"name": "cua-driver"}}
		case "tools/call":
			f.mu.Lock()
			f.calls = append(f.calls, m.Params.Name)
			f.args[m.Params.Name] = append(f.args[m.Params.Name], m.Params.Arguments)
			do := f.answers[m.Params.Name]
			f.mu.Unlock()
			switch got := do(m.Params.Arguments).(type) {
			case error:
				answer["result"] = map[string]any{"isError": true, "content": []any{map[string]any{"type": "text", "text": got.Error()}}}
			default:
				answer["result"] = got
			}
		default:
			answer["error"] = map[string]any{"code": -32601, "message": "no method " + m.Method}
		}
		b, _ := json.Marshal(answer)
		if _, err := out.Write(append(b, '\n')); err != nil {
			return
		}
	}
}

// last are the arguments of the last call of tool, as JSON.
func (f *fake) last(tool string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	calls := f.args[tool]
	if len(calls) == 0 {
		return ""
	}
	b, _ := json.Marshal(calls[len(calls)-1])
	return string(b)
}

// called are the tools called so far, and forgets them.
func (f *fake) called() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	calls := strings.Join(f.calls, " ")
	f.calls = nil
	return calls
}

// newComputer is a computer of the tool, whose drivers are those of f.
func newComputer(f *fake) *computer {
	c := &computer{convs: map[string]*conversation{}}
	f.t.Cleanup(c.close)
	return c
}

// call runs the tool with the settings s and the arguments a, as JSON, in
// the conversation of ctx.
func call(ctx context.Context, c *computer, s settings, a string) (string, error) {
	cs, _ := json.Marshal(s)
	run, err := c.tool().Configure(cs)
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
		t.Fatalf("%s:\ngot  %q\nwant %q", what, got, want)
	}
}

func fails(t *testing.T, what string, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("%s: got %v, want an error with %q", what, err, want)
	}
}

var firefox = settings{}

// The tool is for the owner alone.
func TestWhoMayUseIt(t *testing.T) {
	f := newFake(t)
	c := newComputer(f)
	if !c.tool().OwnerOnly {
		t.Fatal("the tool is not for the owner alone")
	}
	peer := a2a.WithOrigin(context.Background(), a2a.Origin{PublicKey: "peer"})
	_, err := call(peer, c, settings{}, `{"action": "windows"}`)
	fails(t, "a peer", err, "for the owner alone")
	_, err = call(context.Background(), c, firefox, `{"action": "jump"}`)
	fails(t, "an action there is not", err, `there is no action "jump"`)
	if len(f.started) != 0 {
		t.Fatalf("started cua-driver for calls it refuses: %v", f.started)
	}
}

// windows lists the windows of apps the owner has not blocked, front ones first,
// and names the apps of the others, never a terminal.
func TestWindows(t *testing.T) {
	f := newFake(t)
	c := newComputer(f)
	f.windows(
		map[string]any{"window_id": 1, "pid": 10, "app_name": "Alacritty", "title": "ouro", "is_on_screen": true, "z_index": 3, "bounds": map[string]any{"x": 0, "y": 0, "width": 800, "height": 600}},
		map[string]any{"window_id": 2, "pid": 20, "app_name": "firefox", "title": "Back", "is_on_screen": false, "z_index": 1, "bounds": map[string]any{"x": 0, "y": 0, "width": 640, "height": 480}},
		map[string]any{"window_id": 3, "pid": 20, "app_name": "firefox", "title": "Front", "is_on_screen": true, "z_index": 2, "bounds": map[string]any{"x": 0, "y": 0, "width": 1280, "height": 900}},
		map[string]any{"window_id": 4, "pid": 40, "app_name": "Slack", "title": "secret", "is_on_screen": true, "z_index": nil, "bounds": map[string]any{"x": 0, "y": 0, "width": 1, "height": 1}},
		map[string]any{"window_id": 5, "pid": nil, "app_name": "ghost", "title": "", "is_on_screen": true, "z_index": 9, "bounds": map[string]any{"x": 0, "y": 0, "width": 1, "height": 1}},
	)
	got, err := call(context.Background(), c, settings{BlockedApps: []string{"sLaCk"}}, `{"action": "windows"}`)
	is(t, "windows", got, err, "window 3: firefox \"Front\" 1280×900\nwindow 2: firefox \"Back\" 640×480 (not on the screen)\nwindows of apps you may not use: Alacritty, Slack")

	got, err = call(context.Background(), c, settings{}, `{"action": "windows"}`)
	if err != nil || strings.Contains(got, "window 1:") || !strings.Contains(got, "window 4: Slack") {
		t.Fatalf("every app but a terminal: %q, %v", got, err)
	}

	got, err = call(context.Background(), c, settings{BlockedApps: []string{"*"}}, `{"action": "windows"}`)
	is(t, "none allowed", got, err, "no window of an app you may use\nwindows of apps you may not use: Alacritty, firefox, Slack")
	if f.started[0] != "cua-driver" || len(f.started) != 1 {
		t.Fatalf("started %v", f.started)
	}
}

// What look shows of a window: its elements, and its screenshot to the LLM.
func TestLook(t *testing.T) {
	f := newFake(t)
	c := newComputer(f)
	run, _ := c.tool().Configure(json.RawMessage(`{"blocked_apps": []}`))
	llm := &looking{call: `{"action": "look", "window": 7}`}
	if _, err := agent.New(llm, "m", "sys", run).Run(context.Background(), "c", "look", agent.Observer{}); err != nil {
		t.Fatal(err)
	}
	want := "window 7: firefox \"Snakes\", 3 elements\n" +
		"[1] frame \"Snakes\"\n" +
		"  [2] push button \"Back\" (disabled)\n" +
		"  [3] entry \"Search\" = \"boa\" (selected)\n" +
		"with a screenshot of 1280×900 pixels, whose points click takes as x and y"
	if llm.result.Content != want {
		t.Fatalf("got\n%s\nwant\n%s", llm.result.Content, want)
	}
	if len(llm.result.Images) != 1 || llm.result.Images[0].Type != "image/png" || string(llm.result.Images[0].Data) != "PNG" {
		t.Fatalf("shown %v", llm.result.Images)
	}
	if got := f.called(); got != "list_windows bring_to_front get_window_state" {
		t.Fatalf("called %s", got)
	}
	if p := f.last("get_window_state"); p != `{"pid":70,"session":"ouro-`+session("c")+`","timeout_ms":5000,"window_id":7}` {
		t.Fatalf("looked with %s", p)
	}

	// Outside of an answer the screenshot goes nowhere, and a query narrows
	// what is looked at.
	got, err := call(context.Background(), c, firefox, `{"action": "look", "window": 7, "query": "search"}`)
	if err != nil || !strings.HasSuffix(got, "without a screenshot: one is shown only to an agent that answers") {
		t.Fatalf("outside of an answer: %q, %v", got, err)
	}
	if p := f.last("get_window_state"); !strings.Contains(p, `"query":"search"`) {
		t.Fatalf("looked with %s", p)
	}

	// A window without a screenshot says why.
	f.on("get_window_state", func(map[string]any) any {
		return answer("state", map[string]any{"elements": []any{}, "screenshot_error": map[string]any{"code": "surface_identity_unproven"}})
	})
	got, err = call(context.Background(), c, firefox, `{"action": "look", "window": 7}`)
	is(t, "no screenshot", got, err, "window 7: firefox \"Snakes\", 0 elements\nwithout a screenshot (surface_identity_unproven)")
}

// A window with more elements than fit is cut, and says how to find the
// rest; the elements cut stay to act on.
func TestLookCut(t *testing.T) {
	f := newFake(t)
	c := newComputer(f)
	var elements []any
	for i := 1; i <= 2000; i++ {
		elements = append(elements, map[string]any{"element_index": i, "element_token": "t", "role": "list item", "label": strings.Repeat("x", 20), "depth": 0})
	}
	f.on("get_window_state", func(map[string]any) any {
		return answer("state", map[string]any{"elements": elements, "truncated": true})
	})
	got, err := call(context.Background(), c, firefox, `{"action": "look", "window": 7}`)
	if err != nil || len(got) > maxText+500 || !strings.Contains(got, "… and more: look with query") || !strings.Contains(got, "than cua-driver went through in time") {
		t.Fatalf("cut: %d bytes, %v\n%s", len(got), err, got[max(0, len(got)-400):])
	}
	if _, err := call(context.Background(), c, firefox, `{"action": "click", "window": 7, "element": 2000}`); err != nil {
		t.Fatalf("clicking an element that was cut: %v", err)
	}
}

// The actions go to the window by the elements of the last look, in the
// front, or where it is in the background.
func TestActions(t *testing.T) {
	f := newFake(t)
	c := newComputer(f)
	ctx := context.Background()
	_, err := call(ctx, c, firefox, `{"action": "click", "window": 7, "element": 3}`)
	fails(t, "before a look", err, "look at window 7 first")
	if _, err := call(ctx, c, firefox, `{"action": "look", "window": 7}`); err != nil {
		t.Fatal(err)
	}
	_, err = call(ctx, c, firefox, `{"action": "click", "window": 7, "element": 9}`)
	fails(t, "an element there is not", err, "shows no element [9]")

	s := `"session":"ouro-` + session("") + `"`
	for _, c2 := range []struct {
		args, tool, want string
	}{
		{`{"action": "click", "window": 7, "element": 3}`, "click", `{"delivery_mode":"foreground","element_token":"t3","pid":70,` + s + `,"window_id":7}`},
		{`{"action": "click", "window": 7, "x": 10, "y": 20, "count": 2, "button": "right"}`, "click", `{"button":"right","count":2,"delivery_mode":"foreground","pid":70,` + s + `,"window_id":7,"x":10,"y":20}`},
		{`{"action": "type", "window": 7, "element": 3, "text": "python"}`, "type_text", `{"delivery_mode":"foreground","element_token":"t3","pid":70,` + s + `,"text":"python","window_id":7}`},
		{`{"action": "key", "window": 7, "key": "Escape"}`, "press_key", `{"delivery_mode":"foreground","key":"Escape","pid":70,` + s + `,"window_id":7}`},
		{`{"action": "key", "window": 7, "key": "ctrl + s"}`, "hotkey", `{"delivery_mode":"foreground","keys":["ctrl","s"],"pid":70,` + s + `,"window_id":7}`},
		{`{"action": "scroll", "window": 7}`, "scroll", `{"delivery_mode":"foreground","direction":"down","pid":70,` + s + `,"window_id":7}`},
		{`{"action": "scroll", "window": 7, "element": 1, "direction": "up", "amount": 5}`, "scroll", `{"amount":5,"delivery_mode":"foreground","direction":"up","element_token":"t1","pid":70,` + s + `,"window_id":7}`},
	} {
		f.called()
		got, err := call(ctx, c, firefox, c2.args)
		is(t, c2.args, got, err, c2.tool+" done")
		if p := f.last(c2.tool); p != c2.want {
			t.Fatalf("%s: called %s with\n%s\nwant\n%s", c2.args, c2.tool, p, c2.want)
		}
		if calls := f.called(); calls != "list_windows bring_to_front "+c2.tool {
			t.Fatalf("%s: called %s", c2.args, calls)
		}
		if p := f.last("bring_to_front"); p != `{"pid":70,"window_id":7}` {
			t.Fatalf("brought to the front %s", p)
		}
	}

	// Enter after the text.
	got, err := call(ctx, c, firefox, `{"action": "type", "window": 7, "text": "boa", "enter": true}`)
	is(t, "enter", got, err, "type_text done\npressed Enter")
	if p := f.last("press_key"); p != `{"delivery_mode":"foreground","key":"Return","pid":70,`+s+`,"window_id":7}` {
		t.Fatalf("pressed Enter with %s", p)
	}

	// In the background the window stays where it is.
	f.called()
	got, err = call(ctx, c, settings{Background: true}, `{"action": "key", "window": 7, "key": "Tab"}`)
	is(t, "background", got, err, "press_key done")
	if calls := f.called(); calls != "list_windows press_key" || !strings.Contains(f.last("press_key"), `"delivery_mode":"background"`) {
		t.Fatalf("in the background: called %s with %s", calls, f.last("press_key"))
	}

	// What an action lacks.
	_, err = call(ctx, c, firefox, `{"action": "click", "window": 7}`)
	fails(t, "click without a target", err, "click needs element, or x and y")
	_, err = call(ctx, c, firefox, `{"action": "type", "window": 7}`)
	fails(t, "type without text", err, "type needs text")
	_, err = call(ctx, c, firefox, `{"action": "key", "window": 7, "key": "ctrl+"}`)
	fails(t, "a broken key", err, `"ctrl+" is no key`)
	_, err = call(ctx, c, firefox, `{"action": "look"}`)
	fails(t, "no window", err, "which window?")
	_, err = call(ctx, c, firefox, `{"action": "look", "window": 8}`)
	fails(t, "a window there is not", err, "there is no window 8")

	// What the driver refuses is the error of the call.
	f.on("click", func(map[string]any) any { return errors.New("point_outside_window") })
	_, err = call(ctx, c, firefox, `{"action": "click", "window": 7, "x": 5000, "y": 1}`)
	fails(t, "refused", err, "point_outside_window")
}

// No action reaches a window of an app the owner did not allow, nor a
// terminal, whatever the settings say.
func TestForbiddenWindows(t *testing.T) {
	f := newFake(t)
	c := newComputer(f)
	f.windows(
		map[string]any{"window_id": 1, "pid": 10, "app_name": "kitty", "title": "ouro", "is_on_screen": true, "z_index": 1, "bounds": map[string]any{}},
		map[string]any{"window_id": 2, "pid": 20, "app_name": "org.gnome.Console", "title": "", "is_on_screen": true, "z_index": 1, "bounds": map[string]any{}},
		map[string]any{"window_id": 3, "pid": 30, "app_name": "Slack", "title": "", "is_on_screen": true, "z_index": 1, "bounds": map[string]any{}},
	)
	everything := settings{}
	for _, w := range []string{"1", "2"} {
		_, err := call(context.Background(), c, everything, `{"action": "key", "window": `+w+`, "key": "Return"}`)
		fails(t, "a terminal", err, "which is blocked")
	}
	_, err := call(context.Background(), c, settings{BlockedApps: []string{"sLaCk"}}, `{"action": "look", "window": 3}`)
	fails(t, "a blocked app", err, "window 3 is of Slack, which is blocked")
	if calls := f.called(); strings.Contains(calls, "press_key") || strings.Contains(calls, "get_window_state") || strings.Contains(calls, "bring_to_front") {
		t.Fatalf("called %s", calls)
	}
}

// open launches apps not blocked, never a terminal, and nothing
// that is no name of an app.
func TestOpen(t *testing.T) {
	f := newFake(t)
	c := newComputer(f)
	s := settings{BlockedApps: []string{"GiMp"}}
	got, err := call(context.Background(), c, s, `{"action": "open", "app": "firefox"}`)
	is(t, "open", got, err, "launch_app done\nFind its window with windows.")
	if p := f.last("launch_app"); p != `{"name":"firefox"}` {
		t.Fatalf("launched %s", p)
	}
	for app, want := range map[string]string{
		"gimp":                 "blocked you from opening gimp",
		"kitty":                "blocked you from opening kitty",
		"firefox --new-window": "is no name of an app",
		"firefox -P":           "is no name of an app",
		"/usr/bin/firefox":     "is no name of an app",
		"https://example.com":  "is no name of an app",
		"":                     "is no name of an app",
		"firefox; rm -rf ~":    "is no name of an app",
		"Firefox\n--safe-mode": "is no name of an app",
	} {
		a, _ := json.Marshal(map[string]string{"action": "open", "app": app})
		_, err := call(context.Background(), c, s, string(a))
		fails(t, app, err, want)
	}
	_, err = call(context.Background(), c, settings{BlockedApps: []string{"*"}}, `{"action": "open", "app": "gimp"}`)
	fails(t, "every app blocked", err, "blocked you from opening gimp")
	got, err = call(context.Background(), c, settings{}, `{"action": "open", "app": "gimp"}`)
	is(t, "default permits open", got, err, "launch_app done\nFind its window with windows.")
	if n := len(f.args["launch_app"]); n != 2 {
		t.Fatalf("launched %d times", n)
	}
}

// Which names are those of terminals.
func TestTerminal(t *testing.T) {
	for name, want := range map[string]bool{
		"kitty": true, "Alacritty": true, "org.gnome.Console": true, "com.mitchellh.ghostty": true,
		"org.kde.konsole": true, "Terminal": true, "iTerm2": true, "firefox": false, "org.gnome.Nautilus": false,
		"Console": true, "Google Chrome": false, "steam": false,
	} {
		if got := terminal(name); got != want {
			t.Errorf("terminal(%q) = %v", name, got)
		}
	}
}

// The driver starts with the first call, runs on, starts again once it
// ended, and is ended when it was not used for a while or the plugin stops.
func TestDriver(t *testing.T) {
	f := newFake(t)
	c := newComputer(f)
	ctx := context.Background()
	f.failing = errors.New("cua-driver is not installed")
	_, err := call(ctx, c, firefox, `{"action": "windows"}`)
	fails(t, "not installed", err, "cua-driver is not installed")
	f.failing = nil

	for range 2 {
		if _, err := call(ctx, c, firefox, `{"action": "windows"}`); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.drivers) != 1 {
		t.Fatalf("started %d drivers", len(f.drivers))
	}
	// Another program in the settings starts that one.
	if _, err := call(ctx, c, settings{Driver: "/opt/cua-driver"}, `{"action": "windows"}`); err != nil {
		t.Fatal(err)
	}
	if f.started[len(f.started)-1] != "/opt/cua-driver" {
		t.Fatalf("started %v", f.started)
	}
	waitFor(t, f.drivers[0].closed)

	// A driver that ended fails what waits for it, and starts again.
	d := f.drivers[1]
	d.end()
	waitFor(t, d.closed)
	if _, err := d.call(ctx, "list_windows", nil); err == nil || !strings.Contains(err.Error(), "cua-driver ended") {
		t.Fatalf("an ended driver: %v", err)
	}
	if _, err := call(ctx, c, settings{Driver: "/opt/cua-driver"}, `{"action": "windows"}`); err != nil {
		t.Fatal(err)
	}
	if len(f.drivers) != 3 {
		t.Fatalf("started %d drivers", len(f.drivers))
	}

	// Not used for idle, it ends.
	idle = 10 * time.Millisecond
	if _, err := call(ctx, c, firefox, `{"action": "windows"}`); err != nil {
		t.Fatal(err)
	}
	last := f.drivers[len(f.drivers)-1]
	waitFor(t, last.closed)
	c.mu.Lock()
	if c.drv != nil {
		t.Error("the driver that ended is kept")
	}
	c.mu.Unlock()

	idle = time.Hour
	if _, err := call(ctx, c, firefox, `{"action": "windows"}`); err != nil {
		t.Fatal(err)
	}
	c.close()
	waitFor(t, f.drivers[len(f.drivers)-1].closed)
}

// A request of the driver is answered that ouro offers nothing, and a call
// that is given up does not wait for its answer.
func TestProtocol(t *testing.T) {
	toDriver, fromServer := io.Pipe()
	fromDriver, toServer := io.Pipe()
	d := connect("x", toDriver, toServer, func() { toServer.Close(); fromServer.Close() }, func() string { return "it broke" })
	defer d.end()
	got := make(chan string, 1)
	go func() {
		r := bufio.NewReader(fromDriver)
		r.ReadBytes('\n') // the call
		fromServer.Write([]byte(`{"jsonrpc":"2.0","id":"r1","method":"roots/list"}` + "\n"))
		line, _ := r.ReadBytes('\n')
		got <- string(line)
		io.Copy(io.Discard, r)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := d.call(ctx, "list_windows", nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("given up: %v", err)
	}
	if line := <-got; line != `{"error":{"code":-32601,"message":"not supported"},"id":"r1","jsonrpc":"2.0"}`+"\n" {
		t.Fatalf("answered %s", line)
	}
	fromServer.Close()
	waitFor(t, d.closed)
	if _, err := d.call(context.Background(), "list_windows", nil); err == nil || err.Error() != "cua-driver ended: it broke" {
		t.Fatalf("ended: %v", err)
	}
}

// What the owner is shown of a call.
func TestShow(t *testing.T) {
	for call, want := range map[string]string{
		`{"action": "windows"}`:                             "looking at the windows",
		`{"action": "open", "app": "gimp"}`:                 "opening gimp",
		`{"action": "look", "window": 7}`:                   "looking at window 7",
		`{"action": "click", "window": 7, "element": 3}`:    "clicking [3] in window 7",
		`{"action": "click", "window": 7, "x": 1, "y": 2}`:  "clicking at 1,2 in window 7",
		`{"action": "click", "window": 7}`:                  "clicking in window 7",
		`{"action": "type", "window": 7, "text": "secret"}`: "typing into window 7",
		`{"action": "key", "window": 7, "key": "ctrl+s"}`:   "pressing ctrl+s in window 7",
		`{"action": "scroll", "window": 7}`:                 "scrolling window 7",
		`{"action": "jump"}`:                                "",
		`not JSON`:                                          "",
	} {
		if got := Show(json.RawMessage(call)); got != want {
			t.Errorf("Show(%s) = %q, want %q", call, got, want)
		}
	}
}

// session is the session of cua-driver of the conversation id.
func session(id string) string {
	return (&computer{convs: map[string]*conversation{}}).conversation(id).session[len("ouro-"):]
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for range 200 {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("waited in vain")
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

// What the driver fails with, or says that the tool cannot read, is the
// error of the call.
func TestFailures(t *testing.T) {
	f := newFake(t)
	c := newComputer(f)
	ctx := context.Background()
	_, err := call(ctx, c, firefox, `{"action": "windows", "nope": 1}`)
	fails(t, "an argument there is not", err, "nope")

	f.on("list_windows", func(map[string]any) any { return errors.New("no display") })
	for _, a := range []string{`{"action": "windows"}`, `{"action": "look", "window": 7}`} {
		_, err = call(ctx, c, firefox, a)
		fails(t, a, err, "no display")
	}
	f.on("list_windows", func(map[string]any) any { return answer("odd", nil) })
	_, err = call(ctx, c, firefox, `{"action": "windows"}`)
	fails(t, "windows it cannot read", err, "cua-driver lists no windows: odd")
	f = newFake(t)
	c = newComputer(f)

	f.on("launch_app", func(map[string]any) any { return errors.New("nothing to launch") })
	_, err = call(ctx, c, firefox, `{"action": "open", "app": "firefox"}`)
	fails(t, "launch", err, "nothing to launch")

	f.on("get_window_state", func(map[string]any) any { return errors.New("no such window") })
	_, err = call(ctx, c, firefox, `{"action": "look", "window": 7}`)
	fails(t, "look", err, "no such window")
	f.on("get_window_state", func(map[string]any) any { return answer("odd", nil) })
	_, err = call(ctx, c, firefox, `{"action": "look", "window": 7}`)
	fails(t, "a state it cannot read", err, "cua-driver says nothing of window 7: odd")

	// A cut list of elements says of how many, and a screenshot without its
	// size is shown all the same.
	f.on("get_window_state", func(map[string]any) any {
		return answer("state", map[string]any{"total_element_count": 9, "elements": []any{
			map[string]any{"element_index": 4, "element_token": "t4", "role": "label", "depth": 1},
		}}, image("PNG"))
	})
	run, _ := c.tool().Configure(json.RawMessage(`{"blocked_apps": []}`))
	llm := &looking{call: `{"action": "look", "window": 7}`}
	if _, err := agent.New(llm, "m", "sys", run).Run(ctx, "c", "look", agent.Observer{}); err != nil {
		t.Fatal(err)
	}
	if want := "window 7: firefox \"Snakes\", 1 elements of 9\n[4] label\nwith a screenshot, whose points click takes as x and y"; llm.result.Content != want {
		t.Fatalf("got %q", llm.result.Content)
	}

	// Enter that fails after the text.
	f.on("press_key", func(map[string]any) any { return errors.New("no keyboard") })
	_, err = call(ctx, c, firefox, `{"action": "type", "window": 7, "text": "boa", "enter": true}`)
	fails(t, "enter", err, "type_text done\nbut Enter failed: no keyboard")
}

// Where cua-driver lists no elements but writes them as Markdown, look
// shows them nested as they are, without their actions, and the actions take
// them by the tokens of the snapshot, from [0] on.
func TestLookTree(t *testing.T) {
	f := newFake(t)
	c := newComputer(f)
	f.on("get_window_state", func(map[string]any) any {
		return answer("state", map[string]any{"snapshot_id": "s00000002", "total_element_count": 5, "tree_markdown": "" +
			"- [0] window \"Files\" [actions=[slot.reload,view.zoom-in]]\n" +
			"                    - tab panel = \"Files\"\n" +
			"                        - [1] grid \"Content View\" [actions=[]]\n" +
			"                                  - [2] grid cell \"README.md. File\" [actions=[click]]\n" +
			"not a line of the tree\n" +
			"                        - [3] tool bar \"\"\n" +
			"                                  - button = \"Forward\" (disabled)\n"})
	})
	got, err := call(context.Background(), c, firefox, `{"action": "look", "window": 7}`)
	is(t, "tree", got, err, "window 7: firefox \"Snakes\", 4 elements of 5\n"+
		"[0] window \"Files\"\n"+
		"  tab panel = \"Files\"\n"+
		"    [1] grid \"Content View\"\n"+
		"      [2] grid cell \"README.md. File\"\n"+
		"    [3] tool bar \"\"\n"+
		"      button = \"Forward\" (disabled)\n"+
		"without a screenshot")
	if _, err := call(context.Background(), c, firefox, `{"action": "click", "window": 7, "element": 0}`); err != nil {
		t.Fatal(err)
	}
	if p := f.last("click"); !strings.Contains(p, `"element_token":"s00000002:0"`) {
		t.Fatalf("clicked with %s", p)
	}
}
