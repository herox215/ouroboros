package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"ouroboros/internal/identity"
)

func TestLoadMissingReturnsEmpty(t *testing.T) {
	c, err := Load(t.TempDir())
	if err != nil || !reflect.DeepEqual(c, Config{}) {
		t.Fatalf("got %+v, %v", c, err)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested")
	want := Config{BaseURL: "http://x/v1", APIKey: "sk", Model: "m", Name: "n", PrivateKey: "key"}
	if err := Save(dir, want); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestSaveRestrictsPermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, fileName)
	os.WriteFile(path, []byte("{}"), 0o644)
	if err := Save(dir, Config{}); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("perm = %o, want 600", perm)
	}
}

// Saving replaces the file instead of writing into it: whoever reads it sees
// a whole configuration, and a crash cannot leave half of one.
func TestSaveReplacesTheFile(t *testing.T) {
	dir := t.TempDir()
	if err := Save(dir, Config{Name: "old", PrivateKey: "key"}); err != nil {
		t.Fatal(err)
	}
	old, err := os.Open(filepath.Join(dir, fileName))
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	before, _ := old.Stat()
	if err := Save(dir, Config{Name: "new"}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(filepath.Join(dir, fileName))
	if os.SameFile(before, after) {
		t.Fatal("the file was written in place")
	}
	if after.Mode().Perm() != 0o600 {
		t.Fatalf("perm = %o, want 600", after.Mode().Perm())
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Fatalf("saving left %d files behind", len(entries)-1)
	}
}

func TestUpdate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested")
	rename := func(name string) func(*Config) error {
		return func(c *Config) error {
			c.Name = name
			return nil
		}
	}
	if err := Update(dir, rename("alpha")); err != nil {
		t.Fatal(err)
	}
	if err := Update(dir, func(c *Config) error {
		if c.Name != "alpha" {
			t.Errorf("update of %+v", c)
		}
		c.Model = "m"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got, err := Load(dir); err != nil || !reflect.DeepEqual(got, Config{Name: "alpha", Model: "m"}) {
		t.Fatalf("got %+v, %v", got, err)
	}

	// A change that changes nothing or fails writes nothing.
	saved, _ := os.Stat(filepath.Join(dir, fileName))
	boom := errors.New("boom")
	if err := Update(dir, func(c *Config) error {
		c.Name = "beta"
		return boom
	}); !errors.Is(err, boom) {
		t.Fatalf("failed change: %v", err)
	}
	if err := Update(dir, rename("alpha")); err != nil {
		t.Fatal(err)
	}
	if now, _ := os.Stat(filepath.Join(dir, fileName)); !os.SameFile(saved, now) {
		t.Fatal("the file was written without a change")
	}
	if got, _ := Load(dir); got.Name != "alpha" {
		t.Fatalf("got %+v", got)
	}
}

// Changes made at the same time do not overwrite each other.
func TestUpdateIsExclusive(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := Update(dir, func(c *Config) error {
				c.Name += "x"
				return nil
			}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if got, _ := Load(dir); len(got.Name) != 20 {
		t.Fatalf("%d of 20 changes survived", len(got.Name))
	}
}

func TestUpdateReportsErrors(t *testing.T) {
	untouched := func(*Config) error {
		t.Error("a configuration that cannot be read must not be changed")
		return nil
	}

	// A file in the place of the directory.
	file := filepath.Join(t.TempDir(), "file")
	os.WriteFile(file, nil, 0o600)
	if err := Update(filepath.Join(file, "sub"), untouched); err == nil {
		t.Error("Update should report a directory it cannot create")
	}

	// A directory in the place of the lock.
	dir := t.TempDir()
	os.Mkdir(filepath.Join(dir, lockName), 0o700)
	if err := Update(dir, untouched); err == nil {
		t.Error("Update should report a lock it cannot take")
	}

	dir = t.TempDir()
	os.WriteFile(filepath.Join(dir, fileName), []byte("{"), 0o600)
	if err := Update(dir, untouched); err == nil {
		t.Error("Update should report a configuration it cannot read")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, fileName)); string(b) != "{" {
		t.Errorf("an unreadable configuration should be left alone, got %q", b)
	}
}

func TestLoadInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, fileName), []byte("{"), 0o600)
	if _, err := Load(dir); err == nil {
		t.Fatal("want error")
	}
}

func TestComplete(t *testing.T) {
	full := Config{Access: "a", Model: "m", Name: "n"}
	if !full.Complete() {
		t.Fatal("config without node key should be complete")
	}
	// A configuration from before there were accesses names its endpoint.
	if !(Config{BaseURL: "u", Model: "m", Name: "n"}).Complete() {
		t.Fatal("a configuration with an endpoint in place of an access should be complete")
	}
	if (Config{Model: "m", Name: "n"}).Complete() {
		t.Fatal("config without access should be incomplete")
	}
	partial := full
	partial.Model = ""
	if partial.Complete() {
		t.Fatal("config without model should be incomplete")
	}
}

// The ID is the fingerprint of the node's key and nothing that is stored.
func TestIDFollowsFromKey(t *testing.T) {
	id := identity.Generate()
	if got := (Config{PrivateKey: id.Private()}).ID(); got != id.ID() {
		t.Fatalf("ID = %q, want %q", got, id.ID())
	}
	for _, key := range []string{"", "nonsense"} {
		if got := (Config{PrivateKey: key}).ID(); got != "" {
			t.Fatalf("a node without a valid key has the ID %q", got)
		}
	}
}

// A config written before the ID was derived keeps working: its random ID is
// ignored, its key and everything else stay.
func TestLoadIgnoresStoredID(t *testing.T) {
	dir, id := t.TempDir(), identity.Generate()
	old := `{"base_url": "u", "model": "m", "name": "n", "id": "abc123", "private_key": "` + id.Private() + `"}`
	os.WriteFile(filepath.Join(dir, fileName), []byte(old), 0o600)
	c, err := Load(dir)
	if err != nil || !c.Complete() || c.PrivateKey != id.Private() || c.ID() != id.ID() {
		t.Fatalf("got %+v, %v", c, err)
	}
	if err := Save(dir, c); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, fileName)); strings.Contains(string(b), "abc123") {
		t.Fatalf("the old ID was written again: %s", b)
	}
}

// Without a tools section the owner gets every tool and peers get none.
func TestToolLists(t *testing.T) {
	tests := []struct {
		name      string
		tools     *Tools
		owner     []Entry
		peerTools []Entry
	}{
		{"default", nil, Named(AllTools), nil},
		{"only a2a set", &Tools{A2A: Named("list_peers")}, Named(AllTools), Named("list_peers")},
		{"owner without tools", &Tools{H2A: Named()}, Named(), nil},
		{"both set", &Tools{H2A: Named("ask_peer"), A2A: Named(AllTools)}, Named("ask_peer"), Named(AllTools)},
	}
	for _, tt := range tests {
		c := Config{Tools: tt.tools}
		if got := c.OwnerTools(); !reflect.DeepEqual(got, tt.owner) {
			t.Errorf("%s: owner tools %v", tt.name, got)
		}
		if got := c.PeerTools(); !reflect.DeepEqual(got, tt.peerTools) {
			t.Errorf("%s: peer tools %v", tt.name, got)
		}
	}
}

// An owner who took all tools away keeps that choice when the file is rewritten.
func TestToolListsSurviveSaving(t *testing.T) {
	dir := t.TempDir()
	want := Config{Name: "n", Tools: &Tools{H2A: Named(), A2A: Named("list_peers")}}
	if err := Save(dir, want); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir)
	if err != nil || len(got.OwnerTools()) != 0 || !reflect.DeepEqual(got.PeerTools(), Named("list_peers")) {
		t.Fatalf("got %+v, %v", got.Tools, err)
	}
}

// What cannot be read or written is reported, not taken for an empty config.
func TestLoadAndSaveReportErrors(t *testing.T) {
	// A directory in the place of the file can neither be read nor written.
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, fileName), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Fatal("Load should report an unreadable file")
	}
	if err := Save(dir, Config{}); err == nil {
		t.Fatal("Save should report an unwritable file")
	}

	// A file in the place of the directory.
	file := filepath.Join(t.TempDir(), "file")
	os.WriteFile(file, nil, 0o600)
	if err := Save(filepath.Join(file, "sub"), Config{}); err == nil {
		t.Fatal("Save should report a directory it cannot create")
	}
}

// An entry of a list is a name, or an object with the name and settings for
// the tool, and is written the way it was read.
func TestEntries(t *testing.T) {
	dir := t.TempDir()
	written := `{"name": "n", "tools": {
		"h2a": ["*", {"tool": "read", "deny": ["~/tax"]}, {"tool": "list"}],
		"a2a": ["list_peers", {"allow": ["~/share"], "tool": "read", "limit": 3}],
		"protected": ["~/.ssh"]}}`
	os.WriteFile(filepath.Join(dir, fileName), []byte(written), 0o600)
	c, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	owner := []Entry{{Tool: "*"}, {Tool: "read", Settings: json.RawMessage(`{"deny":["~/tax"]}`)}, {Tool: "list", Settings: json.RawMessage(`{}`)}}
	peer := []Entry{{Tool: "list_peers"}, {Tool: "read", Settings: json.RawMessage(`{"allow":["~/share"],"limit":3}`)}}
	if !reflect.DeepEqual(c.OwnerTools(), owner) || !reflect.DeepEqual(c.PeerTools(), peer) || !reflect.DeepEqual(c.Protected(), []string{"~/.ssh"}) {
		t.Fatalf("got %s / %s / %v", c.OwnerTools(), c.PeerTools(), c.Protected())
	}
	if err := Save(dir, c); err != nil {
		t.Fatal(err)
	}
	again, err := Load(dir)
	if err != nil || !reflect.DeepEqual(again, c) {
		t.Fatalf("after saving: %+v, %v", again.Tools, err)
	}
	b, _ := json.Marshal(c.Tools)
	want := `{"h2a":["*",{"deny":["~/tax"],"tool":"read"},{"tool":"list"}],"a2a":["list_peers",{"allow":["~/share"],"limit":3,"tool":"read"}],"protected":["~/.ssh"]}`
	if string(b) != want {
		t.Fatalf("written as %s", b)
	}

	for entry, want := range map[string]string{
		`3`:                         `a tool is a name or an object like {"tool": name, …}`,
		`["read"]`:                  `a tool is a name or an object like {"tool": name, …}`,
		`{"allow": []}`:             `a tool with settings needs its name as "tool"`,
		`{"tool": 3, "allow": []}`:  `a tool with settings needs its name as "tool"`,
		`{"tool": "", "allow": []}`: `a tool with settings needs its name as "tool"`,
	} {
		os.WriteFile(filepath.Join(dir, fileName), []byte(`{"tools": {"a2a": [`+entry+`]}}`), 0o600)
		if _, err := Load(dir); err == nil || err.Error() != want {
			t.Errorf("%s: %v", entry, err)
		}
	}
	// An entry that is read anew forgets the settings it had.
	e := Entry{Tool: "read", Settings: json.RawMessage(`{}`)}
	if err := json.Unmarshal([]byte(`"list"`), &e); err != nil || !reflect.DeepEqual(e, Entry{Tool: "list"}) {
		t.Errorf("got %+v, %v", e, err)
	}
	// Settings that are no object are not written.
	if _, err := json.Marshal(Entry{Tool: "read", Settings: json.RawMessage(`[`)}); err == nil || !strings.Contains(err.Error(), "settings of the tool read: ") {
		t.Errorf("broken settings: %v", err)
	}
	if got := Named(); got == nil || len(got) != 0 {
		t.Errorf("Named() = %#v: no tools is not the same as no list", got)
	}
}

// What is protected is the default list until the owner writes one, also an
// empty one.
func TestProtected(t *testing.T) {
	def := DefaultProtected()
	if len(def) != 9 || def[0] != "~/.ssh" || def[8] != "~/.local/share/keyrings" {
		t.Fatalf("the default: %v", def)
	}
	if got := (Config{}).Protected(); !reflect.DeepEqual(got, def) {
		t.Errorf("without tools: %v", got)
	}
	if got := (Config{Tools: &Tools{A2A: Named("read")}}).Protected(); !reflect.DeepEqual(got, def) {
		t.Errorf("a configuration from before: %v", got)
	}
	dir := t.TempDir()
	if err := Save(dir, Config{Tools: &Tools{Protected: &[]string{}}}); err != nil {
		t.Fatal(err)
	}
	c, err := Load(dir)
	if err != nil || c.Tools.Protected == nil || len(c.Protected()) != 0 {
		t.Errorf("the owner's decision to protect nothing is lost: %+v, %v", c.Tools, err)
	}
	mine := []string{"~/x"}
	if got := (Config{Tools: &Tools{Protected: &mine}}).Protected(); !reflect.DeepEqual(got, mine) {
		t.Errorf("the owner's list: %v", got)
	}
	// A list of the wrong type is an error, not the default.
	os.WriteFile(filepath.Join(dir, fileName), []byte(`{"tools": {"protected": "~/.ssh"}}`), 0o600)
	if _, err := Load(dir); err == nil {
		t.Error("a protected that is no list was read")
	}

	// What the setup writes.
	tools := DefaultTools()
	b, _ := json.Marshal(tools)
	want := `{"h2a":["*"],"a2a":[],"protected":["~/.ssh","~/.gnupg","~/.aws","~/.kube","~/.config/gcloud","~/.docker/config.json","~/.netrc","~/.password-store","~/.local/share/keyrings"]}`
	if string(b) != want {
		t.Errorf("the default tools: %s", b)
	}
	(*tools.Protected)[0] = "changed"
	if DefaultProtected()[0] != "~/.ssh" {
		t.Error("the default list can be changed")
	}
}

// The secrets of the node are its configuration and the lock of it.
func TestFiles(t *testing.T) {
	if got := Files("/node"); !reflect.DeepEqual(got, []string{"/node/config.json", "/node/config.json.lock"}) {
		t.Fatalf("got %v", got)
	}
}

// A field is read as it applies, with the default that holds for what is not
// set, and written from JSON of its type.
func TestFields(t *testing.T) {
	if got := Fields(); !reflect.DeepEqual(got, []string{"name", "model", "tools.h2a", "tools.a2a", "tools.protected"}) {
		t.Fatalf("the fields: %v", got)
	}
	c := Config{Name: "alpha", Model: "gpt", BaseURL: "http://x", APIKey: "sk", PrivateKey: "key"}
	protected, _ := json.Marshal(DefaultProtected())
	for path, want := range map[string]string{
		"name": `"alpha"`, "model": `"gpt"`, "tools.h2a": `["*"]`, "tools.a2a": `[]`, "tools.protected": string(protected),
	} {
		if got, err := c.Field(path); err != nil || string(got) != want {
			t.Errorf("%s = %s, %v; want %s", path, got, err, want)
		}
	}
	// What is set is what applies, an empty list too.
	set := Config{Tools: &Tools{H2A: Named(), A2A: []Entry{{Tool: "read", Settings: json.RawMessage(`{"allow": ["~/share"]}`)}}, Protected: &[]string{}}}
	for path, want := range map[string]string{"tools.h2a": `[]`, "tools.a2a": `[{"allow":["~/share"],"tool":"read"}]`, "tools.protected": `[]`} {
		if got, err := set.Field(path); err != nil || string(got) != want {
			t.Errorf("%s = %s, %v; want %s", path, got, err, want)
		}
	}

	for _, tt := range []struct{ path, value, want string }{
		{"name", `"beta"`, `"beta"`},
		{"model", `"gpt-5"`, `"gpt-5"`},
		{"tools.h2a", `["list_peers", {"tool": "read", "deny": ["~/tax"]}]`, `["list_peers",{"deny":["~/tax"],"tool":"read"}]`},
		{"tools.a2a", `["*"]`, `["*"]`},
		{"tools.a2a", `[]`, `[]`},
		{"tools.protected", `["~/x"]`, `["~/x"]`},
		{"tools.protected", `[]`, `[]`},
	} {
		changed := c
		if err := changed.SetField(tt.path, json.RawMessage(tt.value)); err != nil {
			t.Errorf("%s = %s: %v", tt.path, tt.value, err)
			continue
		}
		if got, _ := changed.Field(tt.path); string(got) != tt.want {
			t.Errorf("%s = %s reads as %s", tt.path, tt.value, got)
		}
		// Nothing else changes, and the one it was copied from stays as it was.
		for _, other := range Fields() {
			was, _ := c.Field(other)
			if now, _ := changed.Field(other); other != tt.path && string(now) != string(was) {
				t.Errorf("%s = %s changed %s to %s", tt.path, tt.value, other, now)
			}
		}
		if changed.BaseURL != "http://x" || changed.APIKey != "sk" || changed.PrivateKey != "key" || c.Tools != nil || c.Name != "alpha" {
			t.Errorf("%s = %s changed %+v, or %+v", tt.path, tt.value, changed, c)
		}
	}
	// Setting one list of a copy leaves the lists of the original alone.
	changed := set
	if err := changed.SetField("tools.a2a", json.RawMessage(`[]`)); err != nil || len(set.Tools.A2A) != 1 || len(changed.Tools.A2A) != 0 {
		t.Errorf("the original was changed: %+v, %v", set.Tools, err)
	}
	if err := changed.SetField("name", json.RawMessage(`"x"`)); err != nil || changed.Tools == nil || len(*changed.Tools.Protected) != 0 {
		t.Errorf("the tools are gone: %+v, %v", changed.Tools, err)
	}
	// A node without tools gets none for a new name.
	plain := c
	if err := plain.SetField("name", json.RawMessage(`"x"`)); err != nil || plain.Tools != nil {
		t.Errorf("got %+v, %v", plain.Tools, err)
	}

	for _, tt := range []struct{ path, value, want string }{
		{"name", `""`, "name cannot be empty"},
		{"model", `""`, "model cannot be empty"},
		{"name", `["beta"]`, "name: json: cannot unmarshal array into Go value of type string"},
		{"name", `nope`, "name: invalid character 'o' in literal null (expecting 'u')"},
		{"tools.h2a", `"*"`, "tools.h2a: json: cannot unmarshal string into Go value of type []config.Entry"},
		{"tools.a2a", `[3]`, `tools.a2a: a tool is a name or an object like {"tool": name, …}`},
		{"tools.a2a", `null`, "tools.a2a has to be a list"},
		{"tools.h2a", `null`, "tools.h2a has to be a list"},
		{"tools.protected", `null`, "tools.protected has to be a list"},
		{"tools.protected", `[1]`, "tools.protected: json: cannot unmarshal number into "},
		{"private_key", `"mine"`, "private_key is changed by the owner alone, with ouro start --setup: no proposal changes it"},
		{"api_key", `"mine"`, "api_key is changed by the owner alone, with ouro start --setup: no proposal changes it"},
		{"base_url", `"http://evil"`, "base_url is changed by the owner alone, with ouro start --setup: no proposal changes it"},
		{"access", `"other"`, "access is changed by the owner alone, with ouro start --setup: no proposal changes it"},
		{"tools", `{}`, `the configuration has no field "tools" that can be proposed: there are name, model, tools.h2a, tools.a2a, tools.protected`},
		{"", `1`, `the configuration has no field "" that can be proposed: there are name, model, tools.h2a, tools.a2a, tools.protected`},
		{FieldUnsandboxed, `true`, "rebuild_without_sandbox is set by the owner alone, in config.json: no proposal changes it"},
	} {
		changed := c
		if err := changed.SetField(tt.path, json.RawMessage(tt.value)); err == nil || !strings.HasPrefix(err.Error(), tt.want) || !reflect.DeepEqual(changed, c) {
			t.Errorf("%s = %s: %v, %+v", tt.path, tt.value, err, changed)
		}
	}
	// What cannot be proposed cannot be read as a field either: not the keys.
	for _, path := range []string{"private_key", "access", "api_key", "base_url", "rebuild_without_sandbox", "tools", "nope"} {
		if got, err := c.Field(path); err == nil || got != nil {
			t.Errorf("reading %s: %s, %v", path, got, err)
		}
	}
}

// What the agent gets to see of the configuration: no key, and the defaults
// that hold.
func TestPublic(t *testing.T) {
	id := identity.Generate()
	c := Config{Name: "alpha", Model: "gpt", Access: "a1b2c3d4", BaseURL: "http://x/v1", APIKey: "sk-secret", PrivateKey: id.Private()}
	got := string(c.Public("localhost"))
	if strings.Contains(got, "sk-secret") || strings.Contains(got, id.Private()) || strings.Contains(got, "key") && !strings.Contains(got, "keyrings") ||
		strings.Contains(got, "a1b2c3d4") || strings.Contains(got, "http://x/v1") {
		t.Fatalf("a secret is shown: %s", got)
	}
	var shown struct {
		ID, Name, Model string
		Provider        string
		Tools           struct {
			H2A, A2A  []string
			Protected []string
		}
	}
	if err := json.Unmarshal([]byte(got), &shown); err != nil {
		t.Fatalf("%v: %s", err, got)
	}
	if shown.ID != id.ID() || shown.Name != "alpha" || shown.Model != "gpt" || shown.Provider != "localhost" ||
		!reflect.DeepEqual(shown.Tools.H2A, []string{"*"}) || shown.Tools.A2A == nil || len(shown.Tools.A2A) != 0 ||
		!reflect.DeepEqual(shown.Tools.Protected, DefaultProtected()) {
		t.Fatalf("shown: %s", got)
	}
	if !strings.Contains(got, "\n  \"tools\": {\n    \"h2a\": [\n") {
		t.Fatalf("not written for reading: %s", got)
	}
	if !strings.Contains(got, "\n  \"rebuild_without_sandbox\": false\n") {
		t.Fatalf("whether rebuild may build without a sandbox: %s", got)
	}
	c.Tools = &Tools{H2A: Named("read"), A2A: Named("list_peers"), Protected: &[]string{"~/x"}}
	c.RebuildWithoutSandbox = true
	if got := string(c.Public("")); !strings.Contains(got, `"read"`) || !strings.Contains(got, `"list_peers"`) || !strings.Contains(got, `"~/x"`) || strings.Contains(got, ".ssh") ||
		!strings.Contains(got, `"rebuild_without_sandbox": true`) {
		t.Fatalf("what is set: %s", got)
	}
}
