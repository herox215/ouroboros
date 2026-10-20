package proposal

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ouroboros/internal/config"
)

// node is a node with a configuration and proposals for it.
type node struct {
	t     *testing.T
	dir   string
	store *Store
}

func newNode(t *testing.T) *node {
	t.Helper()
	n := &node{t: t, dir: t.TempDir()}
	n.store = Open(filepath.Join(n.dir, "proposals.json"))
	if err := config.Save(n.dir, config.Config{BaseURL: "http://x", APIKey: "sk-secret", Model: "gpt", Name: "alpha", PrivateKey: "key"}); err != nil {
		t.Fatal(err)
	}
	return n
}

// config is the configuration of the node as it is now.
func (n *node) config() config.Config {
	n.t.Helper()
	cfg, err := config.Load(n.dir)
	if err != nil {
		n.t.Fatal(err)
	}
	return cfg
}

// propose records a proposal that has to be one.
func (n *node) propose(path, value, reason string) Proposal {
	n.t.Helper()
	p, err := n.store.Add(n.config(), path, json.RawMessage(value), reason)
	if err != nil {
		n.t.Fatalf("propose %s = %s: %v", path, value, err)
	}
	return p
}

// waiting are the IDs of the proposals that wait.
func (n *node) waiting() []string {
	n.t.Helper()
	list, err := n.store.List()
	if err != nil {
		n.t.Fatal(err)
	}
	ids := []string{}
	for _, p := range list {
		ids = append(ids, p.ID)
	}
	return ids
}

// A proposal changes nothing until the owner accepts it.
func TestProposeAndAccept(t *testing.T) {
	n := newNode(t)
	before := time.Now()
	p := n.propose("tools.a2a", `["list_peers", {"tool": "read", "allow": ["~/share"]}]`, "so that peers see who is online")
	if len(p.ID) != 12 || p.Path != "tools.a2a" || string(p.Before) != `[]` || string(p.Value) != `["list_peers",{"allow":["~/share"],"tool":"read"}]` ||
		p.Reason != "so that peers see who is online" || p.At.Before(before) || p.At.After(time.Now()) {
		t.Fatalf("got %+v", p)
	}
	if cfg := n.config(); cfg.Tools != nil {
		t.Fatalf("the proposal changed the configuration: %+v", cfg.Tools)
	}
	list, err := n.store.List()
	if err != nil || len(list) != 1 || list[0].ID != p.ID || !list[0].At.Equal(p.At) || string(list[0].Value) != string(p.Value) {
		t.Fatalf("waiting: %+v, %v", list, err)
	}
	if info, err := os.Stat(filepath.Join(n.dir, "proposals.json")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the file: %v, %v", info, err)
	}

	got, err := n.store.Accept(n.dir, p.ID)
	if err != nil || got.ID != p.ID {
		t.Fatalf("accept: %+v, %v", got, err)
	}
	cfg := n.config()
	if value, _ := cfg.Field("tools.a2a"); string(value) != string(p.Value) {
		t.Fatalf("the configuration after accepting: %s", value)
	}
	// Nothing else was touched.
	if cfg.Name != "alpha" || cfg.APIKey != "sk-secret" || cfg.PrivateKey != "key" || cfg.Tools.H2A != nil || cfg.Tools.Protected != nil {
		t.Fatalf("the configuration after accepting: %+v", cfg)
	}
	if ids := n.waiting(); len(ids) != 0 {
		t.Fatalf("still waiting: %v", ids)
	}
	if _, err := n.store.Accept(n.dir, p.ID); !errors.Is(err, ErrGone) {
		t.Fatalf("accepting twice: %v", err)
	}
}

func TestRefuse(t *testing.T) {
	n := newNode(t)
	first := n.propose("name", `"beta"`, "")
	second := n.propose("model", `"gpt-5"`, "")
	got, err := n.store.Refuse(first.ID)
	if err != nil || got.ID != first.ID || got.Path != "name" {
		t.Fatalf("refuse: %+v, %v", got, err)
	}
	if ids := n.waiting(); len(ids) != 1 || ids[0] != second.ID {
		t.Fatalf("waiting: %v", ids)
	}
	if cfg := n.config(); cfg.Name != "alpha" || cfg.Model != "gpt" {
		t.Fatalf("a refused proposal changed the configuration: %+v", cfg)
	}
	if _, err := n.store.Refuse(first.ID); !errors.Is(err, ErrGone) {
		t.Fatalf("refusing twice: %v", err)
	}
	if _, err := n.store.Refuse("nope"); !errors.Is(err, ErrGone) {
		t.Fatalf("refusing what is not there: %v", err)
	}
	// The one after it is still the one it was.
	if got, err := n.store.Accept(n.dir, second.ID); err != nil || got.Path != "model" || n.config().Model != "gpt-5" {
		t.Fatalf("accept: %+v, %v", got, err)
	}
}

// A proposal that was made for another configuration than the one the node
// has now is not applied.
func TestStale(t *testing.T) {
	n := newNode(t)
	p := n.propose("name", `"beta"`, "")
	other := n.propose("model", `"gpt-5"`, "")
	if err := config.Update(n.dir, func(c *config.Config) error {
		c.Name = "gamma"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	got, err := n.store.Accept(n.dir, p.ID)
	if !errors.Is(err, ErrStale) || got.ID != p.ID {
		t.Fatalf("accept: %+v, %v", got, err)
	}
	if name := n.config().Name; name != "gamma" {
		t.Fatalf("a stale proposal was applied: %q", name)
	}
	// It is gone; what was proposed for a field that did not change is not.
	if ids := n.waiting(); len(ids) != 1 || ids[0] != other.ID {
		t.Fatalf("waiting: %v", ids)
	}
	if _, err := n.store.Accept(n.dir, other.ID); err != nil || n.config().Model != "gpt-5" {
		t.Fatalf("the other proposal: %v", err)
	}

	// Two proposals for one field: the second is stale once the first is in.
	a := n.propose("tools.protected", `["~/a"]`, "")
	b := n.propose("tools.protected", `["~/b"]`, "")
	if _, err := n.store.Accept(n.dir, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := n.store.Accept(n.dir, b.ID); !errors.Is(err, ErrStale) {
		t.Fatalf("the second of two: %v", err)
	}
	if got := n.config().Protected(); len(got) != 1 || got[0] != "~/a" {
		t.Fatalf("protected: %v", got)
	}
	// A field that is no field any more is stale as well.
	list := []Proposal{{ID: "x", Path: "gone", Value: json.RawMessage(`1`), Before: json.RawMessage(`0`)}}
	raw, _ := json.Marshal(list)
	os.WriteFile(filepath.Join(n.dir, "proposals.json"), raw, 0o600)
	if _, err := n.store.Accept(n.dir, "x"); !errors.Is(err, ErrStale) || len(n.waiting()) != 0 {
		t.Fatalf("an unknown field: %v", err)
	}
}

// What is no change of the configuration is no proposal.
func TestWhatIsNoProposal(t *testing.T) {
	n := newNode(t)
	for _, tt := range []struct{ path, value, want string }{
		{"name", `"alpha"`, "name is that already: there is nothing to change"},
		{"tools.a2a", `[]`, "tools.a2a is that already: there is nothing to change"},
		{"tools.h2a", `["*"]`, "tools.h2a is that already: there is nothing to change"},
		{"name", `3`, "name: json: cannot unmarshal number into Go value of type string"},
		{"tools.a2a", `"read"`, "tools.a2a: json: cannot unmarshal string into Go value of type []config.Entry"},
		{"api_key", `"sk-mine"`, "api_key is changed by the owner alone, with ouro start --setup: no proposal changes it"},
		{"nope", `1`, `the configuration has no field "nope" that can be proposed: there are name, description, model, tools.h2a, tools.a2a, tools.protected`},
	} {
		if p, err := n.store.Add(n.config(), tt.path, json.RawMessage(tt.value), ""); err == nil || err.Error() != tt.want || p.ID != "" {
			t.Errorf("%s = %s: %+v, %v", tt.path, tt.value, p, err)
		}
	}
	if ids := n.waiting(); len(ids) != 0 {
		t.Fatalf("waiting: %v", ids)
	}
	if _, err := os.Stat(filepath.Join(n.dir, "proposals.json")); err == nil {
		t.Fatal("a file was written for nothing")
	}
}

// The same change waits once, and no more than Max wait.
func TestLimits(t *testing.T) {
	n := newNode(t)
	first := n.propose("name", `"beta"`, "one reason")
	again := n.propose("name", ` "beta" `, "another reason")
	if again.ID != first.ID || again.Reason != "one reason" || !again.At.Equal(first.At) || len(n.waiting()) != 1 {
		t.Fatalf("the same change twice: %+v, %+v", first, again)
	}
	// Another value for the field, or the value for another field, is another proposal.
	if other := n.propose("model", `"beta"`, ""); other.ID == first.ID {
		t.Fatal("two fields, one proposal")
	}
	for i := len(n.waiting()); i < Max; i++ {
		n.propose("name", `"n`+strings.Repeat("x", i)+`"`, "")
	}
	if ids := n.waiting(); len(ids) != Max || ids[0] != first.ID {
		t.Fatalf("%d waiting", len(ids))
	}
	if p, err := n.store.Add(n.config(), "name", json.RawMessage(`"one more"`), ""); !errors.Is(err, ErrFull) || p.ID != "" {
		t.Fatalf("one more: %+v, %v", p, err)
	}
	if len(n.waiting()) != Max {
		t.Fatal("the list grew beyond its limit")
	}
	// What waits already is still answered.
	if again := n.propose("name", `"beta"`, ""); again.ID != first.ID {
		t.Fatal("the same change is not found in a full list")
	}
	if Max != 16 {
		t.Fatalf("the limit is %d: the README names it", Max)
	}

	// A reason is kept as far as it fits.
	if _, err := n.store.Refuse(first.ID); err != nil {
		t.Fatal(err)
	}
	if p := n.propose("name", `"long"`, strings.Repeat("r", MaxReason+1)); len(p.Reason) != MaxReason {
		t.Fatalf("a reason of %d bytes was kept", len(p.Reason))
	}
	n.store.Refuse(n.waiting()[0])
	if p := n.propose("name", `"fits"`, strings.Repeat("r", MaxReason)); len(p.Reason) != MaxReason {
		t.Fatalf("a reason of %d bytes was kept", len(p.Reason))
	}
}

// What cannot be read or written is reported, and decides nothing.
func TestErrors(t *testing.T) {
	n := newNode(t)
	p := n.propose("name", `"beta"`, "")

	// The configuration cannot be written: the proposal still waits.
	if err := os.WriteFile(filepath.Join(n.dir, "config.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := n.store.Accept(n.dir, p.ID); err == nil || errors.Is(err, ErrStale) {
		t.Fatalf("a configuration that cannot be read: %v", err)
	}
	if ids := n.waiting(); len(ids) != 1 {
		t.Fatalf("waiting: %v", ids)
	}

	// A list that cannot be read is not an empty one.
	os.WriteFile(filepath.Join(n.dir, "proposals.json"), []byte("nope"), 0o600)
	if _, err := n.store.List(); err == nil {
		t.Error("List of a broken file")
	}
	if _, err := n.store.Add(config.Config{Name: "alpha"}, "name", json.RawMessage(`"beta"`), ""); err == nil {
		t.Error("Add to a broken file")
	}
	if _, err := n.store.Refuse(p.ID); err == nil {
		t.Error("Refuse from a broken file")
	}
	if b, _ := os.ReadFile(filepath.Join(n.dir, "proposals.json")); string(b) != "nope" {
		t.Errorf("a broken file was written over: %q", b)
	}
	// An empty file is an empty list.
	os.WriteFile(filepath.Join(n.dir, "proposals.json"), nil, 0o600)
	if list, err := n.store.List(); err != nil || len(list) != 0 {
		t.Errorf("an empty file: %v, %v", list, err)
	}

	// A directory in the place of the file, and one in the place of the lock.
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, "proposals.json"), 0o700)
	if _, err := Open(filepath.Join(dir, "proposals.json")).List(); err == nil {
		t.Error("List of a directory")
	}
	os.Mkdir(filepath.Join(dir, "other.json.lock"), 0o700)
	if _, err := Open(filepath.Join(dir, "other.json")).List(); err == nil {
		t.Error("List without a lock")
	}
}

// What Apply cannot do is reported, and the configuration it would have
// changed stays as it was.
func TestApplyErrors(t *testing.T) {
	for _, tt := range []struct {
		name  string
		apply func(n *node) error
		want  string
	}{
		{
			// The owner's answer does not count for a proposal whose call was
			// given up already.
			name: "a canceled context",
			apply: func(n *node) error {
				p := n.propose("name", `"beta"`, "")
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return n.store.Apply(ctx, p, func(config.Config) error {
					n.t.Error("a canceled call checked the configuration")
					return nil
				})
			},
			want: context.Canceled.Error(),
		},
		{
			// A field that is no field cannot be read, and is not applied.
			name: "an unknown field",
			apply: func(n *node) error {
				p := Proposal{ID: "x", Path: "nope", Before: json.RawMessage(`null`), Value: json.RawMessage(`1`)}
				return n.store.Apply(context.Background(), p, func(config.Config) error {
					n.t.Error("an unknown field checked the configuration")
					return nil
				})
			},
			want: `the configuration has no field "nope" that can be proposed`,
		},
		{
			// A value of the wrong type is refused by the field, not written.
			name: "a value of the wrong type",
			apply: func(n *node) error {
				before, err := n.config().Field("name")
				if err != nil {
					n.t.Fatal(err)
				}
				p := Proposal{ID: "x", Path: "name", Before: before, Value: json.RawMessage(`3`)}
				return n.store.Apply(context.Background(), p, func(config.Config) error {
					n.t.Error("a bad value checked the configuration")
					return nil
				})
			},
			want: "name: json: cannot unmarshal number into Go value of type string",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			n := newNode(t)
			path := filepath.Join(n.dir, "config.json")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			err = tt.apply(n)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("apply: %v, want %q", err, tt.want)
			}
			if tt.name == "a canceled context" && !errors.Is(err, context.Canceled) {
				t.Fatalf("the error is not the one of the context: %v", err)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(before) {
				t.Fatalf("the configuration changed:\n%s\nbefore:\n%s", after, before)
			}
			if cfg := n.config(); cfg.Name != "alpha" || cfg.Model != "gpt" {
				t.Fatalf("the configuration changed: %+v", cfg)
			}
		})
	}
}

// A change the check refuses is not written, though the check sees the field
// already set, and the proposal still waits for another answer.
func TestApplyDoesNotWriteAChangeTheCheckRefuses(t *testing.T) {
	n := newNode(t)
	p := n.propose("name", `"beta"`, "")
	var seen string
	err := n.store.Apply(context.Background(), p, func(c config.Config) error {
		seen = c.Name
		return errors.New("name is taken")
	})
	if err == nil || err.Error() != "name is taken" {
		t.Fatalf("apply: %v", err)
	}
	if seen != "beta" {
		t.Fatalf("the check saw the name %q rather than the change", seen)
	}
	if cfg := n.config(); cfg.Name != "alpha" {
		t.Fatalf("the configuration changed: %+v", cfg)
	}
	if ids := n.waiting(); len(ids) != 1 || ids[0] != p.ID {
		t.Fatalf("proposals: %v", ids)
	}
}
