// Package deepseek contributes DeepSeek with its fixed endpoint and an API key.
package deepseek

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"

	"ouroboros/internal/openai"
	openaiplugin "ouroboros/internal/plugins/openai"
	"ouroboros/plugin"
)

// Name identifies the provider and its plugin.
const Name = "deepseek"

// BaseURL is the OpenAI-compatible DeepSeek API endpoint.
const BaseURL = "https://api.deepseek.com"

// Options offers a key from DEEPSEEK_API_KEY. BaseURL is only overridden by tests.
type Options struct {
	APIKey  string
	BaseURL string
}

// Plugin contributes the DeepSeek provider.
func Plugin(o Options) plugin.Plugin {
	return plugin.New(Name, func(ctx *plugin.Context, _ struct{}) error {
		plugin.Add(ctx, Provider(o))
		return nil
	})
}

// Provider uses the shared OpenAI-compatible client, without asking for an endpoint.
func Provider(o Options) plugin.Provider {
	endpoint := cmp.Or(strings.TrimRight(strings.TrimSpace(o.BaseURL), "/"), BaseURL)
	p := openaiplugin.Provider(openaiplugin.Offer{BaseURL: endpoint})
	p.Name, p.Title = Name, "DeepSeek"
	list := p.Models
	p.Models = func(ctx context.Context, a plugin.Access) (plugin.Models, error) {
		models, err := list(ctx, a)
		if slices.Contains(models.Chat, "deepseek-flash") {
			models.Default = "deepseek-flash"
		}
		return models, err
	}
	p.Login = func(ctx context.Context, d plugin.Dialog, have *plugin.Credentials) (plugin.Credentials, error) {
		known := o.APIKey
		if have != nil {
			if url, key, err := openai.Saved(*have); err == nil && url == endpoint && key != "" {
				known = key
			}
		}
		for {
			q := plugin.Question{Title: "DeepSeek API key", Secret: true, Hint: "the key from platform.deepseek.com",
				Text: []string{"Create an API key at https://platform.deepseek.com/api_keys.", "The DeepSeek API needs API credit, separate from the chat app."}}
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
			c := openai.Credentials(endpoint, key)
			c.Title = "DeepSeek"
			_, err = p.Models(ctx, access{c})
			if errors.Is(err, plugin.ErrKeyRejected) {
				d.Reject("API key rejected, try again")
				known = ""
				continue
			}
			return c, err
		}
	}
	p.Catalog = func(plugin.Access) string { return Name }
	p.Usage = func(ctx context.Context, a plugin.Access) (plugin.Usage, error) {
		endpoint, key, err := saved(a)
		if err != nil {
			return plugin.Usage{}, errors.New("cannot read DeepSeek balance credentials")
		}
		return balance(ctx, endpoint, key)
	}
	return p
}

// saved reads the endpoint and the key of the access a.
func saved(a plugin.Access) (endpoint, key string, err error) {
	c, err := a.Credentials()
	if err != nil {
		return "", "", err
	}
	return openai.Saved(c)
}

// access lets the login validate credentials before they are saved.
type access struct{ c plugin.Credentials }

func (a access) Credentials() (plugin.Credentials, error) { return a.c, nil }
func (a access) Renew(func(*plugin.Credentials) error) error {
	return errors.New("an API key is not renewed")
}
