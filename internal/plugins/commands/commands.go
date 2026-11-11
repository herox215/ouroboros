// Package commands is the plugin that lets the agent run the commands of the
// node by name, as the owner runs them with a slash in a front end.
//
// The commands are the ones this build has: change_model, change_effort and
// usage. Which of them the tool offers is the owner's setting, {"tool":
// "command", "commands": ["usage"]}; without the setting it offers all of
// them. The tool is the owner's alone, and for the thread alone: a peer
// changes neither the model nor the effort nor reads the owner's quota, and
// the sessions do not change what they run with.
package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"ouroboros/internal/session"
	"ouroboros/plugin"
)

// name is the name of the plugin.
const name = "commands"

// Tool is the name of the tool the plugin contributes.
const Tool = "command"

// settings are the settings of the tool: the commands it may run.
type settings struct {
	Commands *[]string `json:"commands" about:"the commands the tool may run, by name" unset:"change_model, change_effort and usage"`
}

// args are the arguments of a call of the tool, whatever command it runs.
type args struct {
	Command string `json:"command"`
	Model   string `json:"model,omitempty"`
	Effort  string `json:"effort,omitempty"`
	Access  string `json:"access,omitempty"`
	Scope   string `json:"scope,omitempty"`
	Session string `json:"session,omitempty"`
}

// needs are the services the plugin uses.
type needs struct {
	// Sessions are the sessions of the node, which tell which conversations
	// there are and what one holds of its context.
	Sessions *session.Service
}

// usageSource is what the usage command reads of the sessions of the node:
// the conversations to name, and what one of them holds of its context. The
// sessions of the node provide it.
type usageSource interface {
	List(context.Context) ([]session.Info, error)
	ContextOf(context.Context, string) (session.ContextUsage, error)
}

// effortSetter is what change_effort reads of the sessions of the node: it
// sets the effort of one session. The sessions of the node provide it.
type effortSetter interface {
	SetEffort(ctx context.Context, id, effort string) error
}

// command is one command the tool can run.
type command struct {
	name, about string
	run         func(context.Context, args) (string, error)
}

// Plugin contributes the tool command, which runs the commands the owner
// allows. The owner cannot be a service of a plugin, so it is handed here as
// to a front end: this plugin decides on nothing but the model to answer with
// and the usage of an access.
func Plugin(owner *plugin.Owner) plugin.Plugin {
	return plugin.New(name, func(ctx *plugin.Context, n needs) error {
		providers := func() []plugin.Provider { return plugin.All[plugin.Provider](ctx) }
		var source usageSource
		if n.Sessions != nil {
			source = n.Sessions
		}
		plugin.Add(ctx, tool(owner, providers, source))
		return nil
	})
}

// tool is the tool with the commands of the node, restricted to the ones the
// settings name.
func tool(owner *plugin.Owner, providers func() []plugin.Provider, source usageSource) plugin.Tool {
	all := registry(owner, providers, source)
	t := plugin.Tool{
		Name:        Tool,
		Description: description(all),
		Run:         runFor(all, defaults()),
		OwnerOnly:   true,
		ThreadOnly:  true,
	}
	t.Options = plugin.OptionsOf[settings]()
	t.Configure = func(raw json.RawMessage) (plugin.Tool, error) {
		var s settings
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&s); err != nil {
			return plugin.Tool{}, fmt.Errorf("settings of the tool %s: %w", Tool, err)
		}
		names, err := chosenNames(s)
		if err != nil {
			return plugin.Tool{}, fmt.Errorf("settings of the tool %s: %w", Tool, err)
		}
		chosen := make([]command, 0, len(names))
		for _, n := range names {
			i := slices.IndexFunc(all, func(c command) bool { return c.name == n })
			chosen = append(chosen, all[i])
		}
		configured := t
		configured.Description = description(chosen)
		configured.Schema = schema(chosen)
		configured.Run = runFor(all, names)
		return configured, nil
	}
	return t
}

// defaults are the commands the tool offers while the settings name none.
func defaults() []string { return []string{"change_model", "change_effort", "usage"} }

// chosenNames are the commands the settings name, or the defaults.
func chosenNames(s settings) ([]string, error) {
	if s.Commands == nil {
		return defaults(), nil
	}
	for _, n := range *s.Commands {
		if !slices.Contains(defaults(), n) {
			return nil, fmt.Errorf("unknown command %q (there are: %s)", n, list(defaults()))
		}
	}
	return *s.Commands, nil
}

// registry are the commands of this build, in the order they are listed.
func registry(owner *plugin.Owner, providers func() []plugin.Provider, source usageSource) []command {
	return []command{
		{
			name:  "change_model",
			about: "change the model the agent answers with from the next message on: model, and access and scope (owner, sessions, thread or peers) where they differ",
			run: func(ctx context.Context, a args) (string, error) {
				return changeModel(ctx, owner, providers, a)
			},
		},
		{
			name:  "change_effort",
			about: "change how much the model thinks before it answers, from the next message on: effort (auto, none, low, medium, high or max, as far as the model takes them), and scope node (the default: the node, which every conversation without an effort of its own has) or session (this conversation alone)",
			run: func(ctx context.Context, a args) (string, error) {
				setter, _ := source.(effortSetter)
				return changeEffort(ctx, owner, providers, setter, plugin.ContextID(ctx), a)
			},
		},
		{
			name:  "usage",
			about: "the remaining quota or balance of the access the sessions and the thread are answered through now, or why the provider tells nothing, and how many tokens the context of a conversation holds: this one by default, or the session named",
			run: func(ctx context.Context, a args) (string, error) {
				reports, err := owner.CurrentUsage(ctx, providers())
				if err != nil {
					return "", err
				}
				text := plugin.UsageText(reports)
				held, err := contextText(ctx, source, plugin.ContextID(ctx), a.Session)
				if err != nil {
					return "", err
				}
				if held != "" {
					text += "\n\n" + held
				}
				return text, nil
			},
		},
	}
}

// runFor runs the command named in the arguments, if names allows it.
func runFor(all []command, names []string) plugin.RunFunc {
	return func(ctx context.Context, raw json.RawMessage) (string, error) {
		var a args
		if err := plugin.Decode(Tool, raw, &a); err != nil {
			return "", err
		}
		if !slices.Contains(names, a.Command) {
			return "", fmt.Errorf("no such command %q: this tool runs %s", a.Command, list(names))
		}
		i := slices.IndexFunc(all, func(c command) bool { return c.name == a.Command })
		if i < 0 {
			return "", fmt.Errorf("no such command %q: this tool runs %s", a.Command, list(names))
		}
		return all[i].run(ctx, a)
	}
}

// contextText tells what the conversation the command runs in, or the
// session that query names, holds of its context: the tokens the provider
// was sent with the last call, how many of them it had cached, and how long
// the context is. It is empty when there is no conversation to tell of, so
// that the usage of the accesses is reported alone.
func contextText(ctx context.Context, source usageSource, current, query string) (string, error) {
	if source == nil {
		return "", nil
	}
	name, id := "this conversation", current
	if query = strings.TrimSpace(query); query != "" {
		found, err := sessionNamed(ctx, source, query)
		if err != nil {
			return "", err
		}
		name, id = "session "+found, found
	}
	if id == "" {
		return "", nil
	}
	u, err := source.ContextOf(ctx, id)
	if err != nil {
		if query != "" {
			return "", err
		}
		return "", nil // a conversation the node does not hold: the accesses alone are reported
	}
	if u.Title != "" {
		name += " " + strconv.Quote(u.Title)
	}
	line := name
	if u.Model != "" {
		line += " · " + u.Model
	}
	switch {
	case u.Cache.Tokens > 0:
		line += fmt.Sprintf(" · %s tokens sent with the last call, %s cached (%d%%)",
			kilo(u.Cache.Tokens), kilo(u.Cache.Cached), u.Cache.Cached*100/u.Cache.Tokens)
	case u.Bytes > 0:
		line += " · no tokens reported of the last call"
	default:
		line += " · no call of the LLM yet"
	}
	if u.Bytes > 0 {
		line += fmt.Sprintf(" · context %.1f KiB", float64(u.Bytes)/1024)
	}
	if u.Running {
		line += " · an answer is on its way"
	}
	return "Context\n  " + line, nil
}

// sessionNamed returns the ID of the session that query names: its ID, the
// start of it, or its title, in that order of exactness, ignoring case. A
// query that names several of them fails, naming them, as does one that
// names none.
func sessionNamed(ctx context.Context, source usageSource, query string) (string, error) {
	list, err := source.List(ctx)
	if err != nil {
		return "", fmt.Errorf("cannot list the sessions: %w", err)
	}
	lower := strings.ToLower(query)
	var exact, prefix, title, part []session.Info
	for _, s := range list {
		switch {
		case s.ID == query:
			exact = append(exact, s)
		case strings.HasPrefix(s.ID, query):
			prefix = append(prefix, s)
		case strings.EqualFold(s.Title, query):
			title = append(title, s)
		case lower != "" && strings.Contains(strings.ToLower(s.Title), lower):
			part = append(part, s)
		}
	}
	for _, found := range [][]session.Info{exact, prefix, title, part} {
		if len(found) == 1 {
			return found[0].ID, nil
		}
		if len(found) > 1 {
			return "", fmt.Errorf("%q names %d sessions: %s", query, len(found), listed(found))
		}
	}
	return "", fmt.Errorf("no session matches %q: name it by its ID, the start of it, or its title", query)
}

// listed names the sessions of a message, at most five of them.
func listed(sessions []session.Info) string {
	names := make([]string, 0, min(len(sessions), 5))
	for _, s := range sessions[:min(len(sessions), 5)] {
		if s.Title == "" {
			names = append(names, s.ID)
		} else {
			names = append(names, fmt.Sprintf("%s %q", s.ID, s.Title))
		}
	}
	if len(sessions) > 5 {
		names = append(names, fmt.Sprintf("and %d more", len(sessions)-5))
	}
	return strings.Join(names, ", ")
}

// kilo is n in thousands, like 12.3k, and below 1000 as it is.
func kilo(n int) string {
	if n < 1000 {
		return strconv.Itoa(n)
	}
	return fmt.Sprintf("%.1fk", float64(n)/1000)
}

// changeModel changes the model of the scope the arguments name: the model of
// the owner's scopes by default, of the access they name or of the one the
// scope is answered through now. It does not ask the owner itself: the
// permission rule for change_model in config.DefaultAsk does, like for any
// other tool, and yolo mode skips it.
func changeModel(ctx context.Context, owner *plugin.Owner, providers func() []plugin.Provider, a args) (string, error) {
	model := strings.TrimSpace(a.Model)
	if model == "" {
		return "", errors.New("change_model needs a model")
	}
	scope := a.Scope
	if scope == "" {
		scope = "owner"
	}
	llms, err := owner.CurrentLLMs()
	if err != nil {
		return "", err
	}
	i := slices.IndexFunc(llms, func(l plugin.LLM) bool { return l.Scope == scope })
	if i < 0 {
		return "", fmt.Errorf("no such scope %q: it is owner, sessions, thread or peers", scope)
	}
	now := llms[i]
	access := strings.TrimSpace(a.Access)
	if access == "" {
		access = now.Access
		if access == "" {
			return "", fmt.Errorf("no access is set up for the scope %s yet", scope)
		}
	}
	label, err := checkModel(ctx, owner, providers(), access, model)
	if err != nil {
		return "", err
	}
	what := map[string]string{
		"owner":    "you, and every scope that follows you",
		"sessions": "the sessions",
		"thread":   "the thread",
		"peers":    "peers",
	}[scope]
	switch scope {
	case "owner":
		_, err = owner.SetModel(access, model)
	case "sessions":
		err = owner.SetSessionModel(access, model)
	case "thread":
		err = owner.SetThreadModel(access, model)
	default:
		err = owner.SetPeerModel(access, model)
	}
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("The agent answers %s with the model %q of the access %s from the next message on.", what, model, label), nil
}

// effortAuto is what the effort is called that leaves it to the model: the
// stored value is "", and the front ends show it as auto.
const effortAuto = "auto"

// changeEffort changes the effort of the node, or of the conversation the
// command runs in. It does not ask the owner: the effort is cheap to undo.
// Where models.dev lists the model, only the efforts it takes are accepted,
// as the slider of /effort offers them.
func changeEffort(ctx context.Context, owner *plugin.Owner, providers func() []plugin.Provider, sessions effortSetter, current string, a args) (string, error) {
	effort := strings.ToLower(strings.TrimSpace(a.Effort))
	if effort == "" {
		return "", fmt.Errorf("change_effort needs an effort: %s", list(effortNames(owner, providers)))
	}
	if effort == effortAuto {
		effort = ""
	}
	names := effortNames(owner, providers)
	if !slices.Contains(names, effortName(effort)) {
		if _, known := owner.Efforts(providers()); known {
			return "", fmt.Errorf("the model does not take the effort %q; it takes: %s", effortName(effort), list(names))
		}
		return "", fmt.Errorf("no such effort %q: it is %s", effortName(effort), list(names))
	}
	scope := strings.ToLower(strings.TrimSpace(a.Scope))
	if scope == "" {
		scope = "node"
	}
	switch scope {
	case "node":
		if err := owner.SetEffort(effort); err != nil {
			return "", err
		}
		return fmt.Sprintf("The effort of the node is %s from the next message on, in every conversation that has none of its own; a conversation with an effort of its own keeps it.", effortName(effort)), nil
	case "session":
		id := current
		if sessions == nil || id == "" {
			return "", errors.New("there is no conversation to set the effort of")
		}
		if err := sessions.SetEffort(ctx, id, effort); err != nil {
			return "", err
		}
		return fmt.Sprintf("The effort of this conversation is %s from the next message on.", effortName(effort)), nil
	}
	return "", fmt.Errorf("no such scope %q: it is node or session", scope)
}

// effortNames are the efforts change_effort accepts, "auto" first: those the
// model takes where models.dev lists it, else the ones the owner chooses from.
func effortNames(owner *plugin.Owner, providers func() []plugin.Provider) []string {
	choices := plugin.Efforts
	if takes, known := owner.Efforts(providers()); known {
		choices = append([]string{""}, takes...)
	}
	names := make([]string, len(choices))
	for i, e := range choices {
		names[i] = effortName(e)
	}
	return names
}

// effortName is what an effort is called: "" is auto.
func effortName(effort string) string {
	if effort == "" {
		return effortAuto
	}
	return effort
}

// checkModel fails if the access does not offer model, and names the ones it
// does. It returns how the access is labeled.
func checkModel(ctx context.Context, owner *plugin.Owner, providers []plugin.Provider, access, model string) (string, error) {
	listed, err := owner.Models(ctx, providers)
	if err != nil {
		return "", fmt.Errorf("cannot list the models of the access %s: %w", access, err)
	}
	i := slices.IndexFunc(listed, func(l plugin.AccessModels) bool { return l.Access.ID == access })
	if i < 0 {
		return "", fmt.Errorf("no access %s", access)
	}
	l := listed[i]
	if !slices.Contains(l.Models, model) {
		if len(l.Models) == 0 {
			if l.Err != "" {
				return "", fmt.Errorf("cannot list the models of the access %s: %s", access, l.Err)
			}
			return "", fmt.Errorf("the access %s lists no models", access)
		}
		return "", fmt.Errorf("the access %s does not offer the model %q; it offers: %s", access, model, strings.Join(l.Models, ", "))
	}
	return l.Access.Label, nil
}

// schema is the JSON schema of the arguments of the chosen commands.
func schema(chosen []command) string {
	names := make([]string, len(chosen))
	for i, c := range chosen {
		names[i] = c.name
	}
	b, _ := json.Marshal(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{"type": "string", "enum": names, "description": "the command to run"},
			"model":   map[string]any{"type": "string", "description": "change_model: the model to answer with from the next message on"},
			"access":  map[string]any{"type": "string", "description": "change_model: the ID of the access that offers the model; the one the scope is answered through now by default"},
			"scope":   map[string]any{"type": "string", "description": "change_model: whose model changes, one of owner (the default), sessions, thread or peers; change_effort: node (the default) or session"},
			"effort":  map[string]any{"type": "string", "description": "change_effort: how much the model thinks before it answers: auto (leave it to the model), none, low, medium, high or max; the ones the model takes"},
			"session": map[string]any{"type": "string", "description": "usage: the conversation to report the context of, by its ID, the start of it, or its title; the one the command runs in by default"},
		},
		"required":             []string{"command"},
		"additionalProperties": false,
	})
	return string(b)
}

// description tells the LLM what the tool does and which commands it runs.
func description(chosen []command) string {
	if len(chosen) == 0 {
		return "No command is configured: the owner allows none of them."
	}
	var b strings.Builder
	b.WriteString("Run one of the commands of this node. The commands: ")
	for i, c := range chosen {
		if i > 0 {
			b.WriteString("; ")
		}
		b.WriteString(c.name + " — " + c.about)
	}
	b.WriteString(".")
	return b.String()
}

// list joins names for a message, or says that there are none.
func list(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}
