package node

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"ouroboros/internal/a2a"
	"ouroboros/internal/config"
	"ouroboros/internal/daemon"
	"ouroboros/internal/discovery"
)

// patience is how long a node may take to come up.
const patience = 5 * time.Second

// run runs the node in the background and waits until it holds its PID file.
// stop ends it and returns what Run returned.
func run(t *testing.T, s Server) (p daemon.Process, stop func() error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var err error
	done := make(chan struct{})
	go func() {
		defer close(done)
		err = s.Run(ctx)
	}()
	stop = func() error {
		cancel()
		<-done
		return err
	}
	t.Cleanup(func() { stop() })
	for deadline := time.Now().Add(patience); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if p, ok := s.Home.Service().Running(); ok {
			return p, stop
		}
		select {
		case <-done:
			t.Fatalf("the node did not come up: %v", err)
		default:
		}
	}
	t.Fatal("the node did not come up in time")
	return p, stop
}

// freeUDPPort returns a UDP port nobody listens on.
func freeUDPPort(t *testing.T) int {
	t.Helper()
	conn, err := net.ListenUDP("udp4", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).Port
}

// fetchCard reads the agent card of the node on port.
func fetchCard(t *testing.T, port int) a2a.AgentCard {
	t.Helper()
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d%s", port, a2a.CardPath))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("agent card: status %d", resp.StatusCode)
	}
	var card a2a.AgentCard
	if err := json.NewDecoder(resp.Body).Decode(&card); err != nil {
		t.Fatal(err)
	}
	return card
}

// The node holds its PID file while it runs, serves its card on the port
// recorded there and gives both up when it is told to stop.
func TestRunServesUntilStopped(t *testing.T) {
	dir := configured(t)
	cfg, _ := dir.Config()
	cfg.Tools = &config.Tools{A2A: []string{"clock", "search"}}
	if err := dir.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer
	s := testServer(dir)
	s.Log = &log
	p, stop := run(t, s)

	if p.PID != os.Getpid() || p.Port == 0 {
		t.Fatalf("process %+v", p)
	}
	got, want := fetchCard(t, p.Port), card(cfg, "test")
	want.URL = got.URL // where it was asked, filled in by the handler
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("card %+v, want %+v", got, want)
	}

	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if _, ok := dir.Service().Running(); ok {
		t.Error("a stopped node should not count as running")
	}
	if _, err := os.Stat(dir.Service().PIDFile); !os.IsNotExist(err) {
		t.Error("a stopped node should remove its PID file")
	}
	if conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", p.Port)); err == nil {
		conn.Close()
		t.Error("a stopped node should close its port")
	}
	id, _ := dir.Identity()
	for _, want := range []string{
		fmt.Sprintf("ouroboros %s (alpha) listening on :%d → gpt", id.ID(), p.Port),
		"peers may use the tools: clock, search",
		"GET " + a2a.CardPath,
		"stopped",
	} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("log lacks %q:\n%s", want, log.String())
		}
	}
}

// A node whose port is taken serves on a free one and says so wherever it
// tells its port: in the PID file and to those who discover it.
func TestRunFallsBackToFreePort(t *testing.T) {
	taken, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	dir := configured(t)
	s := testServer(dir)
	s.HTTPPort = taken.Addr().(*net.TCPAddr).Port
	s.DiscoPort = freeUDPPort(t)
	p, _ := run(t, s)

	if p.Port == 0 || p.Port == s.HTTPPort {
		t.Fatalf("port %d, taken is %d", p.Port, s.HTTPPort)
	}
	if got := fetchCard(t, p.Port); got.Name != "alpha" || got.Version != "test" {
		t.Fatalf("card %+v", got)
	}

	id, _ := dir.Identity()
	for deadline := time.Now().Add(patience); ; {
		nodes, err := discovery.Discover(context.Background(), s.DiscoPort, 100*time.Millisecond)
		if err != nil {
			t.Fatal(err)
		}
		if len(nodes) == 1 {
			want := discovery.Node{ID: id.ID(), Name: "alpha", Model: "gpt", Port: p.Port, PublicKey: id.Public(), Host: nodes[0].Host}
			if nodes[0] != want {
				t.Fatalf("discovered %+v, want %+v", nodes[0], want)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("discovered %+v", nodes)
		}
	}
}

func TestRunNeedsSetup(t *testing.T) {
	dir := configured(t)
	cfg, _ := dir.Config()
	cfg.Model = ""
	dir.SaveConfig(cfg)
	if err := testServer(dir).Run(context.Background()); !errors.Is(err, config.ErrIncomplete) {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(dir.Service().PIDFile); !os.IsNotExist(err) {
		t.Error("a node that did not start should leave no PID file")
	}
}

func TestRunReportsErrors(t *testing.T) {
	// free reports that s, which did not start, left nothing behind.
	free := func(t *testing.T, s Server, port int) {
		t.Helper()
		if _, ok := s.Home.Service().Running(); ok {
			t.Error("a node that did not start should not count as running")
		}
		if _, err := os.Stat(s.Home.Service().PIDFile); !os.IsNotExist(err) {
			t.Error("a node that did not start should leave no PID file")
		}
		ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
		if err != nil {
			t.Fatalf("a node that did not start should give up its port: %v", err)
		}
		ln.Close()
	}
	// reserved returns a server on a port of its own that is free.
	reserved := func(t *testing.T) (Server, int) {
		t.Helper()
		ln, err := net.Listen("tcp", ":0")
		if err != nil {
			t.Fatal(err)
		}
		ln.Close()
		s := testServer(configured(t))
		s.HTTPPort = ln.Addr().(*net.TCPAddr).Port
		return s, s.HTTPPort
	}

	t.Run("unreadable home", func(t *testing.T) {
		if err := testServer(unreadable(t)).Run(context.Background()); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("invalid identity", func(t *testing.T) {
		if err := testServer(keyless(t)).Run(context.Background()); err == nil {
			t.Fatal("want error")
		}
	})
	t.Run("no port", func(t *testing.T) {
		replace(t, &netListen, func(string, string) (net.Listener, error) { return nil, errTest })
		if err := testServer(configured(t)).Run(context.Background()); !errors.Is(err, errTest) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("no discovery port", func(t *testing.T) {
		s, port := reserved(t)
		s.DiscoPort = -1
		if err := s.Run(context.Background()); err == nil || !strings.HasPrefix(err.Error(), "discovery: ") {
			t.Fatalf("got %v", err)
		}
		free(t, s, port)
	})
	t.Run("no responder", func(t *testing.T) {
		s, port := reserved(t)
		s.Responder = func(config.Config) (a2a.Responder, error) { return nil, errTest }
		if err := s.Run(context.Background()); !errors.Is(err, errTest) {
			t.Fatalf("got %v", err)
		}
		free(t, s, port)
	})
	t.Run("already running", func(t *testing.T) {
		s, port := reserved(t)
		release, err := daemon.Hold(s.Home.Service().PIDFile, 41337)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		if err := s.Run(context.Background()); !errors.Is(err, daemon.ErrRunning) {
			t.Fatalf("got %v", err)
		}
		if p, ok := s.Home.Service().Running(); !ok || p.Port != 41337 {
			t.Fatalf("the running node lost its file: %+v, %v", p, ok)
		}
		ln, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
		if err != nil {
			t.Fatalf("a node that did not start should give up its port: %v", err)
		}
		ln.Close()
	})
	t.Run("port lost", func(t *testing.T) {
		// A port that is closed under the node cannot be served.
		replace(t, &netListen, func(network, addr string) (net.Listener, error) {
			ln, err := net.Listen(network, addr)
			if err == nil {
				ln.Close()
			}
			return ln, err
		})
		s := testServer(configured(t))
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		if err := s.Run(ctx); !errors.Is(err, net.ErrClosed) {
			t.Fatalf("got %v", err)
		}
		if _, ok := s.Home.Service().Running(); ok {
			t.Error("a node that failed should not count as running")
		}
	})
}

func TestListen(t *testing.T) {
	port := func(ln net.Listener) int { return ln.Addr().(*net.TCPAddr).Port }

	free, err := listen(0)
	if err != nil {
		t.Fatal(err)
	}
	defer free.Close()
	if port(free) == 0 {
		t.Fatal("no port")
	}

	// The port asked for is the one that is opened, unless it is taken.
	free.Close()
	asked, err := listen(port(free))
	if err != nil {
		t.Fatal(err)
	}
	defer asked.Close()
	if port(asked) != port(free) {
		t.Fatalf("port %d, asked for %d", port(asked), port(free))
	}
	other, err := listen(port(asked))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if port(other) == 0 || port(other) == port(asked) {
		t.Fatalf("port %d, taken is %d", port(other), port(asked))
	}

	// Without any port there is no node.
	replace(t, &netListen, func(string, string) (net.Listener, error) { return nil, errTest })
	for _, p := range []int{0, port(asked)} {
		if _, err := listen(p); !errors.Is(err, errTest) {
			t.Errorf("port %d: got %v", p, err)
		}
	}
}

func TestCard(t *testing.T) {
	got := card(config.Config{Name: "alpha", Model: "gpt"}, "1.2.3")
	if got.Name != "alpha" || got.Version != "1.2.3" || got.ProtocolVersion != a2a.ProtocolVersion {
		t.Fatalf("card %+v", got)
	}
	if got.Description != "Ouroboros instance named alpha" {
		t.Errorf("description %q", got.Description)
	}
	if len(got.Skills) != 1 || got.Skills[0].ID != "chat" || got.Skills[0].Description != "Chat with gpt" {
		t.Errorf("skills %+v", got.Skills)
	}
}
