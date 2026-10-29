// Package llm is what a provider of an LLM is to ouro: a plugin that knows
// how to sign in to an API, which models it has and how the agents talk to
// it. The node may have several accesses, each signed in by one provider and
// kept in the credentials of the node; the configuration names the one that
// is in use.
//
// A provider gets to see its own credentials and nothing else: an Access is
// bound to one access of the node, and what is handed to a provider is never
// the store, the configuration or the access of another provider.
package llm

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"

	"ouroboros/internal/agent"
	"ouroboros/internal/credentials"
)

// Provider is an API the agents can talk to, contributed by a plugin.
type Provider struct {
	// Name names the provider in the accesses it signed in, like "openai".
	// It never changes, and it is the name of the plugin that contributes it.
	Name string
	// Title is what the owner picks the provider by, like "ChatGPT".
	Title string
	// Login signs in and returns what the provider keeps of the access. It
	// asks the owner through d, in as many steps as it needs. have is the
	// access that is signed in to again, nil for a new one. It returns once
	// the API accepted what was entered, or with the error of ctx when the
	// owner gave up.
	Login func(ctx context.Context, d Dialog, have *Credentials) (Credentials, error)
	// Models lists the models the access a reaches.
	Models func(ctx context.Context, a Access) (Models, error)
	// Usage tells what the access a may still spend, as the API of the
	// provider tells it: the quota windows of a plan, the prepaid balance,
	// or both. May be nil for a provider that does not report usage.
	Usage func(ctx context.Context, a Access) (Usage, error)
	// Model returns the model the agents answer with through the access a.
	// It reads the credentials when it needs them, and renews them through a
	// when they ran out: the agents are not built anew for that.
	Model func(a Access) agent.LLM
	// Catalog names the provider of the access a at models.dev, for what it
	// lists of its models: its ID there, like "openai", or where its API is
	// reached. May be nil, or return "", for a provider it does not list.
	Catalog func(a Access) string
	// Logout ends what the access a holds at the API, before it is removed.
	// May be nil. What it fails with is told, and the access removed anyway.
	Logout func(ctx context.Context, a Access) error
}

// Credentials are what a provider keeps of an access, see
// credentials.Credentials.
type Credentials = credentials.Credentials

// Access is one access of the node as its provider sees it: its own
// credentials, to read and to renew, and nothing else.
type Access interface {
	// Credentials reads what the provider keeps of the access, as it is now.
	Credentials() (Credentials, error)
	// Renew lets renew change the credentials and saves them in one step.
	// Other processes that renew at the same time wait: renew gets what the
	// one before it saved, so a token that was replaced is not used twice.
	Renew(renew func(c *Credentials) error) error
}

// Dialog is how a provider asks the owner while it signs in: on the terminal
// or in the terminal UI, which both show what it asks in their own way.
type Dialog interface {
	// Ask shows q and returns what the owner entered, the default of q for
	// nothing. It ends with the error of ctx when ctx is given up, as when
	// what was asked for arrived another way.
	Ask(ctx context.Context, q Question) (string, error)
	// Tell shows the owner a line: what happens next, or what to do.
	Tell(text string)
	// Reject says that the last answer was not accepted, and why.
	Reject(why string)
	// Open shows url in the browser of the owner, as far as there is one
	// here. The owner is shown the URL either way.
	Open(url string)
}

// Question is one thing a provider asks of the owner.
type Question struct {
	Title   string   // of the page, like "API key"
	Text    []string // what is asked, line by line
	Default string   // what an empty answer stands for
	Hint    string   // shown in an empty input, like "Enter for none"
	Secret  bool     // the answer is not shown
	// Known says that Default is an answer the provider already has, like a
	// key that is saved. A dialog with pages still shows the question, for
	// the owner to change the answer; one that asks line by line takes the
	// default without asking.
	Known bool
}

// Models are the models of an access.
type Models struct {
	Chat    []string // those that can chat, to choose from
	All     []string
	Default string // of Chat: the one to offer
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

// ErrKeyRejected means the API refused the key that was tried.
var ErrKeyRejected = errors.New("API key rejected")

// ErrNoProvider means the provider of an access is not part of this ouro: its
// plugin is turned off or left out of the build.
var ErrNoProvider = errors.New("its provider is not part of this ouro")

// Book is the accesses of a node together with the providers of this ouro:
// what builds the model of an access and lists its models.
type Book struct {
	Store     *credentials.Store
	Providers func() []Provider
}

// Provider returns the provider called name.
func (b Book) Provider(name string) (Provider, error) {
	for _, p := range b.Providers() {
		if p.Name == name {
			return p, nil
		}
	}
	return Provider{}, fmt.Errorf("provider %q: %w", name, ErrNoProvider)
}

// of returns the access id, its provider and the access as that provider
// sees it.
func (b Book) of(id string) (credentials.Access, Provider, Access, error) {
	saved, err := b.Store.Get(id)
	if err != nil {
		return saved, Provider{}, nil, fmt.Errorf("access %q: %w", id, err)
	}
	p, err := b.Provider(saved.Provider)
	return saved, p, stored{b.Store, id, saved.Provider}, err
}

// Model returns the model the agents answer with through the access id.
func (b Book) Model(id string) (agent.LLM, error) {
	_, p, a, err := b.of(id)
	if err != nil {
		return nil, err
	}
	return p.Model(a), nil
}

// Revision tells the access id apart from what it was before the owner
// signed in to it anew: "" for one that is no more.
func (b Book) Revision(id string) string {
	saved, _ := b.Store.Get(id)
	return saved.Rev
}

// Label is what the access id is called where it is shown, "" for one that
// is no more.
func (b Book) Label(id string) string {
	saved, err := b.Store.Get(id)
	if err != nil {
		return ""
	}
	return saved.Label()
}

// Models lists the models the access id reaches.
func (b Book) Models(ctx context.Context, id string) (Models, error) {
	_, p, a, err := b.of(id)
	if err != nil {
		return Models{}, err
	}
	return p.Models(ctx, a)
}

// Catalog names the provider of the access id at models.dev, see
// Provider.Catalog: "" for one it does not list, or an access that is none.
func (b Book) Catalog(id string) string {
	_, p, a, err := b.of(id)
	if err != nil || p.Catalog == nil {
		return ""
	}
	return p.Catalog(a)
}

// Logout ends what the access id holds at its API, as far as its provider
// can. An access whose provider is not there has nothing ended.
func (b Book) Logout(ctx context.Context, id string) error {
	_, p, a, err := b.of(id)
	if err != nil || p.Logout == nil {
		return err
	}
	return p.Logout(ctx, a)
}

// Secrets are the texts of all accesses that must not be shown, as in the
// log. Accesses that cannot be read have none to show.
func (b Book) Secrets() []string {
	list, _ := b.Store.List()
	var secrets []string
	for _, a := range list {
		secrets = append(secrets, a.Secrets()...)
	}
	return secrets
}

// stored is an access in the store as the provider sees it. It reaches the
// access id and no other, and only while that is one of the provider: an ID
// that was given to an access of another provider in the meantime opens
// nothing.
type stored struct {
	store    *credentials.Store
	id       string
	provider string
}

// errForeign is the error of a provider that reaches for what is not its own.
var errForeign = errors.New("the access belongs to another provider")

func (s stored) Credentials() (Credentials, error) {
	a, err := s.store.Get(s.id)
	if err != nil {
		return Credentials{}, err
	}
	if a.Provider != s.provider {
		return Credentials{}, errForeign
	}
	return a.Credentials, nil
}

func (s stored) Renew(renew func(c *Credentials) error) error {
	return s.store.Change(func(list []credentials.Access) ([]credentials.Access, error) {
		for i := range list {
			if list[i].ID != s.id {
				continue
			}
			if list[i].Provider != s.provider {
				return nil, errForeign
			}
			return list, renew(&list[i].Credentials)
		}
		return nil, credentials.ErrGone
	})
}

// Unsaved returns c as an access that is not saved yet, for a provider to
// list the models of what the owner just signed in with. What the provider
// renews is kept in c.
func Unsaved(c *Credentials) Access { return unsaved{c} }

type unsaved struct{ c *Credentials }

func (u unsaved) Credentials() (Credentials, error) { return *u.c, nil }

func (u unsaved) Renew(renew func(c *Credentials) error) error {
	next := *u.c
	if err := renew(&next); err != nil {
		return err
	}
	*u.c = next
	return nil
}
