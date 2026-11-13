package gui

import (
	"context"
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
	front := newWebFront(dir, fakeProfile("pythia"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	front.Serve(ctx)
	defer front.Close()

	url := fmt.Sprintf("http://127.0.0.1:%d/login", port)
	text := waitFor(t, url, true)
	if !strings.Contains(text, "Sign in") || !strings.Contains(text, "pythia") {
		t.Fatalf("the login page says %q", text)
	}

	if err := config.Update(string(dir), func(c *config.Config) error {
		c.Web.Enabled = false
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, url, false)
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
