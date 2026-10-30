// Package commands is the plugin that lets the agent run the commands of the
// node by name, as the owner runs them with a slash in a front end.
//
// The commands are the ones this build has: change_model and usage. Which of
// them the tool offers is the owner's setting, {"tool": "command", "commands":
// ["usage"]}; without the setting it offers both. The tool is the owner's
// alone: a peer changes neither the model nor reads the owner's quota.
package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"ouroboros/plugin"
)

// name is the name of the plugin.
const name = "commands"

// Tool is the name of the tool the plugin contributes.
const Tool = "command"

// settings are the settings of the tool: the commands it may run.
type settings struct {
	Commands *[]string `json:"commands" about:"the commands the tool may run, by name" unset:"change_model and usage"`
}

// args are the arguments of a call of the tool, whatever command it runs.
type args struct {
	Command string `json:"command"`
	Model   string `json:"model,omitempty"`
	Access  string `json:"access,omitempty"`
	Scope   string `json:"scope,omitempty"`
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
	return plugin.New(name, func(ctx *plugin.Context, _ struct{}) error {
		providers := func() []plugin.Provider { return plugin.All[plugin.Provider](ctx) }
		plugin.Add(ctx, tool(owner, providers))
		return nil
	})
}

// tool is the tool with the commands of the node, restricted to the ones the
// settings name.
func tool(owner *plugin.Owner, providers func() []plugin.Provider) plugin.Tool {
	all := registry(owner, providers)
	t := plugin.Tool{
		Name:        Tool,
		Description: description(all),
		Run:         runFor(all, defaults()),
		OwnerOnly:   true,
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
func defaults() []string { return []string{"change_model", "usage"} }

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
func registry(owner *plugin.Owner, providers func() []plugin.Provider) []command {
	return []command{
		{
			name:  "change_model",
			about: "change the model the agent answers with from the next message on: model, and access and scope (owner, sessions, thread or peers) where they differ",
			run: func(_ context.Context, a args) (string, error) {
				return changeModel(owner, a)
			},
		},
		{
			name:  "usage",
			about: "the remaining quota or balance of the access the sessions and the thread are answered through now, or why the provider tells nothing",
			run: func(ctx context.Context, _ args) (string, error) {
				reports, err := owner.CurrentUsage(ctx, providers())
				if err != nil {
					return "", err
				}
				return plugin.UsageText(reports), nil
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

// changeModel changes the model of the scope the arguments name, the owner's
// by default, of the access they name or of the one the scope is answered
// through now.
func changeModel(owner *plugin.Owner, a args) (string, error) {
	model := strings.TrimSpace(a.Model)
	if model == "" {
		return "", errors.New("change_model needs a model")
	}
	scope := a.Scope
	if scope == "" {
		scope = "owner"
	}
	access := strings.TrimSpace(a.Access)
	if access == "" {
		llms, err := owner.CurrentLLMs()
		if err != nil {
			return "", err
		}
		i := slices.IndexFunc(llms, func(l plugin.LLM) bool { return l.Scope == scope })
		if i < 0 {
			return "", fmt.Errorf("no such scope %q: it is owner, sessions, thread or peers", scope)
		}
		access = llms[i].Access
		if access == "" {
			return "", fmt.Errorf("no access is set up for the scope %s yet", scope)
		}
	}
	var err error
	switch scope {
	case "owner":
		_, err = owner.SetModel(access, model)
	case "sessions":
		err = owner.SetSessionModel(access, model)
	case "thread":
		err = owner.SetThreadModel(access, model)
	case "peers":
		err = owner.SetPeerModel(access, model)
	default:
		return "", fmt.Errorf("no such scope %q: it is owner, sessions, thread or peers", scope)
	}
	if err != nil {
		return "", err
	}
	what := map[string]string{
		"owner":    "you, and every scope that follows you",
		"sessions": "the sessions",
		"thread":   "the thread",
		"peers":    "peers",
	}[scope]
	return fmt.Sprintf("The agent answers %s with the model %q of the access %s from the next message on.", what, model, access), nil
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
			"scope":   map[string]any{"type": "string", "enum": []string{"owner", "sessions", "thread", "peers"}, "description": "change_model: whose model changes, the owner's by default"},
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
