// Package setup is the setup of the node: the accesses to the APIs its agents
// talk to, the model it answers with and its name. "ouro start" asks for it
// on the terminal, the terminal UI in a dialog of its own; both go through a
// Session, which keeps what the providers sign in with to itself.
package setup

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"ouroboros/internal/config"
	"ouroboros/internal/credentials"
	"ouroboros/internal/home"
	"ouroboros/internal/identity"
	"ouroboros/internal/llm"
	"ouroboros/internal/safefile"
)

// ErrKeyRejected means the API refused the key.
var ErrKeyRejected = llm.ErrKeyRejected

// Models are the models of an access.
type Models = llm.Models

// Session is one run of the setup, from the configuration it started with to
// the one it saves. What a provider signed in with stays in the session until
// Save, and is never handed out.
type Session struct {
	home home.Dir
	book llm.Book
	cfg  config.Config

	// What Login, Again or Use last accepted: the access to save and
	// whether it was signed in to just now, so that Save has to keep it.
	chosen credentials.Access
	signed bool
	picked bool
}

// New starts the setup of the node in dir from its configuration cfg, with
// the providers of this ouro.
func New(dir home.Dir, providers func() []llm.Provider, cfg config.Config) *Session {
	return &Session{home: dir, book: llm.Book{Store: dir.Credentials(), Providers: providers}, cfg: cfg}
}

// Open starts the setup of the node in dir from the configuration it has.
func Open(dir home.Dir, providers func() []llm.Provider) (*Session, error) {
	cfg, err := dir.Config()
	if err != nil {
		return nil, err
	}
	return New(dir, providers, cfg), nil
}

// Name is the name to offer: the one of the node, or the name of the host.
func (s *Session) Name() string { return cmp.Or(s.cfg.Name, hostname()) }

// Provider is a provider to sign in with.
type Provider struct {
	Name  string // how Login takes it
	Title string // how it is shown
}

// Providers are the providers of this ouro, to sign in with.
func (s *Session) Providers() []Provider {
	var list []Provider
	for _, p := range s.book.Providers() {
		list = append(list, Provider{p.Name, p.Title})
	}
	return list
}

// Access is an access of the node as the owner sees it. What it signs in
// with is not part of it.
type Access struct {
	ID       string // stays when the access is renamed
	Provider string // the name of its provider
	Name     string // what the owner calls it, "" if nothing
	Label    string // what it is shown as: Name, or what its service is commonly called
	About    string // what tells it apart, like the endpoint or the account
	Active   bool   // the agents answer through it
	// Missing says that the provider of the access is not part of this
	// ouro: its plugin is turned off or left out.
	Missing bool
}

// Accesses are the accesses of the node, in the order they were added.
func (s *Session) Accesses() ([]Access, error) {
	saved, err := s.book.Store.List()
	if err != nil {
		return nil, err
	}
	cfg, err := s.home.Config()
	if err != nil {
		return nil, err
	}
	list := make([]Access, len(saved))
	for i, a := range saved {
		_, err := s.book.Provider(a.Provider)
		list[i] = Access{ID: a.ID, Provider: a.Provider, Name: a.Name, Label: a.Label(), About: a.About,
			Active: a.ID == cfg.Access, Missing: err != nil}
	}
	return list, nil
}

// Login signs in to a new access with the provider called provider, which
// asks the owner through d, and lists the models it reaches. What it
// accepted is what Save saves.
func (s *Session) Login(ctx context.Context, provider string, d llm.Dialog) (Models, error) {
	return s.login(ctx, credentials.Access{Provider: provider}, nil, d)
}

// Again signs in anew to the access id, as when its key changed or its login
// ran out. Save replaces what the access holds; until then it stays as it is.
func (s *Session) Again(ctx context.Context, id string, d llm.Dialog) (Models, error) {
	saved, err := s.book.Store.Get(id)
	if err != nil {
		return Models{}, err
	}
	return s.login(ctx, saved, &saved.Credentials, d)
}

func (s *Session) login(ctx context.Context, a credentials.Access, have *llm.Credentials, d llm.Dialog) (Models, error) {
	p, err := s.book.Provider(a.Provider)
	if err != nil {
		return Models{}, err
	}
	if a.Credentials, err = p.Login(ctx, d, have); err != nil {
		return Models{}, err
	}
	models, err := p.Models(ctx, llm.Unsaved(&a.Credentials))
	if err != nil {
		return Models{}, err
	}
	s.chosen, s.signed, s.picked = a, true, true
	return s.offer(models, a.ID), nil
}

// Use takes the access id as it is saved and lists the models it reaches.
func (s *Session) Use(ctx context.Context, id string) (Models, error) {
	saved, err := s.book.Store.Get(id)
	if err != nil {
		return Models{}, err
	}
	models, err := s.book.Models(ctx, id)
	if err != nil {
		return Models{}, err
	}
	s.chosen, s.signed, s.picked = saved, false, true
	return s.offer(models, id), nil
}

// offer is models with the model of the node as the default, if the access
// id is the one it answers through and still has it.
func (s *Session) offer(models Models, id string) Models {
	if id == s.cfg.Access && slices.Contains(models.Chat, s.cfg.Model) {
		models.Default = s.cfg.Model
	}
	if models.Default == "" && len(models.Chat) > 0 {
		models.Default = models.Chat[0]
	}
	return models
}

// Rename calls the access id name from now on. Its ID stays, and with it
// what refers to it.
func (s *Session) Rename(id, name string) error { return s.book.Store.Rename(id, name) }

// ErrLastAccess is the error of Remove for the only access of the node.
var ErrLastAccess = errors.New("the last access cannot be removed: add another one first")

// Remove drops the access id, after its provider ended what it holds at the
// API as far as it can. The last access stays. If the agents answer through
// the access, they answer through the first of the others from then on, with
// the model that one offers by default: an access that does not list its
// models cannot take over, and nothing is removed then. It returns what the
// owner should know of how it went, "" if nothing.
func (s *Session) Remove(ctx context.Context, id string) (string, error) {
	saved, err := s.book.Store.List()
	if err != nil {
		return "", err
	}
	at := slices.IndexFunc(saved, func(a credentials.Access) bool { return a.ID == id })
	switch {
	case at < 0:
		return "", credentials.ErrGone
	case len(saved) == 1:
		return "", ErrLastAccess
	}
	cfg, err := s.home.Config()
	if err != nil {
		return "", err
	}
	var next credentials.Access
	var model string
	if cfg.Access == id {
		next = slices.Delete(slices.Clone(saved), at, at+1)[0]
		models, err := s.book.Models(ctx, next.ID)
		if err != nil {
			return "", fmt.Errorf("%s is in use, and %s cannot take over: %w", saved[at].Label(), next.Label(), err)
		}
		if model = s.offer(models, next.ID).Default; model == "" {
			return "", fmt.Errorf("%s is in use, and %s cannot take over: it offers no model", saved[at].Label(), next.Label())
		}
	}
	note := ""
	if err := s.book.Logout(ctx, id); err != nil {
		note = fmt.Sprintf("removed here, but not signed out at the provider: %v", err)
	}
	err = config.Update(string(s.home), func(c *config.Config) error {
		if c.Access == id {
			if next.ID == "" {
				return errors.New("the access was put in use just now: try again")
			}
			c.Access, c.Model = next.ID, model
		}
		return s.book.Store.Remove(id)
	})
	if err != nil {
		return "", err
	}
	s.forget(id)
	if s.cfg.Access == id {
		s.cfg.Access, s.cfg.Model = next.ID, model
	}
	if next.ID != "" {
		note = strings.TrimSpace(fmt.Sprintf("the agents answer with %s of %s now. %s", model, next.Label(), note))
	}
	return note, nil
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

// Save saves the access that Login, Again or Use accepted, as the one the
// agents answer through, with model and name. A new node gets its key, and
// with it its ID, and the tools that config.DefaultTools lists.
func (s *Session) Save(model, name string) error {
	if !s.picked {
		return errors.New("sign in to a provider first")
	}
	if strings.TrimSpace(model) == "" || strings.TrimSpace(name) == "" {
		return errors.New("the node needs a model and a name")
	}
	if s.signed {
		saved, err := s.book.Store.Put(s.chosen)
		if err != nil {
			return err
		}
		s.chosen, s.signed = saved, false
	}
	cfg := withTools(s.cfg)
	cfg.Access, cfg.Model, cfg.Name = s.chosen.ID, model, strings.TrimSpace(name)
	cfg.BaseURL, cfg.APIKey = "", "" // of a configuration from before there were accesses
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

// Listed are the models of one access, as far as they could be listed.
type Listed struct {
	Access Access
	Models []string // to choose from
	// Err says why the access did not list its models, "" if it did. Models
	// are then the ones it listed the last time, if it ever did.
	Err string
}

// listTime is how long an access may take to list its models before the
// others are shown without it; tests shorten it.
var listTime = 15 * time.Second

// Models lists the models of every access of the node, each asked on its
// own and at the same time: one that does not answer does not keep the others
// back, and shows with the reason and the models it listed the last time.
// It fails with config.ErrIncomplete for a node that is not set up.
func (s *Session) Models(ctx context.Context) ([]Listed, error) {
	if !s.cfg.Complete() {
		return nil, config.ErrIncomplete
	}
	accesses, err := s.Accesses()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, listTime)
	defer cancel()
	listed := make([]Listed, len(accesses))
	var wg sync.WaitGroup
	for i, a := range accesses {
		listed[i].Access = a
		wg.Go(func() {
			done := make(chan struct{})
			var models Models
			var err error
			go func() {
				defer close(done)
				models, err = s.book.Models(ctx, a.ID)
			}()
			select {
			case <-done:
			case <-ctx.Done(): // a provider that does not watch ctx is not waited for
				listed[i].Err = "it did not answer in time"
				return
			}
			if err != nil {
				listed[i].Err = err.Error()
				return
			}
			listed[i].Models = models.Chat
		})
	}
	wg.Wait()
	s.remember(listed)
	return listed, nil
}

// remember keeps the lists that came in the file of the node, and fills
// those that did not come with what the file has of them.
func (s *Session) remember(listed []Listed) {
	known := map[string][]string{}
	if b, err := os.ReadFile(s.home.Models()); err == nil {
		json.Unmarshal(b, &known) // what cannot be read is not known
	}
	kept := map[string][]string{}
	for i, l := range listed {
		if l.Err != "" {
			listed[i].Models = known[l.Access.ID]
		}
		if len(listed[i].Models) > 0 {
			kept[l.Access.ID] = listed[i].Models
		}
	}
	b, _ := json.Marshal(kept)                // texts: cannot fail
	safefile.Write(s.home.Models(), b, 0o600) // a list that is not kept is asked for again
}

// forget drops what is kept of the models of the access id.
func (s *Session) forget(id string) {
	known := map[string][]string{}
	b, err := os.ReadFile(s.home.Models())
	if err != nil || json.Unmarshal(b, &known) != nil {
		return
	}
	delete(known, id)
	b, _ = json.Marshal(known) // read just now: cannot fail
	safefile.Write(s.home.Models(), b, 0o600)
}

// ErrNoModel is the error of Pick for a model that is none.
var ErrNoModel = errors.New("the model cannot be empty")

// Pick makes model of the access id what the agents answer with, and reports
// whether they answer through another access than before. They do from the
// next message they get: no restart is needed.
func (s *Session) Pick(id, model string) (switched bool, err error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return false, ErrNoModel
	}
	if _, err := s.book.Store.Get(id); err != nil {
		return false, err
	}
	err = config.Update(string(s.home), func(c *config.Config) error {
		if !c.Complete() {
			return config.ErrIncomplete
		}
		switched = c.Access != id
		c.Access, c.Model = id, model
		return nil
	})
	return switched && err == nil, err
}

// osHostname names this host; tests replace it to get in the way.
var osHostname = os.Hostname

func hostname() string {
	if h, err := osHostname(); err == nil && h != "" {
		return h
	}
	return "ouroboros"
}
