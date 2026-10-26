package assistant_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"ouroboros/internal/assistant"
	"ouroboros/internal/chatgpt"
	"ouroboros/internal/chatgpt/chatgpttest"
	"ouroboros/internal/cli"
	"ouroboros/internal/cli/clitest"
	"ouroboros/internal/config"
	"ouroboros/internal/home"
	"ouroboros/internal/llm/llmtest"
	"ouroboros/plugin"
)

// A node has several accesses, of several providers, and the owner picks the
// model of one of them while a conversation is on its way: the agent goes on
// with it through the other provider, without a restart. What the model of
// the one thought is not sent to the other, what was said and done is.
//
// The provider that signs in with ChatGPT renews its token while the node
// runs: that is written to the credentials and builds no agent anew. And
// nothing an access signs in with is in the configuration or in the log.
func TestTheAgentAnswersThroughTheAccessTheOwnerPicks(t *testing.T) {
	srv := chatgpttest.New(t)
	s := clitest.Settings(t)
	s.ChatGPTAuth, s.ChatGPTAPI = srv.URL, srv.API
	dir := home.Dir(s.Home)
	if err := config.Save(s.Home, clitest.NodeConfig(s)); err != nil {
		t.Fatal(err)
	}

	// A third provider of the test, which is no OpenAI-compatible API, and
	// a way to count how often the model of an access is built.
	var built atomic.Int32
	counted := chatgpt.Provider(chatgpt.Options{Auth: srv.URL, API: srv.API, HostID: dir.HostID})
	counted.Name, counted.Title = "counted", "ChatGPT, counted"
	model := counted.Model
	counted.Model = func(a plugin.Access) plugin.Model {
		built.Add(1)
		return model(a)
	}
	var providers []plugin.Provider
	extra := plugin.New("counted", func(ctx *plugin.Context, _ struct{}) error {
		plugin.Add(ctx, counted)
		plugin.Add(ctx, llmtest.Provider("two"))
		providers = plugin.All[plugin.Provider](ctx)
		return nil
	})
	app, _ := clitest.NewApp(t, s, "", extra)
	agent, ok := cli.Service[*assistant.Assistant](app)
	if !ok {
		t.Fatal("no assistant")
	}
	names := []string{}
	for _, p := range providers {
		names = append(names, p.Name)
	}
	if strings.Join(names, ",") != "openai,deepseek,chatgpt,opencode-go,claude-code,counted,two" {
		t.Fatalf("the providers of this ouro: %v", names)
	}
	owner := plugin.OwnerOf(dir, nil)
	ctx := context.Background()
	ask := func(text string) string {
		t.Helper()
		got, err := agent.Ask(ctx, "talk", text, nil)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}

	// The node as it was set up: an OpenAI-compatible API.
	if got := ask("one"); got != "[gpt-4o] turns=1 last=one" {
		t.Fatalf("with the first access: %q", got)
	}
	first := clitest.MustLoad(t, s).Access

	// The owner signs in with ChatGPT in the setup: a second access, which
	// the node answers through from the next message on.
	setup, err := owner.Setup(providers)
	if err != nil {
		t.Fatal(err)
	}
	browser := &llmtest.Dialog{OnOpen: func(page string) { go srv.Browser(page) }}
	models, err := setup.Login(ctx, "counted", browser)
	if err != nil || models.Default != "gpt-6.1-sol" {
		t.Fatalf("signed in: %+v, %v", models, err)
	}
	if err := setup.Save(models.Default, "alpha"); err != nil {
		t.Fatal(err)
	}
	second := clitest.MustLoad(t, s).Access
	srv.Set(func(st *chatgpttest.State) { st.Reasoning = "gAAAA-thoughts" })
	if got := ask("two"); got != "[gpt-6.1-sol] items=3 last=two" {
		t.Fatalf("with the second access, in the same conversation: %q", got)
	}
	if kept := string(agent.Conversation("talk")); !strings.Contains(kept, "gAAAA-thoughts") {
		t.Fatalf("what the model thought is not kept: %s", kept)
	}

	// The token runs out while the node runs: it is renewed and saved, the
	// access keeps its revision, and the agent is the one it was.
	before, _ := dir.Credentials().Get(second)
	srv.Expire()
	if got := ask("three"); got != "[gpt-6.1-sol] items=6 last=three" {
		t.Fatalf("after the token ran out: %q", got)
	}
	after, _ := dir.Credentials().Get(second)
	if srv.Seen().Refreshes != 1 || built.Load() != 1 || after.Rev != before.Rev || string(after.Secret) == string(before.Secret) {
		t.Fatalf("%d refreshes, %d models built, revision %q (was %q)", srv.Seen().Refreshes, built.Load(), after.Rev, before.Rev)
	}
	// What the model thought went back to its own provider.
	requests := srv.Seen().Requests
	if sent, _ := json.Marshal(requests[len(requests)-1]["input"]); !strings.Contains(string(sent), `"encrypted_content":"gAAAA-thoughts"`) {
		t.Fatalf("the last request: %s", sent)
	}

	// Back to the first access, in the model screen: another provider. The
	// conversation goes on there, without the thoughts of the other model.
	listed, err := owner.Models(ctx, providers)
	if err != nil || len(listed) != 2 || listed[0].Access.ID != first || !listed[1].Access.Active || listed[1].Access.Label != "ChatGPT" || listed[1].Access.About != "me@example.com" {
		t.Fatalf("the models of the accesses: %+v, %v", listed, err)
	}
	if switched, err := owner.SetModel(first, "gpt-4o-mini"); err != nil || !switched {
		t.Fatalf("SetModel = %v, %v", switched, err)
	}
	if got := ask("four"); got != "[gpt-4o-mini] turns=7 last=four" {
		t.Fatalf("back with the first access: %q", got)
	}
	if kept := string(agent.Conversation("talk")); strings.Contains(kept, "gAAAA-thoughts") || strings.Contains(kept, "reasoning") || !strings.Contains(kept, "last=two") {
		t.Fatalf("the thoughts of the other provider are still there: %s", kept)
	}
	// And to the provider of the test, which answers too.
	if _, err := setup.Login(ctx, "two", &llmtest.Dialog{Answers: map[string][]string{"Token": {llmtest.Token}}}); err != nil {
		t.Fatal(err)
	}
	if err := setup.Save("two-large", "alpha"); err != nil {
		t.Fatal(err)
	}
	if got := ask("five"); got != "[two two-large as good] five" {
		t.Fatalf("with the third access: %q", got)
	}

	// No access is in the configuration, and no token in the log, also when
	// a message says it.
	tokens := after.Secrets()
	ask("my tokens are " + strings.Join(tokens, " "))
	configuration, _ := os.ReadFile(filepath.Join(s.Home, "config.json"))
	log, _ := os.ReadFile(filepath.Join(s.Home, "ouro.log"))
	for _, secret := range append(append(tokens, "sk-good"), before.Secrets()...) {
		if strings.Contains(string(configuration), secret) {
			t.Errorf("%q is in the configuration: %s", secret, configuration)
		}
		if strings.Contains(string(log), secret) {
			t.Errorf("%q is in the log", secret)
		}
	}
	if !strings.Contains(string(log), "my tokens are [redacted]") {
		t.Errorf("the log: %s", log)
	}
}
