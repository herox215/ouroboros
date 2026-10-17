package session_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"ouroboros/internal/assistant"
	"ouroboros/internal/history"
	"ouroboros/internal/home"
	"ouroboros/internal/kernel"
	"ouroboros/internal/openai"
	sessionplugin "ouroboros/internal/plugins/session"
	"ouroboros/internal/session"
	"ouroboros/plugin"
)

// The conversations of peers in the archive of the node are named after the
// contacts; a peer that is no contact by the start of its party.
func TestPeersAreNamed(t *testing.T) {
	dir := home.Dir(t.TempDir())
	dir.Contacts().AddTrusted("key of beta", "beta", "gpt")
	archive := history.Open(dir.History())
	hello := []openai.Message{{Role: "user", Content: "hello"}}
	archive.Append(history.Who{Session: "c1", Peer: "key of beta"}, hello, nil)
	archive.Append(history.Who{Session: "c2", Peer: "key of a stranger"}, hello, nil)
	s := sessionplugin.In(dir, nil)
	t.Cleanup(s.Close)
	list, err := s.Peers(context.Background())
	if err != nil || len(list) != 2 {
		t.Fatalf("peers %+v, %v", list, err)
	}
	names := map[string]string{}
	for _, i := range list {
		names[i.ID] = i.Peer
	}
	stranger := history.Party("key of a stranger")
	if names[history.Party("key of beta")+"/c1"] != "beta" || names[stranger+"/c2"] != "unknown peer "+stranger[5:13] {
		t.Fatalf("names %v", names)
	}
}

// The plugin provides the sessions of the node, with the services the other
// plugins contribute, and a client of its socket; it closes the sessions
// when it stops.
func TestPlugin(t *testing.T) {
	dir := home.Dir(t.TempDir())
	type provided struct{ Assistant *assistant.Assistant }
	provider := plugin.NewService("assistant", func(*plugin.Context, struct{}) (provided, error) {
		return provided{assistant.New(nil, assistant.Builder{}, nil)}, nil
	})
	since := time.Date(2026, 10, 17, 3, 0, 0, 0, time.UTC)
	server := plugin.New("server", func(ctx *plugin.Context, _ struct{}) error {
		plugin.Add(ctx, plugin.Services{List: func() []session.ServiceInfo {
			return []session.ServiceInfo{{ID: "s1", Name: "server", Since: since}}
		}})
		return nil
	})
	k := kernel.New()
	if err := k.Start(provider, sessionplugin.Plugin(dir), server); err != nil {
		t.Fatal(err)
	}
	defer k.Stop()
	s, ok := kernel.Service[*session.Service](k)
	if !ok {
		t.Fatal("no sessions")
	}
	if client, ok := kernel.Service[plugin.Sessions](k); !ok || client == nil {
		t.Fatal("no client of the socket")
	}
	if s.Workspace != dir.WorkingDir() {
		t.Errorf("workspace %q", s.Workspace)
	}
	// A plugin started after the sessions contributes all the same.
	if list, err := s.Services(context.Background()); err != nil || len(list) != 1 || list[0].ID != "s1" {
		t.Fatalf("services %+v, %v", list, err)
	}
	k.Stop()
	if _, err := s.New(context.Background()); !errors.Is(err, session.ErrStopped) {
		t.Fatalf("a new session after the stop: %v", err)
	}
}
