package session_test

import (
	"context"
	"testing"

	"ouroboros/internal/history"
	"ouroboros/internal/home"
	"ouroboros/internal/openai"
	sessionplugin "ouroboros/internal/plugins/session"
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
