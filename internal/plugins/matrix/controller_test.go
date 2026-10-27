package matrix

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ouroboros/plugin"
)

type fakeConnection struct {
	started chan struct{}
	stopped chan struct{}
	sends   atomic.Int32
}

func (f *fakeConnection) Run(ctx context.Context, _ func(context.Context, incoming) error) error {
	close(f.started)
	<-ctx.Done()
	close(f.stopped)
	return ctx.Err()
}
func (f *fakeConnection) Send(context.Context, string, string) error { f.sends.Add(1); return nil }
func (f *fakeConnection) SendTo(ctx context.Context, room, peer, text string) error {
	return f.Send(ctx, room, text)
}
func (f *fakeConnection) TypingTo(context.Context, string, string, bool) error { return nil }
func (f *fakeConnection) Close() error                                         { return nil }

func TestControllerStartsOnlyWhileServingAndStops(t *testing.T) {
	s := store{t.TempDir()}
	if err := s.save(configuration{Enabled: true, Token: "token", Revision: "1"}); err != nil {
		t.Fatal(err)
	}
	fake := &fakeConnection{started: make(chan struct{}), stopped: make(chan struct{})}
	c := controller{store: s, connect: func(context.Context, configuration) (connection, error) { return fake, nil }}
	select {
	case <-fake.started:
		t.Fatal("started before serving")
	default:
	}
	c.start()
	c.start() // Idempotent: no second sync loop.
	select {
	case <-fake.started:
	case <-time.After(3 * time.Second):
		t.Fatal("not started")
	}
	c.stop()
	select {
	case <-fake.stopped:
	default:
		t.Fatal("did not stop sync")
	}
	c.stop()
}

func TestGuardRejectsConfigurationChangesBeforeSending(t *testing.T) {
	s := store{t.TempDir()}
	cfg := configuration{Enabled: true, Revision: "1"}
	if err := s.save(cfg); err != nil {
		t.Fatal(err)
	}
	fake := &fakeConnection{}
	g := guardedConnection{connection: fake, store: s, revision: "1"}
	if err := g.SendTo(context.Background(), "!room:example.org", "@owner:example.org", "hello"); err != nil {
		t.Fatal(err)
	}
	cfg.Revision = "2"
	if err := s.save(cfg); err != nil {
		t.Fatal(err)
	}
	if err := g.SendTo(context.Background(), "!room:example.org", "@owner:example.org", "must not leak"); err == nil {
		t.Fatal("sent after policy change")
	}
	if fake.sends.Load() != 1 {
		t.Fatal("unexpected message count")
	}
}

// liveConnection hands the callback of every Run to the test, and runs until
// it is stopped or, with ending, until the test ends it. healthyConnection
// tells its state as well.
type liveConnection struct {
	recordingConnection
	runs   chan func(context.Context, incoming) error
	ending chan error
	closed atomic.Int32
}

func newLiveConnection() *liveConnection {
	return &liveConnection{runs: make(chan func(context.Context, incoming) error, 8)}
}

func (l *liveConnection) Run(ctx context.Context, receive func(context.Context, incoming) error) error {
	l.runs <- receive
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-l.ending:
		return err
	}
}
func (l *liveConnection) Close() error { l.closed.Add(1); return nil }

// running waits for the next Run and returns its callback.
func (l *liveConnection) running(t *testing.T) func(context.Context, incoming) error {
	t.Helper()
	select {
	case receive := <-l.runs:
		return receive
	case <-time.After(15 * time.Second):
		t.Fatal("not connected")
		return nil
	}
}

type healthyConnection struct{ *liveConnection }

func (healthyConnection) Status() string { return "Synced" }

// waitStatus waits until the controller reports text for revision.
func waitStatus(t *testing.T, s store, revision, text string) {
	t.Helper()
	var st connectionStatus
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		st = connectionStatus{}
		if data, err := os.ReadFile(s.path("status.json")); err == nil && json.Unmarshal(data, &st) == nil && st.Revision == revision && st.Text == text {
			return
		}
	}
	t.Fatalf("status %+v, want %q for revision %q", st, text, revision)
}

func TestControllerHandsMessagesOverWhileTheSettingsHold(t *testing.T) {
	s := store{t.TempDir()}
	cfg := configuration{Enabled: true, Token: "token", Revision: "1"}
	if err := s.save(cfg); err != nil {
		t.Fatal(err)
	}
	conn := newLiveConnection()
	var mu sync.Mutex
	var received []incoming
	results := []error{errors.New("session gone"), plugin.ErrGatewayDenied, nil}
	c := &controller{store: s,
		connect: func(context.Context, configuration) (connection, error) { return conn, nil },
		receive: func(_ context.Context, over connection, msg incoming) error {
			if _, ok := over.(guardedConnection); !ok {
				t.Errorf("handed over %T, not the guard", over)
			}
			mu.Lock()
			defer mu.Unlock()
			received = append(received, msg)
			err := results[0]
			results = results[1:]
			return err
		}}
	c.start()
	defer c.stop()
	deliver := conn.running(t)
	waitStatus(t, s, "1", "Synchronizing")
	msg := incoming{RoomID: "!room:test", Sender: "@owner:test", Text: "hello"}
	for _, want := range []error{results[0], plugin.ErrGatewayDenied, nil} {
		if err := deliver(context.Background(), msg); !errors.Is(err, want) {
			t.Fatalf("receive: %v, want %v", err, want)
		}
	}
	// Only a failure of the session is explained to the sender; a denied
	// sender learns nothing.
	if sent := conn.messages(); !slices.Equal(sent, []string{"!room:test @owner:test Ouro could not accept this message. Check the local session and gateway settings."}) {
		t.Fatalf("sent %q", sent)
	}
	waitStatus(t, s, "1", "Connected")
	// Changed settings revoke the connection at once, before the controller
	// reloads them.
	for _, settings := range []string{`{"enabled": false, "token": "token", "revision": "1"}`, `{"enabled": true, "token": "token", "revision": "2"}`} {
		if err := os.WriteFile(s.path("settings.json"), []byte(settings), 0600); err != nil {
			t.Fatal(err)
		}
		if err := deliver(context.Background(), msg); err == nil {
			t.Fatalf("received with settings %s", settings)
		}
	}
	// The new revision gets a connection of its own.
	deliver = conn.running(t)
	if conn.closed.Load() != 1 {
		t.Fatal("the connection of the old settings was not closed")
	}
	// Settings that cannot be read stop the connection until they can.
	if err := os.WriteFile(s.path("settings.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := deliver(context.Background(), msg); err == nil {
		t.Fatal("received with unreadable settings")
	}
	for deadline := time.Now().Add(15 * time.Second); conn.closed.Load() != 2; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("still connected with unreadable settings")
		}
	}
	if err := s.save(configuration{Enabled: true, Token: "token", Revision: "2"}); err != nil {
		t.Fatal(err)
	}
	conn.running(t)
	mu.Lock()
	if len(received) != 3 {
		t.Fatalf("handed over %d messages", len(received))
	}
	mu.Unlock()
	c.stop()
	waitStatus(t, s, "2", "Stopped")
}

func TestControllerReportsOnlySafeSessionErrors(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
		want string
	}{
		{"busy", plugin.ErrBusy, "wait for the answer first"},
		{"wrapped busy", fmt.Errorf("secret access token: %w", plugin.ErrBusy), "wait for the answer first"},
		{"restarting", plugin.ErrRestarting, "ouro is restarting with the new build; send again once it is back"},
		{"wrapped restarting", fmt.Errorf("secret access token: %w", plugin.ErrRestarting), "ouro is restarting with the new build; send again once it is back"},
		{"stopped", plugin.ErrSessionStopped, "the node is stopping"},
		{"wrapped stopped", fmt.Errorf("secret access token: %w", plugin.ErrSessionStopped), "the node is stopping"},
		{"unknown", errors.New("secret access token"), "Ouro could not accept this message. Check the local session and gateway settings."},
		{"wrapped unknown", fmt.Errorf("secret access token: %w", errors.New("private session details")), "Ouro could not accept this message. Check the local session and gateway settings."},
		{"denied", plugin.ErrGatewayDenied, ""},
		{"wrapped denied", fmt.Errorf("secret access token: %w", plugin.ErrGatewayDenied), ""},
		{"success", nil, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := store{t.TempDir()}
			if err := s.save(configuration{Enabled: true, Token: "token", Revision: "1"}); err != nil {
				t.Fatal(err)
			}
			conn := newLiveConnection()
			c := &controller{store: s,
				connect: func(context.Context, configuration) (connection, error) { return conn, nil },
				receive: func(context.Context, connection, incoming) error { return tt.err }}
			c.start()
			defer c.stop()
			deliver := conn.running(t)
			msg := incoming{RoomID: "!room:test", Sender: "@owner:test", Text: "hello"}
			if err := deliver(context.Background(), msg); err != tt.err {
				t.Fatalf("receive: %v, want original error %v", err, tt.err)
			}
			var want []string
			if tt.want != "" {
				want = []string{"!room:test @owner:test " + tt.want}
			}
			if sent := conn.messages(); !slices.Equal(sent, want) {
				t.Fatalf("sent %q, want %q", sent, want)
			}
		})
	}
}

func TestControllerReportsAndRetriesAFailedConnection(t *testing.T) {
	s := store{t.TempDir()}
	if err := s.save(configuration{Enabled: true, Token: "token", Revision: "1"}); err != nil {
		t.Fatal(err)
	}
	conn := healthyConnection{newLiveConnection()}
	conn.ending = make(chan error, 1)
	var connects atomic.Int32
	c := &controller{store: s, connect: func(context.Context, configuration) (connection, error) {
		connects.Add(1)
		return conn, nil
	}}
	c.start()
	conn.running(t)
	waitStatus(t, s, "1", "Synced")
	// The connection's own state goes with it.
	conn.ending <- errors.New("sync failed")
	waitStatus(t, s, "1", "Connection failed; retrying")
	if conn.closed.Load() != 1 {
		t.Fatal("a failed connection was not closed")
	}
	// Stopping does not wait for the retry.
	c.stop()
	waitStatus(t, s, "1", "Stopped")
	if connects.Load() != 1 {
		t.Fatalf("connected %d times", connects.Load())
	}
}

func TestControllerReportsWhatKeepsItFromConnecting(t *testing.T) {
	s := store{t.TempDir()}
	if err := s.save(configuration{Enabled: true, Revision: "1"}); err != nil {
		t.Fatal(err)
	}
	var connects atomic.Int32
	c := &controller{store: s, connect: func(context.Context, configuration) (connection, error) {
		connects.Add(1)
		return nil, errors.New("homeserver unreachable")
	}}
	c.start()
	waitStatus(t, s, "1", "Not logged in")
	if err := s.save(configuration{Enabled: true, Token: "token", Revision: "2"}); err != nil {
		t.Fatal(err)
	}
	waitStatus(t, s, "2", "Connection failed; retrying")
	c.stop()
	waitStatus(t, s, "2", "Stopped")
	if connects.Load() != 1 {
		t.Fatalf("connected %d times", connects.Load())
	}
}
