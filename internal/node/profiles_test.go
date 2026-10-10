package node

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ouroboros/internal/a2a"
	"ouroboros/internal/discovery"
	"ouroboros/internal/home"
)

// profiled returns the node Terra, with the profile Luna besides, each
// answering peers with its name.
func profiled(t *testing.T) (s Server, terra, luna home.Dir) {
	t.Helper()
	terra = configured(t)
	t.Cleanup(func() { os.RemoveAll(string(terra) + "-workspaces") })
	cfg, _ := terra.Config()
	cfg.Name = "Terra"
	terra.SaveConfig(cfg)
	luna, err := terra.AddProfile("Luna")
	if err != nil {
		t.Fatal(err)
	}
	answer := func(dir home.Dir, name string) Server {
		s := testServer(dir)
		s.Responder = func(func(string, ...any)) (a2a.Responder, error) {
			return func(context.Context, string, string) (string, error) { return name + " answers", nil }, nil
		}
		s.Local = hello(name)
		return s
	}
	s = answer(terra, "Terra")
	s.DiscoPort = freeUDPPort(t)
	s.Profiles = []Server{answer(luna, "Luna")}
	return s, terra, luna
}

// The profiles of a node answer on one port, each with its own key and card,
// and each is discovered. A peer reaches the one it names; one that names
// none, as an ouro from before there were profiles does, reaches the first.
func TestProfilesShareThePort(t *testing.T) {
	s, terra, luna := profiled(t)
	var stopped atomic.Int32
	s.OnStop = func() { stopped.Add(1) }
	s.Profiles[0].OnStop = func() { stopped.Add(1) }
	p, stop := run(t, s)
	for dir, name := range map[home.Dir]string{terra: "Terra", luna: "Luna"} {
		if card := fetchCard(t, dir, p.Port); card.Name != name {
			t.Fatalf("the card of %s says %q", name, card.Name)
		}
	}
	base := fmt.Sprintf("https://127.0.0.1:%d/", p.Port)
	terraID, _ := terra.Identity()
	lunaID, _ := luna.Identity()
	if key, err := a2a.FetchKey(context.Background(), base); err != nil || key != terraID.Public() {
		t.Fatalf("the node of the address is %q, %v", key, err)
	}
	nodes, err := discovery.Discover(context.Background(), s.DiscoPort, 300*time.Millisecond)
	if err != nil || len(nodes) != 2 {
		t.Fatalf("discovered %+v, %v", nodes, err)
	}
	for _, n := range nodes {
		if n.Port != p.Port || n.ID != terraID.ID() && n.ID != lunaID.ID() || (n.ID == lunaID.ID()) != (n.Name == "Luna") {
			t.Fatalf("discovered %+v", n)
		}
	}

	// A profile is no more trusted by another than any peer: Terra asks
	// Luna to connect, and is answered once Luna's owner agreed.
	client := &a2a.Client{Identity: terraID, Name: "Terra", Port: p.Port}
	if _, err := client.Send(context.Background(), base, lunaID.Public(), "c", "hi"); !a2a.IsPending(err) {
		t.Fatalf("Terra reached Luna without her owner: %v", err)
	}
	if data, _ := luna.Contacts().Snapshot(); len(data.Incoming) != 1 {
		t.Fatalf("Luna was asked %+v", data)
	}
	if data, _ := terra.Contacts().Snapshot(); len(data.Incoming) != 0 {
		t.Fatalf("Terra was asked %+v", data)
	}
	if _, _, err := luna.Contacts().Approve(terraID.Public()); err != nil {
		t.Fatal(err)
	}
	if reply, err := client.Send(context.Background(), base, lunaID.Public(), "c", "hi"); err != nil || reply != "Luna answers" {
		t.Fatalf("Luna answered %q, %v", reply, err)
	}
	// What Luna trusts, Terra does not: a third node is still a stranger
	// to Terra after Luna let Terra in.
	lunaClient := &a2a.Client{Identity: lunaID, Name: "Luna", Port: p.Port}
	if _, err := lunaClient.Send(context.Background(), base, terraID.Public(), "c", "hi"); !a2a.IsPending(err) {
		t.Fatalf("Luna reached Terra without his owner: %v", err)
	}
	// A key no profile holds is answered by none of them.
	if _, err := a2a.FetchCard(context.Background(), base, "nobody"); !errors.Is(err, a2a.ErrUnverified) {
		t.Fatalf("a card of nobody: %v", err)
	}

	// The front ends name the profile they are for on the socket.
	if got := get(t, s.Home.Socket()); got != "hello from Terra" {
		t.Fatalf("the first one: %q", got)
	}
	for id, want := range map[string]string{terraID.ID(): "hello from Terra", lunaID.ID(): "hello from Luna", "0123456789ab": "the node runs no profile 0123456789ab"} {
		req, _ := http.NewRequest(http.MethodGet, "http://ouro/", nil)
		req.Header.Set(ProfileHeader, id)
		resp, err := local(s.Home.Socket()).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b := make([]byte, 100)
		n, _ := resp.Body.Read(b)
		resp.Body.Close()
		if !strings.Contains(string(b[:n]), want) {
			t.Fatalf("%s: %q", id, b[:n])
		}
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if stopped.Load() != 2 {
		t.Fatalf("%d profiles were told the node stops", stopped.Load())
	}
}

// A profile without a handler for its front ends serves none.
func TestProfileWithoutSocket(t *testing.T) {
	s, _, luna := profiled(t)
	s.Profiles[0].Local = nil
	run(t, s)
	id, _ := luna.Identity()
	req, _ := http.NewRequest(http.MethodGet, "http://ouro/", nil)
	req.Header.Set(ProfileHeader, id.ID())
	resp, err := local(s.Home.Socket()).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

// A node whose profile cannot be run does not start.
func TestProfileThatCannotRun(t *testing.T) {
	for name, spoil := range map[string]func(dir home.Dir){
		"configuration": func(dir home.Dir) { os.WriteFile(filepath.Join(string(dir), "config.json"), []byte("{"), 0o600) },
		"key": func(dir home.Dir) {
			os.WriteFile(filepath.Join(string(dir), "config.json"), []byte(`{"private_key":"nonsense"}`), 0o600)
		},
		"answers": func(home.Dir) {},
	} {
		s, _, luna := profiled(t)
		spoil(luna)
		if name == "answers" {
			s.Profiles[0].Responder = func(func(string, ...any)) (a2a.Responder, error) { return nil, errors.New("no model") }
		}
		if err := s.Run(context.Background()); err == nil {
			t.Errorf("a profile without its %s should fail", name)
		}
		if _, ok := s.Home.Service().Running(); ok {
			t.Errorf("a node whose profile has no %s runs", name)
		}
	}
}
