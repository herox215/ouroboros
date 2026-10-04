package example_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ouroboros/internal/cli/clitest"
	"ouroboros/internal/config"
	"ouroboros/plugin"
	"ouroboros/plugin/example"
)

// Two nodes with the plugin: the one sends a file through the channel of the
// other, in pieces that together are more than a request may carry, and
// hears how much has arrived and with which checksum.
func TestChecksumAFile(t *testing.T) {
	beta := clitest.Settings(t)
	cfg := clitest.NodeConfig(beta)
	cfg.Name = "beta"
	clitest.StartNode(t, beta, cfg, example.Plugin())
	// A node whose plugin has no room for files.
	gamma := clitest.Settings(t)
	cfg.Name = "gamma"
	clitest.StartNode(t, gamma, cfg, plugin.New("full", func(ctx *plugin.Context, _ struct{}) error {
		plugin.Add(ctx, plugin.ChannelHandler{Name: example.Channel, Handle: func(_ context.Context, ch *plugin.Channel) {
			ch.CloseWith("no room")
		}})
		return nil
	}))

	alpha := clitest.Settings(t)
	clitest.Finds(alpha, beta, gamma)
	if err := config.Save(alpha.Home, clitest.NodeConfig(alpha)); err != nil {
		t.Fatal(err)
	}
	checksum := func(args ...string) (string, error) {
		t.Helper()
		app, out := clitest.NewApp(t, alpha, "", example.Plugin())
		err := app.Run(context.Background(), append([]string{"checksum"}, args...))
		return out.String(), err
	}
	content := bytes.Repeat([]byte("ouroboros "), 150_000) // 1.5 MB
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, content, 0o600); err != nil {
		t.Fatal(err)
	}

	// Alpha is not connected to beta: nothing is sent.
	if _, err := checksum("beta", file); !errors.Is(err, plugin.ErrNotConnected) {
		t.Fatalf("not connected: %v", err)
	}
	// Alpha's owner trusts beta, but beta's owner does not trust alpha: beta
	// refuses the channel before the plugin gets it.
	clitest.Trust(t, alpha, beta, "beta")
	var refused *plugin.Error
	if _, err := checksum("beta", file); !errors.As(err, &refused) || refused.Code != plugin.CodeUnauthorized {
		t.Fatalf("not trusted by the peer: %v", err)
	}

	clitest.Trust(t, beta, alpha, "alpha")
	out, err := checksum("beta", file)
	if err != nil {
		t.Fatal(err)
	}
	want := ""
	for arrived := 0; arrived < len(content); {
		arrived = min(arrived+256<<10, len(content))
		want += fmt.Sprintf("beta: %d bytes arrived\n", arrived)
	}
	want += fmt.Sprintf("beta: sha256 %x\n", sha256.Sum256(content))
	if out != want || strings.Count(out, "\n") != 7 {
		t.Fatalf("got:\n%s\nwant:\n%s", out, want)
	}
	// A file may be empty.
	empty := filepath.Join(t.TempDir(), "empty")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := checksum("beta", empty); err != nil || out != fmt.Sprintf("beta: sha256 %x\n", sha256.Sum256(nil)) {
		t.Fatalf("an empty file: %q, %v", out, err)
	}

	// What the peer closes the channel with is the error.
	clitest.Trust(t, alpha, gamma, "gamma")
	clitest.Trust(t, gamma, alpha, "alpha")
	var closed *plugin.ChannelClosed
	if _, err := checksum("gamma", file); !errors.As(err, &closed) || closed.Reason != "no room" {
		t.Fatalf("a peer without room: %v", err)
	}

	if _, err := checksum("beta"); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("without a file: %v", err)
	}
	if _, err := checksum("nobody", file); err == nil || !strings.Contains(err.Error(), `no node "nobody"`) {
		t.Fatalf("unknown node: %v", err)
	}
	if _, err := checksum("beta", filepath.Join(t.TempDir(), "missing")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a file that is not there: %v", err)
	}
	if _, err := checksum("beta", t.TempDir()); err == nil {
		t.Fatal("a directory is no file to send")
	}
}
