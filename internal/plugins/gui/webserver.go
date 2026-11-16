package gui

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"ouroboros/internal/home"
	"ouroboros/internal/web"
)

// The cookies the page keeps in the browser. The session is the one a login
// won; the client tells the windows of the browsers apart, so that two of them
// do not share one history cursor. A tab names itself: see clientHeader and
// clientParam, and the cookie is only what a request without a name falls back
// to.
const (
	sessionCookie = "ouro_web"
	clientCookie  = "ouro_web_client"
	// clientHeader is how the page names its tab on the requests of the app.
	clientHeader = "X-Ouro-Client"
	// clientParam is the same name in the query of the stream: an EventSource
	// takes no header.
	clientParam = "client"
)

const (
	// webWatch is how often the page looks whether the owner turned it on or
	// off, or moved it to another port. The configuration is a small file,
	// and this is what makes a change hold without the node starting anew.
	webWatch = 2 * time.Second
	// webIdle is how long a browser that did nothing keeps its window: each
	// one holds the last three hours of the thread in memory. A browser with
	// a stream open is not idle: its tab is still on the page.
	webIdle = 2 * time.Hour
	// webQueued is how many events a browser that is connected but behind
	// keeps, and how many are held for one that reconnects.
	webQueued = 256
	// webHeader is the header a request of the page carries. A form of
	// another site cannot set it, which is what keeps the cookies of the
	// owner out of reach of one.
	webHeader = "X-Ouro-Web"
	// shutdownGrace is how long a browser may keep a request open while the
	// page stops.
	shutdownGrace = 2 * time.Second
)

// webFront serves the thread to the network as a page a browser shows: the
// same conversation the window has, behind a login of its own. It runs in the
// process of the node, and only while that serves.
type webFront struct {
	dir      home.Dir
	profile  *profile
	name     string
	auth     *web.Sessions
	throttle *web.Throttle // a name, and the address it came from
	callers  *web.Throttle // the address, whatever name it tries
	clients  *webClients
	media    http.Handler // the pictures of the window, behind the login
	handler  http.Handler
	logf     func(format string, args ...any)

	mu sync.Mutex
	// ctx is the watch that keeps the page in step with the configuration,
	// nil while Serve is not running. done is closed once that watch has
	// ended, so that a Stop is over before the next Serve starts listening.
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	srv    *http.Server
	// port is where it serves, 0 while it does not.
	port int
}

// newWebFront returns the page of the thread of p, kept in dir.
func newWebFront(dir home.Dir, p *profile) *webFront {
	name := "Ouroboros"
	if status, err := p.status(); err == nil && status.Name != "" {
		name = status.Name
	}
	w := &webFront{
		dir:      dir,
		profile:  p,
		name:     name,
		auth:     web.NewSessions(web.SessionTTL),
		throttle: web.NewThrottle(5, time.Minute, 15*time.Minute),
		// More room than a name: an address that shares its network may try
		// a few names, but a spray of them is what keeps the node hashing.
		callers: web.NewThrottle(10, time.Minute, 15*time.Minute),
		clients: newWebClients(),
		media:   pictures{dir: dir.Pictures()}.handler(),
		logf:    dir.Log,
	}
	w.handler = w.routes()
	return w
}

// Serve keeps the page in step with the configuration: served while the owner
// turned it on and gave it a user and a password, on the port it says. It
// returns at once; Close ends what it started, and a later Serve listens anew.
func (w *webFront) Serve() {
	w.mu.Lock()
	if w.ctx != nil {
		w.mu.Unlock()
		return // already watching
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	w.ctx, w.cancel, w.done = ctx, cancel, done
	w.mu.Unlock()
	go w.watch(ctx, done)
}

// Close stops the page and every browser that is on it: the node stopped
// serving. It waits for the watch that reconciles the page with the
// configuration to end, so that a node that stops leaves nothing listening
// behind, and a port for the next build is free.
func (w *webFront) Close() {
	w.mu.Lock()
	cancel, done := w.cancel, w.done
	w.ctx, w.cancel, w.done = nil, nil, nil
	w.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
	w.stopServing(true)
}

// watch reconciles the page with the configuration until ctx ends.
func (w *webFront) watch(ctx context.Context, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(webWatch)
	defer ticker.Stop()
	sweep := 0
	for {
		w.reconcile(ctx)
		sweep++
		if sweep%30 == 0 {
			w.clients.reap(webIdle)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// serves reports whether the page is served now: the pictures the thread shows
// are shown there while it is, also when no window of ouro is open.
func (w *webFront) serves() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.srv != nil
}

// stopServing ends the listener and the browsers that are on it. It signs the
// browsers out only when asked to: turning the page off does, moving it to
// another port does not, as a cookie is kept for the host, not for the port.
func (w *webFront) stopServing(revoke bool) {
	w.mu.Lock()
	srv := w.srv
	w.srv, w.port = nil, 0
	w.mu.Unlock()
	if srv == nil {
		if revoke {
			w.auth.RevokeAll()
		}
		return
	}
	// The streams of the browsers first: a request that stays open would keep
	// Shutdown waiting for its whole grace, and past it.
	w.clients.closeAll()
	if srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			// A browser that is still on a stream after the grace is cut off.
			_ = srv.Close()
		}
	}
	if revoke {
		w.auth.RevokeAll()
	}
}

// reconcile starts, stops or moves the page as the configuration says.
func (w *webFront) reconcile(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	cfg, err := w.dir.Config()
	if err != nil {
		w.logf("the page of the thread: the configuration cannot be read: %v", err)
		return
	}
	want := cfg.Web.Ready()
	port := cfg.Web.PortOr()
	w.mu.Lock()
	srv, serving, watching := w.srv, w.port, w.ctx != nil
	w.mu.Unlock()
	if !watching {
		return // Stop was called: the page listens no more
	}
	if !want {
		if srv != nil {
			w.stopServing(true)
			w.logf("the page of the thread is off")
		}
		return
	}
	if srv != nil && serving == port {
		return
	}
	if srv != nil {
		w.stopServing(false)
	}
	if ctx.Err() != nil {
		return
	}
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	if err != nil {
		w.logf("the page of the thread is not served: %v", err)
		return
	}
	w.mu.Lock()
	w.srv = &http.Server{Handler: w.handler, ReadHeaderTimeout: 10 * time.Second}
	srv = w.srv
	w.port = port
	w.mu.Unlock()
	go func() {
		err := srv.Serve(ln)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			w.logf("the page of the thread: %v", err)
		}
	}()
	w.logf("the page of the thread is served on port %d", port)
}

// routes are the ways into the page.
func (w *webFront) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /login", w.loginPage)
	mux.HandleFunc("POST /login", w.login)
	mux.HandleFunc("POST /logout", w.logout)
	mux.HandleFunc("GET /{$}", w.guard(w.page))
	mux.HandleFunc("GET /api/initial", w.guard(w.initial))
	mux.HandleFunc("POST /api/open", w.guard(w.open))
	mux.HandleFunc("GET /api/history", w.guard(w.history))
	mux.HandleFunc("POST /api/send", w.guard(w.send))
	mux.HandleFunc("POST /api/stop", w.guard(w.stop))
	mux.HandleFunc("GET /api/board", w.guard(w.board))
	mux.HandleFunc("GET /api/ticket", w.guard(w.ticket))
	mux.HandleFunc("GET /api/sessions", w.guard(w.sessions))
	mux.HandleFunc("GET /api/session", w.guard(w.session))
	mux.HandleFunc("GET /api/agents", w.guard(w.agents))
	mux.HandleFunc("POST /api/agent/update", w.guard(w.updateAgent))
	mux.HandleFunc("POST /api/agent/create", w.guard(w.createAgent))
	mux.HandleFunc("POST /api/agent/delete", w.guard(w.deleteAgent))
	mux.HandleFunc("GET /api/agent/models", w.guard(w.agentModels))
	mux.HandleFunc("GET /api/jobs", w.guard(w.agentJobs))
	mux.HandleFunc("GET /api/events", w.guard(w.events))
	mux.HandleFunc("GET /assets", w.assets)
	// The pictures the thread shows are the owner's: only behind the login.
	mux.Handle("GET "+mediaPath, w.guard(w.media.ServeHTTP))
	for _, file := range pageFiles() {
		mux.HandleFunc("GET "+file.path, w.file(file))
	}
	return mux
}

// pageFile is one file of the page as it is served.
type pageFile struct {
	path string
	name string // in the file system
	kind string // the content type, "" for none
	page bool   // of the page's own files, not of the window's
}

// pageFiles are the files a browser fetches beside the page itself. They are
// served without a login: they are the same code for everyone, and a service
// worker that caches them cannot do so through a redirect to the login.
func pageFiles() []pageFile {
	return []pageFile{
		{path: "/app.js", name: "app.js", kind: "text/javascript; charset=utf-8"},
		{path: "/app.css", name: "app.css", kind: "text/css; charset=utf-8"},
		{path: "/shim.js", name: "shim.js", kind: "text/javascript; charset=utf-8", page: true},
		{path: "/manifest.webmanifest", name: "manifest.webmanifest", kind: "application/manifest+json", page: true},
		{path: "/sw.js", name: "sw.js", kind: "text/javascript; charset=utf-8", page: true},
		{path: "/login.css", name: "login.css", kind: "text/css; charset=utf-8", page: true},
		{path: "/icon-192.png", name: "icon-192.png", kind: "image/png", page: true},
		{path: "/icon-512.png", name: "icon-512.png", kind: "image/png", page: true},
		{path: "/apple-touch-icon.png", name: "apple-touch-icon.png", kind: "image/png", page: true},
	}
}

// assets tells the page which build of itself the node serves. The page kept
// the name it came with and asks again when it reaches the node after a
// restart: another name is another build, and the page loads itself anew so
// that a rebuild shows without anyone reloading it by hand.
func (w *webFront) assets(rw http.ResponseWriter, r *http.Request) {
	rw.Header().Set("Cache-Control", "no-cache")
	reply(rw, struct {
		Version string `json:"version"`
	}{Version: assetVersion()})
}

// file serves one file of the page.
func (w *webFront) file(f pageFile) http.HandlerFunc {
	var (
		fsys fs.FS
		err  error
	)
	if f.page {
		fsys, err = webpageFiles()
	} else {
		fsys, err = frontendFiles()
	}
	return func(rw http.ResponseWriter, r *http.Request) {
		if err != nil {
			http.Error(rw, err.Error(), http.StatusInternalServerError)
			return
		}
		if f.kind != "" {
			rw.Header().Set("Content-Type", f.kind)
		}
		// A page that is rebuilt must not be shown from a cache that holds
		// the files of the build before it.
		rw.Header().Set("Cache-Control", "no-cache")
		http.ServeFileFS(rw, r, fsys, f.name)
	}
}

// guard lets a request through only with the session of a login.
func (w *webFront) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		if !w.signedIn(r) {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				rw.Header().Set("Content-Type", "application/json")
				rw.WriteHeader(http.StatusUnauthorized)
				_, _ = io.WriteString(rw, `{"error":"signed out"}`)
				return
			}
			http.Redirect(rw, r, "/login", http.StatusSeeOther)
			return
		}
		next(rw, r)
	}
}

// signedIn reports whether r carries the session of a login that holds.
func (w *webFront) signedIn(r *http.Request) bool {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	return w.auth.Valid(cookie.Value)
}

// page serves the shell of the page: the window of ouro, hosted by a script
// that talks to this server instead of to Wails.
func (w *webFront) page(rw http.ResponseWriter, r *http.Request) {
	fsys, err := webpageFiles()
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	rw.Header().Set("Cache-Control", "no-cache")
	file, err := fsys.Open("index.html")
	if err != nil {
		http.Error(rw, err.Error(), http.StatusInternalServerError)
		return
	}
	defer file.Close()
	_, _ = io.Copy(rw, file)
}

// loginPage shows the form, or the page itself for one who is signed in.
func (w *webFront) loginPage(rw http.ResponseWriter, r *http.Request) {
	if w.signedIn(r) {
		http.Redirect(rw, r, "/", http.StatusSeeOther)
		return
	}
	w.showLogin(rw, "", "", http.StatusOK)
}

// login checks what the form sent and lets the browser in.
func (w *webFront) login(rw http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(rw, r.Body, 4096)
	if err := r.ParseForm(); err != nil {
		w.showLogin(rw, "", "", http.StatusBadRequest)
		return
	}
	user, password := r.PostFormValue("user"), r.PostFormValue("password")
	caller, key := callerKey(r, user)
	// The attempts are counted before the password is hashed, not after: a
	// dozen requests that run at once would otherwise all pass an empty count,
	// and each of them would cost the machine a hash. The address counts on its
	// own too: a spray of names would otherwise start a fresh count with every
	// name, and hash forever.
	//
	// A request of the host that does not say whom it came for has no address
	// of its own: every caller behind such a proxy would share one count, and
	// any of them could hold the owner up with names that are not even theirs.
	// Only the name is counted then.
	callers := w.callers
	if caller == "" {
		callers = nil
	}
	if wait, ok := callers.Start(caller); !ok {
		w.logf("the page of the thread: logins from %s are held up for %s", caller, wait.Round(time.Second))
		w.showLogin(rw, user, "Too many attempts: wait a moment and try again.", http.StatusTooManyRequests)
		return
	}
	if wait, ok := w.throttle.Start(key); !ok {
		callers.Cancel(caller)
		w.logf("the page of the thread: logins from %s are held up for %s", key, wait.Round(time.Second))
		w.showLogin(rw, user, "Too many attempts: wait a moment and try again.", http.StatusTooManyRequests)
		return
	}
	cfg, err := w.dir.Config()
	if err != nil {
		// Not a wrong password: the node cannot read itself, and neither
		// counts.
		callers.Cancel(caller)
		w.throttle.Cancel(key)
		w.showLogin(rw, user, "The node cannot read its configuration.", http.StatusInternalServerError)
		return
	}
	// The password is hashed for every user, and only then is the name looked
	// at: a name that is wrong has to cost what a password that is wrong
	// costs, or the login tells which name is the right one.
	sameUser := subtle.ConstantTimeCompare([]byte(user), []byte(cfg.Web.User)) == 1
	samePassword := web.VerifyContext(r.Context(), cfg.Web.Password, password)
	if !sameUser || !samePassword {
		callers.Failed(caller)
		w.throttle.Failed(key)
		w.logf("the page of the thread: a login failed for %q from %s", user, r.RemoteAddr)
		w.showLogin(rw, user, "Wrong user or password.", http.StatusUnauthorized)
		return
	}
	callers.Cleared(caller)
	w.throttle.Cleared(key)
	token, err := w.auth.Issue()
	if err != nil {
		w.showLogin(rw, user, "The session cannot be started.", http.StatusInternalServerError)
		return
	}
	w.setCookie(rw, r, sessionCookie, token, int(web.SessionTTL.Seconds()))
	w.logf("the page of the thread: %q signed in from %s", user, r.RemoteAddr)
	http.Redirect(rw, r, "/", http.StatusSeeOther)
}

// logout ends the session of this browser.
func (w *webFront) logout(rw http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		w.auth.Revoke(cookie.Value)
	}
	w.setCookie(rw, r, sessionCookie, "", -1)
	http.Redirect(rw, r, "/login", http.StatusSeeOther)
}

// showLogin writes the form with what went wrong.
func (w *webFront) showLogin(rw http.ResponseWriter, user, problem string, status int) {
	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	rw.Header().Set("Cache-Control", "no-store")
	rw.Header().Set("X-Frame-Options", "DENY")
	rw.Header().Set("Referrer-Policy", "same-origin")
	rw.WriteHeader(status)
	_ = loginTemplate.Execute(rw, struct {
		Name, User, Error string
	}{w.profile.label(w.name), user, problem})
}

// setCookie keeps value in the browser, for maxAge seconds.
func (w *webFront) setCookie(rw http.ResponseWriter, r *http.Request, name, value string, maxAge int) {
	http.SetCookie(rw, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
	})
}

// callerKey names who tried a password, for the two counts of the login: the
// address it came from, and the address with the name it tried. Behind a proxy
// of the tailnet every request comes from the host itself, so what the proxy
// says the caller was is used: otherwise a single address in the tailnet could
// hold the page up for the owner by guessing their name.
//
// caller is empty when the request came from the host and the proxy did not
// say for whom: the address of the host is everyone's then, and is not
// counted on its own. key still names it with the name.
func callerKey(r *http.Request, user string) (caller, key string) {
	address, known := callerAddress(r)
	if known {
		caller = address
	}
	return caller, address + "|" + user
}

// callerAddress is the address of whoever made the request: the one the
// connection came from, unless that is this host, which is what a proxy of the
// tailnet is, and then the one the proxy forwarded.
//
// A proxy that appends the address it saw, as tailscale serve, nginx and
// httputil.ReverseProxy do, puts it last; what stands before it was written by
// the caller, who may put another address there — the owner's, to hold them up
// — or a text that is no address at all. So only the last entry is read, from
// the host itself, and only when it is an address. A proxy may also add a
// header line of its own rather than append to the caller's, so the last entry
// is the last of the last line.
//
// known is false for a request of the host that names no address: the host is
// the address of every caller of such a proxy.
func callerAddress(r *http.Request) (address string, known bool) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if !net.ParseIP(host).IsLoopback() {
		return host, true
	}
	lines := r.Header.Values("X-Forwarded-For")
	if len(lines) == 0 {
		return host, false
	}
	last := lines[len(lines)-1]
	if i := strings.LastIndexByte(last, ','); i >= 0 {
		last = last[i+1:]
	}
	last = strings.TrimSpace(last)
	if ip := net.ParseIP(last); ip != nil {
		return ip.String(), true
	}
	if h, _, err := net.SplitHostPort(last); err == nil {
		if ip := net.ParseIP(h); ip != nil {
			return ip.String(), true
		}
	}
	return host, false
}

// initial is what the page shows before it opens the thread.
func (w *webFront) initial(rw http.ResponseWriter, r *http.Request) {
	client := w.client(rw, r)
	if client == nil {
		return
	}
	reply(rw, client.window.InitialState())
}

// open shows the newest window of the thread and starts watching it.
func (w *webFront) open(rw http.ResponseWriter, r *http.Request) {
	client := w.client(rw, r)
	if client == nil {
		return
	}
	// What the thread told while no stream was open is in the snapshot this
	// Open takes: a stream that replayed it afterward would show it twice.
	client.forget()
	snapshot, err := client.window.Open()
	if err != nil {
		fail(rw, err)
		return
	}
	reply(rw, snapshot)
}

// history shows the page before the cursor.
func (w *webFront) history(rw http.ResponseWriter, r *http.Request) {
	client := w.client(rw, r)
	if client == nil {
		return
	}
	before, err := strconv.Atoi(r.URL.Query().Get("before"))
	if err != nil {
		http.Error(rw, "the cursor is a number", http.StatusBadRequest)
		return
	}
	snapshot, err := client.window.History(before)
	if err != nil {
		fail(rw, err)
		return
	}
	reply(rw, snapshot)
}

// board shows the board of the profile, as the window does. It is the same
// for every tab, so it needs no window of the tab that asks.
func (w *webFront) board(rw http.ResponseWriter, _ *http.Request) {
	board, err := w.profile.board()
	if err != nil {
		fail(rw, err)
		return
	}
	reply(rw, board)
}

// ticket shows one ticket of the board in full.
func (w *webFront) ticket(rw http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.URL.Query().Get("id"))
	if err != nil {
		http.Error(rw, "a ticket is named by its number", http.StatusBadRequest)
		return
	}
	ticket, err := w.profile.ticket(id)
	if err != nil {
		fail(rw, err)
		return
	}
	reply(rw, ticket)
}

// sessions lists the sessions of the profile, as the window does. It reads
// them through the window of the tab that asks: the list belongs to the
// sessions the node of this profile holds.
func (w *webFront) sessions(rw http.ResponseWriter, r *http.Request) {
	client := w.client(rw, r)
	if client == nil {
		return
	}
	list, err := client.window.Sessions()
	if err != nil {
		fail(rw, err)
		return
	}
	reply(rw, list)
}

// session shows one session in full, with what happened in it lately.
func (w *webFront) session(rw http.ResponseWriter, r *http.Request) {
	client := w.client(rw, r)
	if client == nil {
		return
	}
	track, err := client.window.Session(r.URL.Query().Get("id"))
	if err != nil {
		fail(rw, err)
		return
	}
	reply(rw, track)
}

// agents lists the agents of the profile, as the window does. They are the
// same for every tab, so it needs no window of the tab that asks.
func (w *webFront) agents(rw http.ResponseWriter, _ *http.Request) {
	list, err := w.profile.agents()
	if err != nil {
		fail(rw, err)
		return
	}
	reply(rw, list)
}

// agentJobs lists the jobs of the agents of the profile, as the window does.
func (w *webFront) agentJobs(rw http.ResponseWriter, _ *http.Request) {
	list, err := w.profile.agentJobs()
	if err != nil {
		fail(rw, err)
		return
	}
	reply(rw, list)
}

// agentBody is what the page sends of an agent: which one, and what the
// owner says of it.
type agentBody struct {
	ID string `json:"id"`
	AgentChange
}

// readAgent reads what the page sends of an agent into body, and reports
// whether it could; it answers the page itself where it could not. It
// changes the agents of the node, so only the page itself sends it.
func (w *webFront) readAgent(rw http.ResponseWriter, r *http.Request, body *agentBody) bool {
	if !w.sameOrigin(rw, r) {
		return false
	}
	// A soul is a few kilobytes at most, see agents.MaxSoul.
	r.Body = http.MaxBytesReader(rw, r.Body, 128<<10)
	if err := json.NewDecoder(r.Body).Decode(body); err != nil {
		http.Error(rw, "the agent cannot be read: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

// updateAgent changes an agent of the profile, as the window does.
func (w *webFront) updateAgent(rw http.ResponseWriter, r *http.Request) {
	var body agentBody
	if !w.readAgent(rw, r, &body) {
		return
	}
	agent, err := w.profile.updateAgent(body.ID, body.AgentChange)
	if err != nil {
		fail(rw, err)
		return
	}
	reply(rw, agent)
}

// createAgent makes an agent of the owner's, as the window does.
func (w *webFront) createAgent(rw http.ResponseWriter, r *http.Request) {
	var body agentBody
	if !w.readAgent(rw, r, &body) {
		return
	}
	agent, err := w.profile.createAgent(body.AgentChange)
	if err != nil {
		fail(rw, err)
		return
	}
	reply(rw, agent)
}

// agentModels lists the models an agent may answer with, as the window does.
func (w *webFront) agentModels(rw http.ResponseWriter, _ *http.Request) {
	list, err := w.profile.agentModels()
	if err != nil {
		fail(rw, err)
		return
	}
	reply(rw, list)
}

// deleteAgent removes an agent of the owner's, as the window does.
func (w *webFront) deleteAgent(rw http.ResponseWriter, r *http.Request) {
	var body agentBody
	if !w.readAgent(rw, r, &body) {
		return
	}
	if err := w.profile.deleteAgent(body.ID); err != nil {
		fail(rw, err)
		return
	}
	reply(rw, struct{}{})
}

// send starts the answer to what the owner typed, or steers the one on its
// way, as the window does.
func (w *webFront) send(rw http.ResponseWriter, r *http.Request) {
	client := w.client(rw, r)
	if client == nil {
		return
	}
	if !w.sameOrigin(rw, r) {
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	r.Body = http.MaxBytesReader(rw, r.Body, 16<<20)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(rw, "the message cannot be read: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := client.window.Send(body.Text); err != nil {
		fail(rw, err)
		return
	}
	reply(rw, nil)
}

// stop cancels the answer on its way in the thread.
func (w *webFront) stop(rw http.ResponseWriter, r *http.Request) {
	client := w.client(rw, r)
	if client == nil {
		return
	}
	if !w.sameOrigin(rw, r) {
		return
	}
	if err := client.window.Stop(); err != nil {
		fail(rw, err)
		return
	}
	reply(rw, nil)
}

// sameOrigin refuses a change that no page of this one made: the header is
// what a form of another site cannot set.
func (w *webFront) sameOrigin(rw http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get(webHeader) == "1" {
		return true
	}
	http.Error(rw, "no page of the thread sent this", http.StatusForbidden)
	return false
}

// events streams what the thread tells, one event of the page at a time, for
// as long as the browser shows it.
func (w *webFront) events(rw http.ResponseWriter, r *http.Request) {
	client := w.client(rw, r)
	if client == nil {
		return
	}
	header := rw.Header()
	header.Set("Content-Type", "text/event-stream; charset=utf-8")
	header.Set("Cache-Control", "no-store")
	header.Set("X-Accel-Buffering", "no")
	rw.WriteHeader(http.StatusOK)
	control := http.NewResponseController(rw)
	if err := control.Flush(); err != nil {
		return
	}
	messages, cancel := client.subscribe()
	defer cancel()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case message, ok := <-messages:
			if !ok {
				return // the page is off, or this browser was let go
			}
			if err := writeEvent(rw, message); err != nil {
				return
			}
			if err := control.Flush(); err != nil {
				return
			}
		case <-ping.C:
			if _, err := io.WriteString(rw, ": ping\n\n"); err != nil {
				return
			}
			if err := control.Flush(); err != nil {
				return
			}
		}
	}
}

// writeEvent writes one message of the stream. An event of the stream is
// dropped by EventSource when it carries no data line at all, so "ended" and
// "restarting", which say everything with their name, get an empty one.
func writeEvent(rw io.Writer, message sseEvent) error {
	if _, err := fmt.Fprintf(rw, "event: %s\n", message.event); err != nil {
		return err
	}
	lines := 0
	for line := range strings.Lines(string(message.data)) {
		lines++
		if _, err := fmt.Fprintf(rw, "data: %s\n", strings.TrimSuffix(line, "\n")); err != nil {
			return err
		}
	}
	if lines == 0 {
		if _, err := io.WriteString(rw, "data:\n"); err != nil {
			return err
		}
	}
	_, err := io.WriteString(rw, "\n")
	return err
}

// reply writes value as JSON.
func reply(rw http.ResponseWriter, value any) {
	rw.Header().Set("Content-Type", "application/json")
	rw.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(rw).Encode(value); err != nil {
		return
	}
}

// fail tells the page what went wrong, as the socket tells a front end.
func fail(rw http.ResponseWriter, err error) {
	rw.Header().Set("Content-Type", "text/plain; charset=utf-8")
	rw.Header().Set("Cache-Control", "no-store")
	status := http.StatusConflict
	if errors.Is(err, context.Canceled) {
		status = http.StatusServiceUnavailable
	}
	rw.WriteHeader(status)
	_, _ = io.WriteString(rw, err.Error())
}

// client is the window of the browser that made the request, or nil once the
// reply says why there is none. The page names its tab itself, and a request
// that does not say which tab it is falls back to a cookie, or gets one.
func (w *webFront) client(rw http.ResponseWriter, r *http.Request) *webClient {
	id := r.Header.Get(clientHeader)
	if !validClientID(id) {
		id = r.URL.Query().Get(clientParam)
	}
	if !validClientID(id) {
		if cookie, err := r.Cookie(clientCookie); err == nil {
			id = cookie.Value
		}
	}
	if !validClientID(id) {
		token, err := web.Token()
		if err != nil {
			http.Error(rw, "the window cannot be named: "+err.Error(), http.StatusInternalServerError)
			return nil
		}
		id = token
		w.setCookie(rw, r, clientCookie, id, int((30 * 24 * time.Hour).Seconds()))
	}
	return w.clients.of(id, func() *webClient { return w.newClient(id) })
}

// validClientID reports whether id is one this server made.
func validClientID(id string) bool {
	if len(id) != 43 {
		return false
	}
	for _, r := range id {
		if !strings.ContainsRune("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_", r) {
			return false
		}
	}
	return true
}

// sseEvent is one message of the stream of a browser.
type sseEvent struct {
	event string
	data  []byte
}

// webClient is the window of one browser: its own watch of the thread, its
// own history, and the stream it listens on.
type webClient struct {
	id     string
	window *Window
	cancel context.CancelFunc

	mu     sync.Mutex
	subs   map[chan sseEvent]struct{}
	queue  []sseEvent
	last   time.Time
	closed bool
}

// newClient opens the window of the browser id.
func (w *webFront) newClient(id string) *webClient {
	w.mu.Lock()
	ctx := w.ctx
	w.mu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(ctx)
	window := newWindowContext(ctx, w.profile, w.name)
	window.source = "web"
	client := &webClient{id: id, window: window, cancel: cancel, subs: map[chan sseEvent]struct{}{}, last: time.Now()}
	window.onEmit(func(events []Event) {
		data, err := json.Marshal(events)
		if err != nil {
			return
		}
		client.broadcast(sseEvent{event: "events", data: data})
	})
	window.onEnded(func() { client.broadcast(sseEvent{event: "ended"}) })
	window.onRestart(func() { client.broadcast(sseEvent{event: "restarting"}) })
	return client
}

// broadcast passes one message on to the streams of this browser, or keeps it
// for the one it opens next: events told between two requests of a page that
// is still loading must not be lost.
func (c *webClient) broadcast(message sseEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	// What the thread tells is not the browser doing something: a tab that
	// was reloaded leaves its window behind, and an active thread would keep
	// that window, and its watch, forever.
	if len(c.subs) == 0 {
		if len(c.queue) >= webQueued {
			c.queue = c.queue[1:]
		}
		c.queue = append(c.queue, message)
		return
	}
	for stream := range c.subs {
		select {
		case stream <- message:
		default: // behind: what it missed is in the snapshot of its next Open
		}
	}
}

// forget drops what was told while no stream was open: the snapshot of an Open
// holds it already, and a stream that replayed it would show it twice.
func (c *webClient) forget() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.queue = nil
}

// subscribe starts a stream of what the window tells, with what it told while
// no stream was open. The stream is closed when the browser is let go, which
// is what ends the request that reads it.
func (c *webClient) subscribe() (<-chan sseEvent, func()) {
	stream := make(chan sseEvent, webQueued)
	c.mu.Lock()
	if c.closed {
		close(stream)
		c.mu.Unlock()
		return stream, func() {}
	}
	for _, message := range c.queue {
		stream <- message
	}
	c.queue = nil
	c.subs[stream] = struct{}{}
	c.mu.Unlock()
	return stream, func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		// The idle time of a browser counts from when its stream ended.
		c.last = time.Now()
		if _, ok := c.subs[stream]; ok {
			delete(c.subs, stream)
			close(stream)
		}
	}
}

// close ends the window of this browser and the streams that are on it: a
// stream that is left open would keep its request, and its goroutine, for as
// long as the pings go through.
func (c *webClient) close() {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	for stream := range c.subs {
		close(stream)
		delete(c.subs, stream)
	}
	c.queue = nil
	c.mu.Unlock()
	c.cancel()
	c.window.Close()
}

// webClients are the windows of the browsers that are on the page.
type webClients struct {
	mu   sync.Mutex
	byID map[string]*webClient
}

func newWebClients() *webClients {
	return &webClients{byID: map[string]*webClient{}}
}

// of returns the window of id, which make opens once.
func (c *webClients) of(id string, make func() *webClient) *webClient {
	c.mu.Lock()
	defer c.mu.Unlock()
	client, ok := c.byID[id]
	if ok {
		client.mu.Lock()
		ok = !client.closed
		client.mu.Unlock()
	}
	if !ok {
		client = make()
		c.byID[id] = client
	}
	client.mu.Lock()
	client.last = time.Now()
	client.mu.Unlock()
	return client
}

// reap closes the windows of the browsers that did nothing for idle. A browser
// that has a stream open did something, whether or not the thread told
// anything: reaping it would take the tab's window away while its stream kept
// pinging, and the tab would show nothing new until it was reloaded.
func (c *webClients) reap(idle time.Duration) {
	c.mu.Lock()
	var stale []*webClient
	for id, client := range c.byID {
		client.mu.Lock()
		quiet := !client.closed && len(client.subs) == 0 && time.Since(client.last) > idle
		client.mu.Unlock()
		if quiet {
			stale = append(stale, client)
			delete(c.byID, id)
		}
	}
	c.mu.Unlock()
	for _, client := range stale {
		client.close()
	}
}

// closeAll closes every window: the page is off.
func (c *webClients) closeAll() {
	c.mu.Lock()
	all := make([]*webClient, 0, len(c.byID))
	for id, client := range c.byID {
		all = append(all, client)
		delete(c.byID, id)
	}
	c.mu.Unlock()
	for _, client := range all {
		client.close()
	}
}

// size is how many browsers are on the page, for tests.
func (c *webClients) size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.byID)
}

// loginTemplate is the form the page asks for, in the colors of ouro.
var loginTemplate = template.Must(template.New("login").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover">
<meta name="color-scheme" content="light dark">
<meta name="theme-color" content="#20221d">
<meta name="apple-mobile-web-app-capable" content="yes">
<meta name="apple-mobile-web-app-title" content="Ouro">
<title>Ouroboros</title>
{{/* A phone installs the app from the page it is on: the way in carries the
     manifest too, lest a browser without a session has nothing to install. */}}
<link rel="manifest" href="./manifest.webmanifest">
<link rel="icon" href="./icon-192.png">
<link rel="apple-touch-icon" href="./apple-touch-icon.png">
<link rel="stylesheet" href="./login.css">
</head>
<body>
<main class="card">
<h1 class="brand">Ouroboros</h1>
<p class="node">{{.Name}}</p>
{{if .Error}}<p class="error" role="alert">{{.Error}}</p>{{end}}
<form method="post" action="./login">
<label class="field"><span>User</span>
<input name="user" value="{{.User}}" autocomplete="username" autocapitalize="off" autocorrect="off" spellcheck="false" required autofocus></label>
<label class="field"><span>Password</span>
<input type="password" name="password" autocomplete="current-password" required></label>
<button type="submit">Sign in</button>
</form>
</main>
</body>
</html>
`))
