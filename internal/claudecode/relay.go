package claudecode

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
)

// maxBody is the most bytes a request of the CLI may have, and maxError
// those of a refusal of the API that are kept to tell it.
const (
	maxBody  = 256 << 20
	maxError = 64 << 10
)

// relayOptions are what a relay does with the request it lets through.
type relayOptions struct {
	upstream string
	// frames are the messages ouro handed the CLI, which the request is
	// sent with: see stabilize.
	frames []frame
	// tools are the tools the request is sent with, in the form of the API,
	// in place of those of the CLI.
	tools []apiTool
	// answering is told every piece of text the model writes, calling how
	// many bytes of calls of tools it has written, and alive that data came:
	// each may be nil.
	answering func(text string)
	calling   func(bytes int)
	alive     func()
}

// apiTool is a tool as the Messages API takes it.
type apiTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// relay is the API as the CLI is given it, on the loopback interface: it
// sends the first request for a message to the API, with what it brings of
// the login of the CLI, and refuses every one after it. Native Claude Code
// may try once more after it got an answer, as when it runs a tool it does
// not have; ouro only takes the first. The answer is read as it passes.
type relay struct {
	relayOptions
	url    string
	prefix string
	srv    *http.Server
	ctx    context.Context

	mu      sync.Mutex
	used    bool
	denied  int
	done    chan struct{} // closed once the request that was let through is answered
	status  int
	refused []byte // what the API refused the request with
	err     error  // why the API was not reached, or the answer broke off
	capture capture
}

// listen opens the port of a relay; tests replace it.
var listen = net.Listen

// newRelay starts a relay with o. It ends with ctx, or with close.
func newRelay(ctx context.Context, o relayOptions) (*relay, error) {
	ln, err := listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("claude-code: no port for the relay: %w", err)
	}
	secret := make([]byte, 24)
	rand.Read(secret) // never fails
	r := &relay{relayOptions: o, prefix: "/" + hex.EncodeToString(secret), ctx: ctx, done: make(chan struct{})}
	r.url = "http://" + ln.Addr().String() + r.prefix
	r.srv = &http.Server{Handler: r, BaseContext: func(net.Listener) context.Context { return ctx }}
	go r.srv.Serve(ln)
	return r, nil
}

// close stops the relay and what it sends.
func (r *relay) close() { r.srv.Close() }

// ServeHTTP lets the first request for a message through and refuses the
// others. Nothing of a request is logged: it holds the login of the CLI.
func (r *relay) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost || req.URL.Path != r.prefix+"/v1/messages" {
		http.NotFound(w, req)
		return
	}
	r.mu.Lock()
	first := !r.used
	if first {
		r.used = true
	} else {
		r.denied++
	}
	r.mu.Unlock()
	if !first {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"ouro lets one request of a call through"}}`))
		return
	}
	defer close(r.done)
	err := r.forward(w, req)
	r.mu.Lock()
	r.err = err
	r.mu.Unlock()
}

// forward sends req to the API with the tools of ouro, and its answer back
// to the CLI while it reads it.
func (r *relay) forward(w http.ResponseWriter, req *http.Request) error {
	body, err := io.ReadAll(io.LimitReader(req.Body, maxBody))
	if err != nil {
		return fmt.Errorf("claude-code: the request of Claude Code cannot be read: %w", err)
	}
	if body, err = r.rewrite(body); err != nil {
		return err
	}
	out, _ := http.NewRequestWithContext(r.ctx, http.MethodPost, r.upstream+"/v1/messages", bytes.NewReader(body)) // a URL of ouro: cannot fail
	out.URL.RawQuery = req.URL.RawQuery
	for name, values := range req.Header {
		switch strings.ToLower(name) {
		case "host", "connection", "content-length", "transfer-encoding", "accept-encoding", "proxy-authorization", "proxy-connection":
			continue
		}
		out.Header[name] = values
	}
	out.Header.Set("Accept-Encoding", "identity")
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(out)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		return err // the error of a network, which a fallback takes over
	}
	defer resp.Body.Close()
	r.tell()
	r.mu.Lock()
	r.status = resp.StatusCode
	r.mu.Unlock()
	for name, values := range resp.Header {
		switch strings.ToLower(name) {
		case "connection", "transfer-encoding", "content-length":
			continue
		}
		w.Header()[name] = values
	}
	w.WriteHeader(resp.StatusCode)
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32<<10)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			r.tell()
			chunk := buf[:n]
			r.mu.Lock()
			if resp.StatusCode == http.StatusOK {
				r.capture.feed(chunk, r.answering, r.calling)
			} else if len(r.refused) < maxError {
				r.refused = append(r.refused, chunk...)
			}
			r.mu.Unlock()
			// The answer counts as it is read here: a CLI that no longer
			// takes it does not cut it off.
			if _, werr := w.Write(chunk); werr == nil && flusher != nil {
				flusher.Flush()
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("claude-code: the answer of the API broke off: %w", err)
		}
	}
}

func (r *relay) tell() {
	if r.alive != nil {
		r.alive()
	}
}

// rewrite is body with the tools of ouro in place of those of the CLI, which
// has none, and its messages in the form that keeps the cache, see
// stabilize. The rest stays as the CLI built it.
func (r *relay) rewrite(body []byte) ([]byte, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, fmt.Errorf("claude-code: the request of Claude Code is no JSON: %w", err)
	}
	if len(r.tools) == 0 {
		delete(fields, "tools")
	} else {
		fields["tools"], _ = json.Marshal(r.tools) // JSON that was read: cannot fail
	}
	stabilize(fields, r.frames)
	return json.Marshal(fields)
}

// result is what came of the request that was let through: false if there
// was none. It waits for the answer to be read to its end.
func (r *relay) result(ctx context.Context) (sent bool, err error) {
	r.mu.Lock()
	used := r.used
	r.mu.Unlock()
	if !used {
		return false, nil
	}
	select {
	case <-r.done:
	case <-ctx.Done():
	}
	if ctx.Err() != nil {
		return true, context.Cause(ctx)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	switch {
	case r.err != nil:
		return true, r.err
	case r.status != http.StatusOK:
		return true, refusal(r.status, r.refused)
	}
	return true, r.capture.failure()
}

// refusal is the error of an API that refused a request with status and
// body.
func refusal(status int, body []byte) error {
	var e struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	json.Unmarshal(body, &e) // a body that cannot be read says nothing
	api := &apiError{Status: status, Type: e.Error.Type, Message: orDefault(e.Error.Message, strings.TrimSpace(string(body)))}
	return api.known()
}
