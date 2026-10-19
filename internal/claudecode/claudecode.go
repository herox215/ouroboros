// Package claudecode is the provider "claude-code": the agents answer within
// the Claude plan of the owner (Pro, Max, Team), through the official Claude
// Code CLI the owner signed in to with "claude auth login".
//
// Nothing of the login is read here: no token, no file and no keychain entry
// of Claude Code. Every call starts the CLI, which signs its one request to
// the API itself. ouro hands it the conversation as stream-json and keeps all
// that an agent does to itself: the CLI runs with none of its own tools, and
// a relay on the loopback interface lets exactly one request of it through
// to the API, with the tools of the agent in place of its own, and reads the
// answer as it passes. Which tools are called is told back to the agent, who
// runs them as with every other provider.
//
// What a call costs counts against the usage of the plan that Anthropic sets
// for the Agent SDK and "claude -p", as with every app built on them.
package claudecode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"ouroboros/internal/agent"
	"ouroboros/internal/llm"
)

// Name names the provider in the accesses of a node, and the plugin.
const Name = "claude-code"

// Title is what the owner picks the provider by.
const Title = "Claude Code"

// DefaultUpstream is the API the relay sends the request of the CLI to.
const DefaultUpstream = "https://api.anthropic.com"

// InstallHint is how the owner installs the CLI.
const InstallHint = "npm install -g @anthropic-ai/claude-code"

var (
	// probeTime is how long asking the CLI for its login, its models or the
	// usage of the plan may take. Tests shorten it.
	probeTime = 40 * time.Second
	// idle is how long a call may go without a sign of the CLI or of the
	// API: a model that thinks long goes on as long as it sends something.
	// Tests shorten it.
	idle = 5 * time.Minute
)

// ErrMissing means the CLI is not installed where ouro looks for it.
var ErrMissing = errors.New("claude-code: Claude Code is not installed (no claude on PATH or where it is usually installed): " + InstallHint)

// ErrSignedOut means the CLI has no login that works: it was never signed
// in, or the login ran out or was revoked.
var ErrSignedOut = errors.New("claude-code: Claude Code is not signed in: run \"claude auth login\" as the user ouro runs as, then go on")

// ErrLimit means the usage of the Claude plan is used up for now.
var ErrLimit = errors.New("claude-code: the usage limit of your Claude plan is reached")

// Options are how the provider runs the CLI.
type Options struct {
	// Command is the CLI; "" finds claude on PATH and where it is usually
	// installed.
	Command string
	// Dir is the directory the CLI runs in, one of the node: the CLI tells
	// the model where it runs, and a directory that stays keeps that the same
	// from call to call, for the cache.
	Dir string
	// Upstream is where the relay sends the request of the CLI,
	// DefaultUpstream if empty; tests point it at a fake.
	Upstream string
	// Env is the environment the CLI runs with, that of ouro if nil.
	Env []string
}

func (o Options) upstream() string {
	return strings.TrimRight(orDefault(o.Upstream, DefaultUpstream), "/")
}

func (o Options) env() []string {
	if o.Env != nil {
		return o.Env
	}
	return os.Environ()
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// Provider returns the provider.
func Provider(o Options) llm.Provider {
	return llm.Provider{
		Name:   Name,
		Title:  Title,
		Login:  o.login,
		Models: o.models,
		Usage:  o.usage,
		Model:  func(a llm.Access) agent.LLM { return &client{o: o, access: a} },
		// The models of a Claude plan are those of Anthropic.
		Catalog: func(llm.Access) string { return "anthropic" },
	}
}

// account is what is kept of an access: no secret, as the CLI keeps the
// login.
type account struct {
	// Command is the CLI as it was found when the owner signed in.
	Command string `json:"command"`
	// ConfigDir is the directory of the CLI with the login, as
	// CLAUDE_CONFIG_DIR names it; "" for the one the CLI uses by default.
	ConfigDir string `json:"config_dir,omitempty"`
	Email     string `json:"email,omitempty"`
	Plan      string `json:"plan,omitempty"`
}

// read reads what is kept of an access.
func read(c llm.Credentials) (account, error) {
	var a account
	if err := json.Unmarshal(c.Data, &a); err != nil || a.Command == "" {
		return a, errors.New("claude-code: the access cannot be read: sign in again")
	}
	return a, nil
}

// keep is a as it is kept.
func keep(a account) llm.Credentials {
	data, _ := json.Marshal(a) // texts: cannot fail
	about := a.Email
	if a.Plan != "" {
		about = "Claude " + strings.ToUpper(a.Plan[:1]) + a.Plan[1:]
		if a.Email != "" {
			about += " · " + a.Email
		}
	}
	return llm.Credentials{Title: "Claude", About: about, Data: data}
}

// saved reads the account of the access a, with the CLI where it is now: one
// that moved since the owner signed in is found again.
func (o Options) saved(a llm.Access) (account, error) {
	c, err := a.Credentials()
	if err != nil {
		return account{}, err
	}
	acct, err := read(c)
	if err != nil {
		return acct, err
	}
	if !executable(acct.Command) {
		found, err := o.find()
		if err != nil {
			return acct, err
		}
		acct.Command = found
	}
	return acct, nil
}

// find finds the CLI: Command if it is set, else claude on PATH and then
// where it is usually installed, as a node that runs as a service has a
// PATH that has none of these.
func (o Options) find() (string, error) {
	env := environ(o.env())
	name := orDefault(o.Command, "claude")
	if filepath.IsAbs(name) {
		if executable(name) {
			return name, nil
		}
		return "", ErrMissing
	}
	dirs := filepath.SplitList(env["PATH"])
	if name == "claude" {
		if home := env["HOME"]; home != "" {
			for _, d := range []string{".local/bin", ".claude/local", ".npm-global/bin", ".bun/bin", ".volta/bin", "bin"} {
				dirs = append(dirs, filepath.Join(home, d))
			}
		}
		dirs = append(dirs, "/opt/homebrew/bin", "/usr/local/bin")
	}
	for _, d := range dirs {
		if path := filepath.Join(d, name); d != "" && executable(path) {
			return path, nil
		}
	}
	return "", ErrMissing
}

// executable reports whether path is a file that can be run.
func executable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
}

// environ is env as a map, the last of a name counting.
func environ(env []string) map[string]string {
	m := map[string]string{}
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[k] = v
		}
	}
	return m
}

// status is what "claude auth status" says of the login.
type status struct {
	LoggedIn         bool   `json:"loggedIn"`
	AuthMethod       string `json:"authMethod"`
	Email            string `json:"email"`
	SubscriptionType string `json:"subscriptionType"`
}

// status asks the CLI of acct whether it is signed in. It reads the login of
// the CLI and sends nothing.
func (o Options) status(ctx context.Context, acct account) (status, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTime)
	defer cancel()
	cmd := exec.CommandContext(ctx, acct.Command, "auth", "status")
	cmd.Env = childEnv(o.env(), acct)
	cmd.Dir = o.dir()
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run() // a CLI that is signed out ends with a failure, and says so all the same
	var st status
	if json.Unmarshal(out.Bytes(), &st) != nil {
		if ctx.Err() != nil {
			return st, ctx.Err()
		}
		return st, fmt.Errorf("claude-code: \"claude auth status\" did not tell the login (%v)", err)
	}
	return st, nil
}

// dir is the directory the CLI runs in, created if it is not there; the
// directory of ouro if it cannot be.
func (o Options) dir() string {
	if o.Dir == "" || os.MkdirAll(o.Dir, 0o700) != nil {
		return ""
	}
	return o.Dir
}

// login finds the CLI and waits until it is signed in. The owner signs in
// with the CLI itself: ouro sees nothing of the login.
func (o Options) login(ctx context.Context, d llm.Dialog, have *llm.Credentials) (llm.Credentials, error) {
	acct := account{ConfigDir: environ(o.env())["CLAUDE_CONFIG_DIR"]}
	if have != nil {
		if was, err := read(*have); err == nil {
			acct.ConfigDir = was.ConfigDir
		}
	}
	for tries := 0; ; tries++ {
		cmd, err := o.find()
		if err == nil {
			acct.Command = cmd
			var st status
			if st, err = o.status(ctx, acct); err != nil {
				return llm.Credentials{}, err
			}
			if st.LoggedIn {
				acct.Email, acct.Plan = st.Email, st.SubscriptionType
				d.Tell("Every answer counts against the usage your Claude plan has for the Agent SDK and \"claude -p\", as for every app built on them.")
				return keep(acct), nil
			}
		}
		if tries > 0 {
			d.Reject("still not there")
		}
		q := llm.Question{Title: "Claude Code", Hint: "Enter once it is done", Text: []string{
			"Claude Code is not signed in. Sign in in a terminal, as the user ouro runs as:", "  claude auth login"}}
		if errors.Is(err, ErrMissing) {
			q.Text = []string{"Claude Code is not installed. Install it in a terminal, then sign in:", "  " + InstallHint, "  claude auth login"}
		}
		if _, err := d.Ask(ctx, q); err != nil {
			return llm.Credentials{}, err
		}
	}
}

// models lists the models the plan of the access a has, as the CLI offers
// them, its default first. The CLI tells them without asking the API.
func (o Options) models(ctx context.Context, a llm.Access) (llm.Models, error) {
	acct, err := o.saved(a)
	if err != nil {
		return llm.Models{}, err
	}
	raw, err := o.control(ctx, acct, "initialize", nil)
	if err != nil {
		return llm.Models{}, err
	}
	var out struct {
		Models []struct {
			Value         string `json:"value"`
			ResolvedModel string `json:"resolvedModel"`
		} `json:"models"`
	}
	json.Unmarshal(raw, &out) // a list that cannot be read is none
	var m llm.Models
	for _, row := range out.Models {
		id := orDefault(row.ResolvedModel, row.Value)
		if row.Value == "default" {
			m.Default = id
		}
		if id != "" && id != "default" && !slices.Contains(m.Chat, id) {
			m.Chat = append(m.Chat, id)
		}
	}
	if len(m.Chat) == 0 {
		return llm.Models{}, errors.New("claude-code: Claude Code offers no model")
	}
	if !slices.Contains(m.Chat, m.Default) {
		m.Default = m.Chat[0]
	}
	m.All = m.Chat
	return m, nil
}

// usage reads the limits of the plan of the access a, as the CLI fetches
// them with its login.
func (o Options) usage(ctx context.Context, a llm.Access) ([]llm.UsageWindow, error) {
	acct, err := o.saved(a)
	if err != nil {
		return nil, err
	}
	raw, err := o.control(ctx, acct, "get_usage", map[string]any{"skip_behaviors": true})
	if err != nil {
		return nil, err
	}
	return parseUsage(raw)
}

// parseUsage reads the windows of what get_usage answers: those of "limits",
// or the older windows of five hours and seven days.
func parseUsage(raw json.RawMessage) ([]llm.UsageWindow, error) {
	type window struct {
		Utilization *float64  `json:"utilization"`
		ResetsAt    time.Time `json:"resets_at"`
	}
	var out struct {
		RateLimits *struct {
			Limits []struct {
				Kind     string    `json:"kind"`
				Percent  *float64  `json:"percent"`
				ResetsAt time.Time `json:"resets_at"`
				Scope    *struct {
					Model *struct {
						DisplayName string `json:"display_name"`
					} `json:"model"`
				} `json:"scope"`
			} `json:"limits"`
			FiveHour *window `json:"five_hour"`
			SevenDay *window `json:"seven_day"`
		} `json:"rate_limits"`
	}
	if json.Unmarshal(raw, &out) != nil || out.RateLimits == nil {
		return nil, errors.New("claude-code: Claude Code told no limits of the plan")
	}
	var windows []llm.UsageWindow
	for _, l := range out.RateLimits.Limits {
		if l.Percent == nil {
			continue
		}
		name := l.Kind
		switch {
		case l.Kind == "session":
			name = "5-hour"
		case l.Kind == "weekly_all":
			name = "weekly"
		case l.Scope != nil && l.Scope.Model != nil && l.Scope.Model.DisplayName != "":
			name = "weekly " + l.Scope.Model.DisplayName
		}
		windows = append(windows, llm.UsageWindow{Name: name, UsedPercent: *l.Percent, ResetsAt: l.ResetsAt})
	}
	if len(windows) > 0 {
		return windows, nil
	}
	for i, w := range []*window{out.RateLimits.FiveHour, out.RateLimits.SevenDay} {
		if w != nil && w.Utilization != nil {
			windows = append(windows, llm.UsageWindow{Name: []string{"5-hour", "weekly"}[i], UsedPercent: *w.Utilization, ResetsAt: w.ResetsAt})
		}
	}
	if len(windows) == 0 {
		return nil, errors.New("claude-code: Claude Code told no limits of the plan")
	}
	return windows, nil
}
