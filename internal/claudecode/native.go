package claudecode

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"os/user"
	"slices"
	"strings"
	"sync"
	"time"

	"ouroboros/internal/chat"
)

// overridden are the variables of the environment the CLI is not given: they
// would send it to another API, with another key or with another body than
// the plan of the owner and what ouro asks.
var overridden = []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "ANTHROPIC_FOUNDRY_API_KEY",
	"ANTHROPIC_MODEL", "ANTHROPIC_SMALL_FAST_MODEL", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY",
	"CLAUDE_CODE_EXTRA_BODY", "CLAUDE_CODE_EFFORT_LEVEL", "CLAUDE_CODE_MAX_OUTPUT_TOKENS", "CLAUDE_CONFIG_DIR",
	// What a CLI of Claude Code that runs ouro tells the ones it starts.
	"CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT", "CLAUDE_CODE_SSE_PORT"}

// fixed are the variables every CLI ouro starts runs with: it does not
// update itself, sends no feedback and asks the API for nothing besides the
// request ouro lets through, as a title of the session; it tries a request
// that failed not again, compacts nothing and searches no tools, which
// ouro does as it sees fit; and it does not remind the model of its tokens,
// which would change what the API cached of the conversation from call to
// call.
var fixed = []string{"DISABLE_AUTOUPDATER=1", "DISABLE_FEEDBACK_COMMAND=1", "CLAUDE_CODE_DISABLE_TERMINAL_TITLE=1",
	"CLAUDE_CODE_MAX_RETRIES=0", "DISABLE_AUTO_COMPACT=1", "DISABLE_COMPACT=1", "ENABLE_TOOL_SEARCH=false",
	"CLAUDE_CODE_TOTAL_TOKENS_REMINDER=off"}

// childEnv is the environment the CLI of acct runs with, from env.
func childEnv(env []string, acct account) []string {
	var out []string
	vars := environ(env)
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if !slices.Contains(overridden, name) {
			out = append(out, kv)
		}
	}
	if acct.ConfigDir != "" {
		out = append(out, "CLAUDE_CONFIG_DIR="+acct.ConfigDir)
	}
	// The CLI takes itself to be signed out without a name of the user, as
	// a node that runs as a service may have none.
	if vars["USER"] == "" {
		if u, err := user.Current(); err == nil {
			out = append(out, "USER="+u.Username, "LOGNAME="+u.Username)
		}
	}
	return append(out, fixed...)
}

// streamArgs are the arguments of a CLI that speaks stream-json on stdin and
// stdout, with none of its tools, settings, commands or servers: only what
// ouro hands it.
var streamArgs = []string{"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
	"--tools", "", "--setting-sources", "", "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`,
	"--disable-slash-commands", "--no-session-persistence"}

// event is a line the CLI writes on stdout, as far as ouro reads it.
type event struct {
	Type     string `json:"type"`
	Subtype  string `json:"subtype"`
	IsError  bool   `json:"is_error"`
	NumTurns int    `json:"num_turns"`
	Result   string `json:"result"`
	// Error is the code of an error the CLI answers itself, like
	// "authentication_failed", with its text in Message.
	Error   string `json:"error"`
	Message struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"message"`
	Errors   []string        `json:"errors"`
	Response json.RawMessage `json:"response"`
}

// text is the text of the message of e.
func (e event) text() string {
	var b strings.Builder
	for _, c := range e.Message.Content {
		b.WriteString(c.Text)
	}
	return b.String()
}

// native is a CLI that runs.
type native struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	events chan event
	alive  func()

	mu     sync.Mutex
	stderr []string // the first lines, to tell why it failed
}

// maxStderr is how many lines of stderr are kept.
const maxStderr = 20

// start starts the CLI of acct with args, its requests to the API sent to
// baseURL, if not "". It ends with ctx. alive, if not nil, is told of every
// line it writes.
func (o Options) start(ctx context.Context, acct account, baseURL string, args []string, alive func()) (*native, error) {
	cmd := exec.CommandContext(ctx, acct.Command, args...)
	cmd.Env = childEnv(o.env(), acct)
	if baseURL != "" {
		cmd.Env = append(cmd.Env, "ANTHROPIC_BASE_URL="+baseURL)
	}
	cmd.Dir = o.dir()
	cmd.WaitDelay = time.Second // for what it started, that holds its output
	group(cmd)
	stdin, _ := cmd.StdinPipe()   // before Start: cannot fail
	stdout, _ := cmd.StdoutPipe() // the same
	stderr, _ := cmd.StderrPipe() // the same
	n := &native{cmd: cmd, stdin: stdin, events: make(chan event, 64), alive: alive}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("claude-code: Claude Code does not start: %w", err)
	}
	go n.readStderr(stderr)
	go n.read(stdout)
	return n, nil
}

// read hands the events of stdout to n.events, which it closes at the end.
// A line that is no JSON, as a shim may write, is left out.
func (n *native) read(stdout io.Reader) {
	defer close(n.events)
	lines := bufio.NewScanner(stdout)
	lines.Buffer(make([]byte, 0, 64<<10), 64<<20)
	for lines.Scan() {
		if n.alive != nil {
			n.alive()
		}
		var e event
		if json.Unmarshal(lines.Bytes(), &e) == nil && e.Type != "" {
			n.events <- e
		}
	}
}

func (n *native) readStderr(stderr io.Reader) {
	lines := bufio.NewScanner(stderr)
	for lines.Scan() {
		n.mu.Lock()
		if line := strings.TrimSpace(lines.Text()); line != "" && len(n.stderr) < maxStderr {
			n.stderr = append(n.stderr, line)
		}
		n.mu.Unlock()
	}
}

// said is what the CLI wrote on stderr, shortened, "" for nothing.
func (n *native) said() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	text := strings.Join(n.stderr, " ")
	if len(text) > 400 {
		text = text[:400] + "…"
	}
	if text == "" {
		return ""
	}
	return " (Claude Code said: " + text + ")"
}

// send writes v, of texts and JSON that was read, as a line of
// stream-json. A CLI that does not take it has ended: what it wrote tells
// why.
func (n *native) send(v any) {
	n.stdin.Write(append(raw(v), '\n'))
}

// next is the next event, false when the CLI wrote its last. It fails with
// the cause of ctx once that is done.
func (n *native) next(ctx context.Context) (event, bool, error) {
	var e event
	var ok bool
	select {
	case e, ok = <-n.events:
	case <-ctx.Done():
	}
	if ctx.Err() != nil {
		return event{}, false, context.Cause(ctx)
	}
	return e, ok, nil
}

// end ends the CLI and all it started, and waits for it.
func (n *native) end() {
	n.stdin.Close()
	kill(n.cmd)
	n.cmd.Wait()
	for range n.events { // what is left, for the reader to end
	}
}

// control asks the CLI of acct one control request, like "initialize", and
// returns what it answers. What it is asked needs no request for a message:
// the CLI is not given the relay, which would make it offer the models a
// gateway has instead of those of the plan.
func (o Options) control(ctx context.Context, acct account, subtype string, fields map[string]any) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTime)
	defer cancel()
	n, err := o.start(ctx, acct, "", streamArgs, nil)
	if err != nil {
		return nil, err
	}
	defer n.end()
	request := map[string]any{"subtype": subtype}
	for k, v := range fields {
		request[k] = v
	}
	n.send(map[string]any{"type": "control_request", "request_id": "ouro-" + subtype, "request": request})
	for {
		e, ok, err := n.next(ctx)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("claude-code: Claude Code ended without answering %s%s", subtype, n.said())
		}
		if e.Type != "control_response" {
			continue
		}
		var resp struct {
			Subtype   string          `json:"subtype"`
			RequestID string          `json:"request_id"`
			Error     string          `json:"error"`
			Response  json.RawMessage `json:"response"`
		}
		json.Unmarshal(e.Response, &resp) // one that cannot be read answers nothing
		if resp.RequestID != "ouro-"+subtype {
			continue
		}
		if resp.Subtype != "success" {
			return nil, fmt.Errorf("claude-code: Claude Code refused %s: %s", subtype, orDefault(resp.Error, "it did not say why"))
		}
		return resp.Response, nil
	}
}

// failed is why the CLI answered without a request to the API, from what it
// wrote: the error it answered itself, or the result that failed.
func failed(events []event, said string) error {
	for _, e := range events {
		if e.Type == "assistant" && e.Error != "" {
			switch e.Error {
			case "authentication_failed":
				return ErrSignedOut
			case "rate_limit":
				return chat.Mark(fmt.Errorf("%w: %s", ErrLimit, e.text()), chat.Unavailable|chat.RateLimited)
			}
			return fmt.Errorf("claude-code: %s: %s", e.Error, orDefault(e.text(), "Claude Code did not say more"))
		}
	}
	for _, e := range events {
		if e.Type == "result" && e.IsError {
			why := orDefault(e.Result, strings.Join(e.Errors, "; "))
			return fmt.Errorf("claude-code: Claude Code failed (%s): %s%s", e.Subtype, orDefault(why, "it did not say why"), said)
		}
	}
	return errors.New("claude-code: Claude Code ended without a request to the API" + said)
}
