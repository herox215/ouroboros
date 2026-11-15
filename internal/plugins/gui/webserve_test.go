package gui

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"ouroboros/internal/config"
	"ouroboros/internal/home"
	"ouroboros/internal/web"
	"ouroboros/plugin"
)

// The page is served on the port the configuration names while it is enabled,
// and it stops once the owner turns it off: no restart of the node between.
func TestThePageListens(t *testing.T) {
	dir := home.Dir(t.TempDir())
	hash, err := web.Hash("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	port := freePort(t)
	if err := config.Save(string(dir), config.Config{Model: "m", Web: config.Web{
		Enabled: true, Port: port, User: "owner", Password: hash,
	}}); err != nil {
		t.Fatal(err)
	}
	p := fakeProfile("pythia")
	p.pictures = &pictureChannel{ctx: testContext(t), output: pictureOutput(&fakeSessions{}, nil, pictures{dir: dir.Pictures()})}
	front := newWebFront(dir, p)
	front.Serve()
	defer front.Close()

	url := fmt.Sprintf("http://127.0.0.1:%d/login", port)
	text := waitFor(t, url, true)
	if !strings.Contains(text, "Sign in") || !strings.Contains(text, "pythia") {
		t.Fatalf("the login page says %q", text)
	}
	// The page shows the pictures of the thread while it is served, as the
	// window does while it is open: gateway_send_image reaches the profile
	// through it.
	if got := plugin.All[plugin.GatewayOutput](p.pictures.ctx); len(got) != 1 || got[0].Name != "gui" {
		t.Fatalf("a served page is %d providers: %+v", len(got), got)
	}

	if err := config.Update(string(dir), func(c *config.Config) error {
		c.Web.Enabled = false
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, url, false)
	if got := plugin.All[plugin.GatewayOutput](p.pictures.ctx); len(got) != 0 {
		t.Fatalf("a page that is off left %d providers: %+v", len(got), got)
	}
}

// Stop stops the page: the watch it started ends with it, so that a port that
// a new build is to take over is free, and a node that stopped serves nothing.
// A page that is started again afterward listens again.
func TestStopEndsTheWatch(t *testing.T) {
	dir := home.Dir(t.TempDir())
	hash, err := web.Hash("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	port := freePort(t)
	if err := config.Save(string(dir), config.Config{Model: "m", Web: config.Web{
		Enabled: true, Port: port, User: "owner", Password: hash,
	}}); err != nil {
		t.Fatal(err)
	}
	front := newWebFront(dir, fakeProfile("pythia"))
	front.Serve()
	defer front.Close()
	url := fmt.Sprintf("http://127.0.0.1:%d/login", port)
	waitFor(t, url, true)

	front.Close()
	waitFor(t, url, false)
	// The watch would listen again on its next pass if Stop left it running.
	time.Sleep(webWatch + time.Second)
	waitFor(t, url, false)

	front.Serve()
	waitFor(t, url, true)
}

// freePort is a port nothing listens on, for now.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// waitFor gets url until it answers or stops answering, within a few seconds,
// and returns what it said.
func waitFor(t *testing.T, url string, up bool) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		res, err := http.Get(url)
		if err == nil {
			text, _ := io.ReadAll(res.Body)
			res.Body.Close()
			if up {
				return string(text)
			}
		} else if !up {
			return ""
		}
		time.Sleep(50 * time.Millisecond)
	}
	if up {
		t.Fatalf("%s does not answer", url)
	}
	t.Fatalf("%s answers still", url)
	return ""
}

// A page that is moved to another port signs nobody out: a cookie is kept for
// the host, not for the port, so a browser that was in stays in.
func TestMovingThePortKeepsTheSession(t *testing.T) {
	dir := home.Dir(t.TempDir())
	hash, err := web.Hash("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	first, second := freePort(t), freePort(t)
	if err := config.Save(string(dir), config.Config{Model: "m", Web: config.Web{
		Enabled: true, Port: first, User: "owner", Password: hash,
	}}); err != nil {
		t.Fatal(err)
	}
	front := newWebFront(dir, fakeProfile("pythia"))
	front.Serve()
	defer front.Close()
	base := fmt.Sprintf("http://127.0.0.1:%d", first)
	waitFor(t, base+"/login", true)
	client := stillClient()
	res := signIn(t, client, base, "owner", "correct horse battery")
	cookie := session(t, res)
	res.Body.Close()
	if cookie == nil {
		t.Fatalf("no session: %d", res.StatusCode)
	}

	if err := config.Update(string(dir), func(c *config.Config) error {
		c.Web.Port = second
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	moved := fmt.Sprintf("http://127.0.0.1:%d", second)
	waitFor(t, moved+"/login", true)
	initial := do(t, client, "GET", moved+"/api/initial", cookie, nil)
	if initial.StatusCode != http.StatusOK {
		t.Fatalf("the page on the new port answers %d to the session of the old one", initial.StatusCode)
	}
	initial.Body.Close()
}
