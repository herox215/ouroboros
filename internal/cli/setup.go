package cli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"

	"ouroboros/internal/config"
	"ouroboros/internal/openai"
)

// nonChatModels are substrings of model IDs that cannot be used for chat.
var nonChatModels = []string{
	"audio", "babbage", "dall-e", "davinci", "embedding", "image", "moderation",
	"realtime", "search", "transcribe", "tts", "whisper",
}

// preferredModels are default choices, best first (matched as prefix).
var preferredModels = []string{"gpt-5-mini", "gpt-4.1-mini", "gpt-4o-mini"}

// setup asks for API key, model and node name and saves the configuration.
func (a *App) setup(ctx context.Context, cfg config.Config) (config.Config, error) {
	key, models, err := a.askAPIKey(ctx, cmp.Or(cfg.APIKey, a.settings.APIKey))
	if err != nil {
		return cfg, err
	}
	cfg.APIKey = key
	if cfg.Model, err = a.askModel(models, cfg.Model); err != nil {
		return cfg, err
	}
	name := cmp.Or(cfg.Name, hostname())
	if cfg.Name, err = a.ask(fmt.Sprintf("Node name [%s]: ", name), name); err != nil {
		return cfg, err
	}
	if cfg.ID == "" {
		cfg.ID = config.NewID()
	}
	return cfg, config.Save(a.settings.Home, cfg)
}

// askAPIKey asks until OpenAI accepts a key and returns it with its models.
func (a *App) askAPIKey(ctx context.Context, key string) (string, []string, error) {
	for {
		if key == "" {
			var err error
			if key, err = a.readSecret("OpenAI API key: "); err != nil {
				return "", nil, err
			}
		}
		models, err := openai.New(a.settings.OpenAIURL, key).Models(ctx)
		var apiErr *openai.APIError
		if errors.As(err, &apiErr) && apiErr.Unauthorized() {
			fmt.Fprintln(a.out, "✗ API key rejected, try again")
			key = ""
			continue
		}
		return key, models, err
	}
}

func (a *App) readSecret(prompt string) (string, error) {
	if a.ReadSecret != nil {
		return a.ReadSecret(prompt)
	}
	return a.ask(prompt, "")
}

// askModel lets the user pick a model by number or name.
func (a *App) askModel(all []string, current string) (string, error) {
	models := chatModels(all)
	if len(models) == 0 {
		return "", errors.New("no models available for this API key")
	}
	fmt.Fprintf(a.out, "\nAvailable models:\n\n")
	printColumns(a.out, models)
	def := defaultModel(models, current)
	for {
		pick, err := a.ask(fmt.Sprintf("\nChoose model [number or name, Enter = %s]: ", def), def)
		if err != nil {
			return "", err
		}
		if m, ok := resolveModel(models, all, pick); ok {
			return m, nil
		}
		fmt.Fprintln(a.out, "✗ unknown model")
	}
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

func defaultModel(models []string, current string) string {
	if slices.Contains(models, current) {
		return current
	}
	for _, prefix := range preferredModels {
		if i := slices.IndexFunc(models, func(m string) bool { return strings.HasPrefix(m, prefix) }); i >= 0 {
			return models[i]
		}
	}
	return models[0]
}

// resolveModel accepts a 1-based index into listed or any model name from all.
func resolveModel(listed, all []string, pick string) (string, bool) {
	if n, err := strconv.Atoi(pick); err == nil {
		if n >= 1 && n <= len(listed) {
			return listed[n-1], true
		}
		return "", false
	}
	return pick, slices.Contains(all, pick)
}

func printColumns(w io.Writer, items []string) {
	width := len(slices.MaxFunc(items, func(a, b string) int { return cmp.Compare(len(a), len(b)) }))
	cols := max(1, 96/(width+7))
	for i, item := range items {
		fmt.Fprintf(w, "  %3d) %-*s", i+1, width, item)
		if (i+1)%cols == 0 || i == len(items)-1 {
			fmt.Fprintln(w)
		}
	}
}

func hostname() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "ouroboros"
}
