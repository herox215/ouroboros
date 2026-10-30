package tui

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"ouroboros/plugin"
)

// someSettings are the settings of a node with something of every kind.
func someSettings() Settings {
	return Settings{
		Name: "alpha", Access: "Example", About: "https://api.example/v1", Model: "gpt-4o", ID: "abc123def456",
		Description: "Ouroboros instance named alpha", DescriptionDefault: true,
		ThreadWindow:     "3h",
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
func dialogView(m *Model) string { return ansi.Strip(drawnOf(m.config.view(m))) }

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
		"│ Settings", "› The node", "  The agents", "  Plugins",
		"  Tools of your agent", "  Tools for peers",
		"  Protected paths", "↑↓ select · ←→ choose · enter open · esc back",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("the categories lack %q:\n%s", want, view)
		}
	}
	for _, description := range contents {
		if strings.Contains(view, description) {
			t.Errorf("a category description is shown normally: %q\n%s", description, view)
		}
	}
	if strings.Contains(view, "https://api.example/v1") || strings.Index(view, "The node") > strings.Index(view, "The agents") {
		t.Errorf("the categories, in their order, and no line of one:\n%s", view)
	}
	press(m, "enter")
	view = ansi.Strip(m.View().Content)
	for _, want := range []string{
		"│ Settings › The node",
		"› Name", "alpha", "Example · https://api.example/v1", "↑↓ select · ←→ choose · enter do · esc back",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("lacks %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, about[nodeSection]) || strings.Contains(view, "The agents") || strings.Contains(view, "Context provider") {
		t.Errorf("a category shows its own lines alone:\n%s", view)
	}
	for _, tt := range []struct{ section, label, value, note string }{
		{nodeSection, "Description", "Ouroboros instance named alpha", "the default; public: anyone reads it in the agent card"},
		{nodeSection, "Node ID", "abc123def456", "the fingerprint of the key of the node, which nothing here changes"},
		{agentsSection, "Sessions the thread continues", "3h", "how long after its last message the thread hands a message to a session; a duration like 3h"},
		{agentsSection, "Context provider", "none", "what prepares the context of every call of the LLM; none sends the conversation as the memory keeps it"},
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
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "│ Settings › Plugins") || strings.Contains(view, "runs until ouro starts anew") {
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
	s.About, s.ContextProvider = "", "compact"
	s.PluginsKept, s.ProtectedDefault = "plugin peers needs a scanner", false
	s.Description, s.DescriptionDefault = "knows the way", false
	s.Tools[ForPeersIndex()].All = true
	m, _ = openSettings(t, s)
	for _, tt := range []struct{ section, label, value string }{
		{nodeSection, "Access", "Example"},
		{nodeSection, "Description", "knows the way"},
		{agentsSection, "Context provider", "compact"},
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
	s.ProtectedDefault, s.Improves = false, true
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
	m.config.sel, m.choice = 2, 0
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
	if press(m, "right"); m.choice != 1 {
		t.Errorf("right chooses Info: %d", m.choice)
	}
	if press(m, "right"); m.choice != 1 {
		t.Errorf("beyond the last action: %d", m.choice)
	}
	for range len(m.config.shown(m)) - 1 {
		press(m, "down")
	}
	if m.config.sel != len(m.config.shown(m))-1 || len(m.config.shown(m)) != 15 {
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
	if !strings.Contains(view, "› Changes of ouro") || !strings.Contains(view, "[build]") {
		t.Errorf("selected:\n%s", view)
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
	if m.config.sel != 1 || len(m.config.shown(m)) != 2 || m.choice != 1 {
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
	if strings.Contains(shown(), "what the node is called on the network") {
		t.Errorf("the note of the row left behind is shown:\n%s", shown())
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
	if strings.Contains(shown(), "the fingerprint of the key of the node") {
		t.Errorf("the note of the row selected is shown:\n%s", shown())
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
// the categories, the rows, Info text, and what is typed into a setting.
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
	// Category names fit without their descriptions.
	m, _ := openSettings(t, someSettings())
	view := ansi.Strip(m.View().Content)
	for _, description := range contents {
		if strings.Contains(view, description) {
			t.Errorf("a category description is shown: %q\n%s", description, view)
		}
	}
	m.Update(tea.WindowSizeMsg{Width: 40, Height: 40})
	if l := line(dialogView(m), "› The node"); ansi.StringWidth(l) > 36 {
		t.Errorf("the category exceeds the box: %q", l)
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
	pick(t, m, peersSection, "list_peers", "Info")
	do(m)
	view = dialogView(m)
	if !strings.Contains(view, "w00") || !strings.Contains(view, "w39") {
		t.Errorf("Info text is not wrapped in full:\n%s", view)
	}
	for _, l := range strings.Split(view, "\n") {
		if regexp.MustCompile(`\bw\d\d\b`).MatchString(l) && ansi.StringWidth(l) > 56 {
			t.Errorf("a line of Info exceeds the box: %q", l)
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
	for _, want := range []string{"Settings › Tools of your agent › read", "› allow    everything (default)", "· ~/a", "· ~/b", "As JSON"} {
		if !strings.Contains(view, want) {
			t.Errorf("lacks %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "the paths it may go to") {
		t.Errorf("a tool option note is shown normally:\n%s", view)
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

// The node shows the model for peers, the fallbacks, the effort, the debug
// mode and the yolo mode, which open or turn from here.
func TestSettingsModelRows(t *testing.T) {
	m, b := newModel(t)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	b.settings, b.done = someSettings(), "saved"
	m.status.PeerModel, m.status.Effort = "small", "high" // as the status says
	b.fallbacks = []Fallback{{Access: "a", Model: "one"}, {Access: "b", Model: "two"}}
	deliver(m, run(m, "/settings"))
	for label, want := range map[string]string{"Model for peers": "small", "Fallbacks": "one → two", "Effort": "high", "Debug mode": "off", "Yolo mode": "off"} {
		pick(t, m, nodeSection, label, "")
		if r := m.config.selectedRow(m); r.value != want {
			t.Errorf("%s: %q", label, r.value)
		}
	}
	pick(t, m, nodeSection, "Debug mode", "turn on")
	if do(m); !slices.Equal(b.debug, []bool{true}) {
		t.Errorf("debug: %v", b.debug)
	}
	m.status.Debug = true // as the status says once it is read anew
	pick(t, m, nodeSection, "Debug mode", "turn off")
	if r := m.config.selectedRow(m); r.value != "on" {
		t.Errorf("debug on: %q", r.value)
	}
	if do(m); !slices.Equal(b.debug, []bool{true, false}) {
		t.Errorf("debug: %v", b.debug)
	}

	pick(t, m, nodeSection, "Yolo mode", "turn on")
	if do(m); !slices.Equal(b.yolo, []bool{true}) {
		t.Errorf("yolo: %v", b.yolo)
	}
	m.status.Yolo = true // as the status says once it is read anew
	pick(t, m, nodeSection, "Yolo mode", "turn off")
	if r := m.config.selectedRow(m); r.value != "on" {
		t.Errorf("yolo on: %q", r.value)
	}
	if do(m); !slices.Equal(b.yolo, []bool{true, false}) {
		t.Errorf("yolo: %v", b.yolo)
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

// Info is appended to the actions of rows with notes, without changing the
// stored actions or adding it again when the page is read anew.
func TestSettingsInfoActions(t *testing.T) {
	m, _ := openSettings(t, someSettings())
	for _, section := range sections {
		for i, s := range sections {
			if s == section {
				m.config.category, m.config.opened = i, true
			}
		}
		page := m.config.page(m)
		for range 2 {
			shown := m.config.shown(m)
			for i, r := range shown {
				want := len(page[i].actions)
				if r.note != "" {
					want++
				}
				if len(r.actions) != want {
					t.Fatalf("%s %s: %d actions, want %d", section, r.label, len(r.actions), want)
				}
				for j, a := range page[i].actions {
					if r.actions[j].name != a.name {
						t.Errorf("%s %s: action %d changed", section, r.label, j)
					}
				}
				if r.note != "" && r.actions[len(r.actions)-1].name != "Info" {
					t.Errorf("%s %s: Info is not last", section, r.label)
				}
			}
		}
	}
}

// Info is a read-only level: both return keys keep the same row and action,
// even when the row has no other action or its note is a warning.
func TestSettingsInfo(t *testing.T) {
	for _, tt := range []struct{ section, label string }{
		{nodeSection, "Name"},
		{nodeSection, "Node ID"},
		{pluginsSection, "base"},
		{pluginsSection, "tailscale"},
		{peersSection, "get_config"},
	} {
		t.Run(tt.label, func(t *testing.T) {
			m, b := openSettings(t, someSettings())
			pick(t, m, tt.section, tt.label, "Info")
			checkSettingsInfo(t, m, b)
		})
	}
}

// Tool options have Info too, but entries of their lists have no note.
func TestSettingsToolOptionInfo(t *testing.T) {
	for _, label := range []string{"allow", "deny", "network", "times", "As JSON"} {
		t.Run(label, func(t *testing.T) {
			m, b := openSettings(t, withOptions())
			pick(t, m, ownerSection, "read", "settings")
			do(m)
			for i, r := range m.config.shown(m) {
				if r.sub {
					if len(r.actions) != 1 || r.actions[0].name != "remove" {
						t.Errorf("list entry %s: %+v", r.label, r.actions)
					}
				}
				if r.label == label {
					m.config.sel, m.choice = i, len(r.actions)-1
				}
			}
			if m.config.selectedRow(m).label != label {
				t.Fatalf("no option %s", label)
			}
			checkSettingsInfo(t, m, b)
		})
	}
}

func checkSettingsInfo(t *testing.T, m *Model, b *fakeBackend) {
	t.Helper()
	r := m.config.selectedRow(m)
	if r.note == "" || r.actions[m.choice].name != "Info" {
		t.Fatalf("not on Info: %+v, choice %d", r, m.choice)
	}
	category, sel, choice := m.config.category, m.config.sel, m.choice
	peer, tool, back := m.config.peer, m.config.tool, slices.Clone(m.config.back)
	// Any attempt to reload would replace the current page with an error.
	b.fail = map[string]error{"settings": errors.New("Info must not reload")}
	m.notice = "unchanged"
	for _, key := range []string{"esc", "enter"} {
		if strings.Contains(dialogView(m), r.note) {
			t.Errorf("the note is shown before Info: %q", r.note)
		}
		if cmd := press(m, "enter"); cmd != nil {
			t.Fatal("Info returned a backend command")
		}
		if m.config.info == nil || m.config.info.label != r.label || m.config.info.note != r.note || m.config.info.warn != r.warn {
			t.Fatalf("Info: %+v", m.config.info)
		}
		view := dialogView(m)
		for _, want := range []string{r.label + " · Info"} {
			if !strings.Contains(view, want) {
				t.Errorf("Info lacks %q:\n%s", want, view)
			}
		}
		if !strings.Contains(strings.Join(strings.Fields(strings.ReplaceAll(view, "│", " ")), " "), strings.Join(strings.Fields(r.note), " ")) {
			t.Errorf("Info lacks its note %q:\n%s", r.note, view)
		}
		press(m, "down", "right", "x")
		if m.config.info == nil {
			t.Fatal("another key closed Info")
		}
		if cmd := press(m, key); cmd != nil || m.config.info != nil {
			t.Fatalf("%s did not return from Info", key)
		}
		if m.screen != screenSettings || !m.config.opened || m.config.category != category || m.config.sel != sel || m.choice != choice || m.config.peer != peer || m.config.tool != tool || !slices.Equal(m.config.back, back) {
			t.Errorf("%s changed the level or selection", key)
		}
		if len(b.changes) != 0 || len(b.debug) != 0 || b.setups != 0 || m.config.editing != nil || m.config.err != nil {
			t.Errorf("Info changed settings or notice: %+v, %q", b.changes, m.notice)
		}
	}
}

// The settings of a tool for peers, and of a tool in the list of a single
// peer, open like those of the tools of your agent, and their changes are for
// peers, or for that peer alone. A peer whose list is no more closes what
// showed it.
func TestSettingsOfAToolOfPeers(t *testing.T) {
	m, b := openSettings(t, withOptions())
	pick(t, m, peersSection, "read", "settings")
	do(m)
	if view := dialogView(m); m.config.tool != "read" || !strings.Contains(view, "Settings › Tools for peers › read") || !strings.Contains(view, `{"allow":["~/share"]}`) {
		t.Fatalf("the settings of read for peers:\n%s", view)
	}
	choose(t, m, "As JSON", "change")
	press(m, "enter")
	m.config.input.SetValue("{}")
	if do(m); len(b.changes) != 1 || b.changes[0] != (Setting{Kind: plugin.SetToolSettings, For: plugin.ForPeers, Name: "read", Value: "{}"}) {
		t.Fatalf("for peers: %+v", b.changes)
	}

	b.changes = nil
	pick(t, m, singleSection, "luna", "open")
	do(m)
	choose(t, m, "read", "settings")
	do(m)
	if view := dialogView(m); m.config.peer != "abcdefabcdef" || m.config.tool != "read" || !strings.Contains(view, "Settings › Tools of single peers › luna › read") {
		t.Fatalf("the settings of read for luna:\n%s", view)
	}
	choose(t, m, "As JSON", "change")
	press(m, "enter")
	m.config.input.SetValue("{}")
	if do(m); len(b.changes) != 1 || b.changes[0] != (Setting{Kind: plugin.SetToolSettings, For: plugin.ForPeers, Peer: "abcdefabcdef", Name: "read", Value: "{}"}) {
		t.Fatalf("for luna: %+v", b.changes)
	}

	// The list of luna is removed meanwhile: what is open goes back to the
	// category.
	s := withOptions()
	s.Peers = s.Peers[1:]
	b.settings = s
	deliver(m, m.config.reload(m))
	if view := dialogView(m); m.config.peer != "" || m.config.tool != "" || !m.config.opened || len(m.config.back) != 1 ||
		!strings.Contains(view, "Settings › Tools of single peers") || strings.Contains(view, "luna") {
		t.Fatalf("peer %q, tool %q, back %v:\n%s", m.config.peer, m.config.tool, m.config.back, view)
	}
}

// What shows a peer or a tool that is not in the configuration shows
// nothing, rather than another list.
func TestSettingsPageOfWhatIsNoMore(t *testing.T) {
	m, _ := openSettings(t, withOptions())
	pick(t, m, singleSection, "luna", "")
	c := &m.config
	for _, tt := range []struct{ peer, tool string }{{"gone", ""}, {"gone", "read"}, {"abcdefabcdef", "gone"}, {"", "gone"}} {
		c.peer, c.tool = tt.peer, tt.tool
		if rows := c.page(m); rows != nil {
			t.Errorf("peer %q, tool %q: %+v", tt.peer, tt.tool, rows)
		}
		if c.toolState() != nil {
			t.Errorf("peer %q, tool %q: a tool", tt.peer, tt.tool)
		}
	}
}

// A category without lines has nothing to select and nothing to do.
func TestSettingsEmptyCategory(t *testing.T) {
	s := someSettings()
	s.Plugins = nil
	m, b := openSettings(t, s)
	for i, section := range sections {
		if section == pluginsSection {
			m.config.category = i
		}
	}
	press(m, "enter")
	if !m.config.opened || len(m.config.shown(m)) != 0 || !strings.Contains(dialogView(m), "Settings › Plugins") {
		t.Fatalf("opened %v:\n%s", m.config.opened, dialogView(m))
	}
	press(m, "down", "right")
	if r := m.config.selectedRow(m); m.config.sel != 0 || m.choice != 0 || r.label != "" || r.actions != nil {
		t.Errorf("selected %d, choice %d: %+v", m.config.sel, m.choice, r)
	}
	if cmd := press(m, "enter"); cmd != nil || len(b.changes) != 0 {
		t.Errorf("nothing to do did something: %+v", b.changes)
	}
}

// A peer of the configuration that is no contact is named by its ID, and a
// list of one tool says so.
func TestSettingsPeerWithoutContact(t *testing.T) {
	s := withOptions()
	s.Peers = append(s.Peers, plugin.PeerTools{ID: "fedcbafedcba", Own: true, List: plugin.ToolList{Tools: []plugin.ToolState{
		{Name: "bash", Allowed: true, Listed: true}, {Name: "read"},
	}}})
	m, _ := openSettings(t, s)
	if r := value(t, m, singleSection, "fedcbafedcba"); r.value != "a list of its own: 1 tool" || r.note != "no contact: a list in the configuration" {
		t.Errorf("the peer: %+v", r)
	}
	if got := count(0, "tool"); got != "0 tools" {
		t.Errorf("none: %q", got)
	}
}

// What a setting of a tool is set to is shown as it reads: a list set to
// nothing is none, a switch yes or no, and what is not set is the default.
func TestSettingsOptionValue(t *testing.T) {
	list := plugin.Option{Name: "allow", Kind: plugin.OptionList, Unset: "everything"}
	on := plugin.Option{Name: "network", Kind: plugin.OptionSwitch}
	for _, tt := range []struct {
		o    plugin.ToolOption
		want string
	}{
		{plugin.ToolOption{Option: list}, "everything (default)"},
		{plugin.ToolOption{Option: list, Set: true}, "none"},
		{plugin.ToolOption{Option: list, Set: true, List: []string{"~/a", "~/b"}}, "~/a, ~/b"},
		{plugin.ToolOption{Option: on}, "not set (default)"},
		{plugin.ToolOption{Option: on, Set: true, Value: "true"}, "yes"},
		{plugin.ToolOption{Option: on, Set: true, Value: "false"}, "no"},
		{plugin.ToolOption{Option: plugin.Option{Name: "times", Kind: plugin.OptionNumber}, Set: true, Value: "2"}, "2"},
	} {
		if got := optionValue(tt.o); got != tt.want {
			t.Errorf("%+v: %q, want %q", tt.o, got, tt.want)
		}
	}
}

// A switch that is on turns off.
func TestSettingsSwitchTurnsOff(t *testing.T) {
	s := withOptions()
	read := &s.Tools[plugin.ForOwner].Tools[0]
	read.Options[2].Set, read.Options[2].Value = true, "true"
	read.Settings = `{"deny":["~/a","~/b"],"network":true,"times":2}`
	m, b := openSettings(t, s)
	pick(t, m, ownerSection, "read", "settings")
	do(m)
	if got := shownValue(t, m, "network"); got != "yes" {
		t.Errorf("network: %q", got)
	}
	choose(t, m, "network", "turn off")
	if do(m); len(b.changes) != 1 || b.changes[0] != (Setting{Kind: plugin.SetToolSettings, Name: "read", Value: `{"deny":["~/a","~/b"],"network":false,"times":2}`}) {
		t.Errorf("turned off: %+v", b.changes)
	}
}

// No text is no lines; a text is wrapped at the width, each line after the
// indent.
func TestSettingsWrapped(t *testing.T) {
	if got := wrapped("", "  ", 20, mutedStyle); got != nil {
		t.Errorf("no text: %q", got)
	}
	got := wrapped("one two three four", "> ", 10, lipgloss.NewStyle())
	if strings.Join(got, "|") != "> one two|> three|> four" {
		t.Errorf("wrapped: %q", got)
	}
}

// The list of categories shows each name whole, the one selected marked, and
// what can be done below that one alone.
func TestSettingsCategoriesWhole(t *testing.T) {
	m, _ := openSettings(t, someSettings())
	for i, section := range sections {
		m.config.category = i
		view := dialogView(m)
		if !strings.Contains(view, "› "+section) || strings.Count(view, "[open]  Info") != 1 {
			t.Errorf("%s selected:\n%s", section, view)
		}
		for _, other := range sections {
			if other != section && !strings.Contains(view, "  "+other+" ") {
				t.Errorf("%s selected, %s is not whole:\n%s", section, other, view)
			}
		}
	}
}

// The window over the lines says what lies above and below it, and only
// that: where all of them fit it says nothing, at the first line nothing
// above, at the last nothing below, and there it is full. The dialog is as
// high as the body leaves it, also when it is very low.
func TestSettingsWindowMarks(t *testing.T) {
	m, _ := openSettings(t, someSettings())
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if view := dialogView(m); strings.Contains(view, "more") || !strings.Contains(view, protectedSection) || !strings.Contains(view, nodeSection) {
		t.Fatalf("all categories fit:\n%s", view)
	}
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 23})
	if view := dialogView(m); !strings.Contains(view, "↓ more") || strings.Contains(view, "↑ more") {
		t.Fatalf("at the first category of too many:\n%s", view)
	}
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 14})
	m.config.category = len(sections) - 1
	if view := dialogView(m); strings.Contains(view, "↓ more") || !strings.Contains(view, "↑ more") || !strings.Contains(view, gatewaysSection) {
		t.Fatalf("at the last category:\n%s", view)
	}
	for _, height := range []int{40, 12, 8} {
		m.Update(tea.WindowSizeMsg{Width: 80, Height: height})
		_, h := m.settingsSize()
		if h != max(1, m.bodyHeight()-2) {
			t.Errorf("at a height of %d the dialog may be %d high, in a body of %d", height, h, m.bodyHeight())
		}
		for _, info := range []*row{nil, {label: "a", note: strings.Repeat("a long explanation ", 40)}} {
			m.config.info = info
			if got := lipgloss.Height(drawnOf(m.config.view(m))); got > h {
				t.Errorf("at a height of %d the dialog is %d high, not at most %d", height, got, h)
			}
		}
		m.config.info = nil
	}
}

// What is typed shows its end, where it is typed, also when it is longer
// than the line.
func TestSettingsEditShowsTheEnd(t *testing.T) {
	m, _ := openSettings(t, someSettings())
	pick(t, m, nodeSection, "Description", "change")
	press(m, "enter")
	m.config.input.SetValue(strings.Repeat("x", 120) + "THE END")
	m.config.input.CursorEnd()
	if view := dialogView(m); !strings.Contains(view, "THE END") {
		t.Fatalf("the end of what is typed is not shown:\n%s", view)
	}
}

// After the configuration is read anew, what is chosen stays on an action
// the line still has.
func TestSettingsChoiceStaysOnTheLine(t *testing.T) {
	m, b := openSettings(t, someSettings())
	pick(t, m, agentsSection, "Context provider", "Info")
	if m.choice != 2 {
		t.Fatalf("Info is the action %d", m.choice)
	}
	s := someSettings()
	s.ContextProviders = nil
	b.settings = s
	deliver(m, m.config.reload(m))
	if m.choice != 1 {
		t.Fatalf("with one action fewer the choice is %d", m.choice)
	}
}

// A tool has settings to open only where it is allowed and has settings;
// one without single settings shows them as they are kept, and one without
// any shows none. Without fallbacks the node says so.
func TestSettingsToolDetailsAndActions(t *testing.T) {
	m, _ := openSettings(t, someSettings())
	if r := value(t, m, ownerSection, "get_config"); len(r.actions) != 1 || r.actions[0].name != "refuse" {
		t.Errorf("get_config for your agent: %+v", r.actions)
	}
	if r := value(t, m, peersSection, "list_peers"); len(r.actions) != 1 || r.actions[0].name != "allow" {
		t.Errorf("list_peers for peers: %+v", r.actions)
	}
	if r := value(t, m, peersSection, "read"); len(r.details) != 1 || r.details[0] != (detail{"settings", `{"allow":["~/share"]}`, false}) {
		t.Errorf("read for peers: %+v", r.details)
	}
	if r := value(t, m, ownerSection, "read"); len(r.details) != 0 {
		t.Errorf("read for your agent: %+v", r.details)
	}
	pick(t, m, nodeSection, "Fallbacks", "")
	if r := m.config.selectedRow(m); r.value != "none" {
		t.Errorf("no fallbacks: %q", r.value)
	}
}
