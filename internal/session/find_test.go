package session

import (
	"context"
	"strings"
	"testing"
	"time"

	"ouroboros/internal/history"
	"ouroboros/internal/openai"
)

// Find gives the sessions whose content holds what is looked for, named as
// List and Peers name them: a session of the owner by its ID, a conversation
// of a peer by its party and ID.
func TestFind(t *testing.T) {
	dir := t.TempDir()
	a := history.Open(dir)
	if err := a.Append(history.Who{Session: "s1"}, []openai.Message{{Role: "user", Content: "the mesh of peers"}}, nil); err != nil {
		t.Fatal(err)
	}
	if err := a.Append(history.Who{Session: "c", Peer: "key"}, []openai.Message{{Role: "user", Content: "mesh too"}}, nil); err != nil {
		t.Fatal(err)
	}
	s, _, _ := archived(t, &fakeAgent{}, dir, time.Now())
	got, err := s.Find(context.Background(), "mesh")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, " ") != "peer-2c70e12b7a0646f92279f427c7b38e73/c s1" { // the peer by its party, the owner by its ID
		t.Fatalf("found %q", got)
	}
	if got, _ := s.Find(context.Background(), "never said"); len(got) != 0 {
		t.Fatalf("found %q", got)
	}
}
