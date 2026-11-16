package history

import (
	"testing"
)

func TestDirectory(t *testing.T) {
	dir := t.TempDir()
	a := Open(dir)
	if _, ok, err := a.Directory("owner/s1"); err != nil || ok {
		t.Fatalf("unset %v, %v", ok, err)
	}
	if err := a.SetDirectory("invalid", "/project"); err == nil {
		t.Fatal("accepted invalid id")
	}
	for id, value := range map[string]string{"owner/s1": "/project", "owner/empty": ""} {
		if err := a.SetDirectory(id, value); err != nil {
			t.Fatal(err)
		}
	}
	a.Close()
	a = Open(dir)
	defer a.Close()
	for id, want := range map[string]string{"owner/s1": "/project", "owner/empty": ""} {
		if got, ok, err := a.Directory(id); err != nil || !ok || got != want {
			t.Fatalf("%s: %q, %v, %v", id, got, ok, err)
		}
	}
	if err := a.Append(Who{Session: "s1"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := a.Delete("owner/s1"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := a.Directory("owner/s1"); err != nil || ok {
		t.Fatalf("deleted %v, %v", ok, err)
	}
}

// The archive keeps which agent a conversation runs as, before it is archived
// too, lists it, and forgets it with the conversation.
func TestAgent(t *testing.T) {
	dir := t.TempDir()
	a := Open(dir)
	if _, ok, err := a.Agent("owner/s1"); err != nil || ok {
		t.Fatalf("unset %v, %v", ok, err)
	}
	if err := a.SetAgent("invalid", "archivist"); err == nil {
		t.Fatal("accepted invalid id")
	}
	if err := a.SetAgent("owner/s1", "archivist"); err != nil {
		t.Fatal(err)
	}
	a.Close()
	a = Open(dir)
	defer a.Close()
	if got, ok, err := a.Agent("owner/s1"); err != nil || !ok || got != "archivist" {
		t.Fatalf("kept %q, %v, %v", got, ok, err)
	}
	if err := a.Append(Who{Session: "s2"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	entries, err := a.List()
	if err != nil {
		t.Fatal(err)
	}
	agents := map[string]string{}
	for _, e := range entries {
		agents[e.ID] = e.Agent
	}
	if agents["owner/s1"] != "archivist" || agents["owner/s2"] != "" {
		t.Fatalf("listed %v", agents)
	}
	if err := a.Append(Who{Session: "s1"}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := a.Delete("owner/s1"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := a.Agent("owner/s1"); err != nil || ok {
		t.Fatalf("deleted %v, %v", ok, err)
	}
}
