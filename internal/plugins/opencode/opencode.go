// Package opencode is the plugin that lets the owner use OpenCode Go with
// its API key alone: the provider "opencode-go". Where it is reached and how
// it is spoken to is known, so that nobody has to look it up and type it
// into the setup of a custom provider.
package opencode

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"ouroboros/internal/llm"
	"ouroboros/internal/openai"
	"ouroboros/plugin"
)

// Name names the provider in the accesses it signed in, and the plugin.
const Name = "opencode-go"

// Title is what the owner picks the provider by, and what its accesses are
// called.
const Title = "OpenCode Go"

// BaseURL is where OpenCode Go is reached: GET /models lists its models,
// and the key goes along as a bearer token.
const BaseURL = "https://opencode.ai/zen/go/v1"

// elsewhere are the starts of the IDs of the models OpenCode Go answers on
// another route than POST /chat/completions, in a form of another API:
// /responses for those of OpenAI, xAI and Muse, /messages for those of
// Anthropic, MiniMax and Qwen. The list of models does not say the route, so
// it is told by the family, as https://opencode.ai/docs/go/ lists them. This
// ouro speaks chat completions to OpenCode Go, and offers what answers there.
var elsewhere = []string{"gpt-", "grok-", "muse-", "claude-", "minimax-", "qwen"}

// Options are where the provider reaches OpenCode Go: BaseURL unless a test
// says otherwise.
type Options struct {
	BaseURL string
}

// Plugin contributes the provider for OpenCode Go.
func Plugin(o Options) plugin.Plugin {
	return plugin.New(Name, func(ctx *plugin.Context, _ struct{}) error {
		plugin.Add(ctx, Provider(o))
		return nil
	})
}

// Provider is the provider the plugin contributes. An access is the API key
// of the OpenCode console, kept as a secret with the endpoint it was
// accepted at.
func Provider(o Options) plugin.Provider {
	endpoint := cmp.Or(strings.TrimRight(o.BaseURL, "/"), BaseURL)
	return plugin.Provider{
		Name:  Name,
		Title: Title,
		Login: func(ctx context.Context, d plugin.Dialog, have *plugin.Credentials) (plugin.Credentials, error) {
			return login(ctx, endpoint, d, have)
		},
		Models: func(ctx context.Context, a plugin.Access) (plugin.Models, error) {
			baseURL, key, err := saved(a)
			if err != nil {
				return plugin.Models{}, err
			}
			return models(ctx, baseURL, key)
		},
		Usage: func(ctx context.Context, a plugin.Access) (llm.Usage, error) {
			baseURL, key, err := saved(a)
			if err != nil {
				return llm.Usage{}, errors.New("cannot read OpenCode Go usage credentials")
			}
			return usage(ctx, baseURL, key)
		},
		Model: func(a plugin.Access) plugin.Model {
			baseURL, key, err := saved(a)
			if err != nil {
				return unreachable{err}
			}
			return openai.New(baseURL, key)
		},
		Catalog: func(plugin.Access) string { return Name }, // as models.dev names OpenCode Go
	}
}

// unreachable is the model of an access that cannot be read.
type unreachable struct{ err error }

func (u unreachable) Complete(context.Context, string, []plugin.ChatMessage, []plugin.ChatTool) (plugin.ChatMessage, error) {
	return plugin.ChatMessage{}, u.err
}

// saved reads the endpoint and the key of the access a.
func saved(a plugin.Access) (baseURL, key string, err error) {
	c, err := a.Credentials()
	if err != nil {
		return "", "", err
	}
	return openai.Saved(c)
}

// login asks for the API key until OpenCode Go lists its models for it. The
// key of the access that is signed in to again is offered.
func login(ctx context.Context, endpoint string, d plugin.Dialog, have *plugin.Credentials) (plugin.Credentials, error) {
	known := ""
	if have != nil {
		_, known, _ = openai.Saved(*have) // an access that cannot be read offers nothing
	}
	for {
		q := plugin.Question{Title: "OpenCode Go API key", Secret: true, Hint: "the key of the OpenCode console",
			Text: []string{"The API key of your OpenCode Go subscription,", "from the OpenCode console: opencode.ai"}}
		if known != "" {
			q.Default, q.Known, q.Hint = known, true, "Enter keeps the saved key"
		}
		key, err := d.Ask(ctx, q)
		if err != nil {
			return plugin.Credentials{}, err
		}
		if key = strings.TrimSpace(key); key == "" {
			d.Reject("the API key is needed")
			continue
		}
		_, err = models(ctx, endpoint, key)
		if errors.Is(err, plugin.ErrKeyRejected) {
			d.Reject("API key rejected, try again")
			known = ""
			continue
		}
		c := openai.Credentials(endpoint, key)
		c.Title, c.About = Title, strings.TrimPrefix(endpoint, "https://")
		return c, err
	}
}

// models lists the models of OpenCode Go at endpoint that this ouro can
// answer with: those of the route for chat completions. It fails with
// plugin.ErrKeyRejected if the key is refused.
func models(ctx context.Context, endpoint, key string) (plugin.Models, error) {
	all, err := openai.New(endpoint, key).Models(ctx)
	var apiErr *openai.APIError
	if errors.As(err, &apiErr) && apiErr.Unauthorized() {
		return plugin.Models{}, plugin.ErrKeyRejected
	}
	if err != nil {
		return plugin.Models{}, fmt.Errorf("cannot list the models of OpenCode Go: %w", err)
	}
	chat := slices.DeleteFunc(all, func(id string) bool {
		return slices.ContainsFunc(elsewhere, func(family string) bool { return strings.HasPrefix(id, family) })
	})
	if len(chat) == 0 {
		return plugin.Models{}, errors.New("OpenCode Go lists no model this ouro can answer with: it speaks chat completions")
	}
	return plugin.Models{Chat: chat, All: chat, Default: chat[0]}, nil
}
