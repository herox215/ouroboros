// Package openai is the plugin that makes the agents talk to an
// OpenAI-compatible API: the provider "openai".
package openai

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"ouroboros/internal/openai"
	"ouroboros/plugin"
)

// Offer is what the provider offers before the owner entered anything: the
// endpoint and the key that belongs to it, as OPENAI_BASE_URL and
// OPENAI_API_KEY say.
type Offer struct {
	BaseURL string
	APIKey  string
}

// nonChatModels are substrings of model IDs that cannot be used for chat.
var nonChatModels = []string{
	"audio", "babbage", "dall-e", "davinci", "embedding", "image", "moderation",
	"realtime", "search", "transcribe", "tts", "whisper",
}

// preferredModels are default choices, best first (matched as prefix).
var preferredModels = []string{"gpt-5-mini", "gpt-4.1-mini", "gpt-4o-mini"}

// Plugin contributes the provider for OpenAI-compatible APIs: OpenAI, Ollama,
// LM Studio, vLLM, OpenRouter and the like. An access is an endpoint and the
// key for it, which may be none.
func Plugin(offer Offer) plugin.Plugin {
	return plugin.New(openai.ProviderName, func(ctx *plugin.Context, _ struct{}) error {
		plugin.Add(ctx, Provider(offer))
		return nil
	})
}

// Provider is the provider the plugin contributes.
func Provider(offer Offer) plugin.Provider {
	offer.BaseURL = clean(offer.BaseURL)
	return plugin.Provider{
		Name:  openai.ProviderName,
		Title: "OpenAI-compatible API",
		Login: offer.login,
		Models: func(ctx context.Context, a plugin.Access) (plugin.Models, error) {
			c, err := a.Credentials()
			if err != nil {
				return plugin.Models{}, err
			}
			baseURL, key, err := openai.Saved(c)
			if err != nil {
				return plugin.Models{}, err
			}
			return models(ctx, baseURL, key)
		},
		Model: func(a plugin.Access) plugin.Model {
			c, err := a.Credentials()
			if err != nil {
				return unreachable{err}
			}
			baseURL, key, err := openai.Saved(c)
			if err != nil {
				return unreachable{err}
			}
			return openai.New(baseURL, key)
		},
	}
}

// unreachable is the model of an access that cannot be read.
type unreachable struct{ err error }

func (u unreachable) Complete(context.Context, string, []plugin.ChatMessage, []plugin.ChatTool) (plugin.ChatMessage, error) {
	return plugin.ChatMessage{}, u.err
}

func clean(endpoint string) string { return strings.TrimRight(strings.TrimSpace(endpoint), "/") }

// login asks for the endpoint and the key until the endpoint lists its
// models. A key that is known is only tried at the endpoint it was entered
// for: the one of the access that is signed in to again, or the one offered.
func (o Offer) login(ctx context.Context, d plugin.Dialog, have *plugin.Credentials) (plugin.Credentials, error) {
	var savedURL, savedKey string
	if have != nil {
		savedURL, savedKey, _ = openai.Saved(*have) // an access that cannot be read offers nothing
	}
	endpoint := ""
	for endpoint == "" {
		answer, err := d.Ask(ctx, plugin.Question{Title: "LLM endpoint", Default: cmp.Or(savedURL, o.BaseURL),
			Text: []string{"Any OpenAI-compatible API:", "OpenAI, Ollama, LM Studio, vLLM, …"}})
		if err != nil {
			return plugin.Credentials{}, err
		}
		if endpoint = clean(answer); endpoint == "" {
			d.Reject("the endpoint is needed")
		}
	}
	known := ""
	switch {
	case endpoint == savedURL && savedKey != "":
		known = savedKey
	case endpoint == o.BaseURL:
		known = o.APIKey
	}
	for {
		q := plugin.Question{Title: "API key", Text: []string{"for " + endpoint}, Secret: true, Hint: "Enter for none"}
		if known != "" {
			q.Default, q.Known, q.Hint = known, true, "Enter keeps the saved key"
		}
		key, err := d.Ask(ctx, q)
		if err != nil {
			return plugin.Credentials{}, err
		}
		_, err = models(ctx, endpoint, key)
		if errors.Is(err, plugin.ErrKeyRejected) {
			d.Reject("API key rejected, try again")
			known = ""
			continue
		}
		return openai.Credentials(endpoint, key), err
	}
}

// models lists the models of endpoint with key, which may be empty. It fails
// with plugin.ErrKeyRejected if the endpoint refuses the key.
func models(ctx context.Context, endpoint, key string) (plugin.Models, error) {
	all, err := openai.New(endpoint, key).Models(ctx)
	var apiErr *openai.APIError
	switch {
	case errors.As(err, &apiErr) && apiErr.Unauthorized():
		return plugin.Models{}, plugin.ErrKeyRejected
	case err != nil:
		return plugin.Models{}, fmt.Errorf("cannot list models at %s: %w", endpoint, err)
	case len(all) == 0:
		return plugin.Models{}, errors.New("no models available for this API key")
	}
	chat := chatModels(all)
	return plugin.Models{Chat: chat, All: all, Default: defaultModel(chat)}, nil
}

// chatModels filters out models that cannot chat. If nothing is left, all are returned.
func chatModels(all []string) []string {
	chat := slices.DeleteFunc(slices.Clone(all), func(m string) bool {
		return slices.ContainsFunc(nonChatModels, func(s string) bool { return strings.Contains(m, s) })
	})
	if len(chat) == 0 {
		return all
	}
	return chat
}

// defaultModel is the model to offer of models: a preferred one, or the first.
func defaultModel(models []string) string {
	for _, prefix := range preferredModels {
		if i := slices.IndexFunc(models, func(m string) bool { return strings.HasPrefix(m, prefix) }); i >= 0 {
			return models[i]
		}
	}
	return models[0]
}
