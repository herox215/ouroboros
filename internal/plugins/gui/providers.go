package gui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"ouroboros/internal/credentials"
	"ouroboros/plugin"
)

// The options of the window show the accesses of the node and what each
// scope of it answers with, as the setup and the model screen of the terminal
// UI do, and change them in place: the owner signs in to a provider there, to
// a new access beside the others or anew to one, renames and removes one,
// and picks the model the node, the thread, the sessions and peers answer
// with, the fallbacks and the effort. Every change goes through the Owner, as
// those of the terminal UI do, and holds from the next message on.
//
// Signing in is a dialog: the provider asks as many questions as it needs,
// one after the other, and the page answers them. The page starts it with
// StartLogin and asks LoginStep for what the provider wants next, which waits
// a while for it; AnswerLogin answers what was asked, and CancelLogin gives it
// up. The window and the page in a browser reach it the same way.

// llmOwner is what the options change of the node for its owner: a
// plugin.Owner with the providers the plugins contributed, which tests
// replace.
type llmOwner interface {
	Setup() (*plugin.Setup, error)
	Status() (plugin.NodeStatus, error)
	CurrentLLMs() ([]plugin.LLM, error)
	Fallbacks() ([]plugin.Fallback, error)
	SetModel(access, model string) (bool, error)
	SetThreadModel(access, model string) error
	SetSessionModel(access, model string) error
	SetPeerModel(access, model string) error
	ToggleFallback(access, model string) ([]plugin.Fallback, error)
	SetEffort(effort string) error
	Efforts() ([]string, bool)
}

// ownerLLMs is the llmOwner of a plugin.Owner: the providers are read when
// they are needed, as the plugins may contribute them later.
type ownerLLMs struct {
	*plugin.Owner
	providers func() []plugin.Provider
}

func (o ownerLLMs) Setup() (*plugin.Setup, error) { return o.Owner.Setup(o.providers()) }
func (o ownerLLMs) Efforts() ([]string, bool)     { return o.Owner.Efforts(o.providers()) }

// errNoOptions is what a window answers that cannot change the models of the
// node: one opened without the owner of the node, as tests do.
var errNoOptions = errors.New("this window cannot change the providers of the node")

// The scopes of the node, as the options name them: the owner, which every
// other follows until it has its own, the thread, the sessions and peers.
const (
	scopeOwner    = "owner"
	scopeThread   = "thread"
	scopeSessions = "sessions"
	scopePeers    = "peers"
)

// ProviderInfo is a provider of this ouro the owner signs in with.
type ProviderInfo struct {
	Name  string `json:"name"`
	Title string `json:"title"`
}

// AccessInfo is an access of the node, as the options show it. What it signs
// in with is not part of it.
type AccessInfo struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	// Name is what the owner calls it, "" for nothing; Label what it is
	// shown as, and About what tells it apart, like the account.
	Name  string `json:"name,omitempty"`
	Label string `json:"label"`
	About string `json:"about,omitempty"`
	// Missing says that its provider is not part of this ouro: its plugin is
	// turned off or left out.
	Missing bool `json:"missing,omitempty"`
	// Uses are the scopes that answer through it, see the scopes above, and
	// "fallback" when one of the fallbacks is of it.
	Uses []string `json:"uses,omitempty"`
}

// ProviderList is what the options show of the accesses: the providers to
// sign in with and the accesses the node has, in the order they were added.
type ProviderList struct {
	Providers []ProviderInfo `json:"providers"`
	Accesses  []AccessInfo   `json:"accesses"`
}

// ScopeLLM is what one scope answers with. Own says that the owner chose it
// for the scope; otherwise the scope follows the owner, and Access and Model
// are those of the owner.
type ScopeLLM struct {
	Scope  string `json:"scope"`
	Access string `json:"access"`
	Model  string `json:"model"`
	Own    bool   `json:"own,omitempty"`
}

// FallbackLLM is a model the node goes on with when the one it answers the
// owner with cannot for now.
type FallbackLLM struct {
	Access string `json:"access"`
	Model  string `json:"model"`
}

// LLMList is what the scopes of the node answer with, its fallbacks in their
// order, and how much the model thinks before it answers the owner: Effort,
// "" for what the model does by itself, one of Efforts. EffortsKnown says
// that models.dev lists the model, and Efforts are those it takes then.
type LLMList struct {
	Scopes       []ScopeLLM    `json:"scopes"`
	Fallbacks    []FallbackLLM `json:"fallbacks"`
	Effort       string        `json:"effort"`
	Efforts      []string      `json:"efforts"`
	EffortsKnown bool          `json:"efforts_known,omitempty"`
}

// Providers lists the providers to sign in with and the accesses of the node.
func (w *Window) Providers() (ProviderList, error) { return w.profile.providerList() }

// LLMs tells what the scopes of the node answer with.
func (w *Window) LLMs() (LLMList, error) { return w.profile.llmList() }

// SetLLM makes model of access what scope answers with, from its next
// message on. For any scope but the owner, the access "" has it follow the
// owner again. It returns what the scopes answer with then.
func (w *Window) SetLLM(scope, access, model string) (LLMList, error) {
	return w.profile.setLLM(scope, access, model)
}

// ToggleFallback makes model of access the last of the fallbacks, or none of
// them any more if it is one.
func (w *Window) ToggleFallback(access, model string) (LLMList, error) {
	return w.profile.toggleFallback(access, model)
}

// SetEffort makes effort how much the model thinks before it answers the
// owner.
func (w *Window) SetEffort(effort string) (LLMList, error) { return w.profile.setEffort(effort) }

// RenameAccess calls the access id name, "" for what its service is commonly
// called. Its ID stays, and with it what refers to it.
func (w *Window) RenameAccess(id, name string) (ProviderList, error) {
	return w.profile.renameAccess(id, name)
}

// RemoveAccess removes the access id, see setup.Session.Remove, and returns
// what the owner should know of how it went, "" if nothing.
func (w *Window) RemoveAccess(id string) (string, error) { return w.profile.removeAccess(id) }

// StartLogin signs in to a new access with the provider called provider, or
// anew to the access again if it is not "". It returns the ID of the sign-in,
// for LoginStep.
func (w *Window) StartLogin(provider, again string) (string, error) {
	// The window runs on the desktop of the owner: a page the provider opens
	// is opened there. The page in a browser opens it itself.
	return w.profile.startLogin(provider, again, w.source == "gui")
}

// LoginStep returns what the sign-in id wants next, waiting a while for it:
// a step of the kind "wait" says that it wants nothing yet.
func (w *Window) LoginStep(id string) (LoginStep, error) {
	return w.profile.loginStep(w.ctx, id, loginPoll)
}

// AnswerLogin answers what the sign-in id asks now.
func (w *Window) AnswerLogin(id, answer string) error { return w.profile.answerLogin(id, answer) }

// CancelLogin gives up the sign-in id.
func (w *Window) CancelLogin(id string) error { return w.profile.cancelLogin(id) }

// providerList lists the providers and the accesses, as Window.Providers
// does.
func (p *profile) providerList() (ProviderList, error) {
	if p.llms == nil {
		return ProviderList{}, errNoOptions
	}
	s, err := p.llms.Setup()
	if err != nil {
		return ProviderList{}, err
	}
	accesses, err := s.Accesses()
	if err != nil {
		return ProviderList{}, err
	}
	uses, err := p.uses()
	if err != nil {
		return ProviderList{}, err
	}
	out := ProviderList{Providers: []ProviderInfo{}, Accesses: []AccessInfo{}}
	for _, v := range s.Providers() {
		out.Providers = append(out.Providers, ProviderInfo{Name: v.Name, Title: v.Title})
	}
	for _, a := range accesses {
		out.Accesses = append(out.Accesses, AccessInfo{ID: a.ID, Provider: a.Provider, Name: a.Name, Label: a.Label,
			About: a.About, Missing: a.Missing, Uses: uses[a.ID]})
	}
	return out, nil
}

// uses are the scopes that answer through each access, by its ID.
func (p *profile) uses() (map[string][]string, error) {
	list, err := p.llmList()
	if err != nil {
		return nil, err
	}
	uses := map[string][]string{}
	for _, s := range list.Scopes {
		if s.Scope == scopeOwner || s.Own {
			uses[s.Access] = append(uses[s.Access], s.Scope)
		}
	}
	for _, f := range list.Fallbacks {
		if !slices.Contains(uses[f.Access], "fallback") {
			uses[f.Access] = append(uses[f.Access], "fallback")
		}
	}
	return uses, nil
}

// llmList tells what the scopes answer with, as Window.LLMs does.
func (p *profile) llmList() (LLMList, error) {
	if p.llms == nil {
		return LLMList{}, errNoOptions
	}
	status, err := p.llms.Status()
	if err != nil {
		return LLMList{}, err
	}
	if !status.Configured {
		return LLMList{}, plugin.ErrNotSetUp
	}
	current, err := p.llms.CurrentLLMs()
	if err != nil {
		return LLMList{}, err
	}
	own := map[string]bool{
		scopeOwner:    true,
		scopeThread:   status.ThreadAccess != "",
		scopeSessions: status.SessionAccess != "",
		scopePeers:    status.PeerAccess != "",
	}
	out := LLMList{Scopes: []ScopeLLM{}, Fallbacks: []FallbackLLM{}, Effort: status.Effort}
	for _, l := range current {
		out.Scopes = append(out.Scopes, ScopeLLM{Scope: l.Scope, Access: l.Access, Model: l.Model, Own: own[l.Scope] && l.Scope != scopeOwner})
	}
	fallbacks, err := p.llms.Fallbacks()
	if err != nil {
		return LLMList{}, err
	}
	for _, f := range fallbacks {
		out.Fallbacks = append(out.Fallbacks, FallbackLLM{Access: f.Access, Model: f.Model})
	}
	out.Efforts = slices.Clone(plugin.Efforts)
	if takes, known := p.llms.Efforts(); known {
		out.Efforts, out.EffortsKnown = append([]string{""}, takes...), true
	}
	return out, nil
}

// setLLM changes what a scope answers with, as Window.SetLLM does.
func (p *profile) setLLM(scope, access, model string) (LLMList, error) {
	if p.llms == nil {
		return LLMList{}, errNoOptions
	}
	access, model = strings.TrimSpace(access), strings.TrimSpace(model)
	var err error
	switch scope {
	case scopeOwner:
		if access == "" {
			return LLMList{}, errors.New("the node always answers the owner with a model: choose one")
		}
		_, err = p.llms.SetModel(access, model)
	case scopeThread:
		err = p.llms.SetThreadModel(access, model)
	case scopeSessions:
		err = p.llms.SetSessionModel(access, model)
	case scopePeers:
		err = p.llms.SetPeerModel(access, model)
	default:
		return LLMList{}, fmt.Errorf("the node has no scope %q", scope)
	}
	if err != nil {
		return LLMList{}, err
	}
	return p.llmList()
}

// toggleFallback changes the fallbacks, as Window.ToggleFallback does.
func (p *profile) toggleFallback(access, model string) (LLMList, error) {
	if p.llms == nil {
		return LLMList{}, errNoOptions
	}
	if _, err := p.llms.ToggleFallback(access, model); err != nil {
		return LLMList{}, err
	}
	return p.llmList()
}

// setEffort changes the effort, as Window.SetEffort does.
func (p *profile) setEffort(effort string) (LLMList, error) {
	if p.llms == nil {
		return LLMList{}, errNoOptions
	}
	if err := p.llms.SetEffort(effort); err != nil {
		return LLMList{}, err
	}
	return p.llmList()
}

// renameAccess renames an access, as Window.RenameAccess does.
func (p *profile) renameAccess(id, name string) (ProviderList, error) {
	if p.llms == nil {
		return ProviderList{}, errNoOptions
	}
	if len(name) > maxAccessName {
		return ProviderList{}, fmt.Errorf("the name is longer than %d characters", maxAccessName)
	}
	s, err := p.llms.Setup()
	if err != nil {
		return ProviderList{}, err
	}
	if err := s.Rename(id, name); err != nil {
		return ProviderList{}, accessError(id, err)
	}
	return p.providerList()
}

// maxAccessName is how long the name of an access may be.
const maxAccessName = 80

// removeTime is how long the provider of an access that is removed may take
// to end what it holds at its API.
const removeTime = 30 * time.Second

// removeAccess removes an access, as Window.RemoveAccess does.
func (p *profile) removeAccess(id string) (string, error) {
	if p.llms == nil {
		return "", errNoOptions
	}
	s, err := p.llms.Setup()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), removeTime)
	defer cancel()
	note, err := s.Remove(ctx, id)
	return note, accessError(id, err)
}

// accessError names the access id that is no more in err.
func accessError(id string, err error) error {
	if errors.Is(err, credentials.ErrGone) {
		return fmt.Errorf("the node has no access %q", id)
	}
	return err
}

// LoginQuestion is what a provider asks while it signs in. What a secret
// question offers is not handed out: Known says that the provider has an
// answer already, which an empty answer keeps.
type LoginQuestion struct {
	Title   string   `json:"title"`
	Text    []string `json:"text,omitempty"`
	Default string   `json:"default,omitempty"`
	Hint    string   `json:"hint,omitempty"`
	Secret  bool     `json:"secret,omitempty"`
	Known   bool     `json:"known,omitempty"`
}

// LoginStep is what a sign-in wants of the page, by its Kind:
//
//   - "ask": the page asks Question, and answers it with AnswerLogin;
//   - "tell": the provider tells Text, like what happens next;
//   - "reject": the last answer was not accepted, Text says why;
//   - "open": the page shows the URL Text, for the owner to open;
//   - "done": the access was saved, with the models it offers;
//   - "failed": the sign-in ended without an access, Text says why;
//   - "wait": the provider wants nothing yet.
//
// After "done" and "failed" the sign-in is no more.
type LoginStep struct {
	Kind     string         `json:"kind"`
	Question *LoginQuestion `json:"question,omitempty"`
	Text     string         `json:"text,omitempty"`
	Access   *AccessInfo    `json:"access,omitempty"`
	// Models are the models the access offers to chat with, Default the one
	// to offer of them.
	Models  []string `json:"models,omitempty"`
	Default string   `json:"default,omitempty"`
}

var (
	// loginPoll is how long LoginStep waits for a step; loginIdle how long a
	// sign-in waits for the page to ask for its next one before it is given
	// up, as when the page went away. Tests shorten them.
	loginPoll = 20 * time.Second
	loginIdle = 2 * time.Minute
)

// maxLogins is how many sign-ins may be on their way at once.
const maxLogins = 4

// errNoLogin is the error for a sign-in that is no more, or never was.
var errNoLogin = errors.New("the sign-in is over: start it again")

// logins are the sign-ins of a profile that are on their way, by their ID.
type logins struct {
	mu   sync.Mutex
	runs map[string]*loginRun
}

// loginRun is one sign-in: the provider runs beside the page and asks through
// it, see loginDialog.
type loginRun struct {
	ctx    context.Context
	cancel context.CancelFunc
	browse bool          // a page the provider opens is opened here too
	idle   time.Duration // how long it waits for the page, see loginIdle

	mu    sync.Mutex
	steps []LoginStep
	wake  chan struct{} // closed when a step comes
	reply chan string   // what answers the question asked now, nil while none is
	seen  time.Time     // when the page asked for a step the last time
	ended bool          // the last step came
	over  chan struct{} // closed once the page took the last step
}

// browseURL shows a URL in the browser of the desktop; tests replace it.
var browseURL = plugin.Browse

// startLogin starts a sign-in, as Window.StartLogin does.
func (p *profile) startLogin(provider, again string, browse bool) (string, error) {
	if p.llms == nil {
		return "", errNoOptions
	}
	if strings.TrimSpace(provider) == "" && again == "" {
		return "", errors.New("choose a provider to sign in with")
	}
	s, err := p.llms.Setup()
	if err != nil {
		return "", err
	}
	p.logins.mu.Lock()
	defer p.logins.mu.Unlock()
	if len(p.logins.runs) >= maxLogins {
		return "", errors.New("too many sign-ins are on their way: finish or cancel one first")
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := &loginRun{ctx: ctx, cancel: cancel, browse: browse, wake: make(chan struct{}), seen: time.Now(), over: make(chan struct{}), idle: loginIdle}
	id := credentials.NewID()
	if p.logins.runs == nil {
		p.logins.runs = map[string]*loginRun{}
	}
	p.logins.runs[id] = r
	go r.watch(func() { p.dropLogin(id, r) })
	go r.run(s, provider, again)
	return id, nil
}

// run signs in and saves the access, and tells the page how it went.
func (r *loginRun) run(s *plugin.Setup, provider, again string) {
	d := loginDialog{r}
	var models plugin.SetupModels
	var err error
	if again != "" {
		models, err = s.Again(r.ctx, again, d)
	} else {
		models, err = s.Login(r.ctx, provider, d)
	}
	var saved plugin.SetupAccess
	if err == nil {
		saved, err = s.Keep()
	}
	if err != nil {
		if r.ctx.Err() != nil {
			err = errors.New("the sign-in was given up")
		}
		r.end(LoginStep{Kind: "failed", Text: accessError(again, err).Error()})
		return
	}
	access := AccessInfo{ID: saved.ID, Provider: saved.Provider, Name: saved.Name, Label: saved.Label, About: saved.About}
	r.end(LoginStep{Kind: "done", Access: &access, Models: append([]string{}, models.Chat...), Default: models.Default})
}

// watch gives the sign-in up once the page stopped asking for its steps, and
// forgets it then with drop. One whose last step was taken is forgotten by
// loginStep.
func (r *loginRun) watch(drop func()) {
	tick := time.NewTicker(min(r.idle/4, 15*time.Second))
	defer tick.Stop()
	for {
		select {
		case <-r.over:
			return
		case <-tick.C:
			r.mu.Lock()
			idle := time.Since(r.seen) > r.idle
			r.mu.Unlock()
			if idle {
				r.cancel()
				drop()
				return
			}
		}
	}
}

// dropLogin forgets the sign-in id once it is over, unless another took its
// place.
func (p *profile) dropLogin(id string, r *loginRun) {
	p.logins.mu.Lock()
	defer p.logins.mu.Unlock()
	if p.logins.runs[id] == r {
		delete(p.logins.runs, id)
	}
}

// login is the sign-in id that is on its way.
func (p *profile) login(id string) (*loginRun, error) {
	p.logins.mu.Lock()
	defer p.logins.mu.Unlock()
	r := p.logins.runs[id]
	if r == nil {
		return nil, errNoLogin
	}
	return r, nil
}

// push hands step to the page.
func (r *loginRun) push(step LoginStep) { r.add(step, false) }

// end hands the last step to the page: the sign-in waits for it to be asked
// for, and is over then.
func (r *loginRun) end(step LoginStep) { r.add(step, true) }

func (r *loginRun) add(step LoginStep, last bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ended {
		return
	}
	r.steps = append(r.steps, step)
	r.ended = last
	close(r.wake)
	r.wake = make(chan struct{})
}

// loginStep returns the next step of the sign-in id, as Window.LoginStep does,
// waiting up to wait for one.
func (p *profile) loginStep(ctx context.Context, id string, wait time.Duration) (LoginStep, error) {
	r, err := p.login(id)
	if err != nil {
		return LoginStep{}, err
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	given := r.ctx.Done()
	for {
		r.mu.Lock()
		r.seen = time.Now()
		if len(r.steps) > 0 {
			step := r.steps[0]
			r.steps = r.steps[1:]
			last := r.ended && len(r.steps) == 0
			r.mu.Unlock()
			if last {
				r.cancel()
				close(r.over) // taken once: the steps are empty from now on
				p.dropLogin(id, r)
			}
			return step, nil
		}
		wake := r.wake
		r.mu.Unlock()
		select {
		case <-wake:
		case <-timer.C:
			return LoginStep{Kind: "wait"}, nil
		case <-ctx.Done():
			return LoginStep{}, ctx.Err()
		case <-given:
			// Given up: what it ends with may still be on its way, and is
			// waited for like any step.
			r.mu.Lock()
			over := r.ended && len(r.steps) == 0
			r.mu.Unlock()
			if over {
				return LoginStep{}, errNoLogin
			}
			given = nil
		}
	}
}

// answerLogin answers what the sign-in id asks now, as Window.AnswerLogin
// does.
func (p *profile) answerLogin(id, answer string) error {
	r, err := p.login(id)
	if err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.reply == nil {
		return errors.New("the sign-in asks nothing now")
	}
	r.reply <- answer // room for one: it is taken once
	r.reply = nil
	return nil
}

// cancelLogin gives the sign-in id up, as Window.CancelLogin does. One that
// is over already is no error.
func (p *profile) cancelLogin(id string) error {
	r, err := p.login(id)
	if err != nil {
		return nil
	}
	r.cancel()
	return nil
}

// loginDialog is the plugin.Dialog of a sign-in: it hands what the provider
// wants to the page and waits for the answer.
type loginDialog struct{ r *loginRun }

func (d loginDialog) Ask(ctx context.Context, q plugin.Question) (string, error) {
	reply := make(chan string, 1)
	shown := LoginQuestion{Title: q.Title, Text: q.Text, Hint: q.Hint, Secret: q.Secret, Known: q.Known}
	if !q.Secret {
		shown.Default = q.Default
	} else if q.Default != "" {
		// A secret the provider has is kept with an empty answer.
		shown.Known = true
	}
	d.r.mu.Lock()
	d.r.reply = reply
	d.r.mu.Unlock()
	d.r.push(LoginStep{Kind: "ask", Question: &shown})
	select {
	case answer := <-reply:
		if answer == "" {
			return q.Default, nil
		}
		return answer, nil
	case <-ctx.Done():
		return "", ctx.Err()
	case <-d.r.ctx.Done():
		return "", d.r.ctx.Err()
	}
}

func (d loginDialog) Tell(text string)  { d.r.push(LoginStep{Kind: "tell", Text: text}) }
func (d loginDialog) Reject(why string) { d.r.push(LoginStep{Kind: "reject", Text: why}) }

func (d loginDialog) Open(url string) {
	if d.r.browse {
		_ = browseURL(url) // without a browser here, the link on the page is what there is
	}
	d.r.push(LoginStep{Kind: "open", Text: url})
}
