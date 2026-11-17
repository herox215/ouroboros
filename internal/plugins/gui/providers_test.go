package gui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"ouroboros/internal/config"
	"ouroboros/internal/home"
	"ouroboros/internal/llm/llmtest"
	"ouroboros/internal/setup"
	"ouroboros/plugin"
)

// optionsProfile is the profile of a node that answers with one-large of an
// access of the test provider "one", with the providers "one" and "two", and
// the ID of that access.
func optionsProfile(t *testing.T) (*profile, string) {
	t.Helper()
	dir := home.Dir(t.TempDir())
	providers := func() []plugin.Provider {
		return []plugin.Provider{llmtest.Provider("one"), llmtest.Provider("two")}
	}
	s := setup.New(dir, providers, config.Config{})
	if _, err := s.Login(context.Background(), "one", &llmtest.Dialog{Answers: map[string][]string{"Token": {"good-1"}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Save("one-large", "alpha"); err != nil {
		t.Fatal(err)
	}
	list, err := s.Accesses()
	if err != nil || len(list) != 1 {
		t.Fatalf("accesses %+v, %v", list, err)
	}
	owner := plugin.OwnerOf(dir, nil)
	p := &profile{dir: dir, status: owner.Status, llms: ownerLLMs{Owner: owner, providers: providers}}
	return p, list[0].ID
}

// step waits for the next step of the sign-in id that is no "wait".
func step(t *testing.T, p *profile, id string) LoginStep {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		s, err := p.loginStep(context.Background(), id, 200*time.Millisecond)
		if err != nil {
			t.Fatal(err)
		}
		if s.Kind != "wait" {
			return s
		}
	}
	t.Fatal("the sign-in took no step")
	return LoginStep{}
}

// The options list the providers and the accesses, with what answers through
// each.
func TestProviderList(t *testing.T) {
	p, one := optionsProfile(t)
	list, err := p.providerList()
	if err != nil {
		t.Fatal(err)
	}
	if want := []ProviderInfo{{"one", "Test one"}, {"two", "Test two"}}; !reflect.DeepEqual(list.Providers, want) {
		t.Fatalf("providers %+v", list.Providers)
	}
	if len(list.Accesses) != 1 || list.Accesses[0].ID != one || list.Accesses[0].Provider != "one" ||
		!reflect.DeepEqual(list.Accesses[0].Uses, []string{"owner"}) || list.Accesses[0].About != "account good-1" {
		t.Fatalf("accesses %+v", list.Accesses)
	}
}

// The owner signs in to another provider in the options: what it asks comes
// to the page one step after the other, an answer it refuses is told, and the
// access is saved beside the other without the node answering through it.
func TestLoginAddsAnAccess(t *testing.T) {
	p, one := optionsProfile(t)
	id, err := p.startLogin("two", "", false)
	if err != nil {
		t.Fatal(err)
	}
	ask := step(t, p, id)
	if ask.Kind != "ask" || ask.Question == nil || ask.Question.Title != "Token" || !ask.Question.Secret {
		t.Fatalf("first step %+v", ask)
	}
	if err := p.answerLogin(id, "bad"); err != nil {
		t.Fatal(err)
	}
	if err := p.answerLogin(id, "twice"); err == nil {
		t.Fatal("answered what is not asked")
	}
	if s := step(t, p, id); s.Kind != "reject" || s.Text != "wrong token" {
		t.Fatalf("rejected %+v", s)
	}
	if s := step(t, p, id); s.Kind != "ask" {
		t.Fatalf("asked again %+v", s)
	}
	if err := p.answerLogin(id, "good-2"); err != nil {
		t.Fatal(err)
	}
	done := step(t, p, id)
	if done.Kind != "done" || done.Access == nil || done.Access.Provider != "two" || done.Access.ID == one ||
		!reflect.DeepEqual(done.Models, []string{"two-small", "two-large"}) || done.Default != "two-small" {
		t.Fatalf("done %+v %+v", done, done.Access)
	}
	if _, err := p.loginStep(context.Background(), id, time.Millisecond); !errors.Is(err, errNoLogin) {
		t.Fatalf("a sign-in that is over takes a step: %v", err)
	}
	list, err := p.providerList()
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Accesses) != 2 || list.Accesses[1].ID != done.Access.ID || len(list.Accesses[1].Uses) != 0 {
		t.Fatalf("accesses %+v", list.Accesses)
	}
	llms, err := p.llmList()
	if err != nil {
		t.Fatal(err)
	}
	if llms.Scopes[0] != (ScopeLLM{Scope: scopeOwner, Access: one, Model: "one-large"}) {
		t.Fatalf("the node answers with %+v", llms.Scopes[0])
	}
}

// Signing in anew to an access keeps it, and what answers through it.
func TestLoginAgain(t *testing.T) {
	p, one := optionsProfile(t)
	id, err := p.startLogin("", one, false)
	if err != nil {
		t.Fatal(err)
	}
	if s := step(t, p, id); s.Kind != "ask" {
		t.Fatalf("first step %+v", s)
	}
	if err := p.answerLogin(id, "good-3"); err != nil {
		t.Fatal(err)
	}
	done := step(t, p, id)
	if done.Kind != "done" || done.Access.ID != one || done.Access.About != "account good-3" {
		t.Fatalf("done %+v", done)
	}
	if _, err := p.startLogin("", "none", false); err != nil {
		t.Fatal(err) // the access is looked up when the provider runs
	}
}

// A sign-in that is given up, by the owner or by a page that went away, ends
// without an access.
func TestLoginGivenUp(t *testing.T) {
	p, _ := optionsProfile(t)
	id, err := p.startLogin("two", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if s := step(t, p, id); s.Kind != "ask" {
		t.Fatalf("first step %+v", s)
	}
	if err := p.cancelLogin(id); err != nil {
		t.Fatal(err)
	}
	if s := step(t, p, id); s.Kind != "failed" || !strings.Contains(s.Text, "given up") {
		t.Fatalf("given up %+v", s)
	}
	if err := p.cancelLogin(id); err != nil {
		t.Fatalf("a sign-in that is over is canceled with %v", err)
	}

	idle := loginIdle
	loginIdle = 50 * time.Millisecond
	t.Cleanup(func() { loginIdle = idle })
	id, err = p.startLogin("two", "", false)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := p.login(id); err != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("a sign-in nobody asks for stays")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if list, _ := p.providerList(); len(list.Accesses) != 1 {
		t.Fatalf("accesses %+v", list.Accesses)
	}
}

// Only so many sign-ins are on their way at once, and one needs a provider.
func TestLoginsAreLimited(t *testing.T) {
	p, _ := optionsProfile(t)
	if _, err := p.startLogin(" ", "", false); err == nil {
		t.Fatal("signed in without a provider")
	}
	var ids []string
	for range maxLogins {
		id, err := p.startLogin("two", "", false)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if _, err := p.startLogin("two", "", false); err == nil {
		t.Fatal("more sign-ins than allowed")
	}
	for _, id := range ids {
		p.cancelLogin(id)
		for step(t, p, id).Kind != "failed" {
		}
	}
	if _, err := p.startLogin("two", "", false); err != nil {
		t.Fatal(err)
	}
}

// A page the provider opens is opened on the desktop by the window alone, and
// shown either way.
func TestLoginOpens(t *testing.T) {
	var opened []string
	browse := browseURL
	browseURL = func(url string) error {
		opened = append(opened, url)
		return nil
	}
	t.Cleanup(func() { browseURL = browse })
	for _, desktop := range []bool{true, false} {
		r := &loginRun{ctx: context.Background(), browse: desktop, wake: make(chan struct{})}
		loginDialog{r}.Open("https://example.test/login")
		if len(r.steps) != 1 || !reflect.DeepEqual(r.steps[0], LoginStep{Kind: "open", Text: "https://example.test/login"}) {
			t.Fatalf("steps %+v", r.steps)
		}
	}
	if !reflect.DeepEqual(opened, []string{"https://example.test/login"}) {
		t.Fatalf("opened %v", opened)
	}
}

// A secret the provider has is not handed to the page: an empty answer keeps
// it. What is not secret is offered.
func TestLoginKeepsSecrets(t *testing.T) {
	r := &loginRun{ctx: context.Background(), wake: make(chan struct{})}
	p := &profile{logins: logins{runs: map[string]*loginRun{"x": r}}}
	for _, q := range []plugin.Question{{Title: "Key", Default: "sk-saved", Secret: true}, {Title: "URL", Default: "http://host"}} {
		got := make(chan string)
		go func() {
			answer, _ := loginDialog{r}.Ask(context.Background(), q)
			got <- answer
		}()
		s := step(t, p, "x")
		if q.Secret && (s.Question.Default != "" || !s.Question.Known) || !q.Secret && s.Question.Default != "http://host" {
			t.Fatalf("asked %+v", s.Question)
		}
		if err := p.answerLogin("x", ""); err != nil {
			t.Fatal(err)
		}
		if answer := <-got; answer != q.Default {
			t.Fatalf("an empty answer is %q", answer)
		}
	}
}

// Each scope answers with what the owner picks for it, follows the owner
// again, and the owner always has a model.
func TestSetLLM(t *testing.T) {
	p, one := optionsProfile(t)
	if _, err := p.setLLM(scopeThread, one, "one-small"); err != nil {
		t.Fatal(err)
	}
	list, err := p.setLLM(scopePeers, one, "one-small")
	if err != nil {
		t.Fatal(err)
	}
	want := []ScopeLLM{
		{Scope: scopeOwner, Access: one, Model: "one-large"},
		{Scope: scopeSessions, Access: one, Model: "one-large"},
		{Scope: scopeThread, Access: one, Model: "one-small", Own: true},
		{Scope: scopePeers, Access: one, Model: "one-small", Own: true},
	}
	if !reflect.DeepEqual(list.Scopes, want) {
		t.Fatalf("scopes %+v", list.Scopes)
	}
	providers, _ := p.providerList()
	if !reflect.DeepEqual(providers.Accesses[0].Uses, []string{"owner", "thread", "peers"}) {
		t.Fatalf("uses %v", providers.Accesses[0].Uses)
	}
	if list, err = p.setLLM(scopeThread, "", ""); err != nil || list.Scopes[2].Own || list.Scopes[2].Model != "one-large" {
		t.Fatalf("the thread follows the owner again: %+v, %v", list.Scopes, err)
	}
	if list, err = p.setLLM(scopeOwner, one, "one-small"); err != nil || list.Scopes[0].Model != "one-small" || list.Scopes[1].Model != "one-small" {
		t.Fatalf("the owner %+v, %v", list.Scopes, err)
	}
	if _, err := p.setLLM(scopeOwner, "", ""); err == nil {
		t.Fatal("the owner is left without a model")
	}
	if _, err := p.setLLM("nobody", one, "one-small"); err == nil {
		t.Fatal("a scope that is none")
	}
	if _, err := p.setLLM(scopeSessions, one, " "); err == nil {
		t.Fatal("a model that is none")
	}
}

// The fallbacks and the effort are changed in the options too.
func TestFallbacksAndEffort(t *testing.T) {
	p, one := optionsProfile(t)
	list, err := p.toggleFallback(one, "one-small")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(list.Fallbacks, []FallbackLLM{{one, "one-small"}}) {
		t.Fatalf("fallbacks %+v", list.Fallbacks)
	}
	if providers, _ := p.providerList(); !slices.Contains(providers.Accesses[0].Uses, "fallback") {
		t.Fatalf("uses %v", providers.Accesses[0].Uses)
	}
	if _, err := p.toggleFallback(one, "one-large"); err == nil {
		t.Fatal("the model in use is a fallback of itself")
	}
	if list, err = p.toggleFallback(one, "one-small"); err != nil || len(list.Fallbacks) != 0 {
		t.Fatalf("fallbacks %+v, %v", list.Fallbacks, err)
	}

	if list.Effort != "" || !reflect.DeepEqual(list.Efforts, plugin.Efforts) || list.EffortsKnown {
		t.Fatalf("effort %q of %v", list.Effort, list.Efforts)
	}
	if list, err = p.setEffort("high"); err != nil || list.Effort != "high" {
		t.Fatalf("effort %q, %v", list.Effort, err)
	}
	if _, err := p.setEffort("loud"); err == nil {
		t.Fatal("an effort that is none")
	}
}

// An access is renamed and removed in the options; the last one stays.
func TestRenameAndRemoveAccess(t *testing.T) {
	p, one := optionsProfile(t)
	list, err := p.renameAccess(one, "Private")
	if err != nil || list.Accesses[0].Label != "Private" {
		t.Fatalf("renamed %+v, %v", list.Accesses, err)
	}
	if _, err := p.renameAccess(one, strings.Repeat("x", maxAccessName+1)); err == nil {
		t.Fatal("a name that is too long")
	}
	if _, err := p.renameAccess("none", "x"); err == nil || !strings.Contains(err.Error(), `"none"`) {
		t.Fatalf("renamed an access that is none: %v", err)
	}
	if _, err := p.removeAccess(one); !errors.Is(err, plugin.ErrLastAccess) {
		t.Fatalf("removed the last access: %v", err)
	}

	id, _ := p.startLogin("two", "", false)
	step(t, p, id)
	p.answerLogin(id, "good-2")
	done := step(t, p, id)
	note, err := p.removeAccess(one)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(note, "two-small") {
		t.Fatalf("note %q", note)
	}
	llms, _ := p.llmList()
	if llms.Scopes[0] != (ScopeLLM{Scope: scopeOwner, Access: done.Access.ID, Model: "two-small"}) {
		t.Fatalf("the node answers with %+v", llms.Scopes[0])
	}
}

// A window without the owner changes nothing.
func TestOptionsNeedTheOwner(t *testing.T) {
	p := &profile{}
	if _, err := p.providerList(); !errors.Is(err, errNoOptions) {
		t.Fatal(err)
	}
	if _, err := p.llmList(); !errors.Is(err, errNoOptions) {
		t.Fatal(err)
	}
	if _, err := p.setLLM(scopeOwner, "a", "m"); !errors.Is(err, errNoOptions) {
		t.Fatal(err)
	}
	if _, err := p.toggleFallback("a", "m"); !errors.Is(err, errNoOptions) {
		t.Fatal(err)
	}
	if _, err := p.setEffort(""); !errors.Is(err, errNoOptions) {
		t.Fatal(err)
	}
	if _, err := p.renameAccess("a", "b"); !errors.Is(err, errNoOptions) {
		t.Fatal(err)
	}
	if _, err := p.removeAccess("a"); !errors.Is(err, errNoOptions) {
		t.Fatal(err)
	}
	if _, err := p.startLogin("one", "", false); !errors.Is(err, errNoOptions) {
		t.Fatal(err)
	}
	if _, err := p.loginStep(context.Background(), "x", time.Millisecond); !errors.Is(err, errNoLogin) {
		t.Fatal(err)
	}
	if err := p.answerLogin("x", ""); !errors.Is(err, errNoLogin) {
		t.Fatal(err)
	}
}

// The window binds the options, and opens a page a provider opens on the
// desktop; the page in a browser has them as requests, which only the page
// itself sends.
func TestOptionsOfTheWindowAndThePage(t *testing.T) {
	p, one := optionsProfile(t)
	w := newWindow(p, "alpha")
	t.Cleanup(w.Close)
	if list, err := w.Providers(); err != nil || len(list.Accesses) != 1 {
		t.Fatalf("%+v, %v", list, err)
	}
	if list, err := w.SetLLM(scopeSessions, one, "one-small"); err != nil || !list.Scopes[1].Own {
		t.Fatalf("%+v, %v", list, err)
	}
	id, err := w.StartLogin("two", "")
	if err != nil || !p.logins.runs[id].browse {
		t.Fatalf("the window opens no page: %v", err)
	}
	if s, err := w.LoginStep(id); err != nil || s.Kind != "ask" {
		t.Fatalf("%+v, %v", s, err)
	}
	if err := w.CancelLogin(id); err != nil {
		t.Fatal(err)
	}

	front := newWebFront(p.dir, p)
	handlers := map[string]http.HandlerFunc{
		"/api/providers":     front.providers,
		"/api/llms":          front.llms,
		"/api/llm":           front.setLLM,
		"/api/fallback":      front.toggleFallback,
		"/api/effort":        front.setEffort,
		"/api/access/rename": front.renameAccess,
		"/api/access/remove": front.removeAccess,
		"/api/login/start":   front.startLogin,
		"/api/login/step":    front.loginStep,
		"/api/login/answer":  front.answerLogin,
		"/api/login/cancel":  front.cancelLogin,
	}
	post := func(path, body string, page bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		if page {
			req.Header.Set(webHeader, "1")
		}
		rec := httptest.NewRecorder()
		handlers[path](rec, req)
		return rec
	}
	// What changes the node is sent by the page alone, and read as it is.
	for path := range handlers {
		if path == "/api/providers" || path == "/api/llms" {
			continue
		}
		if rec := post(path, `{}`, false); rec.Code != http.StatusForbidden {
			t.Fatalf("%s without the page is answered %d", path, rec.Code)
		}
		if rec := post(path, `not json`, true); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s of no JSON is answered %d", path, rec.Code)
		}
	}
	for _, path := range []string{"/api/providers", "/api/llms"} {
		if rec := post(path, ``, false); rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
		}
	}
	if rec := post("/api/llm", `{"scope":"thread","access":"`+one+`","model":"one-small"}`, true); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"own":true`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := post("/api/fallback", `{"access":"`+one+`","model":"one-small"}`, true); !strings.Contains(rec.Body.String(), `"fallbacks":[{`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := post("/api/effort", `{"effort":"low"}`, true); !strings.Contains(rec.Body.String(), `"effort":"low"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := post("/api/access/rename", `{"id":"none","name":"x"}`, true); rec.Code != http.StatusConflict {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := post("/api/access/remove", `{"id":"`+one+`"}`, true); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "last access") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	rec := post("/api/login/start", `{"provider":"two"}`, true)
	var started struct{ ID string }
	if err := json.Unmarshal(rec.Body.Bytes(), &started); err != nil || started.ID == "" || p.logins.runs[started.ID].browse {
		t.Fatalf("%s: %v", rec.Body, err)
	}
	if rec := post("/api/login/step", `{"id":"`+started.ID+`"}`, true); !strings.Contains(rec.Body.String(), `"kind":"ask"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := post("/api/login/answer", `{"id":"`+started.ID+`","answer":"good-9"}`, true); rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := post("/api/login/step", `{"id":"`+started.ID+`"}`, true); !strings.Contains(rec.Body.String(), `"kind":"done"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if rec := post("/api/login/cancel", `{"id":"`+started.ID+`"}`, true); rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	for _, route := range []string{"GET /api/providers", "GET /api/llms", "POST /api/llm", "POST /api/fallback", "POST /api/effort",
		"POST /api/access/rename", "POST /api/access/remove", "POST /api/login/start", "POST /api/login/step",
		"POST /api/login/answer", "POST /api/login/cancel"} {
		method, path, _ := strings.Cut(route, " ")
		req := httptest.NewRequest(method, path, nil)
		rec := httptest.NewRecorder()
		front.handler.ServeHTTP(rec, req)
		if rec.Code == http.StatusNotFound || rec.Code == http.StatusMethodNotAllowed {
			t.Fatalf("%s is answered %d", route, rec.Code)
		}
	}
}

// The window binds every option of the node.
func TestWindowBindsTheOptions(t *testing.T) {
	p, one := optionsProfile(t)
	w := newWindow(p, "alpha")
	t.Cleanup(w.Close)
	if list, err := w.LLMs(); err != nil || len(list.Scopes) != 4 {
		t.Fatalf("%+v, %v", list, err)
	}
	if list, err := w.ToggleFallback(one, "one-small"); err != nil || len(list.Fallbacks) != 1 {
		t.Fatalf("%+v, %v", list, err)
	}
	if list, err := w.SetEffort("max"); err != nil || list.Effort != "max" {
		t.Fatalf("%+v, %v", list, err)
	}
	if list, err := w.RenameAccess(one, "Mine"); err != nil || list.Accesses[0].Label != "Mine" {
		t.Fatalf("%+v, %v", list, err)
	}
	if _, err := w.RemoveAccess(one); !errors.Is(err, plugin.ErrLastAccess) {
		t.Fatal(err)
	}
	id, err := w.StartLogin("two", "")
	if err != nil {
		t.Fatal(err)
	}
	step(t, p, id)
	if err := w.AnswerLogin(id, "good-4"); err != nil {
		t.Fatal(err)
	}
	if s := step(t, p, id); s.Kind != "done" {
		t.Fatalf("%+v", s)
	}
}

// brokenOwner is the owner of a node whose options cannot be had: each call
// named in fail fails.
type brokenOwner struct {
	llmOwner
	fail       map[string]bool
	configured bool
	efforts    []string
}

var errBroken = errors.New("broken")

func (b brokenOwner) Setup() (*plugin.Setup, error) {
	if b.fail["Setup"] {
		return nil, errBroken
	}
	return b.llmOwner.Setup()
}

func (b brokenOwner) Status() (plugin.NodeStatus, error) {
	if b.fail["Status"] {
		return plugin.NodeStatus{}, errBroken
	}
	status, err := b.llmOwner.Status()
	status.Configured = b.configured
	return status, err
}

func (b brokenOwner) CurrentLLMs() ([]plugin.LLM, error) {
	if b.fail["CurrentLLMs"] {
		return nil, errBroken
	}
	return b.llmOwner.CurrentLLMs()
}

func (b brokenOwner) Fallbacks() ([]plugin.Fallback, error) {
	if b.fail["Fallbacks"] {
		return nil, errBroken
	}
	return b.llmOwner.Fallbacks()
}

func (b brokenOwner) Efforts() ([]string, bool) {
	if b.efforts != nil {
		return b.efforts, true
	}
	return b.llmOwner.Efforts()
}

// What cannot be read is told, and nothing is changed then.
func TestOptionsThatCannotBeRead(t *testing.T) {
	p, one := optionsProfile(t)
	owner := p.llms
	broken := func(configured bool, fail ...string) {
		b := brokenOwner{llmOwner: owner, fail: map[string]bool{}, configured: configured}
		for _, f := range fail {
			b.fail[f] = true
		}
		p.llms = b
	}
	broken(true, "Setup")
	if _, err := p.providerList(); !errors.Is(err, errBroken) {
		t.Fatal(err)
	}
	if _, err := p.renameAccess(one, "x"); !errors.Is(err, errBroken) {
		t.Fatal(err)
	}
	if _, err := p.removeAccess(one); !errors.Is(err, errBroken) {
		t.Fatal(err)
	}
	if _, err := p.startLogin("two", "", false); !errors.Is(err, errBroken) {
		t.Fatal(err)
	}
	for _, fail := range []string{"Status", "CurrentLLMs", "Fallbacks"} {
		broken(true, fail)
		if _, err := p.llmList(); !errors.Is(err, errBroken) {
			t.Fatalf("%s: %v", fail, err)
		}
	}
	broken(true, "Status")
	if _, err := p.providerList(); !errors.Is(err, errBroken) {
		t.Fatal(err)
	}
	broken(false)
	if _, err := p.llmList(); !errors.Is(err, plugin.ErrNotSetUp) {
		t.Fatal(err)
	}
	p.llms = brokenOwner{llmOwner: owner, configured: true, efforts: []string{"low", "high"}}
	if list, err := p.llmList(); err != nil || !list.EffortsKnown || !reflect.DeepEqual(list.Efforts, []string{"", "low", "high"}) {
		t.Fatalf("%+v, %v", list, err)
	}

	// The accesses of a node whose store cannot be read.
	p.llms = owner
	if err := os.WriteFile(filepath.Join(string(p.dir.Root()), "credentials.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := p.providerList(); err == nil {
		t.Fatal("listed the accesses of a store that cannot be read")
	}
}

// A sign-in that was given up tells what it ended with, and waits for it while
// it is on its way; one that is over and taken tells nothing more. The page
// that goes away stops waiting.
func TestLoginStepsOfASignInGivenUp(t *testing.T) {
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	p := &profile{}
	run := func(ended bool) *loginRun {
		ctx, stop := context.WithCancel(context.Background())
		r := &loginRun{ctx: ctx, cancel: stop, wake: make(chan struct{}), over: make(chan struct{}), ended: ended}
		p.logins = logins{runs: map[string]*loginRun{"x": r}}
		return r
	}

	r := run(false)
	if _, err := p.loginStep(gone, "x", time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("a page that went away: %v", err)
	}
	if s, err := p.loginStep(context.Background(), "x", time.Millisecond); err != nil || s.Kind != "wait" {
		t.Fatalf("%+v, %v", s, err)
	}
	r.cancel()
	if s, err := p.loginStep(context.Background(), "x", 10*time.Millisecond); err != nil || s.Kind != "wait" {
		t.Fatalf("given up, its end on its way: %+v, %v", s, err)
	}
	if _, err := p.loginStep(gone, "x", time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("a page that went away: %v", err)
	}
	go func() {
		time.Sleep(10 * time.Millisecond)
		r.end(LoginStep{Kind: "failed", Text: "given up"})
		r.push(LoginStep{Kind: "tell", Text: "too late"})
	}()
	if s, err := p.loginStep(context.Background(), "x", time.Second); err != nil || s.Kind != "failed" {
		t.Fatalf("%+v, %v", s, err)
	}

	r = run(true)
	r.cancel()
	if _, err := p.loginStep(context.Background(), "x", time.Second); !errors.Is(err, errNoLogin) {
		t.Fatalf("a sign-in that is over: %v", err)
	}
	loginDialog{r}.Tell("told after the end")
	if len(r.steps) != 0 {
		t.Fatalf("steps after the end: %+v", r.steps)
	}
	r = run(false)
	loginDialog{r}.Tell("one moment")
	if !reflect.DeepEqual(r.steps, []LoginStep{{Kind: "tell", Text: "one moment"}}) {
		t.Fatalf("steps %+v", r.steps)
	}
}
