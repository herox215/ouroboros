// Package setup is the setup of the node: the LLM endpoint it uses, the key
// for it, the model and the name of the node. "ouro start" asks for it on the
// terminal, the terminal UI in a dialog of its own; both go through a
// Session, which keeps the key to itself.
package setup

import (
	"cmp"
	"context"
	"errors"
	"fmt"
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

// ErrKeyRejected means the endpoint refused the API key.
var ErrKeyRejected = errors.New("API key rejected")

// Offer is what the setup offers before anything was saved: the endpoint
// and the key that belongs to it, as OPENAI_BASE_URL and OPENAI_API_KEY say.
type Offer struct {
	BaseURL string
	APIKey  string
}

// Session is one run of the setup, from the configuration it started with to
// the one it saves. The endpoint and the key are what Connect last accepted;
// the key is never handed out.
type Session struct {
	home  home.Dir
	offer Offer
	cfg   config.Config

	endpoint, key string
	connected     bool
}

// New starts the setup of the node in dir from its configuration cfg.
func New(dir home.Dir, offer Offer, cfg config.Config) *Session {
	return &Session{home: dir, offer: offer, cfg: cfg}
}

// Open starts the setup of the node in dir from the configuration it has.
func Open(dir home.Dir, offer Offer) (*Session, error) {
	cfg, err := dir.Config()
	if err != nil {
		return nil, err
	}
	return New(dir, offer, cfg), nil
}

// Endpoint is the endpoint to offer: the one of the node, or the one offered.
func (s *Session) Endpoint() string { return cmp.Or(s.cfg.BaseURL, s.offer.BaseURL) }

// Name is the name to offer: the one of the node, or the name of the host.
func (s *Session) Name() string { return cmp.Or(s.cfg.Name, hostname()) }

// HasKey reports whether a key is known for endpoint, which Saved uses.
func (s *Session) HasKey(endpoint string) bool { return s.savedKey(clean(endpoint)) != "" }

// savedKey returns the key that belongs to endpoint, so that a key is never
// sent to an endpoint it was not entered for.
func (s *Session) savedKey(endpoint string) string {
	if endpoint == s.cfg.BaseURL && s.cfg.APIKey != "" {
		return s.cfg.APIKey
	}
	if endpoint == clean(s.offer.BaseURL) {
		return s.offer.APIKey
	}
	return ""
}

func clean(endpoint string) string { return strings.TrimRight(strings.TrimSpace(endpoint), "/") }

// Models are the models of an endpoint.
type Models struct {
	Chat    []string // those that can chat, to choose from
	All     []string
	Default string // of Chat: the model of the node, or a preferred one
}

// Resolve returns the model a user picked: a number from 1 on in Chat, or
// the name of any model.
func (m Models) Resolve(pick string) (string, bool) {
	if n, err := strconv.Atoi(pick); err == nil {
		if n >= 1 && n <= len(m.Chat) {
			return m.Chat[n-1], true
		}
		return "", false
	}
	return pick, slices.Contains(m.All, pick)
}

// Connect lists the models of endpoint with key, which may be empty. It fails
// with ErrKeyRejected if the endpoint refuses the key. What it accepts is what
// Save saves.
func (s *Session) Connect(ctx context.Context, endpoint, key string) (Models, error) {
	endpoint = clean(endpoint)
	all, err := openai.New(endpoint, key).Models(ctx)
	var apiErr *openai.APIError
	switch {
	case errors.As(err, &apiErr) && apiErr.Unauthorized():
		return Models{}, ErrKeyRejected
	case err != nil:
		return Models{}, fmt.Errorf("cannot list models at %s: %w", endpoint, err)
	case len(all) == 0:
		return Models{}, errors.New("no models available for this API key")
	}
	s.endpoint, s.key, s.connected = endpoint, key, true
	chat := chatModels(all)
	return Models{Chat: chat, All: all, Default: defaultModel(chat, s.cfg.Model)}, nil
}

// Saved is Connect with the key known for endpoint (see HasKey), or none.
func (s *Session) Saved(ctx context.Context, endpoint string) (Models, error) {
	return s.Connect(ctx, endpoint, s.savedKey(clean(endpoint)))
}

// Rights are what the agent may do once the setup is saved: the names of the
// tools for the owner and for peers (config.AllTools for every tool), and the
// paths no tool for files touches.
func (s *Session) Rights() (owner, peers, protected []string) {
	tools := withTools(s.cfg).Tools
	names := func(entries []config.Entry) []string {
		list := make([]string, len(entries))
		for i, e := range entries {
			list[i] = e.Tool
		}
		return list
	}
	return names(tools.H2A), names(tools.A2A), *tools.Protected
}

// Save saves the configuration with the endpoint and key Connect accepted,
// model and name. A new node gets its key, and with it its ID, and the tools
// that config.DefaultTools lists.
func (s *Session) Save(model, name string) error {
	if !s.connected {
		return errors.New("connect to the endpoint first")
	}
	if strings.TrimSpace(model) == "" || strings.TrimSpace(name) == "" {
		return errors.New("the node needs a model and a name")
	}
	cfg := withTools(s.cfg)
	cfg.BaseURL, cfg.APIKey, cfg.Model, cfg.Name = s.endpoint, s.key, model, strings.TrimSpace(name)
	// The key makes the node who it is: its ID is the fingerprint of the key.
	if cfg.PrivateKey == "" {
		cfg.PrivateKey = identity.Generate().Private()
	}
	if err := s.home.SaveConfig(cfg); err != nil {
		return err
	}
	s.cfg = cfg
	return nil
}

// withTools returns cfg with what the agent may do written out, for the owner
// to see and change: all of it for a new node, and what is protected for one
// from before there were tools for files.
func withTools(cfg config.Config) config.Config {
	if cfg.Tools == nil {
		cfg.Tools = config.DefaultTools()
		return cfg
	}
	if cfg.Tools.Protected == nil {
		tools := *cfg.Tools
		tools.Protected = config.DefaultTools().Protected
		cfg.Tools = &tools
	}
	return cfg
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

// osHostname names this host; tests replace it to get in the way.
var osHostname = os.Hostname

func hostname() string {
	if h, err := osHostname(); err == nil && h != "" {
		return h
	}
	return "ouroboros"
}
