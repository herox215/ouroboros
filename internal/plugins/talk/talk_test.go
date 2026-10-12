package talk_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"ouroboros/internal/cli"
	"ouroboros/internal/cli/clitest"
	"ouroboros/internal/config"
	"ouroboros/internal/diagnosis"
	"ouroboros/internal/discovery"
	"ouroboros/internal/home"
	"ouroboros/internal/openai/openaitest"
	"ouroboros/internal/peers"
	"ouroboros/internal/plugins/talk"
	"ouroboros/plugin"
)

func TestMain(m *testing.M) { clitest.Main(m) }

// Without a message "ouro chat" is the terminal UI; only pipes get the line-by-line chat.
func TestChatWithTerminalOpensTUI(t *testing.T) {
	s := clitest.Settings(t)
	config.Save(s.Home, clitest.NodeConfig(s))
	app, out := clitest.NewAppOn(t, s, cli.Terminal{Interactive: true})
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the UI has no terminal here: it must give up instead of reading lines
	if err := app.Run(ctx, []string{"chat"}); err == nil || strings.Contains(out.String(), "chatting with your agent") {
		t.Fatalf("got %q, %v", out.String(), err)
	}
}

// frontEnd is a plugin that contributes a front end called name.
func frontEnd(name string) plugin.Plugin {
	return plugin.New(name, func(ctx *plugin.Context, _ struct{}) error {
		plugin.Add(ctx, plugin.FrontEnd{Name: name, Run: func(context.Context) error { return errors.New(name + " opened") }})
		return nil
	})
}

// "ouro chat" asks for the front end like ouro without a command does: the
// one a plugin contributed opens, and none is picked out of several. Without
// one the chat goes line by line, also at a terminal.
func TestChatOpensTheFrontEndThatIsThere(t *testing.T) {
	s := clitest.Settings(t)
	config.Save(s.Home, clitest.NodeConfig(s))
	chat := func(plugins ...plugin.Plugin) (string, error) {
		t.Helper()
		var out bytes.Buffer
		term := cli.Terminal{In: strings.NewReader("one\n"), Out: &out, Interactive: true}
		list := slices.DeleteFunc(clitest.Plugins(s, term, plugins...), func(p plugin.Plugin) bool { return p.Name == "tui" })
		app, err := cli.NewWith(clitest.InProcess(s)(list)...)
		if err != nil {
			t.Fatal(err)
		}
		err = app.Run(context.Background(), []string{"chat"})
		return out.String(), err
	}

	if out, err := chat(); err != nil || !strings.Contains(out, "chatting with your agent") || !strings.Contains(out, "turns=1 last=one") {
		t.Fatalf("without a front end: %q, %v", out, err)
	}
	if out, err := chat(frontEnd("web")); err == nil || err.Error() != "web opened" || out != "" {
		t.Fatalf("with a front end: %q, %v", out, err)
	}
	out, err := chat(frontEnd("web"), frontEnd("gui"))
	if !errors.Is(err, plugin.ErrFrontEnds) || !strings.Contains(err.Error(), "ouro web, ouro gui") || out != "" {
		t.Fatalf("with two front ends: %q, %v", out, err)
	}
}

func TestNodeLifecycle(t *testing.T) {
	s := clitest.Settings(t)
	port := clitest.StartNode(t, s, clitest.NodeConfig(s))

	out, _ := clitest.Run(t, s, "", "status")
	if !strings.Contains(out, "● running") || !strings.Contains(out, fmt.Sprintf("port=%d", port)) {
		t.Fatalf("status: %q", out)
	}

	id := clitest.NodeID(t, s)
	out, err := clitest.Run(t, s, "", "list")
	if err != nil || len(id) != 12 || !strings.Contains(out, id) || !strings.Contains(out, "alpha") || !strings.Contains(out, "gpt-4o") {
		t.Fatalf("list: %q, %v", out, err)
	}

	out, err = clitest.Run(t, s, "", "ask", id[:4], "hello", "world")
	if err != nil || out != "[gpt-4o] turns=1 last=hello world\n" {
		t.Fatalf("ask by ID prefix: %q, %v", out, err)
	}

	if out, err := clitest.Run(t, s, "", "ask", id[:4], "  "); err == nil || err.Error() != "the message is empty" {
		t.Fatalf("an empty message: %q, %v", out, err)
	}

	out, err = clitest.Run(t, s, "one\n\ntwo\n", "ask", "alpha")
	if err != nil || !strings.Contains(out, "turns=1 last=one") || !strings.Contains(out, "turns=3 last=two") {
		t.Fatalf("interactive ask should keep history: %q, %v", out, err)
	}

	out, err = clitest.Run(t, s, "", "ask", fmt.Sprintf("127.0.0.1:%d", port), "direct")
	if err != nil || !strings.Contains(out, "last=direct") {
		t.Fatalf("ask by address: %q, %v", out, err)
	}

	out, _ = clitest.Run(t, s, "", "start")
	if !strings.Contains(out, "already running") {
		t.Fatalf("second start: %q", out)
	}
}

func TestInteractiveAskReportsErrorsAndContinues(t *testing.T) {
	s := clitest.Settings(t)
	cfg := clitest.NodeConfig(s)
	cfg.APIKey = "sk-revoked" // node starts, but every LLM call fails
	clitest.StartNode(t, s, cfg)
	out, err := clitest.Run(t, s, "hi\n", "ask", "alpha")
	if err != nil || !strings.Contains(out, "✗") || !strings.Contains(out, "401") {
		t.Fatalf("got %q, %v", out, err)
	}
}

// A peer learns that the agent failed, not why: the cause stays in the log of
// the node that failed.
func TestPeerIsNotToldWhyTheAgentFailed(t *testing.T) {
	beta := clitest.Settings(t)
	cfg := clitest.NodeConfig(beta)
	cfg.Name, cfg.APIKey = "beta", "sk-revoked"
	clitest.StartNode(t, beta, cfg)
	alpha := clitest.Settings(t)
	clitest.Finds(alpha, beta)
	if err := config.Save(alpha.Home, clitest.NodeConfig(alpha)); err != nil {
		t.Fatal(err)
	}
	clitest.Trust(t, beta, alpha, "alpha")
	clitest.Trust(t, alpha, beta, "beta")

	_, err := clitest.Run(t, alpha, "", "ask", "beta", "hi")
	if err == nil || !strings.Contains(err.Error(), "the agent could not answer (error ") || strings.Contains(err.Error(), "401") {
		t.Fatalf("got %v", err)
	}
}

func TestOneShotAskReturnsError(t *testing.T) {
	s := clitest.Settings(t)
	cfg := clitest.NodeConfig(s)
	cfg.APIKey = "sk-revoked"
	clitest.StartNode(t, s, cfg)
	if _, err := clitest.Run(t, s, "", "ask", "alpha", "hi"); err == nil {
		t.Fatal("want error")
	}
}

func TestAskErrors(t *testing.T) {
	s := clitest.Settings(t)
	if _, err := clitest.Run(t, s, "", "ask"); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Fatalf("no args: %v", err)
	}
	if _, err := clitest.Run(t, s, "", "ask", "nobody", "hi"); err == nil || !strings.Contains(err.Error(), "no node") {
		t.Fatalf("unknown node: %v", err)
	}
}

// The assistant runs in the calling process: it needs a configuration but no running node.
func TestChatWithAssistant(t *testing.T) {
	s := clitest.Settings(t)
	if _, err := clitest.Run(t, s, "", "chat", "hi"); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("unconfigured: %v", err)
	}
	config.Save(s.Home, clitest.NodeConfig(s))

	out, err := clitest.Run(t, s, "", "chat", "hello", "world")
	if err != nil || out != "[gpt-4o] turns=1 last=hello world\n" {
		t.Fatalf("one-shot: %q, %v", out, err)
	}
	out, err = clitest.Run(t, s, "one\n\ntwo\n", "chat")
	if err != nil || !strings.Contains(out, "turns=1 last=one") || !strings.Contains(out, "turns=3 last=two") {
		t.Fatalf("interactive chat should keep history: %q, %v", out, err)
	}

	cfg := clitest.NodeConfig(s)
	cfg.APIKey = "sk-revoked"
	config.Save(s.Home, cfg)
	if _, err := clitest.Run(t, s, "", "chat", "hi"); err == nil {
		t.Fatal("a failing LLM should fail the one-shot chat")
	}
	if out, err := clitest.Run(t, s, "hi\n", "chat"); err != nil || !strings.Contains(out, "✗") || !strings.Contains(out, "401") {
		t.Fatalf("interactive chat should report the error and continue: %q, %v", out, err)
	}
}

// The chat is in the log of the node: the message, the calls and the answer,
// and an answer stopped by Ctrl-C as canceled. The key of the API is not.
func TestChatIsLogged(t *testing.T) {
	s := clitest.Settings(t)
	config.Save(s.Home, clitest.NodeConfig(s))
	logged := func() string {
		b, _ := os.ReadFile(home.Dir(s.Home).Service().LogFile)
		return string(b)
	}
	if _, err := clitest.Run(t, s, "", "chat", "hello", openaitest.Key); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`: message from the owner: "hello [redacted]"`, ": llm call 1 took ", ": text\n",
		`: answered after `, ` (llm calls: 1, tool calls: 0, sent `, ` KiB): "[gpt-4o] turns=1 last=hello [redacted]"`} {
		if !strings.Contains(logged(), want) {
			t.Fatalf("the log lacks %q:\n%s", want, logged())
		}
	}
	if strings.Contains(logged(), openaitest.Key) {
		t.Fatalf("the key is in the log:\n%s", logged())
	}

	talk.Interrupt(t)
	if _, err := clitest.Run(t, s, "", "chat", "stop"); !errors.Is(err, context.Canceled) || err.Error() != "the answer was stopped" {
		t.Fatalf("interrupted: %v", err)
	}
	if !regexp.MustCompile(`: message from the owner: "stop"\n.*: canceled after [^ ]+ \(llm calls: 0, tool calls: 0, sent 0.0 KiB\)\n$`).MatchString(logged()) {
		t.Fatalf("the last line says the answer was canceled:\n%s", logged())
	}
}

// flakyLLM is the fake API behind a switch: while down is set it fails every
// request the way a provider out of order does, with a text that names what
// no peer should learn.
func flakyLLM(t *testing.T) (baseURL string, down *atomic.Bool) {
	t.Helper()
	target, err := url.Parse(openaitest.NewServer(t))
	if err != nil {
		t.Fatal(err)
	}
	working := httputil.NewSingleHostReverseProxy(target)
	down = new(atomic.Bool)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			io.WriteString(w, `{"error":{"message":"upstream llm-7.internal rejected key sk-good"}}`)
			return
		}
		working.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, down
}

// A peer whose message was not answered asks later what went wrong, with the
// error ID it was given. The agent of the node looks it up and explains it,
// to that peer only, in general terms, and only once it can answer again. It
// is a function of scripts, which a peer runs only with a tool of a2a.
func TestPeerAsksWhatWentWrong(t *testing.T) {
	llm, down := flakyLLM(t)
	beta := clitest.Settings(t)
	beta.BaseURL = llm
	cfg := clitest.NodeConfig(beta)
	cfg.Name = "beta"
	cfg.Tools = &config.Tools{A2A: config.Named("list_peers")}
	clitest.StartNode(t, beta, cfg)
	alpha, gamma := clitest.Settings(t), clitest.Settings(t)
	for name, s := range map[string]cli.Settings{"alpha": alpha, "gamma": gamma} {
		clitest.Finds(s, beta)
		cfg := clitest.NodeConfig(s)
		cfg.Name = name
		if err := config.Save(s.Home, cfg); err != nil {
			t.Fatal(err)
		}
		clitest.Trust(t, beta, s, name)
		clitest.Trust(t, s, beta, "beta")
	}
	// What only beta knows about its failure.
	// The status as the error has it: "503" alone is part of an ID or a time
	// now and then.
	internals := []string{"HTTP 503", "llm-7", "sk-good", "upstream", strings.TrimPrefix(llm, "http://"), "openai", beta.Home}
	clean := func(what, text string) {
		t.Helper()
		for _, internal := range internals {
			if strings.Contains(text, internal) {
				t.Fatalf("%s tells %q: %s", what, internal, text)
			}
		}
	}
	// refused returns the error ID of an answer that failed. Such an answer
	// says that it failed and nothing more: no explanation is at hand.
	told := regexp.MustCompile(`^a2a: the agent could not answer \(error ([A-Z2-7]{8})\) \(-32603\)$`)
	refused := func(err error) string {
		t.Helper()
		if err == nil || told.FindStringSubmatch(err.Error()) == nil {
			t.Fatalf("got %v", err)
		}
		return told.FindStringSubmatch(err.Error())[1]
	}

	down.Store(true)
	_, err := clitest.Run(t, alpha, "", "ask", "beta", "hi")
	id := refused(err)
	// As long as beta's agent cannot answer, it cannot explain either.
	_, err = clitest.Run(t, alpha, "", "ask", "beta", "#"+id)
	if again := refused(err); again == id {
		t.Fatalf("the question about error %s failed under the same ID", id)
	}
	// The agent of alpha's owner is told the ID as well.
	out, err := clitest.Run(t, alpha, "", "chat", "@beta hi")
	if err != nil || !strings.Contains(out, "last=error: a2a: the agent could not answer (error ") {
		t.Fatalf("alpha's agent: %q, %v", out, err)
	}
	clean("the failed answer", out)

	down.Store(false)
	explained := "Error " + id + ": on "
	out, err = clitest.Run(t, alpha, "", "ask", "beta", "#"+id)
	if err != nil || !strings.Contains(out, explained) || !strings.Contains(out, diagnosis.CauseLLMFailed.Explain()) {
		t.Fatalf("beta should explain error %s: %q, %v", id, out, err)
	}
	clean("the explanation", out)
	// Alpha's agent asks for its owner the same way.
	out, err = clitest.Run(t, alpha, "", "chat", "@beta #"+id)
	if err != nil || !strings.Contains(out, explained) {
		t.Fatalf("alpha's agent should get the explanation: %q, %v", out, err)
	}

	// Gamma is connected to beta too, and learns nothing with alpha's ID.
	out, err = clitest.Run(t, gamma, "", "ask", "beta", "#"+id)
	if err != nil || !strings.Contains(out, "No failed answer with the error ID "+id) || strings.Contains(out, explained) {
		t.Fatalf("gamma asked about alpha's error: %q, %v", out, err)
	}

	// What beta keeps is the category of the cause, not the cause.
	kept, err := os.ReadFile(filepath.Join(beta.Home, "failures.json"))
	if err != nil || !strings.Contains(string(kept), id) || !strings.Contains(string(kept), string(diagnosis.CauseLLMFailed)) {
		t.Fatalf("failures of beta: %s, %v", kept, err)
	}
	clean("the list of failures", string(kept))
}

// A node outside the local network is found through a VPN peer.
func TestListFindsNodesOnPeers(t *testing.T) {
	remote := clitest.Settings(t)
	clitest.StartNode(t, remote, clitest.NodeConfig(remote))

	s := clitest.Settings(t) // another discovery port: the remote node does not hear its probes
	if out, _ := clitest.Run(t, s, "", "list"); !strings.Contains(out, "no ouroboros nodes") {
		t.Fatalf("without peers nothing should be found: %q", out)
	}
	clitest.Finds(s, remote)
	if out, err := clitest.Run(t, s, "", "list"); err != nil || !strings.Contains(out, "alpha") {
		t.Fatalf("the VPN peer should be found: %q, %v", out, err)
	}
}

// A node the owner is not connected to gets nothing of the message. The
// owner confirms the connection where requests are decided; then the node
// gets a connection request, and the user is told so.
func TestAskLeavesAConnectionRequest(t *testing.T) {
	alpha, beta := clitest.TwoNodes(t)
	if _, err := clitest.Run(t, alpha, "", "ask", "beta", "hi"); !errors.Is(err, plugin.ErrUnconfirmed) || !strings.HasPrefix(err.Error(), "beta is not connected") {
		t.Fatalf("before the owner decided: %v", err)
	}
	if requests, _ := home.Dir(beta.Home).Contacts().Snapshot(); len(requests.Incoming) != 0 {
		t.Fatalf("beta was asked before the owner of alpha decided: %+v", requests)
	}
	a, _ := clitest.NewApp(t, alpha, "")
	if err := clitest.Network(t, alpha, a).Accept(context.Background(), clitest.PublicKey(t, beta)); err != nil {
		t.Fatal(err)
	}
	if requests, _ := home.Dir(beta.Home).Contacts().Snapshot(); len(requests.Incoming) != 1 || requests.Incoming[0].Name != "alpha" {
		t.Fatalf("beta's requests: %+v", requests)
	}
	if _, err := clitest.Run(t, alpha, "", "ask", "beta", "hi"); err == nil || !strings.Contains(err.Error(), "connection request sent to beta, waiting for approval") {
		t.Fatalf("one-shot: %v", err)
	}
	out, err := clitest.Run(t, alpha, "hi\n", "ask", "beta")
	if err != nil || !strings.Contains(out, "connection request sent — waiting for approval") {
		t.Fatalf("interactive: %q, %v", out, err)
	}
}

// lost finds no nodes: the network is down.
type lost struct{ err error }

func (l lost) Find(context.Context) ([]discovery.Node, error) { return nil, l.err }

func TestListWithoutNetwork(t *testing.T) {
	down := errors.New("network is down")
	list := talk.List(io.Discard, plugin.NetworkOf(&peers.Network{Finder: lost{down}}))
	if err := list(context.Background(), nil); !errors.Is(err, down) {
		t.Fatalf("got %v", err)
	}
}

func TestNotRunning(t *testing.T) {
	if out, _ := clitest.Run(t, clitest.Settings(t), "", "list"); !strings.Contains(out, "no ouroboros nodes") {
		t.Fatalf("list: %q", out)
	}
}

// "ouro chat" answers in a session of the node: it starts a node that is not
// running, and stops it again once the answer is given. A node that ran
// before runs on.
func TestChatStartsTheNode(t *testing.T) {
	s := clitest.Settings(t)
	clitest.Env(t, s)
	chat := func(args ...string) (string, error) {
		t.Helper()
		app, out := clitest.NewApp(t, s, "")
		err := app.Run(context.Background(), append([]string{"chat"}, args...))
		return out.String(), err
	}
	if _, err := chat("hi"); !errors.Is(err, config.ErrIncomplete) || !strings.Contains(err.Error(), "run: ouro start") {
		t.Fatalf("a node that is not set up: %v", err)
	}
	config.Save(s.Home, clitest.NodeConfig(s))
	if out, err := chat("hello"); err != nil || out != "[gpt-4o] turns=1 last=hello\n" {
		t.Fatalf("got %q, %v", out, err)
	}
	if _, running := home.Dir(s.Home).Service().Running(); running {
		t.Fatal("the node the chat started should be stopped")
	}
	clitest.StartNode(t, s, clitest.NodeConfig(s))
	if out, err := chat("again"); err != nil || out != "[gpt-4o] turns=1 last=again\n" {
		t.Fatalf("got %q, %v", out, err)
	}
	if _, running := home.Dir(s.Home).Service().Running(); !running {
		t.Fatal("a node that ran before should run on")
	}
}
