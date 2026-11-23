package gui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"ouroboros/internal/config"
	"ouroboros/internal/guard"
	"ouroboros/internal/home"
	"ouroboros/internal/identity"
	"ouroboros/internal/kernel"
	"ouroboros/internal/web"
	"ouroboros/plugin"
)

// renamable is a profile whose name the window can change, kept here rather
// than in the configuration of a node.
func renamable(name string) (*profile, *[]string) {
	var asked []string
	p := &profile{}
	p.status = func() (plugin.NodeStatus, error) { return plugin.NodeStatus{Name: name}, nil }
	p.rename = func(next string) error {
		asked = append(asked, next)
		next = strings.TrimSpace(next)
		if next == "" {
			return errors.New("a profile needs a name of one line")
		}
		name = next
		return nil
	}
	return p, &asked
}

// The window lists the agent of the thread under the name of the profile, and
// renaming it renames the profile: the window and the page say the new name.
func TestWindowListsAndRenamesTheAgentOfTheThread(t *testing.T) {
	p, asked := renamable("pythia")
	w := newWindow(p, "pythia")
	t.Cleanup(w.Close)

	list, err := w.Agents()
	if err != nil {
		t.Fatal(err)
	}
	want := []Agent{
		{ID: "thread", Kind: "thread", Name: "pythia"},
		{ID: "archivist", Kind: "archivist", Name: "Archivist", Reserved: true},
		{ID: "orchestrator", Kind: "orchestrator", Name: "Orchestrator", Reserved: true},
	}
	if !slices.Equal(list, want) {
		t.Fatalf("the agents: %+v", list)
	}

	agent, err := w.UpdateAgent("thread", AgentChange{Name: "  Delphi "})
	if err != nil {
		t.Fatal(err)
	}
	if agent.Name != "Delphi" || len(*asked) != 1 || (*asked)[0] != "  Delphi " {
		t.Fatalf("renamed to %+v, asked %q", agent, *asked)
	}
	if list, _ := w.Agents(); list[0].Name != "Delphi" {
		t.Fatalf("the agents after the rename: %+v", list)
	}
	// The window names the profile as it is called now, not as it opened.
	if state := w.InitialState(); state.Profile != "Delphi" {
		t.Fatalf("the window still names %q", state.Profile)
	}

	// What the profile refuses is no rename, and says why.
	if _, err := w.UpdateAgent("thread", AgentChange{Name: " "}); err == nil || !strings.Contains(err.Error(), "one line") {
		t.Fatalf("an empty name: %v", err)
	}
	// There is no other agent to rename.
	if _, err := w.UpdateAgent("helper", AgentChange{Name: "Delphi"}); err == nil || !strings.Contains(err.Error(), `no agent "helper"`) {
		t.Fatalf("another agent: %v", err)
	}
	if len(*asked) != 2 {
		t.Fatalf("the profile was asked %d times: %q", len(*asked), *asked)
	}
}

// A window without the owner of the node lists the agents but cannot rename
// the agent of the thread, one without the directory of its profile cannot
// rename the others, and one whose profile is not set up or cannot be read
// lists none.
func TestAgentsWithoutOwnerOrProfile(t *testing.T) {
	w := newWindow(fakeProfile("pythia"), "pythia")
	t.Cleanup(w.Close)
	if list, err := w.Agents(); err != nil || len(list) != 3 {
		t.Fatalf("the agents: %+v, %v", list, err)
	}
	if _, err := w.UpdateAgent("thread", AgentChange{Name: "Delphi"}); !errors.Is(err, errNoRename) {
		t.Fatalf("a rename without the owner: %v", err)
	}
	if _, err := w.UpdateAgent("archivist", AgentChange{Name: "Mnemosyne"}); err == nil || !strings.Contains(err.Error(), "not kept") {
		t.Fatalf("a rename without the profile: %v", err)
	}

	unset := newWindow(fakeProfile(""), "Not set up")
	t.Cleanup(unset.Close)
	if _, err := unset.Agents(); err == nil || !strings.Contains(err.Error(), "not set up") {
		t.Fatalf("the agents of a profile not set up: %v", err)
	}
	if state := unset.InitialState(); state.Profile != "Not set up" {
		t.Fatalf("a profile not set up is called %q", state.Profile)
	}

	broken := &profile{status: func() (plugin.NodeStatus, error) { return plugin.NodeStatus{}, errors.New("no config") }}
	b := newWindow(broken, "Luna")
	t.Cleanup(b.Close)
	if _, err := b.Agents(); err == nil || !strings.Contains(err.Error(), "no config") {
		t.Fatalf("the agents of a profile that cannot be read: %v", err)
	}
	if state := b.InitialState(); state.Profile != "Luna" {
		t.Fatalf("a profile that cannot be read is called %q", state.Profile)
	}
}

// The page lists and renames the agents behind the login, and renames only on
// a request of the page itself.
func TestPageListsAndRenamesAgents(t *testing.T) {
	dir := home.Dir(t.TempDir())
	hash, err := web.Hash("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Save(string(dir), config.Config{Model: "m", Web: config.Web{Enabled: true, User: "owner", Password: hash}}); err != nil {
		t.Fatal(err)
	}
	p, asked := renamable("pythia")
	p.dir = dir
	front := newWebFront(dir, p)
	server := httptest.NewServer(front.handler)
	t.Cleanup(server.Close)
	client := stillClient()

	res := do(t, client, http.MethodGet, server.URL+"/api/agents", nil, nil)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("the agents without a login: %d", res.StatusCode)
	}
	res.Body.Close()

	login := signIn(t, client, server.URL, "owner", "correct horse battery")
	cookie := sessionOf(t, login)
	login.Body.Close()

	res = do(t, client, http.MethodGet, server.URL+"/api/agents", cookie, nil)
	var list []Agent
	if err := json.Unmarshal([]byte(body(t, res)), &list); err != nil || res.StatusCode != http.StatusOK {
		t.Fatal(res.StatusCode, err)
	}
	if len(list) != 3 || list[0].Name != "pythia" || list[0].ID != "thread" || list[2].ID != "orchestrator" {
		t.Fatalf("the agents of the page: %+v", list)
	}

	rename := func(header map[string]string, payload string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, server.URL+"/api/agent/update", strings.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		req.AddCookie(cookie)
		req.Header.Set("Content-Type", "application/json")
		for name, value := range header {
			req.Header.Set(name, value)
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}

	// Not from the page: another site cannot rename the agent.
	res = rename(nil, `{"id":"thread","name":"Delphi"}`)
	if got := body(t, res); res.StatusCode != http.StatusForbidden || len(*asked) != 0 {
		t.Fatalf("a rename from elsewhere: %d, %q", res.StatusCode, got)
	}
	page := map[string]string{webHeader: "1"}
	res = rename(page, `{not json`)
	if got := body(t, res); res.StatusCode != http.StatusBadRequest || !strings.Contains(got, "cannot be read") {
		t.Fatalf("a rename that cannot be read: %d, %q", res.StatusCode, got)
	}
	res = rename(page, `{"id":"thread","name":""}`)
	if got := body(t, res); res.StatusCode != http.StatusConflict || !strings.Contains(got, "one line") {
		t.Fatalf("an empty name: %d, %q", res.StatusCode, got)
	}
	res = rename(page, `{"id":"thread","name":"Delphi"}`)
	var agent Agent
	if err := json.Unmarshal([]byte(body(t, res)), &agent); err != nil || res.StatusCode != http.StatusOK {
		t.Fatal(res.StatusCode, err)
	}
	if agent.Name != "Delphi" {
		t.Fatalf("renamed to %+v", agent)
	}
	// The other agents are renamed in the profile, not in its configuration.
	res = rename(page, `{"id":"orchestrator","name":"Dispatch"}`)
	if got := body(t, res); res.StatusCode != http.StatusOK || !strings.Contains(got, `"name":"Dispatch"`) || len(*asked) != 2 {
		t.Fatalf("the orchestrator renamed: %d, %q, %q", res.StatusCode, got, *asked)
	}
	// The page names the new name from then on, also at its login.
	res = do(t, client, http.MethodGet, server.URL+"/api/initial", cookie, nil)
	if got := body(t, res); !strings.Contains(got, `"profile":"Delphi"`) {
		t.Fatalf("the page after the rename: %q", got)
	}
	res = do(t, client, http.MethodGet, server.URL+"/login", nil, nil)
	if got := body(t, res); !strings.Contains(got, "Delphi") {
		t.Fatalf("the login after the rename: %q", got)
	}

	// A profile that cannot be read lists no agents.
	p.status = func() (plugin.NodeStatus, error) { return plugin.NodeStatus{}, errors.New("no config") }
	res = do(t, client, http.MethodGet, server.URL+"/api/agents", cookie, nil)
	if got := body(t, res); res.StatusCode != http.StatusConflict || !strings.Contains(got, "no config") {
		t.Fatalf("the agents of a broken profile: %d, %q", res.StatusCode, got)
	}
}

// The window of the node renames the agent of the thread through the owner,
// as the settings do: the profile is called so from then on, and a name
// another profile of the node has is refused.
func TestOwnerRenamesTheAgentOfTheThread(t *testing.T) {
	dir := home.Dir(t.TempDir())
	if err := config.Save(string(dir), config.Config{Name: "pythia", PrivateKey: identity.Generate().Private(), Access: "a", Model: "m"}); err != nil {
		t.Fatal(err)
	}
	if _, err := dir.AddProfile("hermes"); err != nil {
		t.Fatal(err)
	}
	old := show
	t.Cleanup(func() { show = old })
	var shown *profile
	show = func(_ context.Context, p *profile, _ string) error {
		shown = p
		return nil
	}
	var ctx *plugin.Context
	k := kernel.New()
	if err := k.Start(sessionProvider(&fakeSessions{}), Plugin(plugin.OwnerOf(dir, nil), dir, &guard.Guard{Home: t.TempDir(), Workspace: t.TempDir()}), plugin.New("spy", func(c *plugin.Context, _ struct{}) error {
		ctx = c
		return nil
	})); err != nil {
		t.Fatal(err)
	}
	if err := plugin.All[plugin.Command](ctx)[0].Run(context.Background(), nil); err != nil || shown == nil {
		t.Fatalf("the window did not open: %v", err)
	}
	agent, err := shown.updateAgent(threadAgent, AgentChange{Name: "Delphi"})
	if err != nil || agent.Name != "Delphi" {
		t.Fatalf("renamed to %+v: %v", agent, err)
	}
	if cfg, err := dir.Config(); err != nil || cfg.Name != "Delphi" {
		t.Fatalf("the profile is called %q: %v", cfg.Name, err)
	}
	if _, err := shown.updateAgent(threadAgent, AgentChange{Name: "HERMES"}); err == nil || !strings.Contains(err.Error(), "already") {
		t.Fatalf("the name of another profile: %v", err)
	}
}

// The archivist and the orchestrator are renamed in the file of the agents of
// the profile: the profile keeps its name, and no two agents share one, the
// agent of the thread among them.
func TestWindowRenamesTheArchivistAndTheOrchestrator(t *testing.T) {
	p, asked := renamable("pythia")
	p.dir = home.Dir(t.TempDir())
	w := newWindow(p, "pythia")
	t.Cleanup(w.Close)

	agent, err := w.UpdateAgent("archivist", AgentChange{Name: "  Mnemosyne "})
	if err != nil || agent != (Agent{ID: "archivist", Kind: "archivist", Name: "Mnemosyne", Reserved: true}) {
		t.Fatalf("renamed to %+v: %v", agent, err)
	}
	if len(*asked) != 0 {
		t.Fatalf("the profile was renamed: %q", *asked)
	}
	if a, err := p.dir.Agents().Get("archivist", "pythia"); err != nil || a.Name != "Mnemosyne" {
		t.Fatalf("the profile keeps %+v: %v", a, err)
	}
	for _, taken := range []string{"PYTHIA", "orchestrator"} {
		if _, err := w.UpdateAgent("archivist", AgentChange{Name: taken}); err == nil || !strings.Contains(err.Error(), "already") {
			t.Fatalf("the name %q of another agent: %v", taken, err)
		}
	}
	if _, err := w.UpdateAgent("orchestrator", AgentChange{Name: "mnemosyne"}); err == nil || !strings.Contains(err.Error(), "already") {
		t.Fatalf("the new name of the archivist for the orchestrator: %v", err)
	}
	if _, err := w.UpdateAgent("orchestrator", AgentChange{Name: "Orchestrator", Description: "Does tasks."}); err == nil || !strings.Contains(err.Error(), "is fixed") {
		t.Fatalf("what the orchestrator is for changed: %v", err)
	}
	if _, err := w.UpdateAgent("orchestrator", AgentChange{Name: "two\nlines"}); err == nil || !strings.Contains(err.Error(), "one line") {
		t.Fatalf("a name of two lines: %v", err)
	}
	list, err := w.Agents()
	if err != nil || list[0].Name != "pythia" || list[1].Name != "Mnemosyne" || list[2].Name != "Orchestrator" {
		t.Fatalf("the agents after the renames: %+v, %v", list, err)
	}
}

// The owner gives every agent what it is for and a soul, the agent of the
// thread too, whose name stays that of the profile; and makes agents of their
// own, which they remove again, unlike the built-in ones.
func TestWindowKeepsSoulsAndAgentsOfTheOwner(t *testing.T) {
	p, asked := renamable("pythia")
	p.dir = home.Dir(t.TempDir())
	w := newWindow(p, "pythia")
	t.Cleanup(w.Close)

	agent, err := w.UpdateAgent("thread", AgentChange{Name: "pythia", Description: "Talks with me.", Soul: "Calm.\nDry humour."})
	if err != nil || agent != (Agent{ID: "thread", Kind: "thread", Name: "pythia", Description: "Talks with me.", Soul: "Calm.\nDry humour."}) {
		t.Fatalf("the agent of the thread: %+v, %v", agent, err)
	}
	if len(*asked) != 0 {
		t.Fatalf("the profile was renamed for its soul: %q", *asked)
	}
	agent, err = w.UpdateAgent("thread", AgentChange{Name: "Delphi", Description: "Talks with me.", Soul: "Calm.\nDry humour."})
	if err != nil || agent.Name != "Delphi" || agent.Soul != "Calm.\nDry humour." || len(*asked) != 1 {
		t.Fatalf("the agent of the thread renamed: %+v, %v, %q", agent, err, *asked)
	}

	made, err := w.CreateAgent(AgentChange{Name: " Researcher ", Description: "Digs into sources.", Soul: "Thorough."})
	if err != nil || made.Kind != "custom" || made.Name != "Researcher" || !strings.HasPrefix(made.ID, "agent-") {
		t.Fatalf("an agent of the owner: %+v, %v", made, err)
	}
	if _, err := w.CreateAgent(AgentChange{Name: "delphi"}); err == nil || !strings.Contains(err.Error(), "already") {
		t.Fatalf("an agent called as the profile: %v", err)
	}
	agent, err = w.UpdateAgent(made.ID, AgentChange{Name: "Scholar", Description: "Reads papers."})
	if err != nil || agent.Name != "Scholar" || agent.Description != "Reads papers." || agent.Soul != "" {
		t.Fatalf("the agent of the owner changed: %+v, %v", agent, err)
	}
	list, err := w.Agents()
	if err != nil || len(list) != 4 || list[3].ID != made.ID || list[0].Description != "Talks with me." {
		t.Fatalf("the agents: %+v, %v", list, err)
	}

	if err := w.DeleteAgent("archivist"); err == nil || !strings.Contains(err.Error(), "built-in") {
		t.Fatalf("a built-in agent removed: %v", err)
	}
	if err := w.DeleteAgent("agent-00000000"); err == nil || !strings.Contains(err.Error(), "no agent") {
		t.Fatalf("an agent that is not there removed: %v", err)
	}
	if err := w.DeleteAgent(made.ID); err != nil {
		t.Fatal(err)
	}
	if list, err := w.Agents(); err != nil || len(list) != 3 {
		t.Fatalf("the agents after the removal: %+v, %v", list, err)
	}

	// A profile that is not set up makes no agent.
	unset := newWindow(fakeProfile(""), "Not set up")
	t.Cleanup(unset.Close)
	if _, err := unset.CreateAgent(AgentChange{Name: "Researcher"}); err == nil || !strings.Contains(err.Error(), "not set up") {
		t.Fatalf("an agent of a profile not set up: %v", err)
	}
	if _, err := unset.UpdateAgent("archivist", AgentChange{Name: "Mnemosyne"}); err == nil || !strings.Contains(err.Error(), "not set up") {
		t.Fatalf("an agent of a profile not set up changed: %v", err)
	}
}

// The page makes and removes agents of the owner's, only on a request of the
// page itself.
func TestPageMakesAndRemovesAgents(t *testing.T) {
	dir := home.Dir(t.TempDir())
	hash, err := web.Hash("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Save(string(dir), config.Config{Model: "m", Web: config.Web{Enabled: true, User: "owner", Password: hash}}); err != nil {
		t.Fatal(err)
	}
	p, _ := renamable("pythia")
	p.dir = dir
	front := newWebFront(dir, p)
	server := httptest.NewServer(front.handler)
	t.Cleanup(server.Close)
	client := stillClient()
	login := signIn(t, client, server.URL, "owner", "correct horse battery")
	cookie := sessionOf(t, login)
	login.Body.Close()

	post := func(path string, page bool, payload string) (int, string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, server.URL+path, strings.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		req.AddCookie(cookie)
		req.Header.Set("Content-Type", "application/json")
		if page {
			req.Header.Set(webHeader, "1")
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res.StatusCode, body(t, res)
	}

	if status, got := post("/api/agent/create", false, `{"name":"Researcher"}`); status != http.StatusForbidden {
		t.Fatalf("an agent made from elsewhere: %d, %q", status, got)
	}
	if status, got := post("/api/agent/create", true, `{not json`); status != http.StatusBadRequest || !strings.Contains(got, "cannot be read") {
		t.Fatalf("an agent that cannot be read: %d, %q", status, got)
	}
	if status, got := post("/api/agent/create", true, `{"name":""}`); status != http.StatusConflict || !strings.Contains(got, "cannot be empty") {
		t.Fatalf("an agent without a name: %d, %q", status, got)
	}
	status, got := post("/api/agent/create", true, `{"name":"Researcher","description":"Digs.","soul":"Thorough."}`)
	var made Agent
	if err := json.Unmarshal([]byte(got), &made); err != nil || status != http.StatusOK || made.Kind != "custom" || made.Soul != "Thorough." {
		t.Fatalf("an agent made: %d, %q, %v", status, got, err)
	}
	if status, got := post("/api/agent/update", true, `{"id":"`+made.ID+`","name":"Researcher","soul":"Quick."}`); status != http.StatusOK || !strings.Contains(got, `"soul":"Quick."`) {
		t.Fatalf("its soul changed: %d, %q", status, got)
	}
	if status, got := post("/api/agent/delete", false, `{"id":"`+made.ID+`"}`); status != http.StatusForbidden {
		t.Fatalf("an agent removed from elsewhere: %d, %q", status, got)
	}
	if status, got := post("/api/agent/delete", true, `{"id":"thread"}`); status != http.StatusConflict || !strings.Contains(got, "built-in") {
		t.Fatalf("the agent of the thread removed: %d, %q", status, got)
	}
	if status, got := post("/api/agent/delete", true, `{"id":"`+made.ID+`"}`); status != http.StatusOK {
		t.Fatalf("an agent removed: %d, %q", status, got)
	}
	if list, err := p.agents(); err != nil || len(list) != 3 {
		t.Fatalf("the agents after the removal: %+v, %v", list, err)
	}
}

// modelsOf is a profile whose node has the accesses listed.
func modelsOf(listed ...plugin.AccessModels) func(context.Context) ([]plugin.AccessModels, error) {
	return func(context.Context) ([]plugin.AccessModels, error) { return listed, nil }
}

// The window offers the models of the accesses of the node, and gives an
// agent one of them as its own: one an access offers, or the one it has
// already, also while its access does not list it. The agent of the thread
// answers with the models of the settings.
func TestWindowGivesAgentsAModel(t *testing.T) {
	p, _ := renamable("pythia")
	p.dir = home.Dir(t.TempDir())
	p.models = modelsOf(
		plugin.AccessModels{Access: plugin.SetupAccess{ID: "openai-1", Provider: "openai", Label: "OpenAI"}, Models: []string{"gpt-5", "gpt-5-mini"}},
		plugin.AccessModels{Access: plugin.SetupAccess{ID: "claude-1", Provider: "claude-code", Label: "Claude Code"}, Err: "signed out"},
		plugin.AccessModels{Access: plugin.SetupAccess{ID: "old-1", Provider: "gone", Missing: true}, Models: []string{"m"}},
	)
	w := newWindow(p, "pythia")
	t.Cleanup(w.Close)

	list, err := w.AgentModels()
	if err != nil || len(list) != 2 || list[0].Access != "openai-1" || list[0].Label != "OpenAI" || !slices.Equal(list[0].Models, []string{"gpt-5", "gpt-5-mini"}) ||
		list[1].Error != "signed out" || list[1].Models == nil || len(list[1].Models) != 0 {
		t.Fatalf("the models: %+v, %v", list, err)
	}

	agent, err := w.UpdateAgent("archivist", AgentChange{Name: "Archivist", Access: "openai-1", Model: "gpt-5-mini"})
	if err != nil || agent.Access != "openai-1" || agent.Model != "gpt-5-mini" {
		t.Fatalf("the archivist with a model: %+v, %v", agent, err)
	}
	for change, why := range map[AgentChange]string{
		{Name: "Archivist", Access: "openai-1", Model: "o9"}:     `OpenAI does not offer the model "o9"`,
		{Name: "Archivist", Access: "nope", Model: "gpt-5"}:      `the node has no access "nope"`,
		{Name: "Archivist", Access: "claude-1", Model: "sonnet"}: "the models of Claude Code cannot be listed: signed out",
		{Name: "Archivist", Access: "old-1", Model: "m"}:         `the node has no access "old-1"`,
		{Name: "Archivist", Access: "openai-1"}:                  "or neither",
	} {
		if _, err := w.UpdateAgent("archivist", change); err == nil || !strings.Contains(err.Error(), why) {
			t.Fatalf("%+v: %v", change, err)
		}
	}
	// The model it has stays, whatever its access lists now.
	p.models = modelsOf()
	if agent, err := w.UpdateAgent("archivist", AgentChange{Name: "Archivist", Soul: "Neat.", Access: "openai-1", Model: "gpt-5-mini"}); err != nil || agent.Soul != "Neat." {
		t.Fatalf("the archivist with the model it has: %+v, %v", agent, err)
	}
	if _, err := w.CreateAgent(AgentChange{Name: "Researcher", Access: "openai-1", Model: "gpt-5"}); err == nil || !strings.Contains(err.Error(), "no access") {
		t.Fatalf("an agent with a model the node no longer has: %v", err)
	}
	p.models = modelsOf(plugin.AccessModels{Access: plugin.SetupAccess{ID: "openai-1", Label: "OpenAI"}, Models: []string{"gpt-5"}})
	made, err := w.CreateAgent(AgentChange{Name: "Researcher", Access: "openai-1", Model: "gpt-5"})
	if err != nil || made.Model != "gpt-5" {
		t.Fatalf("an agent made with a model: %+v, %v", made, err)
	}
	// The agent of the thread takes a model of its own as well, and gives it
	// back: then it answers with the defaults of the settings again.
	if agent, err := w.UpdateAgent("thread", AgentChange{Name: "pythia", Access: "openai-1", Model: "gpt-5"}); err != nil || agent.Model != "gpt-5" || agent.Name != "pythia" {
		t.Fatalf("a model of the agent of the thread: %+v, %v", agent, err)
	}
	if agent, err := w.UpdateAgent("thread", AgentChange{Name: "pythia"}); err != nil || agent.Model != "" || agent.Access != "" {
		t.Fatalf("the agent of the thread back on the settings: %+v, %v", agent, err)
	}
	p.models = func(context.Context) ([]plugin.AccessModels, error) { return nil, errors.New("not set up") }
	if _, err := w.AgentModels(); err == nil || !strings.Contains(err.Error(), "not set up") {
		t.Fatalf("models of a node not set up: %v", err)
	}
	if _, err := w.UpdateAgent(made.ID, AgentChange{Name: "Researcher", Access: "openai-1", Model: "gpt-5-mini"}); err == nil || !strings.Contains(err.Error(), "cannot be listed") {
		t.Fatalf("a model while none can be listed: %v", err)
	}
	// A window without the owner lists no models, but keeps the models of the settings.
	bare := newWindow(fakeProfile("pythia"), "pythia")
	t.Cleanup(bare.Close)
	if _, err := bare.AgentModels(); !errors.Is(err, errNoModels) {
		t.Fatalf("the models without the owner: %v", err)
	}
}

// The window gives an agent an effort beside its model, and neither the agent
// of the thread nor an effort no model knows is taken.
func TestWindowGivesAgentsAnEffort(t *testing.T) {
	p, _ := renamable("pythia")
	p.dir = home.Dir(t.TempDir())
	w := newWindow(p, "pythia")
	t.Cleanup(w.Close)

	agent, err := w.UpdateAgent("archivist", AgentChange{Name: "Archivist", Effort: "high"})
	if err != nil || agent.Effort != "high" {
		t.Fatalf("the archivist with an effort: %+v, %v", agent, err)
	}
	if list, err := w.Agents(); err != nil || list[1].Effort != "high" {
		t.Fatalf("the agents: %+v, %v", list, err)
	}
	if _, err := w.UpdateAgent("archivist", AgentChange{Name: "Archivist", Effort: "enormous"}); err == nil || !strings.Contains(err.Error(), "is none of") {
		t.Fatalf("an effort no model knows: %v", err)
	}
	if agent, err := w.UpdateAgent("thread", AgentChange{Name: "pythia", Effort: "low"}); err != nil || agent.Effort != "low" {
		t.Fatalf("an effort of the agent of the thread: %+v, %v", agent, err)
	}
	if agent, err := w.UpdateAgent("thread", AgentChange{Name: "pythia"}); err != nil || agent.Effort != "" {
		t.Fatalf("the agent of the thread back on the settings: %+v, %v", agent, err)
	}
	made, err := w.CreateAgent(AgentChange{Name: "Researcher", Effort: "low"})
	if err != nil || made.Effort != "low" {
		t.Fatalf("an agent made with an effort: %+v, %v", made, err)
	}
	// Back to the effort of the settings.
	if agent, err := w.UpdateAgent(made.ID, AgentChange{Name: "Researcher"}); err != nil || agent.Effort != "" {
		t.Fatalf("an agent back on the settings: %+v, %v", agent, err)
	}
}

func TestPageListsTheModels(t *testing.T) {
	dir := home.Dir(t.TempDir())
	hash, err := web.Hash("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if err := config.Save(string(dir), config.Config{Model: "m", Web: config.Web{Enabled: true, User: "owner", Password: hash}}); err != nil {
		t.Fatal(err)
	}
	p, _ := renamable("pythia")
	p.dir = dir
	p.models = modelsOf(plugin.AccessModels{Access: plugin.SetupAccess{ID: "openai-1", Label: "OpenAI"}, Models: []string{"gpt-5"}})
	front := newWebFront(dir, p)
	server := httptest.NewServer(front.handler)
	t.Cleanup(server.Close)
	client := stillClient()
	res := do(t, client, http.MethodGet, server.URL+"/api/agent/models", nil, nil)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("the models without a login: %d", res.StatusCode)
	}
	res.Body.Close()
	login := signIn(t, client, server.URL, "owner", "correct horse battery")
	cookie := sessionOf(t, login)
	login.Body.Close()
	res = do(t, client, http.MethodGet, server.URL+"/api/agent/models", cookie, nil)
	if got := body(t, res); res.StatusCode != http.StatusOK || !strings.Contains(got, `"access":"openai-1"`) || !strings.Contains(got, `"models":["gpt-5"]`) {
		t.Fatalf("the models of the page: %d, %q", res.StatusCode, got)
	}
	p.models = nil
	res = do(t, client, http.MethodGet, server.URL+"/api/agent/models", cookie, nil)
	if got := body(t, res); res.StatusCode != http.StatusConflict || !strings.Contains(got, "cannot list") {
		t.Fatalf("the models without the owner: %d, %q", res.StatusCode, got)
	}
}
