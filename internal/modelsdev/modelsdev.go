// Package modelsdev knows what models think with, as models.dev lists it: which
// efforts a model of a provider takes. The list is fetched from models.dev
// at most once a day and kept in a file of the node, of it only what this
// package asks; without it, and for a model it does not list, nothing is
// known.
package modelsdev

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"ouroboros/internal/safefile"
)

// URL is where the catalog of models.dev is.
const URL = "https://models.dev/api.json"

// Env names the environment variable that says where the catalog is fetched
// from instead of URL; "off" fetches nothing, as for tests.
const Env = "OURO_MODELS_DEV"

// The bounds of fetching the catalog.
const (
	maxAge   = 24 * time.Hour  // a catalog older than that is fetched anew
	waitTime = time.Hour       // after a fetch that failed, before the next one
	timeout  = 2 * time.Minute // of a fetch
)

// maxSize is how much models.dev may answer; tests make it smaller.
var maxSize = 64 << 20

// Catalog is the catalog of models.dev as a node keeps it.
type Catalog struct {
	file string
	url  string
	get  *http.Client
	now  func() time.Time
	log  func(format string, args ...any)

	mu       sync.Mutex
	kept     *kept     // read from file, nil until it is
	fetching bool      // a fetch is on its way
	failed   time.Time // when the last fetch failed
	done     chan struct{}
}

// kept is what the file of a Catalog holds.
type kept struct {
	At time.Time `json:"at"`
	// Providers are the models of every provider, by its ID at models.dev
	// and by where its API is reached, without "/" at the end.
	Providers map[string]map[string]model `json:"providers"`
}

// model is what the catalog keeps of a model.
type model struct {
	// Efforts are the efforts it takes, as its API names them, from the
	// least to the most; none for a model that thinks without one.
	Efforts []string `json:"efforts,omitempty"`
}

// Open returns the catalog kept in file, which log is told of fetches that
// fail.
func Open(file string, log func(format string, args ...any)) *Catalog {
	return &Catalog{file: file, url: cmp.Or(os.Getenv(Env), URL), get: http.DefaultClient, now: time.Now, log: log, done: closed()}
}

func closed() chan struct{} {
	c := make(chan struct{})
	close(c)
	return c
}

// Efforts returns the efforts model of the provider takes, as its API names
// them, from the least to the most, and whether the catalog knows the model:
// one it knows takes none of them when the list is empty. provider is the
// ID of the provider at models.dev, like "openai", or where its API is
// reached, like "https://opencode.ai/zen/go/v1".
//
// It answers from what is kept, and fetches the catalog anew in the
// background once it is older than a day.
func (c *Catalog) Efforts(provider, model string) ([]string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.load()
	c.refresh()
	if c.kept == nil {
		return nil, false
	}
	m, ok := c.kept.Providers[strings.TrimRight(provider, "/")][model]
	return slices.Clone(m.Efforts), ok
}

// Wait returns once the fetch on its way, if there is one, ended: for tests
// and for a process that ends.
func (c *Catalog) Wait() {
	c.mu.Lock()
	done := c.done
	c.mu.Unlock()
	<-done
}

// load reads the file, once. c.mu is held.
func (c *Catalog) load() {
	if c.kept != nil {
		return
	}
	b, err := os.ReadFile(c.file)
	if err != nil {
		return
	}
	var k kept
	if json.Unmarshal(b, &k) == nil && k.Providers != nil {
		c.kept = &k
	}
}

// refresh fetches the catalog in the background, unless it is new enough,
// a fetch is on its way, or the last one failed a little while ago. c.mu is
// held.
func (c *Catalog) refresh() {
	now := c.now()
	switch {
	case c.url == "off", c.fetching,
		c.kept != nil && now.Sub(c.kept.At) < maxAge,
		!c.failed.IsZero() && now.Sub(c.failed) < waitTime:
		return
	}
	c.fetching, c.done = true, make(chan struct{})
	go c.fetch(c.done)
}

// fetch fetches the catalog and keeps it, and closes done.
func (c *Catalog) fetch(done chan struct{}) {
	defer close(done)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	k, err := c.download(ctx)
	if err == nil {
		b, _ := json.Marshal(k) // plain data: cannot fail
		err = safefile.Write(c.file, b, 0o600)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fetching = false
	if err != nil {
		c.failed = c.now()
		if c.log != nil {
			c.log("the catalog of models.dev could not be fetched: %v", err)
		}
		return
	}
	c.kept, c.failed = k, time.Time{}
}

// download fetches the catalog and returns what is kept of it.
func (c *Catalog) download(ctx context.Context) (*kept, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.get.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxSize)+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxSize {
		return nil, fmt.Errorf("it is larger than %d MiB", maxSize>>20)
	}
	return parse(b, c.now())
}

// listed is what the catalog of models.dev says of a provider.
type listed struct {
	API    string `json:"api"`
	Models map[string]struct {
		Reasoning bool     `json:"reasoning"`
		Options   []option `json:"reasoning_options"`
	} `json:"models"`
}

// option is one of the options of reasoning of a model.
type option struct {
	Type   string   `json:"type"`
	Values []string `json:"values"`
}

// effortsOf returns the efforts among options of reasoning.
func effortsOf(options []option) []string {
	var efforts []string
	for _, o := range options {
		if o.Type == "effort" {
			efforts = append(efforts, o.Values...)
		}
	}
	return efforts
}

// parse returns what is kept of the catalog b, fetched at at: of every model
// whose options of reasoning it lists, the efforts among them. A model that
// thinks but whose options it does not list is not known.
func parse(b []byte, at time.Time) (*kept, error) {
	var all map[string]listed
	if err := json.Unmarshal(b, &all); err != nil {
		return nil, fmt.Errorf("it is no catalog: %w", err)
	}
	k := &kept{At: at, Providers: map[string]map[string]model{}}
	for id, p := range all {
		models := map[string]model{}
		for name, m := range p.Models {
			if !m.Reasoning || m.Options != nil { // else the catalog does not say
				models[name] = model{Efforts: effortsOf(m.Options)}
			}
		}
		k.Providers[id] = models
		if api := strings.TrimRight(p.API, "/"); api != "" {
			k.Providers[api] = models
		}
	}
	return k, nil
}
