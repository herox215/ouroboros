package agents

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestBuiltinValidAndOf(t *testing.T) {
	for _, id := range []string{Thread, Archivist, Orchestrator} {
		if !Builtin(id) || !Valid(id) || Of(id) != id {
			t.Fatalf("%q is not an agent", id)
		}
	}
	if !Valid("agent-0a1b2c3d") || Builtin("agent-0a1b2c3d") {
		t.Fatal("the ID of an agent of the owner")
	}
	for _, id := range []string{"", "helper", "agent-", "agent-0A1B2C3D", "agent-0a1b2c3", "agent-0a1b2c3x", "agent-0a1b2c3d4"} {
		if Valid(id) {
			t.Fatalf("%q is valid", id)
		}
	}
	if Of("") != Thread {
		t.Fatalf("none is %q", Of(""))
	}
	if a := (Agent{Kind: Archivist}); !strings.Contains(a.About(), "memories") {
		t.Fatalf("the archivist is for %q", a.About())
	}
	// What the archivist is for is fixed; the agent of the thread is for what
	// the owner says.
	if a := (Agent{Kind: Archivist, Description: "Tidies."}); !strings.Contains(a.About(), "memories") {
		t.Fatalf("what the owner wrote of the archivist: %q", a.About())
	}
	if a := (Agent{Kind: Thread, Description: "The thread alone."}); a.About() != "The thread alone." {
		t.Fatalf("what the owner wrote: %q", a.About())
	}
	if !strings.Contains((Agent{Kind: Thread}).About(), "personal agent") {
		t.Fatal("the agent of the thread is for nothing")
	}
	for id, reserved := range map[string]bool{Thread: false, Archivist: true, Orchestrator: true, "agent-0a1b2c3d": false} {
		if Reserved(id) != reserved {
			t.Fatalf("%s reserved: %v", id, Reserved(id))
		}
	}
	if a := (Agent{Kind: Custom}); a.About() != "" {
		t.Fatalf("an agent of the owner without a description: %q", a.About())
	}
}

func TestDefaults(t *testing.T) {
	s := Open(filepath.Join(t.TempDir(), "agents.json"))
	list, err := s.List("pythia")
	want := []Agent{{ID: Thread, Kind: Thread, Name: "pythia"}, {ID: Archivist, Kind: Archivist, Name: "Archivist", Reserved: true}, {ID: Orchestrator, Kind: Orchestrator, Name: "Orchestrator", Reserved: true}}
	if err != nil || !slices.Equal(list, want) {
		t.Fatalf("the agents: %+v, %v", list, err)
	}
	if a, err := s.Get(Thread, "pythia"); err != nil || a != want[0] {
		t.Fatalf("the agent of the thread: %+v, %v", a, err)
	}
	if _, err := s.Get("helper", "pythia"); !errors.Is(err, ErrUnknown) {
		t.Fatalf("no agent: %v", err)
	}
	var none *Store
	if list, err := none.List("pythia"); err != nil || !slices.Equal(list, want) {
		t.Fatalf("the agents of no store: %+v, %v", list, err)
	}
	if err := none.Set(Archivist, Agent{Name: "Mnemosyne"}); err == nil {
		t.Fatal("no store renamed an agent")
	}
	if _, err := none.Create(Agent{Name: "Researcher"}); err == nil {
		t.Fatal("no store made an agent")
	}
}

func TestSet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agents.json")
	s := Open(path)
	if err := s.Set(Archivist, Agent{Name: "  Mnemosyne ", Soul: " Careful.\r\n\tNever hasty. "}, "pythia"); err != nil {
		t.Fatal(err)
	}
	if a, err := Open(path).Get(Archivist, "pythia"); err != nil || a != (Agent{ID: Archivist, Kind: Archivist, Name: "Mnemosyne", Soul: "Careful.\n\tNever hasty.", Reserved: true}) {
		t.Fatalf("the file keeps %+v: %v", a, err)
	}
	// What a reserved agent is for is fixed.
	for _, id := range []string{Archivist, Orchestrator} {
		if err := s.Set(id, Agent{Name: "Mnemosyne", Description: "Does tasks."}, "pythia"); !errors.Is(err, ErrReserved) {
			t.Fatalf("what %s is for was changed: %v", id, err)
		}
	}
	if err := s.Set(Thread, Agent{Description: "The thread alone, no sessions."}, "pythia"); err != nil {
		t.Fatal(err)
	}
	if a, err := s.Get(Thread, "pythia"); err != nil || a.Description != "The thread alone, no sessions." {
		t.Fatalf("what the agent of the thread is for: %+v, %v", a, err)
	}
	// The agent of the thread keeps its description and soul here, and its
	// name with the profile.
	if err := s.Set(Thread, Agent{Name: "ignored", Soul: "Warm."}, "pythia"); err != nil {
		t.Fatal(err)
	}
	if a, err := s.Get(Thread, "pythia"); err != nil || a.Name != "pythia" || a.Soul != "Warm." {
		t.Fatalf("the agent of the thread: %+v, %v", a, err)
	}
	for _, refused := range []struct {
		id  string
		a   Agent
		why string
	}{
		{"helper", Agent{Name: "Delphi"}, "no such agent"},
		{"agent-0a1b2c3d", Agent{Name: "Delphi"}, "no such agent"},
		{Orchestrator, Agent{Name: ""}, "cannot be empty"},
		{Orchestrator, Agent{Name: "two\nlines"}, "one line"},
		{Orchestrator, Agent{Name: strings.Repeat("ä", 65)}, "at most 64"},
		{Orchestrator, Agent{Name: "PYTHIA"}, "already"},
		{Orchestrator, Agent{Name: "mnemosyne"}, "already"},
		{Thread, Agent{Description: "two\nlines"}, "one line"},
		{Thread, Agent{Description: strings.Repeat("x", MaxDescription+1)}, "at most 300"},
		{Orchestrator, Agent{Name: "Dispatch", Soul: strings.Repeat("x", MaxSoul+1)}, "at most 8000"},
		{Orchestrator, Agent{Name: "Dispatch", Soul: "a\x00b"}, "control characters"},
		{Orchestrator, Agent{Name: "Dispatch", Soul: "\xff"}, "no valid text"},
	} {
		if err := s.Set(refused.id, refused.a, "pythia"); err == nil || !strings.Contains(err.Error(), refused.why) {
			t.Fatalf("setting %s %+v: %v", refused.id, refused.a, err)
		}
	}
	if err := s.Set(Orchestrator, Agent{Name: strings.Repeat("ä", 64)}); err != nil {
		t.Fatalf("a name of 64 characters: %v", err)
	}
	// A built-in agent as it is by default leaves the file.
	for _, id := range []string{Archivist, Orchestrator} {
		if err := s.Set(id, Agent{Name: map[string]string{Archivist: "Archivist", Orchestrator: "Orchestrator"}[id]}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Set(Thread, Agent{}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil || strings.TrimSpace(string(b)) != "{}" {
		t.Fatalf("the file of agents as they are by default: %s, %v", b, err)
	}
	if err := s.Set(Archivist, Agent{Name: "orchestrator"}); err == nil {
		t.Fatal("the archivist took the name of the orchestrator")
	}
	if err := s.Set(Archivist, Agent{Name: "Archivist", Soul: "Neat."}); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(path); err != nil || strings.Contains(string(b), `"name"`) || !strings.Contains(string(b), `"soul": "Neat."`) {
		t.Fatalf("a default name is kept: %s, %v", b, err)
	}
}

func TestCreateAndDelete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agents.json")
	s := Open(path)
	made, err := s.Create(Agent{ID: "agent-ffffffff", Kind: Thread, Name: " Researcher ", Description: "Digs into sources.", Soul: "Thorough."}, "pythia")
	if err != nil || made.Kind != Custom || made.Name != "Researcher" || !Valid(made.ID) || Builtin(made.ID) || made.ID == "agent-ffffffff" {
		t.Fatalf("an agent of the owner: %+v, %v", made, err)
	}
	other, err := s.Create(Agent{Name: "Reviewer"}, "pythia")
	if err != nil || other.ID == made.ID {
		t.Fatalf("a second agent of the owner: %+v, %v", other, err)
	}
	list, err := Open(path).List("pythia")
	if err != nil || len(list) != 5 || list[3] != made || list[4] != other {
		t.Fatalf("the agents: %+v, %v", list, err)
	}
	for _, name := range []string{"pythia", "researcher", "ARCHIVIST", ""} {
		if _, err := s.Create(Agent{Name: name}, "pythia"); err == nil {
			t.Fatalf("an agent called %q made", name)
		}
	}
	if err := s.Set(made.ID, Agent{Name: "Scholar", Soul: "Patient."}, "pythia"); err != nil {
		t.Fatal(err)
	}
	if a, err := s.Get(made.ID, "pythia"); err != nil || a != (Agent{ID: made.ID, Kind: Custom, Name: "Scholar", Soul: "Patient."}) {
		t.Fatalf("the agent of the owner changed: %+v, %v", a, err)
	}
	if err := s.Set(made.ID, Agent{Name: "Reviewer"}); err == nil {
		t.Fatal("two agents of the owner share a name")
	}

	if err := s.Delete(Thread); !errors.Is(err, ErrBuiltin) {
		t.Fatalf("the agent of the thread removed: %v", err)
	}
	if err := s.Delete("agent-00000000"); !errors.Is(err, ErrUnknown) {
		t.Fatalf("an agent that is not there removed: %v", err)
	}
	if err := s.Delete(made.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(made.ID, "pythia"); !errors.Is(err, ErrUnknown) {
		t.Fatalf("a removed agent: %v", err)
	}

	for i := 1; i < MaxCustom; i++ {
		if _, err := s.Create(Agent{Name: "Agent " + strings.Repeat("i", i)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Create(Agent{Name: "One too many"}); err == nil || !strings.Contains(err.Error(), "at most 32") {
		t.Fatalf("an agent too many: %v", err)
	}
}

func TestFileThatCannotBeRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agents.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := Open(path)
	list, err := s.List("pythia")
	if err == nil || len(list) != 3 || list[1].Name != "Archivist" {
		t.Fatalf("a broken file: %+v, %v", list, err)
	}
	if a, err := s.Get(Archivist, "pythia"); err == nil || a.Name != "Archivist" {
		t.Fatalf("a broken file names %+v: %v", a, err)
	}
	if _, err := s.Get("agent-0a1b2c3d", "pythia"); err == nil || errors.Is(err, ErrUnknown) {
		t.Fatalf("an agent of the owner in a broken file: %v", err)
	}
	if err := s.Set(Archivist, Agent{Name: "Mnemosyne"}); err == nil {
		t.Fatal("a broken file was overwritten")
	}
	if err := os.WriteFile(path, []byte(" \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if list, err := s.List("pythia"); err != nil || list[1].Name != "Archivist" {
		t.Fatalf("an empty file: %+v, %v", list, err)
	}
	// What no agent can be is left out: a name for the agent of the thread,
	// an ID that is none, a second entry of an ID, a name of two lines, an
	// agent of the owner without a name, and what is not trimmed.
	if err := os.WriteFile(path, []byte(`{"agents":[
		{"id":"thread","name":"Delphi"},
		{"id":"helper","name":"x"},
		{"id":"archivist","name":"two\nlines"},
		{"id":"orchestrator","name":"Dispatch","description":"Does tasks.","soul":"Brisk."},
		{"id":"orchestrator","name":"Again"},
		{"id":"agent-0a1b2c3d"},
		{"id":"agent-0a1b2c3e","name":" Padded"},
		{"id":"agent-0a1b2c3f","name":"Researcher","soul":"Thorough."}
	]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	list, err = s.List("pythia")
	if err != nil || len(list) != 4 || list[0].Name != "pythia" || list[1].Name != "Archivist" || list[2].Name != "Dispatch" || list[3].Name != "Researcher" {
		t.Fatalf("what no agent can be: %+v, %v", list, err)
	}
	// What a reserved agent is for is not read: it is fixed.
	if list[2].Description != "" || list[2].Soul != "Brisk." {
		t.Fatalf("the orchestrator of the file: %+v", list[2])
	}
	if err := os.Chmod(path, 0); err == nil && os.Getuid() != 0 {
		if _, err := s.List("pythia"); err == nil {
			t.Fatal("a file that cannot be opened was read")
		}
	}
}

// An agent answers with a model of its own through an access of the node,
// given both, or with the model of the settings, given neither; the agent of
// the thread always with those of the settings.
func TestOwnModel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agents.json")
	s := Open(path)
	made, err := s.Create(Agent{Name: "Researcher", Access: " openai-1 ", Model: " gpt-5 "})
	if err != nil || made.Access != "openai-1" || made.Model != "gpt-5" || !made.OwnModel() {
		t.Fatalf("an agent with a model of its own: %+v, %v", made, err)
	}
	if err := s.Set(Archivist, Agent{Name: "Archivist", Access: "openai-1", Model: "gpt-5-mini"}); err != nil {
		t.Fatal(err)
	}
	list, err := Open(path).List("pythia")
	if err != nil || list[1].Model != "gpt-5-mini" || list[3].Model != "gpt-5" || list[0].OwnModel() {
		t.Fatalf("the agents: %+v, %v", list, err)
	}
	for _, refused := range []Agent{
		{Name: "Researcher", Access: "openai-1"},
		{Name: "Researcher", Model: "gpt-5"},
		{Name: "Researcher", Access: "openai-1", Model: "two\nlines"},
		{Name: "Researcher", Access: strings.Repeat("a", 201), Model: "gpt-5"},
	} {
		if err := s.Set(made.ID, refused); err == nil {
			t.Fatalf("set %+v", refused)
		}
	}
	if err := s.Set(Thread, Agent{Access: "openai-1", Model: "gpt-5"}); !errors.Is(err, ErrThreadModel) {
		t.Fatalf("a model of the agent of the thread: %v", err)
	}
	// Back to the model of the settings.
	if err := s.Set(made.ID, Agent{Name: "Researcher"}); err != nil {
		t.Fatal(err)
	}
	if a, err := s.Get(made.ID, "pythia"); err != nil || a.OwnModel() || a.Access != "" {
		t.Fatalf("an agent back on the settings: %+v, %v", a, err)
	}
	// A model of the agent of the thread, or half of one, is no agent.
	if err := os.WriteFile(path, []byte(`{"agents":[{"id":"thread","access":"a","model":"m"},{"id":"archivist","model":"m"},{"id":"orchestrator","access":"a","model":"m"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if list, err := s.List("pythia"); err != nil || list[0].OwnModel() || list[1].Model != "" || list[2].Model != "m" {
		t.Fatalf("models no agent can have: %+v, %v", list, err)
	}
}
