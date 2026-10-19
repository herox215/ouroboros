package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ouroboros/internal/a2a"
	"ouroboros/internal/assistant"
	"ouroboros/internal/cli"
	"ouroboros/internal/cli/clitest"
	"ouroboros/internal/config"
	"ouroboros/internal/contacts"
	"ouroboros/internal/credentials"
	"ouroboros/internal/home"
	"ouroboros/internal/identity"
	"ouroboros/internal/llm"
	"ouroboros/internal/node"
	"ouroboros/internal/peers"
	"ouroboros/plugin"
)

func TestSettingsFromEnv(t *testing.T) {
	t.Setenv("OURO_HOME", "/tmp/ouro")
	t.Setenv("OURO_PORT", "1234")
	t.Setenv("OURO_DISCO_PORT", "")
	t.Setenv("OPENAI_BASE_URL", "")
	t.Setenv("OPENAI_API_KEY", "sk-env")
	s, err := cli.SettingsFromEnv()
	want := cli.Settings{Home: "/tmp/ouro", HTTPPort: 1234, DiscoPort: 41338, BaseURL: "https://api.openai.com/v1", APIKey: "sk-env"}
	if err != nil || s != want {
		t.Fatalf("got %+v, %v", s, err)
	}
	t.Setenv("OURO_PORT", "abc")
	if _, err := cli.SettingsFromEnv(); err == nil || !strings.Contains(err.Error(), "OURO_PORT") {
		t.Fatalf("invalid port: %v", err)
	}
	t.Setenv("OURO_PORT", "")
	t.Setenv("OURO_DISCO_PORT", "x")
	if _, err := cli.SettingsFromEnv(); err == nil || !strings.Contains(err.Error(), "OURO_DISCO_PORT") {
		t.Fatalf("invalid discovery port: %v", err)
	}
}

// Without OURO_HOME the node lives where the user keeps configurations.
func TestDefaultHome(t *testing.T) {
	t.Setenv("OURO_HOME", "")
	t.Setenv("OURO_PORT", "")
	t.Setenv("OURO_DISCO_PORT", "")
	old := *cli.UserConfigDir
	t.Cleanup(func() { *cli.UserConfigDir = old })

	*cli.UserConfigDir = func() (string, error) { return "/home/me/.config", nil }
	if s, err := cli.SettingsFromEnv(); err != nil || s.Home != "/home/me/.config/ouroboros" || s.HTTPPort != 41337 {
		t.Fatalf("got %+v, %v", s, err)
	}
	nowhere := errors.New("no home")
	*cli.UserConfigDir = func() (string, error) { return "", nowhere }
	if _, err := cli.SettingsFromEnv(); !errors.Is(err, nowhere) {
		t.Fatalf("got %v", err)
	}
}

// The standard ouro starts: its plugins have what they need, and none of
// them declares a part of the frame.
func TestStandardOuro(t *testing.T) {
	var out bytes.Buffer
	app, err := cli.New(clitest.Settings(t), cli.Terminal{In: strings.NewReader(""), Out: &out})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Run(context.Background(), []string{"help"}); err != nil {
		t.Fatal(err)
	}
	// The commands are listed in the order of the plugins.
	at := -1
	for _, command := range []string{"ouro start", "ouro stop", "ouro status", "ouro serve", "ouro list", "ouro chat", "ouro ask", "ouro tui", "ouro gui", "ouro update"} {
		next := strings.Index(out.String(), command)
		if next <= at {
			t.Fatalf("%q is missing or out of order in:\n%s", command, out.String())
		}
		at = next
	}
}

// The owner turns plugins off in the configuration, but not those that are
// ouro, and not one the others need.
func TestPluginsTurnedOff(t *testing.T) {
	s := clitest.Settings(t)
	cfg := clitest.NodeConfig(s)
	cfg.Plugins = &config.Plugins{Off: []string{"talk", "update", "tui", "gui", "gone"}}
	config.Save(s.Home, cfg)
	names := func() []string {
		var names []string
		for _, p := range cli.Plugins(s, cli.Terminal{}) {
			names = append(names, p.Name)
		}
		return names
	}
	got := names()
	if slices.Contains(got, "talk") || slices.Contains(got, "update") || slices.Contains(got, "gui") || !slices.Contains(got, "tui") || !slices.Contains(got, "history") || !slices.Contains(got, "memory") {
		t.Fatalf("running %v", got)
	}
	if _, err := clitest.Run(t, s, "", "update"); err == nil {
		t.Error("the command of a plugin turned off runs")
	}
	// Without discovery, peers cannot start: everything runs.
	cfg.Plugins.Off = []string{"discovery", "talk"}
	config.Save(s.Home, cfg)
	if got := names(); !slices.Contains(got, "discovery") || !slices.Contains(got, "talk") {
		t.Fatalf("running %v", got)
	}
}

// Without the plugin that runs commands there is nothing to run.
func TestOuroWithoutPlugins(t *testing.T) {
	app, err := cli.NewWith()
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Run(context.Background(), []string{"help"}); err == nil || !strings.Contains(err.Error(), "base plugin is missing") {
		t.Fatalf("got %v", err)
	}
}

// shout is a plugin as someone would add it: a command and a tool the owner
// can allow, written against package plugin only.
func shout() plugin.Plugin {
	type needs struct {
		Console *plugin.Console
	}
	return plugin.New("shout", func(ctx *plugin.Context, n needs) error {
		plugin.Add(ctx, plugin.Command{Name: "shout", Args: "TEXT", Help: "print TEXT in capitals", Run: func(_ context.Context, args []string) error {
			fmt.Fprintln(n.Console.Out, strings.ToUpper(strings.Join(args, " ")))
			return nil
		}})
		plugin.Add(ctx, plugin.NewTool("shout", "Returns the text in capitals.",
			[]plugin.Param{{Name: "text"}},
			func(_ context.Context, args struct {
				Text string `json:"text"`
			}) (string, error) {
				return strings.ToUpper(args.Text), nil
			}))
		return nil
	})
}

// A plugin adds to ouro without a change anywhere else: here a command and a
// tool the owner can allow.
func TestPluginExtendsOuro(t *testing.T) {
	s := clitest.Settings(t)
	cfg := clitest.NodeConfig(s)
	cfg.Tools = &config.Tools{H2A: config.Named("shout")}
	config.Save(s.Home, cfg)

	var out bytes.Buffer
	term := cli.Terminal{In: strings.NewReader(""), Out: &out}
	// Listed first, although it needs the console of the base plugin.
	app, err := cli.NewWith(append([]plugin.Plugin{shout()}, cli.Plugins(s, term)...)...)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Run(context.Background(), []string{"shout", "hello", "ouro"}); err != nil || out.String() != "HELLO OURO\n" {
		t.Fatalf("command: %q, %v", out.String(), err)
	}
	out.Reset()
	if err := app.Run(context.Background(), []string{"help"}); err != nil || !strings.Contains(out.String(), "ouro shout TEXT") {
		t.Fatalf("usage: %q, %v", out.String(), err)
	}
	// The owner allowed the tool "shout": the assistant is built with it.
	bot, _ := cli.Service[*assistant.Assistant](app)
	if err := bot.Ready(); err != nil {
		t.Fatalf("the tool of the plugin should be known: %v", err)
	}

	// Without the plugin the same configuration names a tool that is not there.
	plain, _ := clitest.NewApp(t, s, "")
	bot, _ = cli.Service[*assistant.Assistant](plain)
	if err := bot.Ready(); err == nil || !strings.Contains(err.Error(), `unknown tool "shout"`) {
		t.Fatalf("got %v", err)
	}
}

// The tools for files keep out of what the configuration protects as it is
// at the call, and touch nothing while it cannot be read.
func TestFilesFollowTheConfiguration(t *testing.T) {
	user := t.TempDir()
	t.Setenv("HOME", user)
	if err := os.WriteFile(filepath.Join(user, "notes.md"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := clitest.Settings(t)
	var ctx *plugin.Context
	if _, err := cli.NewWith(clitest.Plugins(s, cli.Terminal{In: strings.NewReader(""), Out: &bytes.Buffer{}},
		plugin.New("tools", func(c *plugin.Context, _ struct{}) error { ctx = c; return nil }))...); err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(plugin.All[plugin.Tool](ctx), func(tool plugin.Tool) bool { return tool.Name == "read" })
	if i < 0 {
		t.Fatal("no tool read")
	}
	read := func() (string, error) {
		return plugin.All[plugin.Tool](ctx)[i].Run(context.Background(), json.RawMessage(`{"path":"~/notes.md"}`))
	}
	if got, err := read(); err != nil || got != "hello\n" {
		t.Fatalf("without a configuration: %q, %v", got, err)
	}
	cfg := clitest.NodeConfig(s)
	cfg.Tools = &config.Tools{Protected: &[]string{"~/notes.md"}}
	if err := config.Save(s.Home, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := read(); err == nil || !strings.Contains(err.Error(), `~/notes.md is in "protected" of the configuration`) {
		t.Fatalf("protected: %v", err)
	}
	if err := os.WriteFile(filepath.Join(s.Home, "config.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := read(); err == nil || !strings.Contains(err.Error(), "the configuration cannot be read, so no file is touched") {
		t.Fatalf("a configuration that cannot be read: %v", err)
	}
}

// standard is the standard ouro of a node of its own, with extra plugins.
func standard(t *testing.T, extra ...plugin.Plugin) []plugin.Plugin {
	t.Helper()
	return clitest.Plugins(clitest.Settings(t), cli.Terminal{In: strings.NewReader(""), Out: &bytes.Buffer{}}, extra...)
}

// needing returns a plugin called name that needs the service T.
func needing[T any](name string) plugin.Plugin {
	return plugin.New(name, func(*plugin.Context, struct{ Service T }) error { return nil })
}

// providing returns a plugin called name that provides service.
func providing[T any](name string, service T) plugin.Plugin {
	return plugin.NewService(name, func(*plugin.Context, struct{}) (struct{ Service T }, error) {
		return struct{ Service T }{service}, nil
	})
}

// A plugin that needs something nobody provides stops ouro from starting,
// and so do plugins that need each other: ouro never runs with a part of its
// plugins.
func TestOuroDoesNotStartIncomplete(t *testing.T) {
	_, err := cli.NewWith(standard(t, needing[*testing.T]("lonely"))...)
	if err == nil || !strings.Contains(err.Error(), "plugin lonely needs *testing.T, which no plugin provides") {
		t.Fatalf("got %v", err)
	}

	type ping struct{}
	type pong struct{}
	a := plugin.NewService("a", func(*plugin.Context, struct{ Pong *pong }) (struct{ Ping *ping }, error) {
		return struct{ Ping *ping }{&ping{}}, nil
	})
	b := plugin.NewService("b", func(*plugin.Context, struct{ Ping *ping }) (struct{ Pong *pong }, error) {
		return struct{ Pong *pong }{&pong{}}, nil
	})
	_, err = cli.NewWith(standard(t, a, b)...)
	if err == nil || !strings.Contains(err.Error(), "a needs *cli_test.pong of b, b needs *cli_test.ping of a") {
		t.Fatalf("got %v", err)
	}
}

// The frame of ouro is no service. A plugin cannot ask for the key of the
// node, its contacts or anything that holds them, cannot ask for a way to
// them, and cannot offer something in their place.
func TestFrameIsOutOfReach(t *testing.T) {
	dir := home.Dir(t.TempDir())
	for name, p := range map[string]plugin.Plugin{
		"the identity":                 needing[*identity.Identity]("p"),
		"the configuration":            needing[config.Config]("p"),
		"the home":                     needing[home.Dir]("p"),
		"the contacts":                 needing[*contacts.Store]("p"),
		"trust":                        needing[a2a.Trust]("p"),
		"the client that signs":        needing[*a2a.Client]("p"),
		"the server that verifies":     needing[*a2a.Server]("p"),
		"the node":                     needing[node.Server]("p"),
		"the control of the node":      needing[node.Local]("p"),
		"the network with its insides": needing[*peers.Network]("p"),
		"a way to the identity":        needing[func() (*identity.Identity, error)]("p"),
		"a way to the configuration":   needing[func() (config.Config, error)]("p"),
		"an identity of its own":       providing("p", identity.Generate()),
		"contacts of its own":          providing("p", dir.Contacts()),
		"trust of its own":             providing[a2a.Trust]("p", dir.Contacts()),
		"a home of its own":            providing("p", dir),
		"a network of its own":         providing("p", &peers.Network{}),
		"a client of its own":          providing("p", func() (*a2a.Client, error) { return nil, nil }),
		// What the owner does is handed to a front end in the list of
		// plugins, and to nobody who asks the kernel for it.
		"what the owner does":            needing[*plugin.Owner]("p"),
		"an owner of its own":            providing("p", plugin.OwnerOf(dir, &peers.Network{})),
		"a way to what the owner does":   needing[func() *plugin.Owner]("p"),
		"the setup, which saves the key": needing[*plugin.Setup]("p"),
		// What the node signs in to its LLMs with: a provider is handed its
		// own access, and nobody the store or the book that opens it.
		"the credentials":           needing[*credentials.Store]("p"),
		"credentials of its own":    providing("p", dir.Credentials()),
		"the book of the accesses":  needing[llm.Book]("p"),
		"a way to every credential": providing("p", &llm.Book{Store: dir.Credentials()}),
		// An LLM that is handed the configuration would be handed the key.
		"an LLM that reads the configuration": providing("p", func(config.Config) plugin.Model { return nil }),
	} {
		_, err := cli.NewWith(standard(t, p)...)
		if err == nil || !strings.Contains(err.Error(), "plugin p declares the service") || !strings.Contains(err.Error(), "never a service") {
			t.Errorf("%s: got %v", name, err)
		}
	}
}

// What the standard plugins provide cannot be replaced by another plugin.
func TestServicesCannotBeReplaced(t *testing.T) {
	for name, p := range map[string]plugin.Plugin{
		"*plugin.Network": providing("mine", plugin.NetworkOf(&peers.Network{})),
		"*plugin.Console": providing("mine", plugin.NewConsole(plugin.Terminal{})),
	} {
		_, err := cli.NewWith(standard(t, p)...)
		if err == nil || !strings.Contains(err.Error(), name+" is provided by plugin") || !strings.Contains(err.Error(), "and by plugin mine") {
			t.Errorf("%s: got %v", name, err)
		}
	}
}

// imports returns what the Go files in dir import, tests left aside.
func imports(t *testing.T, dir ...string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(append(append([]string{"..", ".."}, dir...), "*.go")...))
	if err != nil || len(files) == 0 {
		t.Fatalf("files in %v: %v, %v", dir, files, err)
	}
	var imports []string
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, spec := range f.Imports {
			path, _ := strconv.Unquote(spec.Path.Value)
			imports = append(imports, path)
		}
	}
	return imports
}

// A plugin is written against package plugin and nothing else of ouro: the
// example plugin, which is the one to copy, keeps to that, also where it
// adds to the terminal UI. So does the terminal UI: the plugin and the UI it
// opens know ouro by package plugin alone.
func TestPluginsImportOnlyThePluginPackage(t *testing.T) {
	for name, test := range map[string]struct {
		dir     []string
		allowed []string
	}{
		"the example plugin": {[]string{"plugin", "example"}, []string{"ouroboros/plugin"}},
		"the tui plugin":     {[]string{"internal", "plugins", "tui"}, []string{"ouroboros/plugin", "ouroboros/internal/tui", "ouroboros/internal/workdir"}},
		"the terminal UI":    {[]string{"internal", "tui"}, []string{"ouroboros/plugin"}},
	} {
		found := imports(t, test.dir...)
		if !slices.Contains(found, "ouroboros/plugin") {
			t.Fatalf("%s imports %v", name, found)
		}
		for _, path := range found {
			if strings.HasPrefix(path, "ouroboros/") && !slices.Contains(test.allowed, path) {
				t.Errorf("%s imports %s", name, path)
			}
		}
	}
}

// The terminal UI is a plugin like any other: nothing but the list of plugins
// knows it, so ouro builds without it once it is taken out of the list.
func TestOnlyTheListKnowsTheTerminalUI(t *testing.T) {
	for _, dir := range [][]string{
		{"cmd", "ouro"}, {"plugin"}, {"plugin", "example"}, {"internal", "kernel"}, {"internal", "assistant"}, {"internal", "node"},
		{"internal", "plugins", "assistant"}, {"internal", "plugins", "base"}, {"internal", "plugins", "discovery"},
		{"internal", "plugins", "node"}, {"internal", "plugins", "openai"}, {"internal", "plugins", "peers"},
		{"internal", "plugins", "session"}, {"internal", "plugins", "tailscale"}, {"internal", "plugins", "talk"},
		{"internal", "plugins", "update"},
	} {
		for _, path := range imports(t, dir...) {
			if path == "ouroboros/internal/plugins/tui" || path == "ouroboros/internal/tui" {
				t.Errorf("%s imports %s", filepath.Join(dir...), path)
			}
		}
	}
	if !slices.Contains(imports(t, "internal", "cli"), "ouroboros/internal/plugins/tui") {
		t.Fatal("the list of plugins should name the terminal UI")
	}
}

// ouro without the tui plugin runs: without a command it prints the usage,
// also for a user at a terminal, and the chat goes line by line. Its
// sessions are in its process here: no node is needed.
func TestOuroWithoutTheTerminalUI(t *testing.T) {
	s := clitest.Settings(t)
	if err := config.Save(s.Home, clitest.NodeConfig(s)); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	term := cli.Terminal{In: strings.NewReader("hello\n"), Out: &out, Interactive: true}
	list := slices.DeleteFunc(cli.Plugins(s, term), func(p plugin.Plugin) bool { return p.Name == "tui" })
	if len(list) != len(cli.Plugins(s, term))-1 {
		t.Fatal("the standard ouro should have the tui plugin")
	}
	app, err := cli.NewWith(clitest.InProcess(s)(list)...)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Run(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	usage := out.String()
	if !strings.Contains(usage, "ouro start") || !strings.Contains(usage, "ouro chat") || strings.Contains(usage, "(default)") || strings.Contains(usage, "ouro tui") {
		t.Fatalf("usage:\n%s", usage)
	}
	if err := app.Run(context.Background(), []string{"tui"}); err == nil || !strings.Contains(err.Error(), `unknown command "tui"`) {
		t.Fatalf("got %v", err)
	}
	out.Reset()
	if err := app.Run(context.Background(), []string{"chat"}); err != nil || !strings.Contains(out.String(), "turns=1 last=hello") {
		t.Fatalf("chat: %q, %v", out.String(), err)
	}
}
