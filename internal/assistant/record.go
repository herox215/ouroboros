package assistant

import (
	"bytes"
	"cmp"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"ouroboros/internal/agent"
	"ouroboros/internal/config"
)

// maxLogged is how many bytes of a text the log takes: of the message of the
// owner, of what the LLM says along with calls of tools, of the answer, of the
// arguments of a tool and of an error.
const maxLogged = 300

// redacted is what the log has in place of a secret.
const redacted = "[redacted]"

// sizeOnly are the arguments of tools that hold the content of a file: the log
// takes their size, not their text.
var sizeOnly = map[string][]string{"write": {"content"}, "edit": {"old", "new"}}

// oneLine keeps a text that goes into the log on one line.
var oneLine = strings.NewReplacer("\n", `\n`, "\r", `\r`)

// record writes what the agent does on the way to one answer into the log:
// the message, every call of the LLM with what it said, every tool call and
// how the answer ended. A call that is under way when the answer is stopped is written as
// canceled, at once, also when it never returns.
type record struct {
	log     func(format string, args ...any)
	ctx     context.Context
	chat    string
	secrets []string
	stop    func() bool // releases the context of the answer
	done    func()      // called once the last line is written

	mu      sync.Mutex
	start   time.Time
	calls   int       // of the LLM
	sent    int       // bytes to the LLM, see agent.Call
	tokens  int       // tokens sent to the LLM, as far as the API said
	cached  int       // of those, the tokens the provider had cached
	tooled  int       // tool calls that ended, those of scripts included
	calling time.Time // when the call of the LLM that is under way started; zero if none is
	tools   []flight  // under way, in the order they started
	closed  bool      // the last line is written
}

// flight is a tool call that is under way.
type flight struct {
	tool  string
	args  json.RawMessage
	start time.Time
}

// newRecord starts the record of the answer to text in the conversation chat,
// which ends with ctx. done is called once its last line is written.
func newRecord(ctx context.Context, log func(format string, args ...any), chat string, secrets []string, text string, done func()) *record {
	r := &record{log: log, ctx: ctx, chat: chat, secrets: longestFirst(secrets), start: time.Now(), done: done}
	r.line("message from the owner: %s", r.text(text))
	r.stop = context.AfterFunc(ctx, r.cancel)
	return r
}

// line writes one line of the conversation.
func (r *record) line(format string, args ...any) {
	r.log("chat %s: "+format, append([]any{r.chat}, args...)...)
}

// end writes how the answer ended: with reply, or with err. An answer whose
// context ended is canceled, whatever it returned: the owner does not get it.
func (r *record) end(reply string, err error) {
	r.stop()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped() {
		return
	}
	if err != nil {
		r.close("failed after %s (%s): %s", took(time.Since(r.start)), r.counts(), r.text(err.Error()))
		return
	}
	r.close("answered after %s (%s): %s", took(time.Since(r.start)), r.counts(), r.text(reply))
}

// cancel writes that the answer was canceled, see canceled.
func (r *record) cancel() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.canceled()
}

// stopped reports whether the context of the answer has ended, and then
// writes that the answer was canceled. What the agent reports after that is
// not written: it is what the cancellation did. Holds r.mu.
func (r *record) stopped() bool {
	if r.ctx.Err() == nil {
		return false
	}
	r.canceled()
	return true
}

// canceled writes the calls that are under way as canceled, the innermost
// first, and then that the answer was, unless the last line is written.
// Holds r.mu.
func (r *record) canceled() {
	if r.closed {
		return
	}
	if !r.calling.IsZero() {
		r.line("llm call %d took %s: canceled", r.calls, took(time.Since(r.calling)))
	}
	for _, f := range slices.Backward(r.tools) {
		r.line("tool %s %s took %s: canceled", r.plain(f.tool), r.args(f.tool, f.args), took(time.Since(f.start)))
	}
	r.close("canceled after %s (%s)", took(time.Since(r.start)), r.counts())
}

// counts is what the answer cost: calls of the LLM, calls of tools and the
// bytes sent to the LLM. Holds r.mu.
func (r *record) counts() string {
	counts := fmt.Sprintf("llm calls: %d, tool calls: %d, sent %.1f KiB", r.calls, r.tooled, float64(r.sent)/1024)
	if r.tokens > 0 {
		counts += ", " + usage(r.tokens, r.cached)
	}
	return counts
}

// usage is how many tokens were sent and how many of them were cached.
func usage(tokens, cached int) string {
	return fmt.Sprintf("%s tokens, %s cached (%d%%)", kilo(tokens), kilo(cached), cached*100/tokens)
}

// kilo is n in thousands, like 12.3k, below 1000 as it is.
func kilo(n int) string {
	if n < 1000 {
		return strconv.Itoa(n)
	}
	return fmt.Sprintf("%.1fk", float64(n)/1000)
}

// close writes the last line. Holds r.mu.
func (r *record) close(format string, args ...any) {
	r.line(format, args...)
	r.closed = true
	r.done()
}

// call notes a call of the LLM, and writes it when it has ended: how long it
// took, how it ended and, along with calls of tools, what the LLM said.
func (r *record) call(c agent.Call) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped() {
		return
	}
	if !c.Done {
		r.calls++
		r.sent += c.Sent
		r.calling = time.Now()
		return
	}
	r.calling = time.Time{}
	outcome := "text"
	switch {
	case c.Err != nil:
		outcome = "failed " + r.text(c.Err.Error())
	case c.ToolCalls > 0:
		// What the LLM says along with calls of tools is in no other line.
		// The text of a call that ends in text is the answer, and that has a
		// line of its own.
		outcome = "tool calls: " + strconv.Itoa(c.ToolCalls)
		if c.Content != "" {
			outcome += ", said " + r.text(c.Content)
		}
	}
	var cost []string
	if c.Tokens > 0 {
		r.tokens += c.Tokens
		r.cached += c.Cached
		cost = append(cost, usage(c.Tokens, c.Cached))
	}
	if c.Thought > 0 {
		cost = append(cost, fmt.Sprintf("thought %.1f KiB", float64(c.Thought)/1024))
	}
	if len(cost) > 0 {
		outcome += " [" + strings.Join(cost, ", ") + "]"
	}
	r.line("llm call %d took %s: %s", r.calls, took(c.Took), outcome)
}

// steered writes a message of the owner that the answer takes on its way.
func (r *record) steered(text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped() {
		return
	}
	r.line("message from the owner on the way: %s", r.text(text))
}

// trace writes a line of what a call of the LLM sent and got, for the debug
// mode: without secrets, but not shortened. Nothing is written once the last
// line is.
func (r *record) trace(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.line("debug %s", oneLine.Replace(r.redact(fmt.Sprintf(format, args...))))
}

// step notes a tool call, and writes it when it has ended. Of its result only
// the size is written.
func (r *record) step(s agent.Step) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped() {
		return
	}
	if !s.Done {
		r.tools = append(r.tools, flight{s.Tool, s.Args, time.Now()})
		return
	}
	// The call that ends is the last of its kind that started: a tool a
	// script calls ends before the script does.
	for i := len(r.tools) - 1; i >= 0; i-- {
		if r.tools[i].tool == s.Tool && bytes.Equal(r.tools[i].args, s.Args) {
			r.tools = slices.Delete(r.tools, i, i+1)
			break
		}
	}
	r.tooled++
	outcome := fmt.Sprintf("ok (%d bytes)", len(s.Result))
	if s.Err != nil {
		outcome = "error " + r.text(s.Err.Error())
	}
	r.line("tool %s %s took %s: %s", r.plain(s.Tool), r.args(s.Tool, s.Args), took(s.Took), outcome)
}

// text is s as the log takes a text: without secrets, shortened and quoted.
func (r *record) text(s string) string {
	s, rest := shorten(r.redact(s))
	return strconv.Quote(s) + more(rest)
}

// plain is s without secrets, shortened and on one line, for what is no
// sentence: the name and the arguments of a tool.
func (r *record) plain(s string) string {
	s, rest := shorten(r.redact(s))
	return oneLine.Replace(s) + more(rest)
}

// args are the arguments of a call of tool as the log takes them: the content
// of a file only by its size. Arguments of such a tool that are no JSON
// object are written by their size alone.
func (r *record) args(tool string, raw json.RawMessage) string {
	names := sizeOnly[tool]
	if len(names) == 0 {
		return r.plain(string(raw))
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return fmt.Sprintf("(%d bytes)", len(raw))
	}
	for _, name := range names {
		if v, ok := fields[name]; ok {
			var s string
			if json.Unmarshal(v, &s) != nil {
				s = string(v)
			}
			fields[name], _ = json.Marshal(fmt.Sprintf("(%d bytes)", len(s)))
		}
	}
	short, _ := json.Marshal(fields) // what was read as JSON is written as JSON
	return r.plain(string(short))
}

// redact replaces every secret in s.
func (r *record) redact(s string) string {
	for _, secret := range r.secrets {
		s = strings.ReplaceAll(s, secret, redacted)
	}
	return s
}

// shorten cuts s to at most maxLogged bytes, where a character begins, and
// returns how many bytes it left out.
func shorten(s string) (string, int) {
	if len(s) <= maxLogged {
		return s, 0
	}
	cut := maxLogged
	for !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut], len(s) - cut
}

// more says how much of a text was left out.
func more(rest int) string {
	if rest == 0 {
		return ""
	}
	return fmt.Sprintf("… (+%d bytes)", rest)
}

// took is a duration as the log writes it.
func took(d time.Duration) string { return d.Round(time.Millisecond).String() }

// secrets are the texts of cfg that never go into the log: the private key of
// the node, as it is stored and in the other usual encodings, of the whole key
// and of its seed, and the key of the API of a configuration from before
// there were accesses.
func secrets(cfg config.Config) []string {
	list := []string{cfg.APIKey, cfg.PrivateKey}
	if key, err := base64.StdEncoding.DecodeString(cfg.PrivateKey); err == nil {
		for _, b := range [][]byte{key, key[:min(len(key), ed25519.SeedSize)]} {
			list = append(list,
				base64.StdEncoding.EncodeToString(b), base64.RawStdEncoding.EncodeToString(b),
				base64.URLEncoding.EncodeToString(b), base64.RawURLEncoding.EncodeToString(b),
				hex.EncodeToString(b), strings.ToUpper(hex.EncodeToString(b)))
		}
	}
	return list
}

// longestFirst is list without what is empty or there twice, the longest
// first: so none is left in part because a shorter one was replaced inside
// it.
func longestFirst(list []string) []string {
	list = slices.DeleteFunc(slices.Clone(list), func(s string) bool { return s == "" })
	slices.SortFunc(list, func(a, b string) int { return cmp.Or(len(b)-len(a), strings.Compare(a, b)) })
	return slices.Compact(list)
}
