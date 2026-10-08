package node

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ouroboros/internal/config"
	"ouroboros/internal/daemon"
	"ouroboros/internal/home"
)

// The profiles of a node are listed, added while it is stopped and renamed.
func TestProfileCommand(t *testing.T) {
	dir := home.Dir(filepath.Join(t.TempDir(), "node"))
	t.Cleanup(func() { os.RemoveAll(string(dir) + "-workspaces") })
	dir.SaveConfig(config.Config{BaseURL: "http://localhost:1", Model: "gpt", Name: "Terra"})
	c := commands(dir, nil)
	out := c.term.Out.(*bytes.Buffer)
	ctx := context.Background()
	if err := c.profile(ctx, []string{"add", "Luna"}); err != nil {
		t.Fatal(err)
	}
	luna, err := dir.SelectProfile("Luna")
	if err != nil {
		t.Fatal(err)
	}
	lunaID, _ := luna.Identity()
	if got := out.String(); got != "+ profile "+lunaID.ID()+" (Luna), with the access and the model of the first one\n" {
		t.Fatalf("added: %q", got)
	}
	out.Reset()
	if err := c.profile(ctx, []string{"rename", "luna", "Selene"}); err != nil {
		t.Fatal(err)
	}
	// Any profile lists them all.
	lister := commands(luna, nil)
	if err := lister.profile(ctx, nil); err != nil {
		t.Fatal(err)
	}
	terraID, _ := dir.Identity()
	if got := lister.term.Out.(*bytes.Buffer).String(); got != terraID.ID()+"  Terra  model=gpt\n"+lunaID.ID()+"  Selene  model=gpt\n" {
		t.Fatalf("listed %q", got)
	}

	for _, args := range [][]string{{"add"}, {"remove", "Selene"}, {"rename", "Selene"}, {"add", "Terra"}, {"rename", "Mars", "Venus"}} {
		if err := c.profile(ctx, args); err == nil {
			t.Errorf("%v should fail", args)
		}
	}
	lease, err := daemon.Hold(dir.Service().PIDFile, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	if err := c.profile(ctx, []string{"add", "Sol"}); err == nil || !strings.Contains(err.Error(), "the node runs: stop it first") {
		t.Fatalf("added while it runs: %v", err)
	}
}

// What cannot be read cannot be listed.
func TestProfileCommandReportsErrors(t *testing.T) {
	dir := home.Dir(filepath.Join(t.TempDir(), "node"))
	dir.SaveConfig(config.Config{BaseURL: "http://localhost:1", Model: "gpt", Name: "Terra"})
	os.WriteFile(filepath.Join(string(dir), "profiles"), nil, 0o600)
	if err := commands(dir, nil).profile(context.Background(), nil); err == nil {
		t.Error("profiles that cannot be listed should fail")
	}
	os.Remove(filepath.Join(string(dir), "profiles"))
	os.WriteFile(filepath.Join(string(dir), "config.json"), []byte(`{"private_key":"nonsense"}`), 0o600)
	if err := commands(dir, nil).profile(context.Background(), nil); err == nil {
		t.Error("a profile without a key should fail")
	}
	os.WriteFile(filepath.Join(string(dir), "config.json"), []byte("{"), 0o600)
	if err := commands(dir, nil).profile(context.Background(), nil); err == nil {
		t.Error("a profile that cannot be read should fail")
	}
}
