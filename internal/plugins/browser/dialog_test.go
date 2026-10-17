package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// A dialog the page shows interrupts the action that made it, and is
// answered with the action dialog.
func TestDialog(t *testing.T) {
	f := newFake(t)
	b := newBrowser(f)
	ctx := context.Background()
	f.page["point"] = map[string]any{"x": 1, "y": 1, "what": `button "Delete"`}
	f.on("Input.dispatchMouseEvent", func(params json.RawMessage) (any, error) {
		if !strings.Contains(string(params), "mouseReleased") {
			return map[string]any{}, nil
		}
		f.emit("Page.javascriptDialogOpening", map[string]string{"type": "confirm", "message": "Sure?"})
		return nil, errSilent // the browser answers once the dialog is
	})
	want := `click: the page shows a confirm "Sure?" and waits for an answer: answer it with the action dialog, accept true for OK or false for Cancel`
	got, err := call(ctx, b, settings{}, `{"action": "click", "element": 1}`)
	is(t, "click", got, err, want)
	// While it is open the page does nothing else.
	sent := len(f.got())
	got, err = call(ctx, b, settings{}, `{"action": "read"}`)
	is(t, "read", got, err, strings.Replace(want, "click", "read", 1))
	if len(f.got()) != sent {
		t.Fatalf("sent %v", f.got()[sent:])
	}

	_, err = call(ctx, b, settings{}, `{"action": "dialog"}`)
	fails(t, "without accept", err, `the page shows a confirm "Sure?": dialog needs accept`)
	f.fail("Page.handleJavaScriptDialog", "refused")
	_, err = call(ctx, b, settings{}, `{"action": "dialog", "accept": false}`)
	fails(t, "refused", err, "Page.handleJavaScriptDialog: refused")
	f.on("Page.handleJavaScriptDialog", func(json.RawMessage) (any, error) { return map[string]any{}, nil })
	got, err = call(ctx, b, settings{}, `{"action": "dialog", "accept": false}`)
	is(t, "cancel", got, err, `answered the confirm "Sure?" with Cancel; now at "Start" https://example.com/`)
	if p := f.last("Page.handleJavaScriptDialog"); p != `{"accept":false}` {
		t.Fatalf("answered with %s", p)
	}
	_, err = call(ctx, b, settings{}, `{"action": "dialog", "accept": true}`)
	fails(t, "no dialog", err, "the page shows no dialog")

	// A prompt, which the browser says closed only after the next call
	// began; then an alert, which is accepted.
	f.on("Input.dispatchKeyEvent", func(json.RawMessage) (any, error) {
		f.emit("Page.javascriptDialogOpening", map[string]string{"type": "prompt", "message": "Name?", "defaultPrompt": "you"})
		return nil, errSilent
	})
	got, err = call(ctx, b, settings{}, `{"action": "key", "key": "Enter"}`)
	is(t, "prompt", got, err, `key: the page shows a prompt "Name?" and waits for an answer: answer it with the action dialog, accept true and the text to enter (it offers "you"), or false for Cancel`)
	got, err = call(ctx, b, settings{}, `{"action": "dialog", "accept": true, "text": "ouro"}`)
	is(t, "answer", got, err, `answered the prompt "Name?" with "ouro"; now at "Start" https://example.com/`)
	if p := f.last("Page.handleJavaScriptDialog"); p != `{"accept":true,"promptText":"ouro"}` {
		t.Fatalf("answered with %s", p)
	}
	f.on("Input.dispatchKeyEvent", func(json.RawMessage) (any, error) {
		f.emit("Page.javascriptDialogClosed", map[string]any{"result": true}) // of the prompt
		f.emit("Page.javascriptDialogOpening", map[string]string{"type": "alert", "message": "Saved"})
		return nil, errSilent
	})
	if _, err = call(ctx, b, settings{}, `{"action": "key", "key": "Enter"}`); err != nil {
		t.Fatal(err)
	}
	got, err = call(ctx, b, settings{}, `{"action": "dialog", "accept": true, "text": "ignored"}`)
	is(t, "alert", got, err, `answered the alert "Saved" with OK; now at "Start" https://example.com/`)
	if p := f.last("Page.handleJavaScriptDialog"); p != `{"accept":true}` {
		t.Fatalf("answered with %s", p)
	}
}

// What the tool says of the dialogs there are.
func TestWaiting(t *testing.T) {
	for d, want := range map[dialog]string{
		{Type: "alert", Message: "Hi"}:      `the page shows a alert "Hi" and waits for an answer: answer it with the action dialog, accept true`,
		{Type: "beforeunload", Message: ""}: `the page shows a beforeunload "" and waits for an answer: answer it with the action dialog, accept true to leave the page, or false to stay`,
	} {
		if got := waiting(&d); got != want {
			t.Errorf("%v: %q", d, got)
		}
	}
}

// The console returns what the page wrote since the last call, of too much
// the end.
func TestConsole(t *testing.T) {
	f := newFake(t)
	b := newBrowser(f)
	ctx := context.Background()
	got, err := call(ctx, b, settings{}, `{"action": "console"}`)
	is(t, "nothing", got, err, "the page wrote nothing to its console since the last call")

	f.on("Input.dispatchKeyEvent", func(params json.RawMessage) (any, error) {
		if strings.Contains(string(params), "keyUp") {
			return map[string]any{}, nil
		}
		f.emit("Runtime.consoleAPICalled", map[string]any{"type": "log", "args": []map[string]any{{"type": "string", "value": "hello"}, {"type": "number", "value": 4}}})
		f.emit("Runtime.exceptionThrown", map[string]any{"exceptionDetails": map[string]any{"text": "Uncaught", "exception": map[string]any{"description": "Error: boom\n    at x (a.js:1:2)\n    at y (a.js:3:4)"}}})
		f.emit("Log.entryAdded", map[string]any{"entry": map[string]any{"level": "error", "text": "Failed to load resource: 404", "url": "https://example.com/a.png"}})
		f.emit("Page.lifecycleEvent", map[string]any{"name": "load"}) // no line
		return map[string]any{}, nil
	})
	if _, err := call(ctx, b, settings{}, `{"action": "key", "key": "Tab"}`); err != nil {
		t.Fatal(err)
	}
	got, err = call(ctx, b, settings{}, `{"action": "console"}`)
	is(t, "lines", got, err, "[log] hello 4\n[exception] Error: boom at x (a.js:1:2)\n[error] Failed to load resource: 404 (https://example.com/a.png)")
	got, err = call(ctx, b, settings{}, `{"action": "console"}`)
	is(t, "read", got, err, "the page wrote nothing to its console since the last call")

	// More than are kept, and more than a call returns.
	long := strings.Repeat("x", maxConsoleLine-10)
	f.on("Input.dispatchKeyEvent", func(params json.RawMessage) (any, error) {
		if strings.Contains(string(params), "keyUp") {
			return map[string]any{}, nil
		}
		for i := range maxConsole + 5 {
			f.emit("Runtime.consoleAPICalled", map[string]any{"type": "log", "args": []map[string]any{{"type": "string", "value": fmt.Sprint(i, long)}}})
		}
		return map[string]any{}, nil
	})
	if _, err := call(ctx, b, settings{}, `{"action": "key", "key": "Tab"}`); err != nil {
		t.Fatal(err)
	}
	got, err = call(ctx, b, settings{}, `{"action": "console"}`)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(got, "\n")
	if len(got) > maxPage+100 || !strings.HasPrefix(lines[1], "[log] ") || !strings.HasPrefix(lines[len(lines)-1], fmt.Sprint("[log] ", maxConsole+4, "x")) {
		t.Fatalf("%d bytes, %d lines: %.100q … %.30q", len(got), len(lines), got, lines[len(lines)-1])
	}
	if want := fmt.Sprintf("[%d lines before these were left out]", maxConsole+5-(len(lines)-1)); lines[0] != want {
		t.Fatalf("%q, want %q", lines[0], want)
	}
}

// How the lines of the console read.
func TestConsoleLine(t *testing.T) {
	for event, want := range map[string]string{
		`Runtime.consoleAPICalled {"type":"warning","args":[{"type":"string","value":"a"},{"type":"undefined"},{"type":"object","subtype":"null","value":null},` +
			`{"type":"boolean","value":true},{"type":"function","description":"function f() {}"},{"type":"symbol"}]}`: `[warning] a undefined null true function f() {} symbol`,
		`Runtime.consoleAPICalled {"type":"log","args":[{"type":"object","preview":{"properties":[{"name":"a","type":"number","value":"1"},{"name":"b","type":"string","value":"x"}],"overflow":true}},` +
			`{"type":"object","subtype":"array","preview":{"subtype":"array","properties":[{"name":"0","type":"string","value":"y"},{"name":"1","type":"number","value":"2"}]}}]}`: `[log] {a: 1, b: "x", …} ["y", 2]`,
		`Runtime.exceptionThrown {"exceptionDetails":{"text":"Uncaught SyntaxError"}}`:                                               "[exception] Uncaught SyntaxError",
		`Log.entryAdded {"entry":{"level":"verbose","text":"noise"}}`:                                                                "",
		`Log.entryAdded {"entry":{"level":"warning","text":"deprecated"}}`:                                                           "[warning] deprecated",
		`Runtime.executionContextCreated {"context":{}}`:                                                                             "",
		`Runtime.consoleAPICalled {"type":"log","args":[{"type":"string","value":"x` + strings.Repeat("ä", maxConsoleLine) + `"}]}`:  "[log] x" + strings.Repeat("ä", (maxConsoleLine-8)/2) + "…",
		`Runtime.consoleAPICalled {"type":"log","args":[{"type":"string","value":"` + strings.Repeat("x", maxConsoleLine-6) + `"}]}`: "[log] " + strings.Repeat("x", maxConsoleLine-6),
	} {
		method, params, _ := strings.Cut(event, " ")
		if got := consoleLine(method, json.RawMessage(params)); got != want {
			t.Errorf("%.60s: %.80q, want %.80q", event, got, want)
		}
	}
}
