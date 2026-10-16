package browser

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
)

// errClosed is what a call fails with once the browser has gone: it ended,
// or was ended.
var errClosed = errors.New("the browser has closed")

// conn is a connection to the DevTools of a browser over its pipe: messages
// of JSON that each end with a zero byte, the commands one way, their
// replies and the events the other.
type conn struct {
	w     io.WriteCloser
	write func(context.Context, []byte) error // optional message transport

	mu       sync.Mutex // guards what follows
	next     int64
	pending  map[int64]chan reply
	sessions map[string]*session // the sessions listened to
	detached map[string]bool     // sessions whose target went away
	done     chan struct{}       // closed once the browser is gone
}

// session is what the connection keeps of a session it listens to: what
// its page says without being asked.
type session struct {
	events  chan event // of the main frame and the frames in it, for an action to wait for
	dialog  *dialog    // the dialog the page shows, nil while it shows none
	opened  *opening   // the next dialog it shows
	console []string   // what the page wrote to its console, and its errors
	dropped int        // what was left out of console as there was too much
}

// The limits of the console of a page: the lines kept since it was last
// read, and the bytes of a line.
const (
	maxConsole     = 200
	maxConsoleLine = 1 << 10
)

// opening is the next dialog a page shows: done is closed once it does,
// after d is set to the dialog.
type opening struct {
	done chan struct{}
	d    *dialog
}

// dialog is a dialog of JavaScript a page shows, like a confirm. While it is
// open the page does nothing else: it is the error of everything asked of
// it then.
type dialog struct {
	Type    string `json:"type"` // alert, confirm, prompt or beforeunload
	Message string `json:"message"`
	Default string `json:"defaultPrompt"`
}

func (d *dialog) Error() string { return fmt.Sprintf("the page shows a %s %q", d.Type, d.Message) }

// message is what goes over the pipe: a command, its reply or an event.
type message struct {
	ID        int64           `json:"id,omitempty"`
	Method    string          `json:"method,omitempty"`
	Params    any             `json:"params,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *cdpError       `json:"error,omitempty"`
}

// reply is the answer to a command.
type reply struct {
	result json.RawMessage
	err    error
}

// event is something a session reports without being asked, like a page
// that has loaded.
type event struct {
	method string
	params json.RawMessage
}

// cdpError is a command the browser refused.
type cdpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *cdpError) Error() string { return e.Message }

// newConn returns a connection that reads what the browser says from r and
// writes commands to w, until r ends.
func newConn(r io.Reader, w io.WriteCloser) *conn {
	c := &conn{w: w, pending: map[int64]chan reply{}, sessions: map[string]*session{},
		detached: map[string]bool{}, done: make(chan struct{})}
	go c.read(r)
	return c
}

// read hands every message of r to whoever waits for it.
func (c *conn) read(r io.Reader) {
	br := bufio.NewReader(r)
	for {
		line, err := br.ReadBytes(0)
		if err != nil {
			break
		}
		var m struct {
			message
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(line[:len(line)-1], &m) != nil {
			continue
		}
		c.mu.Lock()
		switch {
		case m.ID != 0:
			if ch, ok := c.pending[m.ID]; ok {
				delete(c.pending, m.ID)
				r := reply{result: m.Result}
				if m.Error != nil {
					r.err = m.Error
				}
				ch <- r
			}
		case m.Method == "Target.detachedFromTarget":
			var p struct {
				SessionID string `json:"sessionId"`
			}
			json.Unmarshal(m.Params, &p)
			c.detached[p.SessionID] = true
		default:
			if s, ok := c.sessions[m.SessionID]; ok {
				s.take(m.Method, m.Params)
			}
		}
		c.mu.Unlock()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	close(c.done)
	for id, ch := range c.pending {
		delete(c.pending, id)
		ch <- reply{err: errClosed}
	}
}

// call runs method with params in session ("" for the browser itself) and
// decodes its result into result, unless that is nil.
func (c *conn) call(ctx context.Context, session, method string, params any, result any) error {
	ch := make(chan reply, 1)
	if err := waitLock(ctx, c.mu.TryLock); err != nil {
		return err
	}
	select {
	case <-c.done:
		c.mu.Unlock()
		return errClosed
	default:
	}
	c.next++
	id := c.next
	c.pending[id] = ch
	b, _ := json.Marshal(message{ID: id, Method: method, Params: params, SessionID: session}) // of plain values: cannot fail
	var err error
	if c.write != nil {
		err = c.write(ctx, append(b, 0))
	} else {
		_, err = c.w.Write(append(b, 0))
	}
	if err != nil {
		delete(c.pending, id)
	}
	c.mu.Unlock()
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errClosed
	}
	select {
	case r := <-ch:
		if r.err != nil {
			return fmt.Errorf("%s: %w", method, r.err)
		}
		if result != nil {
			return json.Unmarshal(r.result, result)
		}
		return nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return ctx.Err()
	}
}

// take keeps what the event method with params says of the page. The
// connection is locked.
func (s *session) take(method string, params json.RawMessage) {
	switch {
	case strings.HasPrefix(method, "Page.frame"):
		select {
		case s.events <- event{method, params}:
		default: // nobody waits for this many: they are old
		}
	case method == "Page.javascriptDialogOpening":
		var d dialog
		json.Unmarshal(params, &d)
		s.dialog, s.opened.d = &d, &d
		close(s.opened.done)
	case method == "Page.javascriptDialogClosed":
		s.answered()
	default:
		if line := consoleLine(method, params); line != "" {
			if len(s.console) == maxConsole {
				s.console = s.console[1:]
				s.dropped++
			}
			s.console = append(s.console, line)
		}
	}
}

// answered forgets the dialog of the page, if it shows one.
func (s *session) answered() {
	if s.dialog != nil {
		s.dialog = nil
		s.opened = &opening{done: make(chan struct{})}
	}
}

// listen returns the events of the frames of session from now on, until
// forget.
func (c *conn) listen(id string) <-chan event {
	s := &session{events: make(chan event, 64), opened: &opening{done: make(chan struct{})}}
	c.mu.Lock()
	c.sessions[id] = s
	c.mu.Unlock()
	return s.events
}

// dialog returns the dialog the page of session shows, nil if none, and
// the next one it shows; of a session not listened to, one that never comes.
func (c *conn) dialog(id string) (*dialog, *opening) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.sessions[id]
	if !ok {
		return nil, &opening{}
	}
	return s.dialog, s.opened
}

// answered forgets the dialog of session, which was answered: the event
// that it closed may come after the next call.
func (c *conn) answered(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s, ok := c.sessions[id]; ok {
		s.answered()
	}
}

// console returns what the page of session wrote to its console since the
// last call, and how much of it was left out.
func (c *conn) console(id string) ([]string, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.sessions[id]
	if !ok {
		return nil, 0
	}
	lines, dropped := s.console, s.dropped
	s.console, s.dropped = nil, 0
	return lines, dropped
}

// forget stops listening to session.
func (c *conn) forget(session string) {
	c.mu.Lock()
	delete(c.sessions, session)
	delete(c.detached, session)
	c.mu.Unlock()
}

// gone reports whether the target of session went away, as a tab the owner
// closed in the window of the browser.
func (c *conn) gone(session string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.detached[session]
}

// closed reports whether the browser has gone.
func (c *conn) closed() bool {
	select {
	case <-c.done:
		return true
	default:
		return false
	}
}
