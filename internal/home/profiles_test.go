package home

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ouroboros/internal/config"
	"ouroboros/internal/credentials"
	"ouroboros/internal/history"
	"ouroboros/internal/openai"
)

// node returns the directory of a node that is set up, called Terra, with
// the access LegacyAccess.
func node(t *testing.T) Dir {
	t.Helper()
	root := Dir(filepath.Join(t.TempDir(), "ouroboros"))
	t.Cleanup(func() { os.RemoveAll(string(root) + "-workspaces") })
	if err := root.SaveConfig(config.Config{Name: "Terra", BaseURL: "https://api.example/v1", APIKey: "secret", Model: "m", Description: "existing"}); err != nil {
		t.Fatal(err)
	}
	if _, err := root.Config(); err != nil { // moves the key into the credentials
		t.Fatal(err)
	}
	return root
}

// A profile that is added keeps what the node had where it was, and has its
// own identity and state besides: only the access and the model are those
// of the first one, and the process and the accesses are shared.
func TestAddProfile(t *testing.T) {
	root := node(t)
	terra, _ := root.Identity()
	archive := history.Open(root.History())
	archive.Append(history.Who{Session: "old"}, []openai.Message{{Role: "user", Content: "kept"}}, nil)
	archive.SetTitle(history.Owner("old"), "The conversation")
	before, _ := os.ReadFile(filepath.Join(string(root), "config.json"))

	luna, err := root.AddProfile("  Luna ")
	if err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(filepath.Join(string(root), "config.json")); string(before) != string(after) {
		t.Fatal("the configuration of the node changed")
	}
	if title, _ := archive.Title(history.Owner("old")); title != "The conversation" {
		t.Fatalf("title %q", title)
	}
	cfg, _ := luna.Config()
	id, _ := luna.Identity()
	if id.ID() == terra.ID() || cfg.Name != "Luna" || cfg.Description != "" || cfg.Tools != nil || cfg.Access != LegacyAccess || cfg.Model != "m" {
		t.Fatalf("the profile %+v", cfg)
	}
	if luna != Dir(filepath.Join(string(root), "profiles", id.ID())) || luna.Root() != root || root.Root() != root {
		t.Fatalf("the profile is in %s", luna)
	}
	if luna.Socket() != root.Socket() || luna.Service().PIDFile != root.Service().PIDFile || luna.Models() != root.Models() {
		t.Fatal("the process is not shared")
	}
	if _, err := luna.Credentials().Get(LegacyAccess); err != nil {
		t.Fatalf("the accesses are not shared: %v", err)
	}
	if luna.History() == root.History() || luna.UpdateDir() == root.UpdateDir() {
		t.Fatal("the state is shared")
	}
	if data, err := luna.Contacts().Snapshot(); err != nil || len(data.Incoming)+len(data.Outgoing)+len(data.Trusted) != 0 {
		t.Fatalf("contacts %+v, %v", data, err)
	}
	if entries, _ := os.ReadDir(filepath.Join(string(root), "profiles")); len(entries) != 1 {
		t.Fatalf("left behind: %v", entries)
	}
	if profiles, err := root.Profiles(); err != nil || !slices.Equal(profiles, []Dir{root, luna}) {
		t.Fatalf("profiles %v, %v", profiles, err)
	}
	if again, _ := luna.Identity(); again.ID() != id.ID() {
		t.Fatal("the identity changed")
	}
}

// What is not a profile is not listed: a file, a directory of another name
// and a profile on its way.
func TestProfiles(t *testing.T) {
	root := node(t)
	if profiles, err := root.Profiles(); err != nil || !slices.Equal(profiles, []Dir{root}) {
		t.Fatalf("without others %v, %v", profiles, err)
	}
	dir := filepath.Join(string(root), "profiles")
	for _, name := range []string{"notes", ".new-0123456789ab", "0123456789AB", "0123456789abc", "ba9876543210"} {
		os.MkdirAll(filepath.Join(dir, name), 0o700)
	}
	os.WriteFile(filepath.Join(dir, "0123456789ab"), nil, 0o600)
	if profiles, err := root.Profiles(); err != nil || !slices.Equal(profiles, []Dir{root, Dir(filepath.Join(dir, "ba9876543210"))}) {
		t.Fatalf("profiles %v, %v", profiles, err)
	}
	// A directory of profiles that is a file cannot be read.
	other := node(t)
	os.WriteFile(filepath.Join(string(other), "profiles"), nil, 0o600)
	if _, err := other.Profiles(); err == nil {
		t.Fatal("a file should fail")
	}
	// A directory that is named like a profile, outside a node, is no
	// profile.
	if d := Dir(filepath.Join(t.TempDir(), "elsewhere", "0123456789ab")); d.Root() != d {
		t.Fatalf("root of %s is %s", d, d.Root())
	}
}

// A profile needs a name of one line that no other profile has, whatever the
// case of its letters, and a node that is set up.
func TestAddProfileFails(t *testing.T) {
	root := node(t)
	luna, _ := root.AddProfile("Luna")
	id, _ := luna.Identity()
	for _, name := range []string{"", "  ", "a\nb", "\x1b[31mred", "luna", "TERRA", id.ID()} {
		if _, err := root.AddProfile(name); err == nil {
			t.Errorf("%q should fail", name)
		}
	}
	if _, err := Dir(t.TempDir()).AddProfile("Luna"); !errors.Is(err, config.ErrIncomplete) {
		t.Errorf("a node that is not set up: %v", err)
	}
	broken := Dir(t.TempDir())
	os.WriteFile(filepath.Join(string(broken), "config.json"), []byte("{"), 0o600)
	if _, err := broken.AddProfile("Luna"); err == nil {
		t.Error("a configuration that cannot be read should fail")
	}

	// What cannot be locked, read or written leaves no profile behind.
	locked := node(t)
	os.Mkdir(filepath.Join(string(locked), "profiles.lock"), 0o700)
	if _, err := locked.AddProfile("Luna"); err == nil {
		t.Error("a lock that cannot be taken should fail")
	}
	unreadable := node(t)
	sol, _ := unreadable.AddProfile("Sol")
	os.WriteFile(filepath.Join(string(sol), "config.json"), []byte("{"), 0o600)
	if _, err := unreadable.AddProfile("Luna"); err == nil {
		t.Error("a profile that cannot be read should fail")
	}
	full := node(t)
	full.AddProfile("Sol")
	dir := filepath.Join(string(full), "profiles")
	os.Chmod(dir, 0o500)
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
	if _, err := full.AddProfile("Luna"); err == nil {
		t.Error("a profile that cannot be written should fail")
	}
	os.Chmod(dir, 0o700)
	moved := node(t)
	rename = func(string, string) error { return errors.New("no room") }
	t.Cleanup(func() { rename = os.Rename })
	if _, err := moved.AddProfile("Luna"); err == nil || err.Error() != "no room" {
		t.Errorf("a profile that cannot be moved into place: %v", err)
	}
	if profiles, _ := moved.Profiles(); len(profiles) != 1 {
		t.Errorf("left behind: %v", profiles)
	}
	if entries, _ := os.ReadDir(filepath.Join(string(moved), "profiles")); len(entries) != 0 {
		t.Errorf("left behind: %v", entries)
	}
}

// A profile is renamed to a name no other one has.
func TestRenameProfile(t *testing.T) {
	root := node(t)
	luna, _ := root.AddProfile("Luna")
	if err := luna.RenameProfile(" luna "); err != nil {
		t.Fatal(err)
	}
	if err := root.RenameProfile("Gaia"); err != nil {
		t.Fatal(err)
	}
	if cfg, _ := luna.Config(); cfg.Name != "luna" {
		t.Fatalf("renamed to %q", cfg.Name)
	}
	if cfg, _ := root.Config(); cfg.Name != "Gaia" || cfg.Model != "m" {
		t.Fatalf("the node %+v", cfg)
	}
	for _, name := range []string{"", "gaia"} {
		if err := luna.RenameProfile(name); err == nil {
			t.Errorf("%q should fail", name)
		}
	}
	os.Remove(filepath.Join(string(root), "profiles.lock"))
	os.Mkdir(filepath.Join(string(root), "profiles.lock"), 0o700)
	if err := luna.RenameProfile("Sol"); err == nil {
		t.Error("a lock that cannot be taken should fail")
	}
	unlisted := node(t)
	os.WriteFile(filepath.Join(string(unlisted), "profiles"), nil, 0o600)
	if err := unlisted.RenameProfile("Gaia"); err == nil {
		t.Error("profiles that cannot be listed should fail")
	}
	other := node(t)
	sol, _ := other.AddProfile("Sol")
	os.Chmod(string(sol), 0o500)
	t.Cleanup(func() { os.Chmod(string(sol), 0o700) })
	if err := sol.RenameProfile("Helios"); err == nil {
		t.Error("a configuration that cannot be written should fail")
	}
}

// A profile is selected by its ID, its name or the start of its ID that no
// other has.
func TestSelectProfile(t *testing.T) {
	root := node(t)
	terra, _ := root.Identity()
	var profiles []Dir
	for _, name := range []string{"Luna", "Sol", "Helios", "Gaia", "Mars", "Venus", "Ceres"} {
		p, _ := root.AddProfile(name)
		profiles = append(profiles, p)
	}
	luna := profiles[0]
	id, _ := luna.Identity()
	for _, target := range []string{"Luna", "luna", id.ID(), id.ID()[:10]} {
		if got, err := root.SelectProfile(target); err != nil || got != luna {
			t.Errorf("%s: %s, %v", target, got, err)
		}
	}
	if got, err := luna.SelectProfile(terra.ID()); err != nil || got != root {
		t.Errorf("the first one: %s, %v", got, err)
	}
	if _, err := root.SelectProfile("Pluto"); err == nil || !strings.Contains(err.Error(), `the node has no profile "Pluto"`) {
		t.Errorf("none: %v", err)
	}
	if _, err := root.SelectProfile(""); err == nil {
		t.Error("an empty name should fail")
	}
	// Of 17 IDs, two start with the same hex digit at the latest.
	starts := map[byte]int{}
	shared := byte(0)
	for i := 0; shared == 0; i++ {
		p := root
		if i < len(profiles) {
			p = profiles[i]
		} else if i > len(profiles) {
			p, _ = root.AddProfile(fmt.Sprint("Moon ", i))
		}
		c, _ := p.Config()
		if starts[c.ID()[0]]++; starts[c.ID()[0]] == 2 {
			shared = c.ID()[0]
		}
	}
	if _, err := root.SelectProfile(string(shared)); err == nil || !strings.Contains(err.Error(), "starts the ID of several profiles") {
		t.Errorf("%c starts several IDs: %v", shared, err)
	}
	os.WriteFile(filepath.Join(string(profiles[1]), "config.json"), []byte("{"), 0o600)
	if _, err := root.SelectProfile("Pluto"); err == nil || strings.Contains(err.Error(), "no profile") {
		t.Errorf("a profile that cannot be read: %v", err)
	}
	os.RemoveAll(filepath.Join(string(root), "profiles"))
	os.WriteFile(filepath.Join(string(root), "profiles"), nil, 0o600)
	if _, err := root.SelectProfile("Luna"); err == nil {
		t.Error("profiles that cannot be listed should fail")
	}
}

// Each profile works in a workspace of its own next to the node, however
// many there are; the agent of one does not see what the others keep.
func TestWorkspacesAndSecrets(t *testing.T) {
	root := node(t)
	if root.WorkingDir() != "" {
		t.Fatal("a node without a key has a workspace")
	}
	terraID, _ := root.Identity()
	workspaces := string(root) + "-workspaces"
	if w := root.WorkingDir(); w != filepath.Join(workspaces, terraID.ID()) {
		t.Fatalf("workspace of the only profile %s", w)
	}
	shared := append(config.Files(string(root)), credentials.Files(string(root))...)
	if got := root.Secrets(); !slices.Equal(got, shared) {
		t.Fatalf("secrets of one profile %v", got)
	}
	luna, _ := root.AddProfile("Luna")
	lunaID, _ := luna.Identity()
	if w := root.WorkingDir(); w != filepath.Join(workspaces, terraID.ID()) {
		t.Fatalf("workspace %s", w)
	}
	if w := luna.WorkingDir(); w != filepath.Join(workspaces, lunaID.ID()) {
		t.Fatalf("workspace %s", w)
	}
	if got := root.Secrets(); !slices.Equal(got, append(slices.Clone(shared), string(luna), filepath.Join(workspaces, lunaID.ID()))) {
		t.Fatalf("secrets of the first one %v", got)
	}
	want := append(slices.Clone(shared), filepath.Join(string(root), "contacts.json"), filepath.Join(string(root), "proposals.json"),
		filepath.Join(string(root), "failures.json"), root.History(), root.UpdateDir(), filepath.Join(workspaces, terraID.ID()))
	want = append(want, config.Files(string(luna))...)
	if got := luna.Secrets(); !slices.Equal(got, want) {
		t.Fatalf("secrets of another one\n%v\nwant\n%v", got, want)
	}
	// A profile without an identity has no workspace to keep from the
	// others, and works where it did.
	os.WriteFile(filepath.Join(string(luna), "config.json"), []byte(`{"private_key":"nonsense"}`), 0o600)
	if got := root.Secrets(); !slices.Equal(got, append(slices.Clone(shared), string(luna))) {
		t.Fatalf("secrets %v", got)
	}
	if luna.WorkingDir() != "" {
		t.Fatal("a profile without an identity has a workspace")
	}
	os.WriteFile(filepath.Join(string(luna), "config.json"), []byte(`{`), 0o600)
	if luna.WorkingDir() != "" {
		t.Fatal("a profile whose configuration cannot be read has a workspace")
	}
}
