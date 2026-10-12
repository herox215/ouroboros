package tui

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"ouroboros/plugin"
)

// someSettings are the settings of a node with something of every kind.
func someSettings() Settings {
	return Settings{
		Name: "alpha", Access: "Example", About: "https://api.example/v1", Model: "gpt-4o", ID: "abc123def456",
		Description: "Ouroboros instance named alpha", DescriptionDefault: true,
		ContextProviders: []string{"compact"},
		Plugins: []plugin.PluginState{
			{Name: "base", On: true, Running: true, Fixed: "it runs the commands of ouro"},
			{Name: "history", On: true, Running: true},
			{Name: "tailscale", Running: true},
			{Name: "talk", On: true},
			{Name: "old", Missing: true},
		},
		Tools: [2]plugin.ToolList{
			{All: true, Unset: true, Tools: []plugin.ToolState{
				{Name: "read", Description: "Read a text file. More of it.", Allowed: true, Configurable: true},
				{Name: "get_config", Description: "Show the configuration.", Allowed: true, OwnerOnly: true},
			}},
			{Tools: []plugin.ToolState{
				{Name: "read", Description: "Read a text file.", Allowed: true, Listed: true, Settings: `{"allow":["~/share"]}`, Configurable: true},
				{Name: "get_config", Description: "Show the configuration.", OwnerOnly: true},
				{Name: "list_peers", Description: "List the peers."},
			}},
		},
		Protected: []string{"~/.ssh", "~/.gnupg"}, ProtectedDefault: true,
	}
}

// openSettings opens /settings on a model whose backend has s.
func openSettings(t *testing.T, s Settings) (*Model, *fakeBackend) {
	t.Helper()
	m, b := newModel(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	b.settings, b.done = s, "saved"
	deliver(m, run(m, "/settings"))
	if m.screen != screenSettings {
		t.Fatalf("screen %v", m.screen)
	}
	return m, b
}

// pick opens the category section and selects its row with label, and the
// action called name.
func pick(t *testing.T, m *Model, section, label, name string) {
	t.Helper()
	m.config.peer, m.config.tool, m.config.back = "", "", nil
	for i, s := range sections {
		if s == section {
			m.config.category, m.config.opened = i, true
		}
	}
	for i, r := range m.config.shown(m) {
		if r.label != label {
			continue
		}
		m.config.sel, m.choice = i, 0
		if name == "" {
			return
		}
		for j, a := range r.actions {
			if a.name == name {
				m.choice = j
				return
			}
		}
		t.Fatalf("%s %s has no action %q: %v", section, label, name, r.actions)
	}
	t.Fatalf("no row %s %s", section, label)
}

// dialogView is the settings dialog alone, without the chat behind it.
func dialogView(m *Model) string { return ansi.Strip(m.config.view(m)) }

// do runs the action selected and what follows it.
func do(m *Model) { deliver(m, press(m, "enter")) }

// value is the value of the row of section with label.
func value(t *testing.T, m *Model, section, label string) row {
	t.Helper()
	for _, r := range m.config.rows {
		if r.section == section && r.label == label {
			return r
		}
	}
	t.Fatalf("no row %s %s", section, label)
	return row{}
}

// The settings screen lists the categories, shows the configuration of the
// one that is opened, and never a key.
func TestSettingsShow(t *testing.T) {
	m, _ := openSettings(t, someSettings())
	view := ansi.Strip(m.View().Content)
	for _, want := range []string{
		"│ Settings", "› The node", contents[nodeSection], "  The agents", "  Plugins",
		"  Tools of your agent", "  Tools for peers", "what your agent may do when a peer asks it",
		"  Protected paths", "the paths no tool for files touches", "↑↓ select · enter open · esc back",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("the categories lack %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "https://api.example/v1") || strings.Contains(view, "[") || strings.Index(view, "The node") > strings.Index(view, "The agents") {
		t.Errorf("the categories, in their order, and no line of one:\n%s", view)
	}
	press(m, "enter")
	view = ansi.Strip(m.View().Content)
	for _, want := range []string{
		"│ Settings › The node", "The access and the models are changed with the setup and /model, the effort with /effort.",
		"› Name", "alpha", "Example · https://api.example/v1", "↑↓ select · ←→ choose · enter do · esc back",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("lacks %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "The agents") || strings.Contains(view, "Context provider") {
		t.Errorf("a category shows its own lines alone:\n%s", view)
	}
	for _, tt := range []struct{ section, label, value, note string }{
		{nodeSection, "Description", "Ouroboros instance named alpha", "the default; public: anyone reads it in the agent card"},
		{nodeSection, "Node ID", "abc123def456", "the fingerprint of the key of the node, which nothing here changes"},
		{agentsSection, "Context provider", "none", "what prepares the context of every call of the LLM; none sends the conversation as the memory keeps it"},
		{agentsSection, "Bash without a sandbox", "no", "bash runs its commands in a sandbox: without network, and only where the tools for files may write"},
		{pluginsSection, "base", "on", "it runs the commands of ouro"},
		{pluginsSection, "history", "on", ""},
		{pluginsSection, "tailscale", "off", "runs until ouro starts anew"},
		{pluginsSection, "talk", "on", "does not run until ouro starts anew"},
		{pluginsSection, "old", "off", "not part of this build"},
		{ownerSection, "All tools", "yes, the default", "every tool, also those of plugins turned on later"},
		{ownerSection, "read", "allowed, as all tools are", "Read a text file."},
		{peersSection, "All tools", "no", "every tool but those for your agent alone, also those of plugins turned on later"},
		{peersSection, "read", "allowed", "Read a text file."},
		{peersSection, "list_peers", "not allowed", "List the peers."},
		{peersSection, "get_config", "for your agent alone", "Show the configuration."},
		{protectedSection, "The default", "in use", "the usual places of keys and credentials"},
	} {
		if r := value(t, m, tt.section, tt.label); r.value != tt.value || r.note != tt.note {
			t.Errorf("%s %s: %q, %q", tt.section, tt.label, r.value, r.note)
		}
	}
	if strings.Count(view, "[") != 1 {
		t.Errorf("one line shows what can be done with it:\n%s", view)
	}
	pick(t, m, pluginsSection, "tailscale", "turn on")
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "│ Settings › Plugins") || !strings.Contains(view, "runs until ouro starts anew") {
		t.Errorf("the plugins:\n%s", view)
	}
	if r := value(t, m, peersSection, "get_config"); len(r.actions) != 0 {
		t.Error("a tool for the owner alone can be allowed for peers")
	}
	if r := value(t, m, pluginsSection, "base"); len(r.actions) != 0 {
		t.Error("a fixed plugin can be turned off")
	}

	// Another configuration.
	s := someSettings()
	s.About, s.ContextProvider, s.Sandbox = "", "compact", "bubblewrap is not installed"
	s.BashWithoutSandbox = true
	s.PluginsKept, s.ProtectedDefault = "plugin peers needs a scanner", false
	s.Description, s.DescriptionDefault = "knows the way", false
	s.Tools[ForPeersIndex()].All = true
	m, _ = openSettings(t, s)
	for _, tt := range []struct{ section, label, value string }{
		{nodeSection, "Access", "Example"},
		{nodeSection, "Description", "knows the way"},
		{agentsSection, "Context provider", "compact"},
		{agentsSection, "Bash without a sandbox", "yes"},
		{pluginsSection, "Turned off", "none: all run"},
		{peersSection, "All tools", "yes"},
		{protectedSection, "The default", "not in use"},
	} {
		if r := value(t, m, tt.section, tt.label); r.value != tt.value {
			t.Errorf("%s %s: %q", tt.section, tt.label, r.value)
		}
	}
	if r := value(t, m, nodeSection, "Description"); r.note != "public: anyone reads it in the agent card; empty is the default" {
		t.Errorf("description: %q", r.note)
	}
	if r := value(t, m, agentsSection, "Bash without a sandbox"); r.note != "bash runs its commands as you: with network, the secrets of the node and the source of the core in reach"+
		"; there is no sandbox here (bubblewrap is not installed), so without yes bash runs nothing" {
		t.Errorf("bash: %q", r.note)
	}
	if r := value(t, m, pluginsSection, "Turned off"); !r.warn || !strings.Contains(r.note, "plugin peers needs a scanner") {
		t.Errorf("kept: %+v", r)
	}
}

// ForPeersIndex is where the tools for peers are in Settings.Tools.
func ForPeersIndex() int { return int(plugin.ForPeers) }

// A tool that a peer gets only from a list of its own in tools.peers is shown
// for peers as that, and cannot be allowed for all of them.
func TestSettingsShowATrustedTool(t *testing.T) {
	s := someSettings()
	peers := &s.Tools[ForPeersIndex()]
	peers.Tools = append(peers.Tools, plugin.ToolState{Name: "bash", Description: "Run a command.", Trusted: true})
	m, _ := openSettings(t, s)
	if r := value(t, m, peersSection, "bash"); r.value != "for a peer with a list of its own alone" || len(r.actions) != 0 {
		t.Errorf("bash for peers: %q, %d actions", r.value, len(r.actions))
	}
}

// Every action makes its change, the footer says from when it holds, and the
// configuration is read anew.
func TestSettingsChange(t *testing.T) {
	m, b := openSettings(t, someSettings())
	for _, tt := range []struct {
		section, label, action string
		want                   Setting
	}{
		{agentsSection, "Context provider", "compact", Setting{Kind: plugin.SetContextProvider, Value: "compact"}},
		{agentsSection, "Context provider", "none", Setting{Kind: plugin.SetContextProvider}},
		{agentsSection, "Bash without a sandbox", "allow", Setting{Kind: plugin.SetBashWithoutSandbox, On: true}},
		{agentsSection, "Changes of ouro", "build", Setting{Kind: plugin.SetImproves, On: true}},
		{pluginsSection, "history", "turn off", Setting{Kind: plugin.SetPlugin, Name: "history"}},
		{pluginsSection, "tailscale", "turn on", Setting{Kind: plugin.SetPlugin, Name: "tailscale", On: true}},
		{pluginsSection, "old", "forget", Setting{Kind: plugin.SetPlugin, Name: "old", On: true}},
		{ownerSection, "All tools", "turn off", Setting{Kind: plugin.SetAllTools}},
		{peersSection, "All tools", "turn on", Setting{Kind: plugin.SetAllTools, For: plugin.ForPeers, On: true}},
		{ownerSection, "read", "refuse", Setting{Kind: plugin.SetTool, Name: "read"}},
		{peersSection, "list_peers", "allow", Setting{Kind: plugin.SetTool, For: plugin.ForPeers, Name: "list_peers", On: true}},
		{protectedSection, "~/.ssh", "remove", Setting{Kind: plugin.SetProtected, Value: "~/.gnupg"}},
	} {
		b.changes = nil
		pick(t, m, tt.section, tt.label, tt.action)
		do(m)
		if len(b.changes) != 1 || b.changes[0] != tt.want {
			t.Errorf("%s %s %s: %+v", tt.section, tt.label, tt.action, b.changes)
		}
		if m.notice != "saved" || m.noticeErr {
			t.Errorf("notice %q", m.notice)
		}
	}

	s := someSettings()
	s.BashWithoutSandbox, s.ProtectedDefault, s.Improves = true, false, true
	s.Tools[ForPeersIndex()].All = true
	b.settings = s
	deliver(m, m.config.reload(m))
	if r := value(t, m, agentsSection, "Changes of ouro"); r.value != "builds them, the default" {
		t.Errorf("changes of ouro: %q", r.value)
	}
	for _, tt := range []struct {
		section, label, action string
		want                   Setting
	}{
		{agentsSection, "Bash without a sandbox", "refuse", Setting{Kind: plugin.SetBashWithoutSandbox}},
		{agentsSection, "Changes of ouro", "propose", Setting{Kind: plugin.SetImproves}},
		{peersSection, "All tools", "turn off", Setting{Kind: plugin.SetAllTools, For: plugin.ForPeers}},
		{protectedSection, "The default", "use it", Setting{Kind: plugin.SetProtected, On: true}},
	} {
		b.changes = nil
		pick(t, m, tt.section, tt.label, tt.action)
		do(m)
		if len(b.changes) != 1 || b.changes[0] != tt.want {
			t.Errorf("%s %s %s: %+v", tt.section, tt.label, tt.action, b.changes)
		}
	}

	// What the node refuses is said in red, and nothing else changes.
	b.fail = map[string]error{"change": errors.New("ouro cannot start without it")}
	pick(t, m, pluginsSection, "history", "turn off")
	do(m)
	if m.notice != "ouro cannot start without it" || !m.noticeErr {
		t.Errorf("refused: %q", m.notice)
	}
}

// A text is typed below the list: enter saves it, esc keeps what was.
func TestSettingsEdit(t *testing.T) {
	m, b := openSettings(t, someSettings())
	pick(t, m, nodeSection, "Name", "change")
	press(m, "enter")
	if m.config.editing == nil || m.config.input.Value() != "alpha" {
		t.Fatalf("editing %+v %q", m.config.editing, m.config.input.Value())
	}
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "the name of the node  enter saves · esc keeps what was") || !strings.Contains(view, "enter save · esc keep what was") {
		t.Errorf("the edit line:\n%s", view)
	}
	press(m, "-", "2")
	m.Update(tea.FocusMsg{}) // what is no key goes to the input too
	do(m)
	if len(b.changes) != 1 || b.changes[0] != (Setting{Kind: plugin.SetName, Value: "alpha-2"}) || m.config.editing != nil {
		t.Fatalf("saved %+v", b.changes)
	}

	b.changes = nil
	press(m, "enter", "x", "esc")
	if m.config.editing != nil || len(b.changes) != 0 || m.notice != "nothing changed" || m.screen != screenSettings || !m.config.opened {
		t.Fatalf("kept: %+v, %q", b.changes, m.notice)
	}

	for _, tt := range []struct {
		section, label, action, typed string
		want                          Setting
	}{
		{protectedSection, "Add a path", "change", "~/keys", Setting{Kind: plugin.SetProtected, Value: "~/.ssh\n~/.gnupg\n~/keys"}},
		{nodeSection, "Description", "change", "knows the way", Setting{Kind: plugin.SetDescription, Value: "knows the way"}},
	} {
		b.changes = nil
		pick(t, m, tt.section, tt.label, tt.action)
		press(m, "enter")
		m.config.input.SetValue(tt.typed)
		do(m)
		if len(b.changes) != 1 || b.changes[0] != tt.want {
			t.Errorf("%s %s: %+v", tt.section, tt.label, b.changes)
		}
	}
	// What the owner edits starts from what is there.
	pick(t, m, nodeSection, "Description", "change")
	if press(m, "enter"); m.config.input.Value() != "Ouroboros instance named alpha" {
		t.Errorf("the description: %q", m.config.input.Value())
	}
	press(m, "esc")
}

// The keys move between categories, between the lines of one and between
// what can be done with a line, and stop at the ends; the setup and the model
// open from here.
func TestSettingsKeys(t *testing.T) {
	m, b := openSettings(t, someSettings())
	if press(m, "up"); m.config.category != len(sections)-1 || m.config.opened {
		t.Errorf("up at the first category goes around to the last: %d", m.config.category)
	}
	if press(m, "down"); m.config.category != 0 {
		t.Errorf("down from the last: %d", m.config.category)
	}
	m.config.category = len(sections) - 1
	m.config.sel, m.choice = 2, 1
	if press(m, "enter"); !m.config.opened || m.config.sel != 0 || m.choice != 0 {
		t.Errorf("opened %v at %d, choice %d", m.config.opened, m.config.sel, m.choice)
	}
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "│ Settings › Protected paths") || !strings.Contains(view, "› ~/.ssh") {
		t.Errorf("the last category:\n%s", view)
	}
	if press(m, "esc"); m.config.opened || m.screen != screenSettings || m.config.category != len(sections)-1 {
		t.Errorf("esc leaves the category: %v, %v, %d", m.config.opened, m.screen, m.config.category)
	}

	pick(t, m, nodeSection, "Name", "change")
	if press(m, "up"); m.config.sel != len(m.config.shown(m))-1 {
		t.Errorf("up at the first row goes around to the last: %d", m.config.sel)
	}
	if press(m, "down"); m.config.sel != 0 {
		t.Errorf("down from the last: %d", m.config.sel)
	}
	press(m, "left")
	if press(m, "right"); m.choice != 0 {
		t.Errorf("beyond the only action: %d", m.choice)
	}
	for range len(m.config.shown(m)) - 1 {
		press(m, "down")
	}
	if m.config.sel != len(m.config.shown(m))-1 || len(m.config.shown(m)) != 9 {
		t.Errorf("below the last: %d of %d", m.config.sel, len(m.config.shown(m)))
	}
	pick(t, m, agentsSection, "Context provider", "none")
	if press(m, "right"); m.choice != 1 {
		t.Errorf("right: %d", m.choice)
	}
	if press(m, "left"); m.choice != 0 {
		t.Errorf("left: %d", m.choice)
	}
	press(m, "right")
	if press(m, "down"); m.choice != 0 {
		t.Errorf("another line starts at its first action: %d", m.choice)
	}
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "› Bash without a sandbox") || !strings.Contains(view, "[allow]") {
		t.Errorf("selected:\n%s", view)
	}
	// A line without actions does nothing.
	pick(t, m, nodeSection, "Node ID", "")
	if cmd := press(m, "enter"); cmd != nil {
		t.Error("a line without actions did something")
	}

	pick(t, m, nodeSection, "Model", "open /model")
	press(m, "enter")
	if m.screen != screenModel {
		t.Errorf("model: %v", m.screen)
	}
	press(m, "esc")
	m.config.category = 2
	if deliver(m, run(m, "/settings")); m.config.opened || m.config.category != 0 {
		t.Errorf("opened anew: %v, %d", m.config.opened, m.config.category)
	}
	pick(t, m, nodeSection, "Access", "open /setup")
	press(m, "enter") // the setup animates: its frames are not delivered
	if b.setups != 1 || m.onboard == nil {
		t.Errorf("setup: %d", b.setups)
	}

	// Read anew with fewer lines, the selection stays on one that is there.
	m, b = openSettings(t, someSettings())
	pick(t, m, protectedSection, "~/.ssh", "remove")
	m.config.sel, m.choice = len(m.config.shown(m))-1, 1
	b.settings = Settings{Name: "alpha"}
	deliver(m, m.config.reload(m))
	if m.config.sel != 1 || len(m.config.shown(m)) != 2 || m.choice != 0 {
		t.Errorf("after reading anew: %d of %d, choice %d", m.config.sel, len(m.config.shown(m)), m.choice)
	}
	if press(m, "esc"); m.screen != screenSettings || m.config.opened {
		t.Errorf("esc in a category: %v", m.screen)
	}
	if press(m, "esc"); m.screen != screenChat {
		t.Errorf("esc: %v", m.screen)
	}
}

// The lines that do not fit are scrolled to the one selected; what cannot
// be read is said.
func TestSettingsScrollAndFail(t *testing.T) {
	m, _ := openSettings(t, someSettings())
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 11})
	m.config.category = len(sections) - 1
	view := dialogView(m)
	if !strings.Contains(view, "› Protected paths") || strings.Contains(view, "The node") || !strings.Contains(view, "↑ more") {
		t.Errorf("the categories scrolled:\n%s", view)
	}
	pick(t, m, protectedSection, "The default", "")
	view = dialogView(m)
	if !strings.Contains(view, "› The default") || strings.Contains(view, "~/.ssh") {
		t.Errorf("scrolled:\n%s", view)
	}
	pick(t, m, nodeSection, "Name", "change")
	press(m, "enter")
	if view := dialogView(m); !strings.Contains(view, "› Name") || !strings.Contains(view, "enter saves") {
		t.Errorf("editing on a small screen:\n%s", view)
	}

	m, b := newModel(t)
	b.fail = map[string]error{"settings": errors.New("config.json is broken")}
	deliver(m, run(m, "/settings"))
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "✗ config.json is broken") {
		t.Errorf("failed:\n%s", view)
	}
	if cmd := press(m, "enter"); cmd != nil || m.config.opened {
		t.Error("nothing to do did something")
	}

	// Before the configuration is read, the screen says that it is.
	m, _ = newModel(t)
	m.openSettings()
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "reading the configuration") {
		t.Errorf("reading:\n%s", view)
	}
}

// The lines hold their place while the selection moves: every row has the
// same lines, and the window follows the selection by the line, moving only
// when it would leave it.
func TestSettingsNoJump(t *testing.T) {
	m, _ := openSettings(t, someSettings())
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 20})
	pick(t, m, nodeSection, "Name", "change")
	shown := func() string { return dialogView(m) }
	where := func(what string) int {
		t.Helper()
		for i, l := range strings.Split(shown(), "\n") {
			if strings.Contains(l, what) {
				return i
			}
		}
		t.Fatalf("%q is not shown:\n%s", what, shown())
		return -1
	}
	if strings.Count(shown(), "[") != 1 {
		t.Errorf("what can be done is shown on one line alone:\n%s", shown())
	}
	name, key := where("Name"), where("Description")
	if press(m, "down"); where("Name") != name || where("Description") != key {
		t.Errorf("the lines moved when the selection did:\n%s", shown())
	}
	if !strings.Contains(shown(), "what the node is called on the network") {
		t.Errorf("the note of the row left behind is gone:\n%s", shown())
	}
	// To the last row, which the window follows to and stops at.
	for range len(m.config.shown(m)) {
		if strings.Contains(shown(), "› Node ID") {
			break
		}
		press(m, "down")
	}
	if !strings.Contains(shown(), "› Node ID") {
		t.Errorf("the selection is not on the last row:\n%s", shown())
	}
	node, key := where("Node ID"), where("Model")
	press(m, "up")
	if where("Node ID") != node || where("Model") != key {
		t.Errorf("the lines moved when the selection went back one:\n%s", shown())
	}
	press(m, "down")
	if !strings.Contains(shown(), "the fingerprint of the key of the node") {
		t.Errorf("the note of the row selected is cut off:\n%s", shown())
	}
	// Back to the categories starts at their first line.
	if press(m, "esc"); m.config.top != 0 {
		t.Errorf("the categories start at line %d", m.config.top)
	}
	for i, s := range sections {
		if s == nodeSection {
			m.config.category = i
		}
	}
	press(m, "enter")
	if m.config.sel != 0 || m.config.top != 0 {
		t.Errorf("a category opens at row %d, line %d", m.config.sel, m.config.top)
	}
}

func TestFirstSentence(t *testing.T) {
	for text, want := range map[string]string{"One. Two.": "One.", "Only one": "Only one", "e.g.x. Two": "e.g.x.", ". Two": "."} {
		if got := firstSentence(text); got != want {
			t.Errorf("%q: %q", text, got)
		}
	}
}

// What does not fit in the box is cut off or wrapped within it: the lines of
// the categories, the rows, their notes, and what is typed into a setting.
func TestSettingsFitTheBox(t *testing.T) {
	line := func(v, of string) string {
		t.Helper()
		for _, l := range strings.Split(v, "\n") {
			if strings.Contains(l, of) {
				return l
			}
		}
		t.Fatalf("no line with %q:\n%s", of, v)
		return ""
	}
	// The categories say what is in them in one column.
	m, _ := openSettings(t, someSettings())
	view := ansi.Strip(m.View().Content)
	at := func(l, s string) int { return ansi.StringWidth(l[:strings.Index(l, s)]) }
	if node, agents := line(view, "› The node"), line(view, "  The agents"); at(node, contents[nodeSection]) != at(agents, contents[agentsSection]) {
		t.Errorf("the categories are not aligned:\n%s", view)
	}
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 40})
	if l := line(ansi.Strip(m.View().Content), "› The node"); !strings.HasSuffix(strings.TrimRight(l, " │"), "…") {
		t.Errorf("the category is not cut off: %q", l)
	}

	s := someSettings()
	s.Name = "a-name-much-longer-than-the-box-is-wide-" + strings.Repeat("x", 40)
	words := make([]string, 40)
	for i := range words {
		words[i] = fmt.Sprintf("w%02d", i)
	}
	s.Tools[ForPeersIndex()].Tools[2].Description = strings.Join(words, " ")
	m, _ = openSettings(t, s)
	m.Update(tea.WindowSizeMsg{Width: 60, Height: 60})
	pick(t, m, nodeSection, "Name", "change")
	if l := line(ansi.Strip(m.View().Content), "› Name"); !strings.HasSuffix(strings.TrimRight(l, " │"), "…") {
		t.Errorf("the row is not cut off: %q", l)
	}
	press(m, "enter")
	m.config.input.CursorEnd()
	view = dialogView(m)
	lines := strings.Split(view, "\n")
	input := slices.IndexFunc(lines, func(l string) bool { return strings.HasPrefix(l, "│ > ") })
	if input < 0 || !strings.HasPrefix(lines[input+1], "╰") {
		t.Errorf("the input takes more than its line:\n%s", view)
	}
	press(m, "esc")
	pick(t, m, peersSection, "list_peers", "")
	for _, l := range strings.Split(dialogView(m), "\n") {
		if regexp.MustCompile(`\bw\d\d\b`).MatchString(l) && !strings.HasPrefix(l, "│     w") {
			t.Errorf("a line of the note is not indented: %q", l)
		}
	}
}

// The window moves no sooner than the selection would leave it: on the
// lowest screen that shows the first rows unmoved, the last line of the
// selected one is the last in the box.
func TestSettingsWindowMovesAtTheEdge(t *testing.T) {
	m, _ := openSettings(t, someSettings())
	pick(t, m, nodeSection, "Access", "")
	h := 40
	for ; ; h-- {
		m.config.top = 0
		m.Update(tea.WindowSizeMsg{Width: 80, Height: h - 1})
		if m.View(); m.config.top != 0 {
			break
		}
	}
	m.config.top = 0
	m.Update(tea.WindowSizeMsg{Width: 80, Height: h})
	box := strings.Split(dialogView(m), "\n")
	if m.config.top != 0 || !strings.Contains(box[len(box)-3], "[open /setup]") || !strings.Contains(box[len(box)-2], "↓ more") {
		t.Fatalf("at a height of %d, line %d is at the top:\n%s", h, m.config.top, strings.Join(box, "\n"))
	}
}

// withOptions are someSettings where read has settings one by one: allow,
// not set, and deny, set; and a peer with a list of its own and one without.
func withOptions() Settings {
	s := someSettings()
	options := []plugin.ToolOption{
		{Option: plugin.Option{Name: "allow", Kind: plugin.OptionList, About: "the paths it may go to", Unset: "everything"}},
		{Option: plugin.Option{Name: "deny", Kind: plugin.OptionList, About: "paths it does not go to", Unset: "none"}, Set: true, List: []string{"~/a", "~/b"}},
		{Option: plugin.Option{Name: "network", Kind: plugin.OptionSwitch, About: "it reaches other hosts", Unset: "no"}},
		{Option: plugin.Option{Name: "times", Kind: plugin.OptionNumber, About: "how often"}, Set: true, Value: "2"},
	}
	read := &s.Tools[plugin.ForOwner].Tools[0]
	read.Options, read.Settings = options, `{"deny":["~/a","~/b"],"times":2}`
	own := s.Tools[plugin.ForPeers]
	own.Tools = append(slices.Clone(own.Tools), plugin.ToolState{Name: "bash", Description: "Run a command.", Allowed: true, Listed: true})
	s.Peers = []plugin.PeerTools{
		{ID: "abcdefabcdef", Name: "luna", Own: true, List: own},
		{ID: "123456123456", Name: "sol", List: s.Tools[plugin.ForPeers]},
	}
	return s
}

// A tool shows its settings below its line, each by its name, and what holds
// while one is not set.
func TestSettingsShowTheSettingsOfATool(t *testing.T) {
	m, _ := openSettings(t, withOptions())
	pick(t, m, ownerSection, "read", "")
	view := dialogView(m)
	for _, want := range []string{"• allow    everything (default)", "• deny     ~/a, ~/b", "• network  no (default)", "• times    2", "[refuse]  settings"} {
		if !strings.Contains(view, want) {
			t.Errorf("lacks %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "{") {
		t.Errorf("the settings are shown as JSON:\n%s", view)
	}
	// A tool that is not allowed has no settings to show.
	if r := value(t, m, peersSection, "list_peers"); r.details != nil || len(r.actions) != 1 {
		t.Errorf("list_peers for peers: %+v", r)
	}
}

// The settings of a tool open on a level of their own, where each is set by
// itself; esc goes back to the tool.
func TestSettingsOfATool(t *testing.T) {
	m, b := openSettings(t, withOptions())
	pick(t, m, ownerSection, "read", "settings")
	do(m)
	if m.config.tool != "read" {
		t.Fatalf("not opened: %q", m.config.tool)
	}
	view := dialogView(m)
	for _, want := range []string{"Settings › Tools of your agent › read", "› allow    everything (default)", "the paths it may go to", "· ~/a", "· ~/b", "As JSON"} {
		if !strings.Contains(view, want) {
			t.Errorf("lacks %q:\n%s", want, view)
		}
	}
	at := func(label, action string) {
		t.Helper()
		for i, r := range m.config.shown(m) {
			if r.label != label {
				continue
			}
			for j, a := range r.actions {
				if a.name == action {
					m.config.sel, m.choice = i, j
					return
				}
			}
		}
		t.Fatalf("no %s %s in %+v", label, action, m.config.shown(m))
	}
	setting := func(value string) Setting {
		return Setting{Kind: plugin.SetToolSettings, Name: "read", Value: value}
	}
	for _, tt := range []struct {
		label, action, typed string
		want                 Setting
	}{
		{"allow", "add", " ~/share ", setting(`{"allow":["~/share"],"deny":["~/a","~/b"],"times":2}`)},
		{"allow", "set to none", "", setting(`{"allow":[],"deny":["~/a","~/b"],"times":2}`)},
		{"deny", "add", "~/c", setting(`{"deny":["~/a","~/b","~/c"],"times":2}`)},
		{"deny", "reset", "", setting(`{"times":2}`)},
		{"~/a", "remove", "", setting(`{"deny":["~/b"],"times":2}`)},
		{"network", "turn on", "", setting(`{"deny":["~/a","~/b"],"network":true,"times":2}`)},
		{"times", "change", "3", setting(`{"deny":["~/a","~/b"],"times":3}`)},
		{"times", "change", "often", setting(`{"deny":["~/a","~/b"],"times":"often"}`)},
		{"As JSON", "change", "{}", setting("{}")},
	} {
		b.changes = nil
		at(tt.label, tt.action)
		cmd := press(m, "enter")
		if m.config.editing != nil {
			m.config.input.SetValue(tt.typed)
			cmd = press(m, "enter")
		}
		deliver(m, cmd)
		if len(b.changes) != 1 || b.changes[0] != tt.want {
			t.Errorf("%s %s: %+v", tt.label, tt.action, b.changes)
		}
		if m.config.tool != "read" {
			t.Fatalf("%s %s left the settings of the tool", tt.label, tt.action)
		}
	}
	// The last setting reset takes the settings away.
	s := withOptions()
	read := &s.Tools[plugin.ForOwner].Tools[0]
	read.Options = read.Options[3:]
	read.Settings = `{"times":2}`
	b.settings, b.changes = s, nil
	deliver(m, m.config.reload(m))
	at("times", "reset")
	do(m)
	if len(b.changes) != 1 || b.changes[0] != setting("") {
		t.Errorf("reset the last: %+v", b.changes)
	}
	if press(m, "esc"); m.config.tool != "" || m.config.selectedRow(m).label != "read" {
		t.Errorf("back at %q, %q", m.config.tool, m.config.selectedRow(m).label)
	}
	// A tool that is no more closes its settings.
	pick(t, m, ownerSection, "read", "settings")
	do(m)
	b.settings = someSettings()
	b.settings.Tools[plugin.ForOwner].Tools = b.settings.Tools[plugin.ForOwner].Tools[1:]
	deliver(m, m.config.reload(m))
	if m.config.tool != "" || !m.config.opened {
		t.Errorf("still open: %q", m.config.tool)
	}
}

// A single peer gets a list of its own, which opens like a category and
// whose changes are of that peer alone.
func TestSettingsSinglePeers(t *testing.T) {
	m, b := openSettings(t, withOptions())
	if r := value(t, m, singleSection, "luna"); r.value != "a list of its own: 2 tools" || r.note != "node abcdefabcdef" {
		t.Errorf("luna: %+v", r)
	}
	if r := value(t, m, singleSection, "sol"); r.value != "the tools for peers" {
		t.Errorf("sol: %+v", r)
	}
	for _, tt := range []struct {
		label, action string
		want          Setting
	}{
		{"luna", "remove the list", Setting{Kind: plugin.SetPeerList, Peer: "abcdefabcdef"}},
		{"sol", "give it a list", Setting{Kind: plugin.SetPeerList, Peer: "123456123456", On: true}},
	} {
		b.changes = nil
		pick(t, m, singleSection, tt.label, tt.action)
		do(m)
		if len(b.changes) != 1 || b.changes[0] != tt.want {
			t.Errorf("%s %s: %+v", tt.label, tt.action, b.changes)
		}
	}
	b.changes = nil
	pick(t, m, singleSection, "Add a peer", "by its ID")
	press(m, "enter")
	m.config.input.SetValue(" 0123456789ab ")
	if do(m); len(b.changes) != 1 || b.changes[0] != (Setting{Kind: plugin.SetPeerList, Peer: "0123456789ab", On: true}) {
		t.Errorf("added: %+v", b.changes)
	}

	pick(t, m, singleSection, "luna", "open")
	do(m)
	if view := dialogView(m); !strings.Contains(view, "Settings › Tools of single peers › luna") || !strings.Contains(view, "bash") {
		t.Errorf("the list of luna:\n%s", view)
	}
	for _, tt := range []struct {
		label string
		want  Setting
	}{
		{"bash", Setting{Kind: plugin.SetTool, For: plugin.ForPeers, Peer: "abcdefabcdef", Name: "bash"}},
		{"All tools", Setting{Kind: plugin.SetAllTools, For: plugin.ForPeers, Peer: "abcdefabcdef", On: true}},
	} {
		b.changes = nil
		for i, r := range m.config.shown(m) {
			if r.label == tt.label {
				m.config.sel, m.choice = i, 0
			}
		}
		if do(m); len(b.changes) != 1 || b.changes[0] != tt.want {
			t.Errorf("%s: %+v", tt.label, b.changes)
		}
	}
	if press(m, "esc"); m.config.peer != "" || m.config.selectedRow(m).label != "luna" {
		t.Errorf("back at %q, %q", m.config.peer, m.config.selectedRow(m).label)
	}
	if press(m, "esc"); m.config.opened {
		t.Error("esc in a category stays in it")
	}
}

// The node shows the model for peers, the fallbacks, the effort and the
// debug mode, which open or turn from here.
func TestSettingsModelRows(t *testing.T) {
	m, b := newModel(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	b.settings, b.done = someSettings(), "saved"
	m.status.PeerModel, m.status.Effort = "small", "high" // as the status says
	b.fallbacks = []Fallback{{Access: "a", Model: "one"}, {Access: "b", Model: "two"}}
	deliver(m, run(m, "/settings"))
	for label, want := range map[string]string{"Model for peers": "small", "Fallbacks": "one → two", "Effort": "high", "Debug mode": "off"} {
		pick(t, m, nodeSection, label, "")
		if r := m.config.selectedRow(m); r.value != want {
			t.Errorf("%s: %q", label, r.value)
		}
	}
	pick(t, m, nodeSection, "Debug mode", "turn on")
	if do(m); !slices.Equal(b.debug, []bool{true}) {
		t.Errorf("debug: %v", b.debug)
	}
}

// A tool that is refused and allowed again gets its settings back: it does
// not go everywhere they kept it from.
func TestSettingsOfARefusedTool(t *testing.T) {
	m, b := openSettings(t, someSettings())
	pick(t, m, peersSection, "read", "refuse")
	do(m)
	if len(b.changes) != 1 || b.changes[0] != (Setting{Kind: plugin.SetTool, For: plugin.ForPeers, Name: "read"}) {
		t.Fatalf("refused: %+v", b.changes)
	}
	s := someSettings()
	peers := &s.Tools[ForPeersIndex()]
	for i := range peers.Tools {
		if peers.Tools[i].Name == "read" {
			peers.Tools[i].Allowed, peers.Tools[i].Settings = false, ""
		}
	}
	b.settings = s
	deliver(m, m.config.reload(m))
	b.changes = nil
	pick(t, m, peersSection, "read", "allow")
	do(m)
	if len(b.changes) != 1 || b.changes[0] != (Setting{Kind: plugin.SetToolSettings, For: plugin.ForPeers, Name: "read", Value: `{"allow":["~/share"]}`}) {
		t.Fatalf("allowed again: %+v", b.changes)
	}
	// Once more it is allowed as a tool without settings.
	deliver(m, m.config.reload(m))
	b.changes = nil
	pick(t, m, peersSection, "read", "allow")
	do(m)
	if len(b.changes) != 1 || b.changes[0] != (Setting{Kind: plugin.SetTool, For: plugin.ForPeers, Name: "read", On: true}) {
		t.Fatalf("allowed without settings: %+v", b.changes)
	}
}
