package gui

import (
	"context"
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
// do not share one history cursor.
const (
	sessionCookie = "ouro_web"
	clientCookie  = "ouro_web_client"
)

const (
	// webWatch is how often the page looks whether the owner turned it on or
	// off, or moved it to another port. The configuration is a small file,
	// and this is what makes a change hold without the node starting anew.
	webWatch = 2 * time.Second
	// webIdle is how long a browser that did nothing keeps its window: each
	// one holds the last three hours of the thread in memory.
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
	throttle *web.Throttle
	clients  *webClients
	handler  http.Handler
	logf     func(format string, args ...any)

	mu  sync.Mutex
	ctx context.Context
	srv *http.Server
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
		clients:  newWebClients(),
		logf:     dir.Log,
	}
	w.handler = w.routes()
	return w
}

// Serve keeps the page in step with the configuration: served while the owner
// turned it on and gave it a user and a password, on the port it says.
func (w *webFront) Serve(ctx context.Context) {
	w.mu.Lock()
	if w.ctx != nil {
		w.mu.Unlock()
		return
	}
	w.ctx = ctx
	w.mu.Unlock()
	go w.watch(ctx)
}

// Close stops the page and every browser that is on it.
func (w *webFront) Close() {
	w.mu.Lock()
	srv := w.srv
	w.srv, w.port = nil, 0
	w.mu.Unlock()
	if srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}
	w.clients.closeAll()
	w.auth.RevokeAll()
}

// watch reconciles the page with the configuration until ctx ends.
func (w *webFront) watch(ctx context.Context) {
	ticker := time.NewTicker(webWatch)
	defer ticker.Stop()
	defer w.Close()
	sweep := 0
	for {
		w.reconcile()
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

// reconcile starts, stops or moves the page as the configuration says.
func (w *webFront) reconcile() {
	cfg, err := w.dir.Config()
	if err != nil {
		w.logf("the page of the thread: the configuration cannot be read: %v", err)
		return
	}
	want := cfg.Web.Ready()
	port := cfg.Web.PortOr()
	w.mu.Lock()
	srv, serving, ctx := w.srv, w.port, w.ctx
	w.mu.Unlock()
	if ctx == nil {
		return // Serve was never called: nothing keeps this page in step
	}
	if !want {
		if srv != nil {
			w.Close()
			w.logf("the page of the thread is off")
		}
		return
	}
	if srv != nil && serving == port {
		return
	}
	if srv != nil {
		w.Close()
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
	mux.HandleFunc("GET /api/events", w.guard(w.events))
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
	// The id of this browser's window: a page that is opened a second time in
	// another tab gets its own, so that one history cursor is one tab's.
	if _, err := r.Cookie(clientCookie); err != nil {
		if id, err := web.Token(); err == nil {
			w.setCookie(rw, r, clientCookie, id, int((30 * 24 * time.Hour).Seconds()))
		}
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
	key := throttleKey(r, user)
	if !w.throttle.Allowed(key) {
		w.showLogin(rw, user, "Too many attempts: wait a moment and try again.", http.StatusTooManyRequests)
		return
	}
	cfg, err := w.dir.Config()
	if err != nil {
		w.showLogin(rw, user, "The node cannot read its configuration.", http.StatusInternalServerError)
		return
	}
	if cfg.Web.User == "" || cfg.Web.Password == "" || user != cfg.Web.User || !web.Verify(cfg.Web.Password, password) {
		w.throttle.Failed(key)
		w.logf("the page of the thread: a login failed for %q from %s", user, r.RemoteAddr)
		w.showLogin(rw, user, "Wrong user or password.", http.StatusUnauthorized)
		return
	}
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
	}{w.name, user, problem})
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

// throttleKey names who tried a password: the address it came from, and the
// user it tried. Behind a proxy of the tailnet every request comes from the
// host itself, so the user is what tells attempts apart.
func throttleKey(r *http.Request, user string) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return host + "|" + user
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
		case message := <-messages:
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

// writeEvent writes one message of the stream.
func writeEvent(rw io.Writer, message sseEvent) error {
	if _, err := fmt.Fprintf(rw, "event: %s\n", message.event); err != nil {
		return err
	}
	for line := range strings.Lines(string(message.data)) {
		if _, err := fmt.Fprintf(rw, "data: %s\n", strings.TrimSuffix(line, "\n")); err != nil {
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
// reply says why there is none.
func (w *webFront) client(rw http.ResponseWriter, r *http.Request) *webClient {
	id := ""
	if cookie, err := r.Cookie(clientCookie); err == nil {
		id = cookie.Value
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

	mu    sync.Mutex
	subs  map[chan sseEvent]struct{}
	queue []sseEvent
	last  time.Time
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
	c.last = time.Now()
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

// subscribe starts a stream of what the window tells, with what it told while
// no stream was open.
func (c *webClient) subscribe() (<-chan sseEvent, func()) {
	stream := make(chan sseEvent, webQueued)
	c.mu.Lock()
	for _, message := range c.queue {
		stream <- message
	}
	c.queue = nil
	c.subs[stream] = struct{}{}
	c.mu.Unlock()
	return stream, func() {
		c.mu.Lock()
		delete(c.subs, stream)
		c.mu.Unlock()
	}
}

// close ends the window of this browser.
func (c *webClient) close() {
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
	if !ok {
		client = make()
		c.byID[id] = client
	}
	client.mu.Lock()
	client.last = time.Now()
	client.mu.Unlock()
	return client
}

// reap closes the windows of the browsers that did nothing for idle.
func (c *webClients) reap(idle time.Duration) {
	c.mu.Lock()
	var stale []*webClient
	for id, client := range c.byID {
		client.mu.Lock()
		last := client.last
		client.mu.Unlock()
		if time.Since(last) > idle {
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
