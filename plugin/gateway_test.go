package plugin_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"ouroboros/internal/agent"
	"ouroboros/internal/assistant"
	"ouroboros/internal/openai"
	"ouroboros/internal/session"
	"ouroboros/plugin"
)

var gatewayAlice = plugin.GatewayIdentity{Gateway: "test-installation", Subject: "alice"}

func gatewayOwner(context.Context, plugin.GatewayIdentity, plugin.GatewayOperation) (plugin.GatewayPermission, error) {
	return plugin.GatewayPermission{OwnerSession: true}, nil
}

type gatewayBackend struct {
	plugin.Sessions      // Unexpected calls panic instead of reaching a real node.
	mu                   sync.Mutex
	created              int
	ask, steer, canceled int
	text                 string
	source               openai.MessageSource
	past                 []plugin.SessionEvent
	watch                context.Context
	err                  error
}

func (s *gatewayBackend) New(context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.created++
	return fmt.Sprintf("gateway-%d", s.created), s.err
}
func (s *gatewayBackend) Ask(ctx context.Context, _, text string) error {
	s.ask++
	s.text, s.source = text, openai.MessageSourceFrom(ctx)
	return s.err
}
func (s *gatewayBackend) Steer(ctx context.Context, _, text string) error {
	s.steer++
	s.text, s.source = text, openai.MessageSourceFrom(ctx)
	return s.err
}
func (s *gatewayBackend) Watch(ctx context.Context, _ string) ([]plugin.SessionEvent, <-chan plugin.SessionEvent, error) {
	s.watch = ctx
	return s.past, nil, s.err
}
func (s *gatewayBackend) Cancel(context.Context, string) error {
	s.canceled++
	return s.err
}

func TestGatewayDeniesImplicitOwner(t *testing.T) {
	for name, policy := range map[string]plugin.GatewayAuthorize{
		"missing": nil,
		"zero grant": func(context.Context, plugin.GatewayIdentity, plugin.GatewayOperation) (plugin.GatewayPermission, error) {
			return plugin.GatewayPermission{}, nil
		},
		"policy error": func(context.Context, plugin.GatewayIdentity, plugin.GatewayOperation) (plugin.GatewayPermission, error) {
			return plugin.GatewayPermission{OwnerSession: true}, errors.New("revoked")
		},
	} {
		t.Run(name, func(t *testing.T) {
			backend := &gatewayBackend{}
			g := plugin.NewGatewayAdapter(backend, policy)
			if _, err := g.Open(t.Context(), gatewayAlice); !errors.Is(err, plugin.ErrGatewayDenied) {
				t.Fatalf("open: %v", err)
			}
			if backend.created != 0 {
				t.Fatal("denied request reached backend")
			}
		})
	}
	for _, who := range []plugin.GatewayIdentity{{}, {Gateway: "test"}, {Gateway: " ", Subject: "alice"}} {
		if _, err := plugin.NewGatewayAdapter(&gatewayBackend{}, gatewayOwner).Open(t.Context(), who); !errors.Is(err, plugin.ErrGatewayDenied) {
			t.Fatalf("empty identity: %v", err)
		}
	}
	if _, err := plugin.NewGatewayAdapter(nil, gatewayOwner).Open(t.Context(), gatewayAlice); !errors.Is(err, plugin.ErrGatewayDenied) {
		t.Fatalf("nil backend: %v", err)
	}
	var zero plugin.GatewayAdapter
	if _, err := zero.Open(t.Context(), gatewayAlice); !errors.Is(err, plugin.ErrGatewayDenied) {
		t.Fatalf("zero adapter: %v", err)
	}
}

func TestGatewayIsolationAndRevocation(t *testing.T) {
	backend := &gatewayBackend{}
	revoked := false
	var ops []plugin.GatewayOperation
	g := plugin.NewGatewayAdapter(backend, func(_ context.Context, _ plugin.GatewayIdentity, op plugin.GatewayOperation) (plugin.GatewayPermission, error) {
		ops = append(ops, op)
		return plugin.GatewayPermission{OwnerSession: !revoked}, nil
	})
	id, err := g.Open(t.Context(), gatewayAlice)
	if err != nil {
		t.Fatal(err)
	}
	calls := []struct {
		op  plugin.GatewayOperation
		run func(plugin.GatewayIdentity, string) error
	}{
		{plugin.GatewaySend, func(w plugin.GatewayIdentity, id string) error { return g.Send(t.Context(), w, id, "go") }},
		{plugin.GatewayPoll, func(w plugin.GatewayIdentity, id string) error { _, err := g.Poll(t.Context(), w, id); return err }},
		{plugin.GatewayCancel, func(w plugin.GatewayIdentity, id string) error { return g.Cancel(t.Context(), w, id) }},
		{plugin.GatewayReply, func(w plugin.GatewayIdentity, id string) error { return g.Reply(t.Context(), w, id, "yes") }},
	}
	for _, call := range calls {
		for _, who := range []plugin.GatewayIdentity{
			{Gateway: gatewayAlice.Gateway, Subject: "bob"},
			{Gateway: "other-installation", Subject: gatewayAlice.Subject},
		} {
			if err := call.run(who, id); !errors.Is(err, plugin.ErrGatewaySession) {
				t.Fatalf("%s other identity: %v", call.op, err)
			}
		}
		if err := call.run(gatewayAlice, "owner-existing-session"); !errors.Is(err, plugin.ErrGatewaySession) {
			t.Fatalf("%s arbitrary session: %v", call.op, err)
		}
		revoked = true
		if err := call.run(gatewayAlice, id); !errors.Is(err, plugin.ErrGatewayDenied) {
			t.Fatalf("%s revoked: %v", call.op, err)
		}
		revoked = false
		if err := call.run(gatewayAlice, id); err != nil {
			t.Fatalf("%s allowed: %v", call.op, err)
		}
		if ops[len(ops)-1] != call.op {
			t.Fatalf("wrong operation: %v", ops)
		}
	}
	if backend.ask != 1 || backend.steer != 1 || backend.canceled != 1 {
		t.Fatalf("unexpected backend calls: %+v", backend)
	}
	// A new adapter cannot attach old bindings, even with the same policy.
	if err := plugin.NewGatewayAdapter(backend, gatewayOwner).Send(t.Context(), gatewayAlice, id, "go"); !errors.Is(err, plugin.ErrGatewaySession) {
		t.Fatalf("attachment: %v", err)
	}
}

func TestGatewaySnapshotsAndBackendErrors(t *testing.T) {
	backend := &gatewayBackend{past: []plugin.SessionEvent{{Kind: plugin.SessionAsked, Args: json.RawMessage(`{"question":"Which?"}`)}}}
	g := plugin.NewGatewayAdapter(backend, gatewayOwner)
	id, err := g.Open(t.Context(), gatewayAlice)
	if err != nil {
		t.Fatal(err)
	}
	past, err := g.Poll(t.Context(), gatewayAlice, id)
	if err != nil {
		t.Fatal(err)
	}
	if backend.watch.Err() != context.Canceled {
		t.Fatal("poll left a watcher open")
	}
	past[0].Args[0] = 'x'
	past[0].Text = "changed"
	if backend.past[0].Args[0] != '{' || backend.past[0].Text != "" {
		t.Fatal("poll exposed backend storage")
	}
	backend.err = plugin.ErrBusy
	if err := g.Send(t.Context(), gatewayAlice, id, "go"); !errors.Is(err, plugin.ErrBusy) {
		t.Fatalf("busy: %v", err)
	}
	if backend.steer != 0 {
		t.Fatal("send escalated to question reply")
	}
	if _, err := g.Poll(t.Context(), gatewayAlice, id); !errors.Is(err, plugin.ErrBusy) || backend.watch.Err() != context.Canceled {
		t.Fatalf("watch error: %v", err)
	}
	backend.err = nil
	text := "  Run it this time\n"
	if err := g.Reply(t.Context(), gatewayAlice, id, text); err != nil {
		t.Fatal(err)
	}
	if backend.text != text || backend.source.Interface != "api" {
		t.Fatalf("reply modified: %q, %+v", backend.text, backend.source)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := g.Send(ctx, gatewayAlice, id, "go"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled context: %v", err)
	}
}

func TestGatewayDurableBindings(t *testing.T) {
	backend := &gatewayBackend{}
	g := plugin.NewGatewayAdapter(backend, gatewayOwner)
	id, err := g.Open(t.Context(), gatewayAlice)
	if err != nil {
		t.Fatal(err)
	}
	// Persistence is an explicit host operation, not an implicit attachment.
	data, err := json.Marshal(g.Bindings())
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]plugin.GatewayIdentity
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	revoked := false
	restored, err := plugin.NewGatewayAdapterWithBindings(backend, func(context.Context, plugin.GatewayIdentity, plugin.GatewayOperation) (plugin.GatewayPermission, error) {
		return plugin.GatewayPermission{OwnerSession: !revoked}, nil
	}, saved)
	if err != nil {
		t.Fatal(err)
	}
	if backend.created != 1 || backend.ask != 0 {
		t.Fatal("restore called backend")
	}
	// Neither the constructor input nor a returned snapshot is live storage.
	bob := plugin.GatewayIdentity{Gateway: gatewayAlice.Gateway, Subject: "bob"}
	saved[id] = bob
	saved["owner-existing-session"] = gatewayAlice
	snapshot := restored.Bindings()
	delete(snapshot, id)
	snapshot["owner-existing-session"] = gatewayAlice
	if restored.Bindings()[id] != gatewayAlice || len(restored.Bindings()) != 1 {
		t.Fatal("bindings leaked mutable storage")
	}
	if err := restored.Send(t.Context(), gatewayAlice, id, "resumed"); err != nil {
		t.Fatal(err)
	}
	if backend.ask != 1 || backend.text != "resumed" {
		t.Fatal("restored binding did not reach backend")
	}
	for _, request := range []struct {
		who plugin.GatewayIdentity
		id  string
	}{
		{bob, id},
		{plugin.GatewayIdentity{Gateway: "another-installation", Subject: gatewayAlice.Subject}, id},
		{gatewayAlice, "owner-existing-session"},
	} {
		if err := restored.Send(t.Context(), request.who, request.id, "denied"); !errors.Is(err, plugin.ErrGatewaySession) {
			t.Fatalf("unbound request: %v", err)
		}
	}
	revoked = true
	if err := restored.Send(t.Context(), gatewayAlice, id, "revoked"); !errors.Is(err, plugin.ErrGatewayDenied) {
		t.Fatalf("restored binding bypassed revocation: %v", err)
	}
	denied, err := plugin.NewGatewayAdapterWithBindings(backend, nil, g.Bindings())
	if err != nil {
		t.Fatal(err)
	}
	if err := denied.Send(t.Context(), gatewayAlice, id, "no grant"); !errors.Is(err, plugin.ErrGatewayDenied) {
		t.Fatalf("restored binding was a grant: %v", err)
	}
	fresh := plugin.NewGatewayAdapter(backend, gatewayOwner)
	if err := fresh.Send(t.Context(), gatewayAlice, id, "not restored"); !errors.Is(err, plugin.ErrGatewaySession) {
		t.Fatalf("implicit persistence: %v", err)
	}
	if backend.ask != 1 {
		t.Fatal("denied request reached backend")
	}
	newID, err := restored.Open(t.Context(), gatewayAlice)
	if !errors.Is(err, plugin.ErrGatewayDenied) || newID != "" {
		t.Fatalf("revoked open: %q, %v", newID, err)
	}
	revoked = false
	newID, err = restored.Open(t.Context(), gatewayAlice)
	if err != nil || newID == id || len(restored.Bindings()) != 2 {
		t.Fatalf("open after restore: %q, %v", newID, err)
	}
}

func TestGatewayRestoreValidation(t *testing.T) {
	for name, bindings := range map[string]map[string]plugin.GatewayIdentity{
		"empty session": {"": gatewayAlice},
		"blank session": {" \t": gatewayAlice},
		"empty gateway": {"session": {Subject: "alice"}},
		"blank gateway": {"session": {Gateway: " ", Subject: "alice"}},
		"empty subject": {"session": {Gateway: "test"}},
		"blank subject": {"session": {Gateway: "test", Subject: " \t"}},
	} {
		t.Run(name, func(t *testing.T) {
			bindings["valid"] = gatewayAlice
			g, err := plugin.NewGatewayAdapterWithBindings(&gatewayBackend{}, gatewayOwner, bindings)
			if err == nil || g != nil {
				t.Fatalf("accepted invalid snapshot: %v, %v", g, err)
			}
		})
	}
	g, err := plugin.NewGatewayAdapterWithBindings(&gatewayBackend{}, gatewayOwner, nil)
	if err != nil || len(g.Bindings()) != 0 {
		t.Fatalf("empty snapshot: %v, %v", g, err)
	}
	// A faulty backend must not transfer even a restored binding to another user.
	g, err = plugin.NewGatewayAdapterWithBindings(&gatewayBackend{}, gatewayOwner, map[string]plugin.GatewayIdentity{"gateway-1": gatewayAlice})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.Open(t.Context(), plugin.GatewayIdentity{Gateway: "test", Subject: "bob"}); !errors.Is(err, plugin.ErrGatewaySession) {
		t.Fatalf("duplicate backend ID: %v", err)
	}
	if g.Bindings()["gateway-1"] != gatewayAlice {
		t.Fatal("duplicate ID transferred restored binding")
	}
}

// namelessBackend makes sessions without an ID.
type namelessBackend struct{ plugin.Sessions }

func (namelessBackend) New(context.Context) (string, error) { return "", nil }

// A session the backend cannot make, or makes without an ID, is bound to
// nobody.
func TestGatewayOpenThatFails(t *testing.T) {
	failed := errors.New("no session")
	backend := &gatewayBackend{err: failed}
	g := plugin.NewGatewayAdapter(backend, gatewayOwner)
	if id, err := g.Open(t.Context(), gatewayAlice); !errors.Is(err, failed) || id != "" {
		t.Fatalf("backend error: %q, %v", id, err)
	}
	if len(g.Bindings()) != 0 {
		t.Fatalf("bound %v", g.Bindings())
	}
	nameless := plugin.NewGatewayAdapter(namelessBackend{}, gatewayOwner)
	if id, err := nameless.Open(t.Context(), gatewayAlice); !errors.Is(err, plugin.ErrGatewaySession) || id != "" {
		t.Fatalf("no ID: %q, %v", id, err)
	}
	if len(nameless.Bindings()) != 0 {
		t.Fatalf("bound %v", nameless.Bindings())
	}
}

func TestGatewayConcurrentOpen(t *testing.T) {
	g := plugin.NewGatewayAdapter(&gatewayBackend{}, gatewayOwner)
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if _, err := g.Open(t.Context(), gatewayAlice); err != nil {
				t.Error(err)
			}
			for _, who := range g.Bindings() {
				if who != gatewayAlice {
					t.Error("inconsistent snapshot")
				}
			}
		})
	}
	wg.Wait()
}

// Use the real session service to verify that the adapter's request/response
// contract drives observable session events, questions, and cancellation.
type gatewayAgent struct {
	session.Assistant
}

func (*gatewayAgent) Ready() error                 { return nil }
func (*gatewayAgent) Context(string) int           { return 0 }
func (*gatewayAgent) Cache(string) assistant.Cache { return assistant.Cache{} }
func (*gatewayAgent) Effort() string               { return "" }
func (*gatewayAgent) Title(context.Context, []openai.Message) (string, error) {
	return "gateway test", nil
}
func (*gatewayAgent) Run(ctx context.Context, _, text string, _ func(assistant.Step), _ func(string)) (string, error) {
	if text == "wait" {
		<-ctx.Done()
		return "", ctx.Err()
	}
	return agent.AskOwner(ctx, json.RawMessage(`{"question":"Which?","options":["a","b"]}`))
}

func gatewayUntil(t *testing.T, g *plugin.GatewayAdapter, id string, kind plugin.SessionKind) []plugin.SessionEvent {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		events, err := g.Poll(t.Context(), gatewayAlice, id)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range events {
			if e.Kind == kind {
				return events
			}
		}
		select {
		case <-deadline.C:
			t.Fatalf("waiting for %s: %+v", kind, events)
		case <-tick.C:
		}
	}
}

func TestGatewayDrivesSessions(t *testing.T) {
	s := session.New(&gatewayAgent{})
	t.Cleanup(s.Close)
	g := plugin.NewGatewayAdapter(s, gatewayOwner)
	id, err := g.Open(t.Context(), gatewayAlice)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Send(t.Context(), gatewayAlice, id, "choose"); err != nil {
		t.Fatal(err)
	}
	events := gatewayUntil(t, g, id, plugin.SessionAsked)
	found := false
	for _, e := range events {
		if e.Kind == plugin.SessionAsked && !e.Done && e.AsQuestion().Question == "Which?" {
			found = true
		}
		if e.Kind == plugin.SessionMessage && e.Source.Interface != "api" {
			t.Fatalf("source: %+v", e)
		}
	}
	if !found {
		t.Fatalf("question missing: %+v", events)
	}
	if err := g.Send(t.Context(), gatewayAlice, id, "b"); !errors.Is(err, plugin.ErrBusy) {
		t.Fatalf("send answered a question: %v", err)
	}
	if err := g.Reply(t.Context(), gatewayAlice, id, "b"); err != nil {
		t.Fatal(err)
	}
	events = gatewayUntil(t, g, id, plugin.SessionAnswer)
	answered := false
	for _, e := range events {
		if e.Kind == plugin.SessionAnswer && e.Text == "b" {
			answered = true
		}
	}
	if !answered {
		t.Fatalf("reply did not reach agent: %+v", events)
	}
	id, err = g.Open(t.Context(), gatewayAlice)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Send(t.Context(), gatewayAlice, id, "wait"); err != nil {
		t.Fatal(err)
	}
	if err := g.Cancel(t.Context(), gatewayAlice, id); err != nil {
		t.Fatal(err)
	}
	gatewayUntil(t, g, id, plugin.SessionCanceled)
}
