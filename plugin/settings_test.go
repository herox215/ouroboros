package plugin_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"ouroboros/internal/cli/clitest"
	"ouroboros/internal/config"
	"ouroboros/internal/credentials"
	"ouroboros/internal/home"
	"ouroboros/internal/sandbox"
	"ouroboros/plugin"
)

type greeting string

// The plugins of a build for the tests: maker provides what user needs,
// free needs and provides nothing, frame cannot be turned off.
var (
	maker = plugin.NewService("maker", func(*plugin.Context, struct{}) (struct{ Word greeting }, error) {
		return struct{ Word greeting }{"hi"}, nil
	})
	user  = plugin.New("user", func(*plugin.Context, struct{ Word greeting }) error { return nil })
	free  = plugin.New("free", func(*plugin.Context, struct{}) error { return nil })
	frame = plugin.New("frame", func(*plugin.Context, struct{}) error { return nil })
	fixed = map[string]string{"frame": "it is the frame"}
)

// built returns the owner of the node of a new home configured with cfg,
// and the plugins that run of maker, user, free and frame.
func built(t *testing.T, cfg config.Config) (*plugin.Owner, string, []string) {
	t.Helper()
	dir := t.TempDir()
	if err := config.Save(dir, cfg); err != nil {
		t.Fatal(err)
	}
	o := plugin.OwnerOf(home.Dir(dir), nil)
	var names []string
	for _, p := range plugin.Activate(o, []plugin.Plugin{maker, user, free, frame}, fixed) {
		names = append(names, p.Name)
	}
	return o, dir, names
}

// off is a configuration that turns plugins off.
func off(names ...string) config.Config {
	cfg := config.Config{Name: "alpha", Model: "m", BaseURL: "http://x/v1"}
	cfg.Plugins = &config.Plugins{Off: names}
	return cfg
}

// The configuration turns plugins off, but not those that cannot be, and
// not when the rest cannot start.
func TestActivate(t *testing.T) {
	o, _, running := built(t, off("free", "frame", "gone"))
	if !slices.Equal(running, []string{"maker", "user", "frame"}) {
		t.Fatalf("running %v", running)
	}
	s, err := o.Settings(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []plugin.PluginState{
		{Name: "maker", On: true, Running: true, Fixed: "a plugin that is on needs it: plugin user needs plugin_test.greeting, which no plugin provides"},
		{Name: "user", On: true, Running: true},
		{Name: "free"},
		{Name: "frame", On: true, Running: true, Fixed: "it is the frame"},
		{Name: "gone", Missing: true},
	}
	if !reflect.DeepEqual(s.Plugins, want) || s.PluginsKept != "" {
		t.Fatalf("plugins %+v, kept %q", s.Plugins, s.PluginsKept)
	}

	// What user needs is turned off: all run, and the settings say why.
	o, _, running = built(t, off("maker"))
	if len(running) != 4 {
		t.Fatalf("running %v", running)
	}
	if s, _ := o.Settings(nil, nil); !strings.Contains(s.PluginsKept, "plugin user needs plugin_test.greeting") || s.Plugins[0].On || !s.Plugins[0].Running ||
		s.Plugins[0].Fixed != "" {
		t.Fatalf("kept %q, %+v", s.PluginsKept, s.Plugins[0])
	}

	// A configuration that cannot be read turns nothing off.
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "config.json"), []byte("{"), 0o600)
	o = plugin.OwnerOf(home.Dir(dir), nil)
	if got := plugin.Activate(o, []plugin.Plugin{maker, free}, nil); len(got) != 2 {
		t.Fatalf("running %v", got)
	}
	if _, err := o.Settings(nil, nil); err == nil {
		t.Error("settings of a configuration that cannot be read")
	}
}

// The tools of the tests: read takes settings, list_peers none.
func tools() []plugin.Tool {
	type settings struct {
		Allow []string `json:"allow"`
	}
	read := plugin.Configurable(plugin.NewTool("read", "Read a file. Really.", nil, func(context.Context, struct{}) (string, error) { return "", nil }),
		func(settings) (plugin.RunFunc, error) {
			return func(context.Context, json.RawMessage) (string, error) { return "", nil }, nil
		})
	list := plugin.NewTool("list_peers", "List the peers.", nil, func(context.Context, struct{}) (string, error) { return "", nil })
	return []plugin.Tool{read, list}
}

var providers = []plugin.ContextProvider{{Name: "compact"}, {Name: "compact"}, {Name: "other"}}

// The settings are the configuration as it applies, without a key.
func TestSettings(t *testing.T) {
	cfg := clitest.NodeConfig(clitest.Settings(t))
	cfg.APIKey, cfg.ContextProvider, cfg.RebuildWithoutSandbox, cfg.BashWithoutSandbox = "sk-secret", "compact", true, true
	cfg.Tools = &config.Tools{H2A: []config.Entry{{Tool: "*"}, {Tool: "read", Settings: json.RawMessage(`{}`)}},
		A2A: []config.Entry{{Tool: "read", Settings: json.RawMessage(`{"allow":["~/share"]}`)}}}
	o, _, _ := built(t, cfg)
	s, err := o.Settings(tools(), providers)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(s); strings.Contains(string(b), "sk-secret") {
		t.Fatalf("the key is shown: %s", b)
	}
	// The access shows as what it is called and where it reaches, as the
	// node made it of the endpoint of the configuration.
	if s.Name != cfg.Name || s.Access != "localhost" || s.About != cfg.BaseURL || s.Model != cfg.Model || s.ID != cfg.ID() ||
		s.ContextProvider != "compact" || !slices.Equal(s.ContextProviders, []string{"compact", "other"}) || !s.RebuildWithoutSandbox || !s.BashWithoutSandbox ||
		!s.ProtectedDefault || !slices.Equal(s.Protected, config.DefaultProtected()) {
		t.Fatalf("settings %+v", s)
	}
	owner, peers := s.Tools[plugin.ForOwner], s.Tools[plugin.ForPeers]
	if !owner.All || owner.Unset || len(owner.Tools) != 5 {
		t.Fatalf("owner %+v", owner)
	}
	if read := owner.Tools[0]; read.Name != "read" || !read.Allowed || !read.Listed || !read.Configurable || read.Settings != "" || read.Description != "Read a file. Really." {
		t.Errorf("read for the owner %+v", read)
	}
	if config := owner.Tools[2]; config.Name != "get_config" || !config.OwnerOnly || !config.Allowed {
		t.Errorf("get_config for the owner %+v", config)
	}
	if peers.All || peers.Unset {
		t.Fatalf("peers %+v", peers)
	}
	if read := peers.Tools[0]; !read.Allowed || !read.Listed || read.Settings != `{"allow":["~/share"]}` {
		t.Errorf("read for peers %+v", read)
	}
	if list := peers.Tools[1]; list.Allowed || list.Configurable {
		t.Errorf("list_peers for peers %+v", list)
	}
	// Where there is no sandbox, the settings say why.
	t.Setenv(sandbox.Env, "1")
	if s, _ := o.Settings(nil, nil); s.Sandbox != "this process runs in a sandbox already, and sandboxes do not nest" {
		t.Errorf("sandbox %q", s.Sandbox)
	}
	t.Setenv(sandbox.Env, "")
	cfg.Tools.A2A = config.Named("*")
	o, _, _ = built(t, cfg)
	s, _ = o.Settings(tools(), nil)
	if peers := s.Tools[plugin.ForPeers]; !peers.All || !peers.Tools[1].Allowed || peers.Tools[2].Allowed {
		t.Errorf("all tools for peers %+v", peers)
	}
}

// Every change is checked before it is written, and says from when it
// holds; one that fails writes nothing.
func TestChange(t *testing.T) {
	o, dir, _ := built(t, off())
	read := func() config.Config {
		t.Helper()
		cfg, err := config.Load(dir)
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	change := func(s plugin.Setting) string {
		t.Helper()
		holds, err := o.Change(s, tools(), providers)
		if err != nil {
			t.Fatalf("%+v: %v", s, err)
		}
		return holds
	}
	refused := func(s plugin.Setting, why string) {
		t.Helper()
		before := read()
		if _, err := o.Change(s, tools(), providers); err == nil || !strings.Contains(err.Error(), why) {
			t.Errorf("%+v: %v, want %q", s, err, why)
		}
		if !reflect.DeepEqual(read(), before) {
			t.Errorf("%+v changed the configuration", s)
		}
	}
	entries := func(who plugin.Audience) string {
		t.Helper()
		cfg := read()
		list := cfg.OwnerTools()
		if who == plugin.ForPeers {
			list = cfg.PeerTools()
		}
		b, _ := json.Marshal(list)
		return string(b)
	}

	if holds := change(plugin.Setting{Kind: plugin.SetName, Value: " beta "}); read().Name != "beta" || !strings.Contains(holds, "called so from now on") {
		t.Errorf("name: %q, %q", read().Name, holds)
	}
	refused(plugin.Setting{Kind: plugin.SetName, Value: " "}, "name cannot be empty")

	if holds := change(plugin.Setting{Kind: plugin.SetDescription, Value: " knows the way "}); read().Description != "knows the way" || !strings.Contains(holds, "card of the node") {
		t.Errorf("description: %q, %q", read().Description, holds)
	}
	if s, _ := o.Settings(tools(), nil); s.Description != "knows the way" || s.DescriptionDefault {
		t.Errorf("description shown: %q, %v", s.Description, s.DescriptionDefault)
	}
	refused(plugin.Setting{Kind: plugin.SetDescription, Value: "two\nlines"}, "description is one line")
	refused(plugin.Setting{Kind: plugin.SetDescription, Value: strings.Repeat("x", config.MaxDescription+1)}, "at most 300")
	if change(plugin.Setting{Kind: plugin.SetDescription, Value: " "}); read().Description != "" {
		t.Errorf("no description: %q", read().Description)
	}
	if s, _ := o.Settings(tools(), nil); s.Description != "Ouroboros instance named beta" || !s.DescriptionDefault {
		t.Errorf("the default shown: %q, %v", s.Description, s.DescriptionDefault)
	}

	if holds := change(plugin.Setting{Kind: plugin.SetContextProvider, Value: "compact"}); read().ContextProvider != "compact" || !strings.Contains(holds, "from the next message") {
		t.Errorf("provider: %q", holds)
	}
	refused(plugin.Setting{Kind: plugin.SetContextProvider, Value: "nope"}, `this build has no context provider "nope"`)
	if change(plugin.Setting{Kind: plugin.SetContextProvider}); read().ContextProvider != "" {
		t.Error("no provider")
	}

	if holds := change(plugin.Setting{Kind: plugin.SetRebuildWithoutSandbox, On: true}); !read().RebuildWithoutSandbox || !strings.Contains(holds, "next rebuild") {
		t.Errorf("rebuild: %q", holds)
	}
	if holds := change(plugin.Setting{Kind: plugin.SetBashWithoutSandbox, On: true}); !read().BashWithoutSandbox || !strings.Contains(holds, "next command of bash") {
		t.Errorf("bash: %q", holds)
	}

	if holds := change(plugin.Setting{Kind: plugin.SetPlugin, Name: "free"}); !slices.Equal(read().PluginsOff(), []string{"free"}) || !strings.Contains(holds, "once ouro starts anew") {
		t.Errorf("plugin off: %v, %q", read().PluginsOff(), holds)
	}
	change(plugin.Setting{Kind: plugin.SetPlugin, Name: "user"})
	if !slices.Equal(read().PluginsOff(), []string{"free", "user"}) {
		t.Errorf("sorted: %v", read().PluginsOff())
	}
	change(plugin.Setting{Kind: plugin.SetPlugin, Name: "maker"}) // nobody needs it now
	refused(plugin.Setting{Kind: plugin.SetPlugin, Name: "user", On: true}, "so ouro cannot start: plugin user needs plugin_test.greeting")
	change(plugin.Setting{Kind: plugin.SetPlugin, Name: "maker", On: true})
	change(plugin.Setting{Kind: plugin.SetPlugin, Name: "user", On: true})
	refused(plugin.Setting{Kind: plugin.SetPlugin, Name: "maker"}, "so ouro cannot start: plugin user needs plugin_test.greeting, which no plugin provides")
	refused(plugin.Setting{Kind: plugin.SetPlugin, Name: "frame"}, "frame cannot be turned off: it is the frame")
	refused(plugin.Setting{Kind: plugin.SetPlugin, Name: "nope"}, `this build has no plugin "nope"`)
	// A provider is the plugin of its name: while an access of the node
	// signs in with it, it stays on. Once the access is gone, it can go.
	store := home.Dir(dir).Credentials()
	access, _ := store.Put(credentials.Access{Provider: "free"})
	refused(plugin.Setting{Kind: plugin.SetPlugin, Name: "free"}, "free cannot be turned off: an access of the node signs in with it. Remove the access in /setup first")
	store.Remove(access.ID)
	change(plugin.Setting{Kind: plugin.SetPlugin, Name: "free"})
	if change(plugin.Setting{Kind: plugin.SetPlugin, Name: "free", On: true}); read().Plugins != nil {
		t.Errorf("none off: %+v", read().Plugins)
	}
	config.Update(dir, func(c *config.Config) error { c.Plugins = &config.Plugins{Off: []string{"gone"}}; return nil })
	if change(plugin.Setting{Kind: plugin.SetPlugin, Name: "gone", On: true}); read().Plugins != nil {
		t.Errorf("forgotten: %+v", read().Plugins)
	}

	// A list that is not set is written out when it is changed.
	change(plugin.Setting{Kind: plugin.SetAllTools, For: plugin.ForPeers})
	if read().Tools == nil || read().Tools.A2A == nil || entries(plugin.ForPeers) != `[]` {
		t.Errorf("no tools for peers, written: %+v", read().Tools)
	}

	// The tools of the owner: all, and then all but read.
	change(plugin.Setting{Kind: plugin.SetTool, Name: "read"})
	if got := entries(plugin.ForOwner); got != `["list_peers","get_config","propose_config","withdraw_proposal"]` {
		t.Errorf("all but read: %s", got)
	}
	change(plugin.Setting{Kind: plugin.SetTool, Name: "read", On: true})
	change(plugin.Setting{Kind: plugin.SetTool, Name: "read", On: true}) // listed already
	change(plugin.Setting{Kind: plugin.SetToolSettings, Name: "read", Value: `{ "allow": ["~/x"] }`})
	change(plugin.Setting{Kind: plugin.SetAllTools, On: true})
	if got := entries(plugin.ForOwner); got != `["*",{"allow":["~/x"],"tool":"read"}]` {
		t.Errorf("all again, settings kept: %s", got)
	}
	change(plugin.Setting{Kind: plugin.SetAllTools, On: true}) // on already
	change(plugin.Setting{Kind: plugin.SetToolSettings, Name: "read", Value: ""})
	if got := entries(plugin.ForOwner); got != `["*"]` {
		t.Errorf("without settings under *: %s", got)
	}
	// Also where the entry comes before "*" in a file written by hand.
	config.Update(dir, func(c *config.Config) error {
		tools := *c.Tools
		tools.H2A = []config.Entry{{Tool: "read", Settings: json.RawMessage(`{"allow":["~/x"]}`)}, {Tool: config.AllTools}}
		c.Tools = &tools
		return nil
	})
	if got := entries(plugin.ForOwner); got != `[{"allow":["~/x"],"tool":"read"},"*"]` {
		t.Fatalf("written by hand: %s", got)
	}
	change(plugin.Setting{Kind: plugin.SetToolSettings, Name: "read", Value: ""})
	if got := entries(plugin.ForOwner); got != `["*"]` {
		t.Errorf("without settings before *: %s", got)
	}
	change(plugin.Setting{Kind: plugin.SetAllTools})
	if got := entries(plugin.ForOwner); got != `["read","list_peers","get_config","propose_config","withdraw_proposal"]` {
		t.Errorf("all named: %s", got)
	}
	change(plugin.Setting{Kind: plugin.SetAllTools}) // off already
	// Turned on where it is listed first, it stays listed once.
	change(plugin.Setting{Kind: plugin.SetTool, Name: "read", On: true})
	if got := entries(plugin.ForOwner); got != `["read","list_peers","get_config","propose_config","withdraw_proposal"]` {
		t.Errorf("listed first: %s", got)
	}
	change(plugin.Setting{Kind: plugin.SetToolSettings, Name: "read", Value: `{"allow":["~/y"]}`})
	change(plugin.Setting{Kind: plugin.SetToolSettings, Name: "read", Value: "{}"})
	if got := entries(plugin.ForOwner); got != `["read","list_peers","get_config","propose_config","withdraw_proposal"]` {
		t.Errorf("settings taken away: %s", got)
	}

	// The tools of peers: none, then some, and never one of the owner alone.
	change(plugin.Setting{Kind: plugin.SetTool, For: plugin.ForPeers, Name: "list_peers", On: true})
	change(plugin.Setting{Kind: plugin.SetToolSettings, For: plugin.ForPeers, Name: "read", Value: `{"allow":["~/share"]}`})
	if got := entries(plugin.ForPeers); got != `["list_peers",{"allow":["~/share"],"tool":"read"}]` {
		t.Errorf("peers: %s", got)
	}
	change(plugin.Setting{Kind: plugin.SetTool, For: plugin.ForPeers, Name: "list_peers"})
	change(plugin.Setting{Kind: plugin.SetTool, For: plugin.ForPeers, Name: "list_peers"}) // not listed
	change(plugin.Setting{Kind: plugin.SetAllTools, For: plugin.ForPeers, On: true})
	change(plugin.Setting{Kind: plugin.SetTool, For: plugin.ForPeers, Name: "read"})
	if got := entries(plugin.ForPeers); got != `["list_peers"]` {
		t.Errorf("peers, all but read: %s", got)
	}
	change(plugin.Setting{Kind: plugin.SetTool, For: plugin.ForPeers, Name: "list_peers"})
	if got := entries(plugin.ForPeers); got != `[]` {
		t.Errorf("peers, none: %s", got)
	}
	refused(plugin.Setting{Kind: plugin.SetTool, For: plugin.ForPeers, Name: "get_config", On: true}, "get_config is for your agent alone: no peer gets it")
	refused(plugin.Setting{Kind: plugin.SetTool, Name: "nope", On: true}, `there is no tool "nope"`)
	refused(plugin.Setting{Kind: plugin.SetToolSettings, Name: "read", Value: "allow ~/x"}, "the settings of a tool are a JSON object")
	refused(plugin.Setting{Kind: plugin.SetToolSettings, Name: "read", Value: `{"deny": ["~/x"]}`}, "settings of the tool read")
	refused(plugin.Setting{Kind: plugin.SetToolSettings, Name: "list_peers", Value: `{"allow": ["~/x"]}`}, "the tool list_peers takes no settings")

	// The paths no tool touches.
	change(plugin.Setting{Kind: plugin.SetProtected, Value: "~/.ssh\n\n ~/keys \n~/.ssh\n"})
	if got := read().Protected(); !slices.Equal(got, []string{"~/.ssh", "~/keys"}) {
		t.Errorf("protected %v", got)
	}
	change(plugin.Setting{Kind: plugin.SetProtected, Value: ""})
	if got := read().Protected(); got == nil || len(got) != 0 {
		t.Errorf("nothing protected %v", got)
	}
	change(plugin.Setting{Kind: plugin.SetProtected, On: true})
	if read().Tools.Protected != nil {
		t.Error("the default")
	}
	refused(plugin.Setting{Kind: plugin.SetProtected, Value: "relative"}, "has to be absolute")
	refused(plugin.Setting{Kind: 99}, "no such setting")

	// The log says what was changed, not to what.
	b, _ := os.ReadFile(filepath.Join(dir, "ouro.log"))
	for _, want := range []string{
		"the owner changed the settings: the name\n", "the owner changed the settings: the plugin free\n",
		"the owner changed the settings: the settings of the tool read for peers\n", "the owner changed the settings: all tools of the owner\n",
	} {
		if !strings.Contains(string(b), want) {
			t.Errorf("log lacks %q", want)
		}
	}
	if strings.Contains(string(b), "~/share") || strings.Contains(string(b), "beta") {
		t.Errorf("the log has a value:\n%s", b)
	}
}
