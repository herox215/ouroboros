package browser

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// fake is a browser for the tests: it answers the commands of DevTools the
// way a browser does, as far as the tool needs, unless a test says other
// answers with on, and keeps what it was sent.
type fake struct {
	t *testing.T

	mu       sync.Mutex
	sent     []string // the methods, with the function of page.js for Runtime.evaluate
	params   map[string][]json.RawMessage
	answers  map[string]func(params json.RawMessage) (any, error)
	page     map[string]any // what the functions of page.js return
	sessions int
	started  []bool // the visible of every browser started
	failing  error  // what starting a browser fails with
	procs    []*process
	// emit sends an event of the session of the command that is answered,
	// for an answer of on.
	emit func(method string, params any)
}

// errSilent is what an answer of on returns for the fake to answer
// nothing, as a browser does while its page shows a dialog.
var errSilent = errors.New("no answer")

func newFake(t *testing.T) *fake {
	f := &fake{t: t, params: map[string][]json.RawMessage{}, answers: map[string]func(json.RawMessage) (any, error){},
		page: map[string]any{
			"read": map[string]any{"title": "Start", "url": "https://example.com/", "text": "Hello", "elements": 0},
			"view": map[string]any{"title": "Start", "url": "https://example.com/", "y": 0, "height": 2000, "width": 1280, "screen": 900},
		}}
	old := []any{start, grace, loadTimeout, idle, closeTimeout}
	start = f.start
	grace, loadTimeout, idle, closeTimeout = 10*time.Millisecond, 50*time.Millisecond, time.Hour, time.Second
	t.Cleanup(func() {
		start = old[0].(func(context.Context, string, bool) (*process, error))
		grace, loadTimeout, idle, closeTimeout = old[1].(time.Duration), old[2].(time.Duration), old[3].(time.Duration), old[4].(time.Duration)
		for _, p := range f.procs {
			p.end()
		}
	})
	return f
}

// on makes the fake answer method, or the function of page.js, with answer.
func (f *fake) on(method string, answer func(params json.RawMessage) (any, error)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.answers[method] = answer
}

// fail makes method fail.
func (f *fake) fail(method, message string) {
	f.on(method, func(json.RawMessage) (any, error) { return nil, &cdpError{Code: -32000, Message: message} })
}

// got are the methods the fake was sent.
func (f *fake) got() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.sent)
}

// last is what the last call of method had as its params.
func (f *fake) last(method string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	all := f.params[method]
	if len(all) == 0 {
		return ""
	}
	return string(all[len(all)-1])
}

// start starts a fake browser, as launch starts one.
func (f *fake) start(_ context.Context, _ string, visible bool) (*process, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failing != nil {
		return nil, f.failing
	}
	f.started = append(f.started, visible)
	commands, toFake := io.Pipe()
	fromFake, replies := io.Pipe()
	c := newConn(fromFake, toFake)
	var w sync.Mutex
	go func() {
		br := bufio.NewReader(commands)
		for {
			line, err := br.ReadBytes(0)
			if err != nil {
				return
			}
			var m struct {
				ID        int64           `json:"id"`
				Method    string          `json:"method"`
				Params    json.RawMessage `json:"params"`
				SessionID string          `json:"sessionId"`
			}
			json.Unmarshal(line[:len(line)-1], &m)
			send := func(v any) {
				b, _ := json.Marshal(v)
				w.Lock()
				defer w.Unlock()
				replies.Write(append(b, 0))
			}
			result, err := f.answer(m.Method, m.Params, func(method string, params any) {
				send(map[string]any{"method": method, "params": params, "sessionId": m.SessionID})
			})
			if errors.Is(err, errSilent) {
				continue
			}
			if err != nil {
				send(map[string]any{"id": m.ID, "error": err})
				continue
			}
			send(map[string]any{"id": m.ID, "result": result})
		}
	}()
	var once sync.Once
	p := &process{conn: c, visible: visible, end: func() {
		once.Do(func() {
			toFake.Close()
			replies.Close()
			<-c.done
		})
	}}
	f.procs = append(f.procs, p)
	return p, nil
}

// answer is what the fake answers method with, which may send events of the
// session it runs in with event before.
func (f *fake) answer(method string, params json.RawMessage, event func(method string, params any)) (any, error) {
	name := method
	if method == "Runtime.evaluate" {
		var p struct {
			Expression string `json:"expression"`
		}
		json.Unmarshal(params, &p)
		call, ok := strings.CutPrefix(p.Expression, pageJS+".")
		if !ok {
			f.t.Errorf("an expression that is not of page.js: %.80s", p.Expression)
		}
		name, _, _ = strings.Cut(call, "(")
	}
	f.mu.Lock()
	f.sent = append(f.sent, name)
	f.params[name] = append(f.params[name], params)
	answer := f.answers[name]
	value, isPage := f.page[name]
	f.emit = event
	f.mu.Unlock()
	if answer != nil {
		return answer(params)
	}
	loads := func() {
		event("Page.frameStartedLoading", map[string]string{"frameId": "other"})
		event("Page.frameStartedLoading", map[string]string{"frameId": "T"})
		event("Page.frameStoppedLoading", map[string]string{"frameId": "T"})
	}
	switch {
	case isPage:
		return map[string]any{"result": map[string]any{"value": value}}, nil
	case method == "Target.createBrowserContext":
		return map[string]string{"browserContextId": "C"}, nil
	case method == "Target.createTarget":
		return map[string]string{"targetId": "T"}, nil
	case method == "Target.attachToTarget":
		f.mu.Lock()
		f.sessions++
		id := fmt.Sprintf("S%d", f.sessions)
		f.mu.Unlock()
		return map[string]string{"sessionId": id}, nil
	case method == "Page.navigate", method == "Page.navigateToHistoryEntry":
		loads()
		return map[string]string{"frameId": "T"}, nil
	case method == "Page.getNavigationHistory":
		return map[string]any{"currentIndex": 2, "entries": []map[string]int{{"id": 1}, {"id": 2}, {"id": 3}}}, nil
	case method == "Page.captureScreenshot":
		return map[string]string{"data": "SlBFRw=="}, nil // JPEG
	}
	return map[string]any{}, nil
}
