package matrix

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	transport "ouroboros/internal/matrix"
	"ouroboros/plugin"
)

type verificationFake struct {
	*liveConnection
	mu        sync.Mutex
	state     plugin.GatewayVerification
	actions   []string
	err       error
	statusErr error
}

func (f *verificationFake) VerificationStatus(ctx context.Context) (plugin.GatewayVerification, error) {
	if _, ok := ctx.Deadline(); !ok {
		panic("unbounded status")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.state, f.statusErr
}
func (f *verificationFake) VerificationAction(ctx context.Context, action, txn string) error {
	if _, ok := ctx.Deadline(); !ok {
		panic("unbounded action")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.actions = append(f.actions, action+":"+txn)
	return f.err
}
func verificationFixture(t *testing.T) (store, configuration, *verificationFake) {
	t.Helper()
	s := store{t.TempDir()}
	cfg := configuration{Enabled: true, Token: "secret", DeviceID: "device", Revision: "r1"}
	if err := s.save(cfg); err != nil {
		t.Fatal(err)
	}
	f := &verificationFake{liveConnection: newLiveConnection(), state: plugin.GatewayVerification{Stage: "comparing", TransactionID: "t1", DeviceID: "other", Emojis: []string{"🐶", "🐱", "🦁", "🐎", "🦄", "🐷", "🐘"}}}
	return s, cfg, f
}
func writeVerificationFixture(t *testing.T, s store, st verificationStatus) {
	t.Helper()
	data, _ := json.Marshal(st)
	if err := os.WriteFile(s.path("verification.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
}
func TestVerificationMailboxPreservesSettingsAndSerializes(t *testing.T) {
	s, cfg, f := verificationFixture(t)
	s.verificationTick(context.Background(), cfg.Revision, "session", f, map[string]bool{})
	before, _ := os.ReadFile(s.path("settings.json"))
	action := plugin.GatewayChange{VerificationAction: "confirm", VerificationTransactionID: "t1"}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); results <- s.change(context.Background(), action, nil) }()
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("queued %d actions", successes)
	}
	after, _ := os.ReadFile(s.path("settings.json"))
	if string(before) != string(after) {
		t.Fatal("verification rewrote settings")
	}
	for _, name := range []string{"verification.json", "verification-action.json"} {
		fi, err := os.Stat(s.path(name))
		if err != nil || fi.Mode().Perm() != 0600 {
			t.Fatalf("mailbox permissions: %v %v", fi, err)
		}
	}
	consumed := map[string]bool{}
	s.verificationTick(context.Background(), cfg.Revision, "session", f, consumed)
	s.verificationTick(context.Background(), cfg.Revision, "session", f, consumed)
	// A second explicit confirmation of the same transaction cannot replay it.
	if err := s.change(context.Background(), action, nil); err != nil {
		t.Fatal(err)
	}
	s.verificationTick(context.Background(), cfg.Revision, "session", f, consumed)
	if len(f.actions) != 1 || f.actions[0] != "confirm:t1" {
		t.Fatalf("actions %v", f.actions)
	}
}
func TestVerificationRejectsStaleDisabledAndMismatchedActions(t *testing.T) {
	for _, test := range []string{"old", "future", "revision", "disabled", "loggedout", "transaction", "noemoji", "terminal", "unknown", "mixed", "mixed-enabled"} {
		t.Run(test, func(t *testing.T) {
			s, cfg, f := verificationFixture(t)
			st := verificationStatus{Revision: cfg.Revision, Session: "s1", At: time.Now(), Verification: f.state}
			action := plugin.GatewayChange{VerificationAction: "confirm", VerificationTransactionID: "t1"}
			switch test {
			case "old":
				st.At = time.Now().Add(-time.Minute)
			case "future":
				st.At = time.Now().Add(time.Minute)
			case "revision":
				st.Revision = "old"
			case "disabled":
				cfg.Enabled = false
			case "loggedout":
				cfg.Token = ""
			case "transaction":
				action.VerificationTransactionID = "t2"
			case "noemoji":
				st.Verification.Emojis = nil
			case "terminal":
				st.Verification.Stage = "done"
			case "unknown":
				action.VerificationAction = "trust"
			case "mixed":
				action.Password = "secret"
			case "mixed-enabled":
				action.Enabled = true
			}
			if err := s.save(cfg); err != nil {
				t.Fatal(err)
			}
			writeVerificationFixture(t, s, st)
			if err := s.change(context.Background(), action, nil); err == nil {
				t.Fatal("accepted invalid action")
			}
			snapshot, err := s.snapshot()
			if err != nil {
				t.Fatal(err)
			}
			if test == "old" || test == "future" || test == "revision" || test == "disabled" || test == "loggedout" {
				if snapshot.Verification != nil {
					t.Fatal("exposed invalid status")
				}
			}
		})
	}
}
func TestVerificationTickRevalidatesRevisionSessionAndTransaction(t *testing.T) {
	for _, test := range []string{"revision", "reconnect", "transaction", "expired", "disabled"} {
		t.Run(test, func(t *testing.T) {
			s, cfg, f := verificationFixture(t)
			s.verificationTick(context.Background(), cfg.Revision, "s1", f, map[string]bool{})
			if err := s.change(context.Background(), plugin.GatewayChange{VerificationAction: "confirm", VerificationTransactionID: "t1"}, nil); err != nil {
				t.Fatal(err)
			}
			session := "s1"
			switch test {
			case "revision":
				cfg.Revision = "r2"
			case "reconnect":
				session = "s2"
			case "transaction":
				f.state.TransactionID = "t2"
			case "disabled":
				cfg.Enabled = false
			case "expired":
				data, _ := os.ReadFile(s.path("verification-action.json"))
				var req verificationRequest
				json.Unmarshal(data, &req)
				req.At = time.Now().Add(-time.Minute)
				data, _ = json.Marshal(req)
				os.WriteFile(s.path("verification-action.json"), data, 0600)
			}
			if err := s.save(cfg); err != nil {
				t.Fatal(err)
			}
			s.verificationTick(context.Background(), cfg.Revision, session, f, map[string]bool{})
			if len(f.actions) != 0 {
				t.Fatalf("executed stale action %v", f.actions)
			}
		})
	}
}
func TestVerificationActionsAndSanitizedErrors(t *testing.T) {
	for _, action := range []string{"start", "cancel", "confirm"} {
		t.Run(action, func(t *testing.T) {
			s, cfg, f := verificationFixture(t)
			txn := "t1"
			if action == "start" {
				f.state = plugin.GatewayVerification{Stage: "idle"}
				txn = ""
			}
			s.verificationTick(context.Background(), cfg.Revision, "s1", f, map[string]bool{})
			if err := s.change(context.Background(), plugin.GatewayChange{VerificationAction: action, VerificationTransactionID: txn}, nil); err != nil {
				t.Fatal(err)
			}
			f.err = errors.New("SECRET SDK RESPONSE")
			s.verificationTick(context.Background(), cfg.Revision, "s1", f, map[string]bool{})
			if len(f.actions) != 1 || f.actions[0] != action+":"+txn {
				t.Fatalf("actions %v", f.actions)
			}
			snapshot, _ := s.snapshot()
			if snapshot.Verification == nil || snapshot.Verification.Stage != "failed" || strings.Contains(snapshot.Verification.Message, "SECRET") {
				t.Fatalf("unsafe status %+v", snapshot.Verification)
			}
			f.statusErr = errors.New("SECRET STATUS")
			s.verificationTick(context.Background(), cfg.Revision, "s1", f, map[string]bool{})
			snapshot, _ = s.snapshot()
			if snapshot.Verification.Stage != "unavailable" || strings.Contains(snapshot.Verification.Message, "SECRET") {
				t.Fatalf("unsafe status %+v", snapshot.Verification)
			}
		})
	}
}
func TestControllerVerificationUsesLiveConnection(t *testing.T) {
	s, _, f := verificationFixture(t)
	c := &controller{store: s, connect: func(context.Context, configuration) (connection, error) { return f, nil }}
	c.start()
	defer c.stop()
	f.running(t)
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		snapshot, _ := s.snapshot()
		if snapshot.Verification != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no verification status")
		}
	}
	if err := s.change(context.Background(), plugin.GatewayChange{VerificationAction: "confirm", VerificationTransactionID: "t1"}, nil); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		f.mu.Lock()
		count := len(f.actions)
		f.mu.Unlock()
		if count == 1 {
			c.stop()
			snapshot, err := s.snapshot()
			if err != nil || snapshot.Verification != nil {
				t.Fatalf("verification survived disconnect: %+v %v", snapshot.Verification, err)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("action not executed on live connection")
		}
	}
}
func TestSessionConnectionForwardsVerification(t *testing.T) {
	_, _, f := verificationFixture(t)
	wrapped := &sessionConnection{connection: f}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	status, err := wrapped.VerificationStatus(ctx)
	if err != nil || status.TransactionID != "t1" {
		t.Fatalf("forward status: %+v %v", status, err)
	}
	if err := wrapped.VerificationAction(ctx, "cancel", "t1"); err != nil {
		t.Fatal(err)
	}
	if len(f.actions) != 1 || f.actions[0] != "cancel:t1" {
		t.Fatalf("forward action: %v", f.actions)
	}
}

func TestVerificationTransportAdapter(t *testing.T) {
	exchanging := publicVerification(transport.VerificationSnapshot{Stage: "exchanging", TransactionID: "txn"})
	if exchanging.Stage != "ready" || !verificationAllowed("cancel", "txn", exchanging) {
		t.Fatal("key exchange must remain cancellable")
	}
	state := publicVerification(transport.VerificationSnapshot{Stage: "comparing", TransactionID: "txn", DeviceID: "device", Error: "secret", Emojis: []transport.VerificationEmoji{{Symbol: "🐶", Description: "untrusted remote description"}}})
	if state.Stage != "comparing" || state.TransactionID != "txn" || state.DeviceID != "device" || len(state.Emojis) != 1 || state.Emojis[0] != "🐶" || strings.Contains(state.Message, "secret") {
		t.Fatalf("mapping %+v", state)
	}
	for _, stage := range []string{"idle", "requested", "ready", "confirmed", "done", "cancelled", "failed", "unknown"} {
		state := publicVerification(transport.VerificationSnapshot{Stage: stage, Error: "secret"})
		if state.Message == "" || strings.Contains(state.Message, "secret") {
			t.Fatalf("unsafe stage mapping %+v", state)
		}
	}
	adapter := &sdkConnection{client: &transport.Client{}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := adapter.VerificationStatus(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("ignored cancellation: %v", err)
	}
	if _, err := adapter.VerificationStatus(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"start", "confirm", "cancel", "unknown"} {
		if err := adapter.VerificationAction(context.Background(), action, ""); err == nil {
			t.Fatalf("accepted %s on an inactive SDK client", action)
		}
	}
}
