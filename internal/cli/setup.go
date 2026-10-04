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
	"ouroboros/internal/home"
	"ouroboros/internal/identity"
	"ouroboros/internal/openai"
)

// nonChatModels are substrings of model IDs that cannot be used for chat.
var nonChatModels = []string{
	"audio", "babbage", "dall-e", "davinci", "embedding", "image", "moderation",
	"realtime", "search", "transcribe", "tts", "whisper",
}

// preferredModels are default choices, best first (matched as prefix).
var preferredModels = []string{"gpt-5-mini", "gpt-4.1-mini", "gpt-4o-mini"}

// wizard is the interactive setup of the node.
type wizard struct {
	term *console
	home home.Dir
	// Offered as defaults: the endpoint, and the key that belongs to it.
	baseURL string
	apiKey  string
}

// run asks for endpoint, API key, model and node name and saves the configuration.
func (w *wizard) run(ctx context.Context, cfg config.Config) (config.Config, error) {
	prevURL := cfg.BaseURL
	endpoint := cmp.Or(cfg.BaseURL, w.baseURL)
	answer, err := w.term.prompt(fmt.Sprintf("API endpoint (OpenAI-compatible) [%s]: ", endpoint), endpoint)
	if err != nil {
		return cfg, err
	}
	cfg.BaseURL = strings.TrimRight(answer, "/")

	key, models, err := w.askAPIKey(ctx, cfg.BaseURL, w.knownKey(cfg, prevURL))
	if err != nil {
		return cfg, err
	}
	cfg.APIKey = key
	if cfg.Model, err = w.askModel(models, cfg.Model); err != nil {
		return cfg, err
	}
	name := cmp.Or(cfg.Name, hostname())
	if cfg.Name, err = w.term.prompt(fmt.Sprintf("Node name [%s]: ", name), name); err != nil {
		return cfg, err
	}
	// The key makes the node who it is: its ID is the fingerprint of the key.
	if cfg.PrivateKey == "" {
		cfg.PrivateKey = identity.Generate().Private()
	}
	return cfg, w.home.SaveConfig(cfg)
}

// knownKey returns a key that belongs to cfg.BaseURL, so that a key is never
// sent to an endpoint it was not entered for.
func (w *wizard) knownKey(cfg config.Config, prevURL string) string {
	if cfg.BaseURL == prevURL && cfg.APIKey != "" {
		return cfg.APIKey
	}
	if cfg.BaseURL == strings.TrimRight(w.baseURL, "/") {
		return w.apiKey
	}
	return ""
}

// askAPIKey asks until the endpoint accepts a key (possibly none) and returns
// it with the endpoint's models.
func (w *wizard) askAPIKey(ctx context.Context, baseURL, key string) (string, []string, error) {
	for {
		if key == "" {
			var err error
			if key, err = w.term.readSecret("API key (Enter for none): "); err != nil {
				return "", nil, err
			}
		}
		models, err := openai.New(baseURL, key).Models(ctx)
		var apiErr *openai.APIError
		switch {
		case errors.As(err, &apiErr) && apiErr.Unauthorized():
			fmt.Fprintln(w.term.out, "✗ API key rejected, try again")
			key = ""
		case err != nil:
			return "", nil, fmt.Errorf("cannot list models at %s: %w", baseURL, err)
		default:
			return key, models, nil
		}
	}
}

// askModel lets the user pick a model by number or name.
func (w *wizard) askModel(all []string, current string) (string, error) {
	models := chatModels(all)
	if len(models) == 0 {
		return "", errors.New("no models available for this API key")
	}
	fmt.Fprintf(w.term.out, "\nAvailable models:\n\n")
	printColumns(w.term.out, models)
	def := defaultModel(models, current)
	for {
		pick, err := w.term.prompt(fmt.Sprintf("\nChoose model [number or name, Enter = %s]: ", def), def)
		if err != nil {
			return "", err
		}
		if m, ok := resolveModel(models, all, pick); ok {
			return m, nil
		}
		fmt.Fprintln(w.term.out, "✗ unknown model")
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
