// Package llmtest provides a second provider and a scripted dialog for tests
// of what ouro does with providers.
package llmtest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"ouroboros/internal/agent"
	"ouroboros/internal/llm"
	"ouroboros/internal/openai"
)

// Dialog answers what a provider asks from a script, and records what it
// was shown. It is safe for a provider that asks beside its work.
type Dialog struct {
	mu sync.Mutex
	// Answers are the answers by the title of the question, in the order
	// they are given: "" stands for the default of the question. A question
	// without an answer left waits until it is given up.
	Answers map[string][]string
	// OnOpen is called with a URL the provider opens, if not nil.
	OnOpen func(url string)

	asked                  []llm.Question
	told, rejected, opened []string
}

// Ask answers q from the script.
func (d *Dialog) Ask(ctx context.Context, q llm.Question) (string, error) {
	d.mu.Lock()
	d.asked = append(d.asked, q)
	left := d.Answers[q.Title]
	if len(left) > 0 {
		d.Answers[q.Title] = left[1:]
	}
	d.mu.Unlock()
	if len(left) == 0 {
		<-ctx.Done()
		return "", ctx.Err()
	}
	if left[0] == "" {
		return q.Default, nil
	}
	return left[0], nil
}

// Tell records text.
func (d *Dialog) Tell(text string) { d.add(&d.told, text) }

// Reject records why.
func (d *Dialog) Reject(why string) { d.add(&d.rejected, why) }

// Open records url and hands it to OnOpen.
func (d *Dialog) Open(url string) {
	d.add(&d.opened, url)
	if d.OnOpen != nil {
		d.OnOpen(url)
	}
}

func (d *Dialog) add(to *[]string, text string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	*to = append(*to, text)
}

// Asked are the questions asked so far.
func (d *Dialog) Asked() []llm.Question {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]llm.Question(nil), d.asked...)
}

// Told is what the provider told so far.
func (d *Dialog) Told() []string { return d.list(&d.told) }

// Rejected is why the provider rejected answers so far.
func (d *Dialog) Rejected() []string { return d.list(&d.rejected) }

// Opened are the URLs the provider opened so far.
func (d *Dialog) Opened() []string { return d.list(&d.opened) }

func (d *Dialog) list(of *[]string) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), *of...)
}

// Token is the token the Provider signs in with; one that starts with it
// signs in too, as another account.
const Token = "good"

// Down is a token that signs in, of an account whose models cannot be listed.
const Down = Token + "-down"

// Provider returns a provider called name that is no OpenAI-compatible API.
// Its Login asks for a "Token" until it gets one that starts with Token. Its
// models are name-small and name-large, and its model answers
// "[name MODEL as TOKEN] TEXT" for the last message TEXT. An access that
// signed in with Down does not list its models.
func Provider(name string) llm.Provider {
	token := func(a llm.Access) (string, error) {
		c, err := a.Credentials()
		if err != nil {
			return "", err
		}
		var s struct {
			Token string `json:"token"`
		}
		if err := json.Unmarshal(c.Secret, &s); err != nil || s.Token == "" {
			return "", errors.New(name + ": the access has no token")
		}
		return s.Token, nil
	}
	return llm.Provider{
		Name:  name,
		Title: "Test " + name,
		Login: func(ctx context.Context, d llm.Dialog, have *llm.Credentials) (llm.Credentials, error) {
			for {
				q := llm.Question{Title: "Token", Text: []string{"the token of " + name}, Secret: true}
				if have != nil {
					q.Hint = "signing in again"
				}
				answer, err := d.Ask(ctx, q)
				if err != nil {
					return llm.Credentials{}, err
				}
				if strings.HasPrefix(answer, Token) {
					secret, _ := json.Marshal(map[string]string{"token": answer})
					return llm.Credentials{Title: "Test " + name, About: "account " + answer, Secret: secret}, nil
				}
				d.Reject("wrong token")
			}
		},
		Models: func(_ context.Context, a llm.Access) (llm.Models, error) {
			t, err := token(a)
			if err != nil {
				return llm.Models{}, err
			}
			if t == Down {
				return llm.Models{}, errors.New(name + " is down")
			}
			list := []string{name + "-small", name + "-large"}
			return llm.Models{Chat: list, All: list, Default: list[0]}, nil
		},
		Model: func(a llm.Access) agent.LLM { return model{name, func() (string, error) { return token(a) }} },
	}
}

// model is the model of the Provider.
type model struct {
	name  string
	token func() (string, error)
}

func (m model) Complete(_ context.Context, id string, msgs []openai.Message, _ []openai.Tool) (openai.Message, error) {
	token, err := m.token()
	if err != nil {
		return openai.Message{}, err
	}
	last := ""
	if len(msgs) > 0 {
		last = msgs[len(msgs)-1].Content
	}
	return openai.Message{Role: "assistant", Content: fmt.Sprintf("[%s %s as %s] %s", m.name, id, token, last)}, nil
}
