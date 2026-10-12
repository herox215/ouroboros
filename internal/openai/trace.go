package openai

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// A trace is told what a call of the LLM sends and gets, beyond what the
// caller sees of it: for the debug mode of a node, to find out why a call
// costs what it does.
type trace func(format string, args ...any)

type traceKey struct{}

// WithTrace returns a context whose calls of the LLM tell log, line by line,
// what they send and get: the request in short, how far it begins like the
// last request of its session (what a provider can have cached of it), the
// headers of the response and the usage as the API sent it.
func WithTrace(ctx context.Context, log func(format string, args ...any)) context.Context {
	return context.WithValue(ctx, traceKey{}, trace(log))
}

// traceOf returns the trace of ctx, nil if it has none.
func traceOf(ctx context.Context) trace {
	t, _ := ctx.Value(traceKey{}).(trace)
	return t
}

// thoughtKey marks the sink a stream tells what its model thinks.
type thoughtKey struct{}

// WithThought returns a context whose calls of the LLM tell sink every piece
// of what the model thinks before it writes, as the stream brings it: for a
// front end that shows the thinking as it happens. A provider that keeps its
// thinking to itself, as the responses API of OpenAI does, tells nothing.
func WithThought(ctx context.Context, sink func(text string)) context.Context {
	return context.WithValue(ctx, thoughtKey{}, sink)
}

// thoughtOf returns the sink of ctx for what the model thinks, nil if it has
// none.
func thoughtOf(ctx context.Context) func(text string) {
	sink, _ := ctx.Value(thoughtKey{}).(func(text string))
	return sink
}

// answerKey marks the sink a stream tells what its model writes.
type answerKey struct{}

// WithAnswering returns a context whose calls of the LLM tell sink every piece
// of the answer it writes, as the stream brings it: for a front end that shows
// the answer while it is on its way. The whole of it comes with the message
// all the same, so a front end shows the pieces only until then.
func WithAnswering(ctx context.Context, sink func(text string)) context.Context {
	return context.WithValue(ctx, answerKey{}, sink)
}

// AnsweringOf returns the sink of ctx for what the model writes, nil if it has
// none.
func AnsweringOf(ctx context.Context) func(text string) {
	sink, _ := ctx.Value(answerKey{}).(func(text string))
	return sink
}

// maxTraced is how many bytes of a text a line of a trace takes.
const maxTraced = 200

// maxSessions is how many sessions the last requests are kept of, to compare
// the next with: beyond that they are forgotten, and the next request of each
// is taken as its first.
const maxSessions = 64

// sent are the last requests of the sessions that were traced, by session.
type sent struct {
	mu   sync.Mutex
	last map[string]request
}

// request is what a request sent, as far as a trace compares it: the tools
// by their hash, and every message as JSON.
type request struct {
	tools string
	msgs  [][]byte
}

// traceRequest tells t what a request of a completion in session sends, and
// how far it begins like the last request of the session.
func (c *Client) traceRequest(t trace, session, model string, msgs []Message, tools []Tool, options, cacheKey bool, breakpoints int) {
	items := make([][]byte, len(msgs))
	size := 0
	for i, m := range msgs {
		items[i], _ = json.Marshal(m) // texts and JSON: cannot fail
		size += len(items[i])
	}
	toolBytes, _ := json.Marshal(tools)
	t("llm request: %s/chat/completions, model %s, session %s, %d messages (%.1f KiB), %d tools (%.1f KiB, hash %s), stream_options %t, prompt_cache_key %t, cache_control breakpoints %d",
		c.baseURL, model, session, len(msgs), float64(size)/1024, len(tools), float64(len(toolBytes))/1024, hash(tools), options, cacheKey, breakpoints)
	t("llm prefix: %s", c.sent.Compare(session, tools, items))
}

// TraceOf returns what a call of the LLM under ctx tells its trace with, nil
// if it has none: see WithTrace. It is for a provider that is no
// OpenAI-compatible API, which tells the same lines of its own requests.
func TraceOf(ctx context.Context) func(format string, args ...any) {
	if t := traceOf(ctx); t != nil {
		return t
	}
	return nil
}

// Sent are the last requests of the sessions a client traced, to say how far
// the next one of a session begins like the one before. Its zero value is
// ready to use.
type Sent = sent

// Compare says how far the request of session with tools and items, each a
// message as JSON, begins like the last request of the session that was
// compared: what a provider can have cached of it. It keeps the request for
// the next one.
func (s *sent) Compare(session string, tools any, items [][]byte) string {
	now := request{tools: hash(tools), msgs: items}
	s.mu.Lock()
	last, ok := s.last[session]
	if s.last == nil || len(s.last) >= maxSessions && !ok {
		s.last = map[string]request{}
	}
	s.last[session] = now
	s.mu.Unlock()
	if !ok {
		return "first request of the session in the trace"
	}
	return compare(last, now)
}

// TraceResponse tells t the status and the headers of resp, which came
// after took.
func TraceResponse(t func(format string, args ...any), resp *http.Response, took time.Duration) {
	traceResponse(t, resp, took.Round(time.Millisecond).String())
}

// Hash is a short hash of v as JSON, as a trace names tools by.
func Hash(v any) string { return hash(v) }

// compare says how far the request now begins like the request last: where
// the first message differs, and how. A provider caches a request from its
// beginning up to the first byte that differs from one it had before.
func compare(last, now request) string {
	var notes []string
	if last.tools != now.tools {
		notes = append(notes, fmt.Sprintf("the tools changed (hash %s, before %s): many providers send them first, and nothing is cached", now.tools, last.tools))
	}
	same, shared := 0, 0
	for same < min(len(last.msgs), len(now.msgs)) && slices.Equal(last.msgs[same], now.msgs[same]) {
		shared += len(now.msgs[same])
		same++
	}
	total := 0
	for _, m := range now.msgs {
		total += len(m)
	}
	switch {
	case same == len(last.msgs):
		notes = append(notes, fmt.Sprintf("all %d messages of the last request are kept, %d are new; %s of the bytes are the same",
			len(last.msgs), len(now.msgs)-same, percent(shared, total)))
	case same == len(now.msgs):
		notes = append(notes, fmt.Sprintf("this request is the first %d of the %d messages of the last one; %s of the bytes are the same",
			same, len(last.msgs), percent(shared, total)))
	default:
		was, is := last.msgs[same], now.msgs[same]
		at := 0
		for at < min(len(was), len(is)) && was[at] == is[at] {
			at++
		}
		notes = append(notes, fmt.Sprintf("breaks at message %d of %d (%s), at byte %d of it: it was %s, it is %s; before it %s of the bytes are the same",
			same, len(now.msgs), role(is), at, around(was, at), around(is, at), percent(shared+at, total)))
	}
	return strings.Join(notes, "; ")
}

// role is the role of a message as JSON.
func role(msg []byte) string {
	var m struct {
		Role string `json:"role"`
	}
	json.Unmarshal(msg, &m)
	return "role " + strconv.Quote(m.Role)
}

// around is what msg has from byte at on, shortened and quoted, with its size.
func around(msg []byte, at int) string {
	rest := msg[at:]
	if len(rest) > maxTraced {
		return fmt.Sprintf("%d bytes long, from there %q… (+%d bytes)", len(msg), rest[:maxTraced], len(rest)-maxTraced)
	}
	return fmt.Sprintf("%d bytes long, from there %q", len(msg), rest)
}

// percent is part of all, in percent.
func percent(part, all int) string {
	if all == 0 {
		return "100%"
	}
	return fmt.Sprintf("%.1f%%", float64(part)*100/float64(all))
}

// hash is a short hash of v as JSON.
func hash(v any) string {
	b, _ := json.Marshal(v) // tools: cannot fail
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:6])
}

// unsafeHeaders are the headers of a response a trace leaves out.
var unsafeHeaders = []string{"Set-Cookie"}

// traceResponse tells t the status and the headers of resp.
func traceResponse(t trace, resp *http.Response, took string) {
	var headers []string
	for name, values := range resp.Header {
		if slices.Contains(unsafeHeaders, name) {
			continue
		}
		headers = append(headers, name+": "+strings.Join(values, ", "))
	}
	slices.Sort(headers)
	t("llm response: HTTP %d after %s, headers {%s}", resp.StatusCode, took, strings.Join(headers, "; "))
}

// answered is what the API said of a completion besides the message, for a
// trace: who answered, how the choice ended and the usage as it was sent.
type answered struct {
	ID                string          `json:"id"`
	Model             string          `json:"model"`
	Provider          string          `json:"provider"`
	SystemFingerprint string          `json:"system_fingerprint"`
	Usage             json.RawMessage `json:"usage"`
	Choices           []struct {
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
}

// read takes what data, a response or an event of a stream, says: every
// field that it has.
func (a *answered) read(data []byte) {
	var in answered
	if json.Unmarshal(data, &in) != nil {
		return
	}
	a.ID = cmp.Or(in.ID, a.ID)
	a.Model = cmp.Or(in.Model, a.Model)
	a.Provider = cmp.Or(in.Provider, a.Provider)
	a.SystemFingerprint = cmp.Or(in.SystemFingerprint, a.SystemFingerprint)
	if len(in.Usage) > 0 && string(in.Usage) != "null" {
		a.Usage = in.Usage
	}
	for _, c := range in.Choices {
		if c.FinishReason != "" {
			a.Choices = in.Choices
		}
	}
}

// tell tells t what the API said, with how many events a stream had.
func (a *answered) tell(t trace, events int) {
	finish := ""
	if len(a.Choices) > 0 {
		finish = a.Choices[0].FinishReason
	}
	usage := string(a.Usage)
	if usage == "" {
		usage = "none"
	}
	t("llm answered: id %q, model %q, provider %q, system_fingerprint %q, finish_reason %q, events %d, usage %s",
		a.ID, a.Model, a.Provider, a.SystemFingerprint, finish, events, usage)
}
