//go:build linux

package tray

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/xml"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
	"github.com/godbus/dbus/v5/prop"
	"ouroboros/internal/desktop"
)

// No test talks to the session bus of the user, not even with a mutant of
// Run: a test without a bus of its own finds none.
func TestMain(m *testing.M) {
	os.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/nonexistent/ouroboros-test-bus")
	os.Exit(m.Run())
}

func TestModelAndProtocol(t *testing.T) {
	for _, tc := range []struct{ state, status, label string }{
		{"idle", "Active", "ready"}, {"working", "Active", "working (3)"},
		{"waiting", "NeedsAttention", "waiting for you (2)"}, {"error", "NeedsAttention", "status unavailable"},
		{"offline", "Active", "offline"}, {"unknown", "Active", "ready"},
	} {
		got := model(desktop.Status{State: tc.state, Running: 3, Waiting: 2})
		if got.status != tc.status || !strings.Contains(got.tooltip, tc.label) {
			t.Errorf("%s: %+v", tc.state, got)
		}
	}
	props := properties(model(desktop.Status{State: "idle"}))[itemInterface]
	for name, want := range map[string]string{"IconPixmap": "a(iiay)", "AttentionIconPixmap": "a(iiay)", "ToolTip": "(sa(iiay)ss)", "WindowId": "u", "Menu": "o", "ItemIsMenu": "b"} {
		if got := dbus.SignatureOf(props[name].Value).String(); got != want {
			t.Errorf("%s signature %s, want %s", name, got, want)
		}
	}
	for _, size := range []int{22, 32} {
		p := ring(size)
		if len(p.Data) != size*size*4 || int(p.Width) != size || p.Height != p.Width {
			t.Fatal("bad dimensions")
		}
		transparent, opaque, partial := false, false, false
		for i := 0; i < len(p.Data); i += 4 {
			switch p.Data[i] {
			case 0:
				transparent = true
			case 255:
				opaque = true
			default:
				partial = true
			}
		}
		if !transparent || !opaque || !partial {
			t.Fatalf("%d: missing transparency/opaque/antialias pixels", size)
		}
	}
	// Every pixel of the icon stays as it was drawn. Where floating point
	// operations may be fused, the last bit of a sample can differ.
	if runtime.GOARCH == "amd64" {
		for size, want := range map[int]string{
			22: "1276a3e95443ce5d697d66befd5fc5a7a615c7788e5489817851ee3108422d1f",
			32: "e20ad825953c5dfe0cefd0ef079655859bf28b71edf1d68ee435a0ca4051e34f",
		} {
			if got := fmt.Sprintf("%x", sha256.Sum256(ring(size).Data)); got != want {
				t.Errorf("ring(%d) has other pixels: sha256 %s", size, got)
			}
		}
	}
}

type fakeWatcher struct {
	registrations chan string
	refusals      atomic.Int32 // how many registrations it refuses first
	bus           *dbus.Conn   // its connection, see watcherOn
}

func (w *fakeWatcher) RegisterStatusNotifierItem(service string) *dbus.Error {
	w.registrations <- service
	if w.refusals.Add(-1) >= 0 {
		return dbus.MakeFailedError(errors.New("not now"))
	}
	return nil
}

// privateBus starts a bus of the test's own and returns its address: no test
// touches the session bus of the user.
func privateBus(t *testing.T) string {
	t.Helper()
	daemon, err := exec.LookPath("dbus-daemon")
	if err != nil {
		t.Skip("dbus-daemon unavailable: private bus integration not run")
	}
	cmd := exec.Command(daemon, "--session", "--nofork", "--print-address=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() {
		t.Fatal("no private bus address")
	}
	return scanner.Text()
}

// connect connects to the bus at address. Like the connection of Run, it
// closes when ctx is done.
func connect(t *testing.T, ctx context.Context, address string, options ...dbus.ConnOption) *dbus.Conn {
	t.Helper()
	conn, err := dbus.Connect(address, append([]dbus.ConnOption{dbus.WithContext(ctx)}, options...)...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

// watcherOn connects a fake watcher to the bus at address that refuses the
// first refusals registrations. It has no name yet, see take.
func watcherOn(t *testing.T, address string, refusals int32) *fakeWatcher {
	t.Helper()
	watcher := &fakeWatcher{registrations: make(chan string, 8), bus: connect(t, context.Background(), address)}
	watcher.refusals.Store(refusals)
	if err := watcher.bus.Export(watcher, watcherPath, watcherName); err != nil {
		t.Fatal(err)
	}
	return watcher
}

// take owns the name of the watcher, also when another watcher has it.
func (w *fakeWatcher) take(t *testing.T) {
	t.Helper()
	flags := dbus.NameFlagAllowReplacement | dbus.NameFlagReplaceExisting | dbus.NameFlagDoNotQueue
	if reply, err := w.bus.RequestName(watcherName, flags); err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("watcher name: %v %v", reply, err)
	}
}

// watch owns the name of the watcher on the bus at address with a fake that
// refuses the first refusals registrations. The bus then never starts a
// watcher of the system for the name. A later watch takes the name over.
func watch(t *testing.T, address string, refusals int32) *fakeWatcher {
	t.Helper()
	watcher := watcherOn(t, address, refusals)
	watcher.take(t)
	return watcher
}

// registration waits for the next registration with watcher.
func registration(t *testing.T, watcher *fakeWatcher) string {
	t.Helper()
	select {
	case name := <-watcher.registrations:
		return name
	case <-time.After(10 * time.Second):
		t.Fatal("no watcher registration")
	}
	return ""
}

func TestPrivateBusLifecycle(t *testing.T) {
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", privateBus(t))
	client, err := dbus.ConnectSessionBus()
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	watcher := &fakeWatcher{registrations: make(chan string, 8)}
	if err = client.Export(watcher, watcherPath, watcherName); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	var waiting atomic.Bool
	opened := make(chan struct{}, 3)
	go func() {
		done <- Run(ctx, func(context.Context) desktop.Status {
			if waiting.Load() {
				return desktop.Status{State: "waiting", Waiting: 2, Running: 1, Sessions: []string{"Research session"}}
			}
			return desktop.Status{State: "idle"}
		}, func() { opened <- struct{}{} })
	}()
	// Start the watcher after Run: exercise late appearance as well as restart.
	if reply, err := client.RequestName(watcherName, dbus.NameFlagDoNotQueue); err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		t.Fatalf("watcher name: %v %v", reply, err)
	}
	awaitRegistration := func() string {
		t.Helper()
		select {
		case name := <-watcher.registrations:
			if !strings.HasPrefix(name, ":") {
				t.Fatalf("registration must use unique name: %q", name)
			}
			return name
		case err := <-done:
			t.Fatalf("Run exited early: %v", err)
		case <-time.After(6 * time.Second):
			t.Fatal("no watcher registration")
		}
		return ""
	}
	service := awaitRegistration()
	object := client.Object(service, itemPath)
	callCtx, callCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer callCancel()
	for _, method := range []string{"Activate", "SecondaryActivate", "ContextMenu", "Scroll"} {
		args := []any{int32(0), int32(0)}
		if method == "Scroll" {
			args = []any{int32(1), "vertical"}
		}
		if err := object.CallWithContext(callCtx, itemInterface+"."+method, 0, args...).Err; err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		select {
		case <-opened:
			t.Fatal("SNI activation must not open the GUI")
		default:
		}
	}
	// Read the raw map: godbus Store reconstructs variants containing structs
	// from generic slices and can lose their original wire signatures.
	getAll := func() map[string]dbus.Variant {
		t.Helper()
		call := object.CallWithContext(callCtx, "org.freedesktop.DBus.Properties.GetAll", 0, itemInterface)
		if call.Err != nil {
			t.Fatal(call.Err)
		}
		return call.Body[0].(map[string]dbus.Variant)
	}
	all := getAll()
	if all["ItemIsMenu"].Value() != true || all["Menu"].Value() != menuPath || all["IconPixmap"].Signature().String() != "a(iiay)" {
		t.Fatalf("bad property values/signatures: menu=%v pixmaps=%s", all["ItemIsMenu"], all["IconPixmap"].Signature())
	}
	if err := object.CallWithContext(callCtx, "org.freedesktop.DBus.Properties.Set", 0, itemInterface, "Status", dbus.MakeVariant("Passive")).Err; err == nil {
		t.Fatal("properties should be read-only")
	}
	var xmlText string
	if err := object.CallWithContext(callCtx, "org.freedesktop.DBus.Introspectable.Introspect", 0).Store(&xmlText); err != nil {
		t.Fatal(err)
	}
	var node introspect.Node
	if err := xml.Unmarshal([]byte(xmlText), &node); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(xmlText, "NewToolTip") || !strings.Contains(xmlText, "SecondaryActivate") {
		t.Fatal("incomplete introspection")
	}
	testMenuProtocol(t, client, service, opened)
	duplicateCtx, duplicateCancel := context.WithTimeout(context.Background(), time.Second)
	err = Run(duplicateCtx, func(context.Context) desktop.Status { return desktop.Status{} }, func() {})
	duplicateCancel()
	if err == nil || !strings.Contains(err.Error(), "another Ouroboros tray") {
		t.Fatalf("duplicate Run: %v", err)
	}
	signals := make(chan *dbus.Signal, 16)
	client.Signal(signals)
	defer client.RemoveSignal(signals)
	if err := client.AddMatchSignal(dbus.WithMatchObjectPath(itemPath)); err != nil {
		t.Fatal(err)
	}
	if err := client.AddMatchSignal(dbus.WithMatchObjectPath(menuPath)); err != nil {
		t.Fatal(err)
	}
	waiting.Store(true)
	sawStatus, sawTooltip, sawProperties, sawLayout := false, false, false, false
	timeout := time.NewTimer(6 * time.Second)
	defer timeout.Stop()
	for !sawStatus || !sawTooltip || !sawProperties || !sawLayout {
		select {
		case sig := <-signals:
			switch sig.Name {
			case menuInterface + ".LayoutUpdated":
				sawLayout = len(sig.Body) == 2 && sig.Body[0] == uint32(2) && sig.Body[1] == int32(0)
			case itemInterface + ".NewStatus":
				sawStatus = len(sig.Body) == 1 && sig.Body[0] == "NeedsAttention"
			case itemInterface + ".NewToolTip":
				sawTooltip = true
			case "org.freedesktop.DBus.Properties.PropertiesChanged":
				sawProperties = true
			}
		case <-timeout.C:
			t.Fatalf("missing change signals: status=%v tooltip=%v properties=%v", sawStatus, sawTooltip, sawProperties)
		}
	}
	menuObject := client.Object(service, menuPath)
	var revision uint32
	var layout menuLayout
	if err := menuObject.CallWithContext(callCtx, menuInterface+".GetLayout", 0, int32(0), int32(-1), []string{}).Store(&revision, &layout); err != nil {
		t.Fatal(err)
	}
	if revision != 2 {
		t.Fatalf("menu revision: %d", revision)
	}
	found := false
	for _, child := range layout.Children {
		var row menuLayout
		if err := dbus.Store([]any{child.Value()}, &row); err != nil {
			t.Fatal(err)
		}
		if row.ID == 100 && row.Properties["label"].Value() == "Research session" && row.Properties["enabled"].Value() == false {
			found = true
		}
	}
	if !found {
		t.Fatal("running session name missing from updated menu")
	}
	all = getAll()
	if all["Status"].Value() != "NeedsAttention" {
		t.Fatal("status did not update")
	}
	var tip tooltip
	if err := dbus.Store([]any{all["ToolTip"].Value()}, &tip); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tip.Description, "waiting for you (2)") || tip.Title != tip.Description {
		t.Fatalf("tooltip: %+v", tip)
	}
	if _, err := client.ReleaseName(watcherName); err != nil {
		t.Fatal(err)
	}
	if _, err := client.RequestName(watcherName, dbus.NameFlagDoNotQueue); err != nil {
		t.Fatal(err)
	}
	if got := awaitRegistration(); got != service {
		t.Fatalf("restarted registration: %s != %s", got, service)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not stop")
	}
	// Its private connection released the singleton; the independent client lives.
	// Closing a connection returns before dbus-daemon necessarily processes
	// its EOF. Wait for that observable release rather than race the daemon.
	deadline := time.Now().Add(3 * time.Second)
	for {
		reply, err := client.RequestName(busName, dbus.NameFlagDoNotQueue)
		if err != nil {
			t.Fatal(err)
		}
		if reply == dbus.RequestNameReplyPrimaryOwner {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("name not released: %v", reply)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestInvalidCallbacks(t *testing.T) {
	const required = "tray: sample and open callbacks are required"
	if err := Run(context.Background(), nil, func() {}); err == nil || err.Error() != required {
		t.Fatalf("nil sampler accepted: %v", err)
	}
	if err := Run(context.Background(), func(context.Context) desktop.Status { return desktop.Status{} }, nil); err == nil || err.Error() != required {
		t.Fatalf("nil open accepted: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Run(ctx, func(context.Context) desktop.Status { t.Fatal("sample called"); return desktop.Status{} }, func() {}); err != nil {
		t.Fatal(err)
	}
}

// Without a session bus Run fails and says why. Stopped while it connects to
// a bus that does not answer, it just stops. It samples nothing either way.
func TestRunWithoutBus(t *testing.T) {
	sample := func(context.Context) desktop.Status { t.Error("sampled without a bus"); return desktop.Status{} }
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path="+filepath.Join(t.TempDir(), "missing"))
	if err := Run(context.Background(), sample, func() {}); err == nil || !strings.Contains(err.Error(), "tray: connect session bus") {
		t.Fatalf("missing bus: %v", err)
	}
	socket := filepath.Join(t.TempDir(), "silent")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path="+socket)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	accepted := make(chan net.Conn, 1)
	go func() {
		if conn, err := listener.Accept(); err == nil {
			accepted <- conn
		}
		cancel() // Run connects and waits for an answer
	}()
	if err := Run(ctx, sample, func() {}); err != nil {
		t.Fatalf("stopped while connecting: %v", err)
	}
	select {
	case conn := <-accepted:
		conn.Close()
	default:
		t.Fatal("Run did not connect")
	}
}

type foreignHandler struct{}

func (foreignHandler) LookupObject(dbus.ObjectPath) (dbus.ServerObject, bool) { return nil, false }

// What keeps the tray from starting is an error of run: a connection that
// closed, one whose handler exports nothing, and a stop while it sets up.
func TestRunSetupFailures(t *testing.T) {
	idle := func(context.Context) desktop.Status { return desktop.Status{State: "idle"} }
	t.Run("closed", func(t *testing.T) {
		conn := connect(t, context.Background(), privateBus(t))
		conn.Close()
		if err := run(context.Background(), conn, time.Millisecond, idle, func() {}); err == nil || !strings.Contains(err.Error(), "tray: acquire name") {
			t.Fatalf("closed connection: %v", err)
		}
	})
	t.Run("foreign handler", func(t *testing.T) {
		conn := connect(t, context.Background(), privateBus(t), dbus.WithHandler(foreignHandler{}))
		if err := run(context.Background(), conn, time.Millisecond, idle, func() {}); err == nil || !strings.Contains(err.Error(), "tray: export item") {
			t.Fatalf("foreign handler: %v", err)
		}
	})
	t.Run("stopped", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		conn := connect(t, ctx, privateBus(t))
		stop := func(context.Context) desktop.Status { cancel(); return desktop.Status{State: "idle"} }
		if err := run(ctx, conn, time.Millisecond, stop, func() {}); !errors.Is(err, context.Canceled) {
			t.Fatalf("stopped while setting up: %v", err)
		}
	})
}

// A watcher that refuses the registration gets it again with the next sample.
// Once it took it, and while nothing changes, the tray neither registers again
// nor publishes a new menu. Stopped, it returns no error.
func TestRunRegistersAgain(t *testing.T) {
	address := privateBus(t)
	watcher := watch(t, address, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn := connect(t, ctx, address)
	sampled := make(chan struct{}, 1)
	sample := func(context.Context) desktop.Status {
		select {
		case sampled <- struct{}{}:
		default:
		}
		return desktop.Status{State: "idle"}
	}
	nextSample := func() {
		t.Helper()
		select {
		case <-sampled:
		default:
		}
		select {
		case <-sampled:
		case <-time.After(10 * time.Second):
			t.Fatal("not sampled")
		}
	}
	done := make(chan error, 1)
	go func() { done <- run(ctx, conn, time.Millisecond, sample, func() {}) }()
	service := registration(t, watcher)
	if again := registration(t, watcher); again != service {
		t.Fatalf("registered %s, then %s", service, again)
	}
	// The sample after the next one comes after a whole turn with the watcher.
	nextSample()
	nextSample()
	select {
	case name := <-watcher.registrations:
		t.Fatalf("registered again after the watcher took it: %s", name)
	default:
	}
	callCtx, callCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer callCancel()
	var revision uint32
	var layout menuLayout
	client := connect(t, context.Background(), address)
	if err := client.Object(service, menuPath).CallWithContext(callCtx, menuInterface+".GetLayout", 0, int32(0), int32(0), []string{}).Store(&revision, &layout); err != nil {
		t.Fatal(err)
	}
	if revision != 1 {
		t.Fatalf("an unchanged status published menu revision %d", revision)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("stopped: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not stop")
	}
}

// The tray follows the watcher without a sample in between: it registers
// with a watcher that takes over from the one before.
func TestRunFollowsTheWatcher(t *testing.T) {
	address := privateBus(t)
	before := watch(t, address, 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn := connect(t, ctx, address)
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, conn, time.Hour, func(context.Context) desktop.Status { return desktop.Status{State: "idle"} }, func() {})
	}()
	service := registration(t, before)
	if again := registration(t, watch(t, address, 0)); again != service {
		t.Fatalf("registered %s, then %s", service, again)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("stopped: %v", err)
	}
}

// send sends the tray at service a signal of a name, as if it was one the
// tray subscribed to.
func send(t *testing.T, from *dbus.Conn, service, name string, body ...any) {
	t.Helper()
	dot := strings.LastIndex(name, ".")
	msg := &dbus.Message{Type: dbus.TypeSignal, Headers: map[dbus.HeaderField]dbus.Variant{
		dbus.FieldPath:        dbus.MakeVariant(dbus.ObjectPath("/org/freedesktop/DBus")),
		dbus.FieldInterface:   dbus.MakeVariant(name[:dot]),
		dbus.FieldMember:      dbus.MakeVariant(name[dot+1:]),
		dbus.FieldDestination: dbus.MakeVariant(service),
		dbus.FieldSignature:   dbus.MakeVariant(dbus.SignatureOf(body...)),
	}, Body: body}
	if call := from.Send(msg, nil); call.Err != nil {
		t.Fatal(call.Err)
	}
}

// Only the watcher changing hands makes the tray register again: neither
// another signal with the same arguments, nor the change of another name.
func TestRunIgnoresOtherSignals(t *testing.T) {
	address := privateBus(t)
	first := watch(t, address, 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn := connect(t, ctx, address)
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, conn, time.Hour, func(context.Context) desktop.Status { return desktop.Status{State: "idle"} }, func() {})
	}()
	service := registration(t, first)
	// The signals come from the connection of the next watcher, before it
	// takes the name over: the tray sees them first.
	second := watcherOn(t, address, 0)
	send(t, second.bus, service, "org.example.Tray.Changed", watcherName, "", ":1.1")
	send(t, second.bus, service, "org.freedesktop.DBus.NameOwnerChanged", "org.example.Other", "", ":1.1")
	second.take(t)
	registration(t, second)
	// Once a third watcher has the registration, the tray handled all before.
	registration(t, watch(t, address, 0))
	for name, w := range map[string]*fakeWatcher{"first": first, "second": second} {
		select {
		case <-w.registrations:
			t.Fatalf("the %s watcher got a registration for another signal", name)
		default:
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("stopped: %v", err)
	}
}

// A change shows at once: the menu, the status and the tooltip are new
// before the next sample.
func TestRunPublishesChanges(t *testing.T) {
	address := privateBus(t)
	watcher := watch(t, address, 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn := connect(t, ctx, address)
	// Each sample after the first waits for the test to hand out its status.
	calls, statuses := make(chan struct{}), make(chan desktop.Status)
	first, last := true, desktop.Status{State: "idle"}
	sample := func(ctx context.Context) desktop.Status {
		if first {
			first = false
			return last
		}
		select {
		case calls <- struct{}{}:
		case <-ctx.Done():
			return last
		}
		select {
		case last = <-statuses:
		case <-ctx.Done():
		}
		return last
	}
	done := make(chan error, 1)
	go func() { done <- run(ctx, conn, time.Millisecond, sample, func() {}) }()
	service := registration(t, watcher)
	await := func() {
		t.Helper()
		select {
		case <-calls:
		case err := <-done:
			t.Fatalf("run ended: %v", err)
		case <-time.After(10 * time.Second):
			t.Fatal("not sampled")
		}
	}
	await()
	statuses <- desktop.Status{State: "waiting", Waiting: 1}
	await() // the next sample: the turn with the change is over
	callCtx, callCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer callCancel()
	client := connect(t, context.Background(), address)
	call := client.Object(service, itemPath).CallWithContext(callCtx, "org.freedesktop.DBus.Properties.GetAll", 0, itemInterface)
	if call.Err != nil {
		t.Fatal(call.Err)
	}
	all := call.Body[0].(map[string]dbus.Variant)
	var tip tooltip
	if err := dbus.Store([]any{all["ToolTip"].Value()}, &tip); err != nil {
		t.Fatal(err)
	}
	if all["Status"].Value() != "NeedsAttention" || !strings.Contains(tip.Title, "waiting for you (1)") {
		t.Fatalf("status %v, tooltip %+v", all["Status"], tip)
	}
	var revision uint32
	var layout menuLayout
	if err := client.Object(service, menuPath).CallWithContext(callCtx, menuInterface+".GetLayout", 0, int32(0), int32(-1), []string{}).Store(&revision, &layout); err != nil {
		t.Fatal(err)
	}
	if revision != 2 {
		t.Fatalf("menu revision %d", revision)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("stopped: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not stop")
	}
}

// A bus that goes away ends the tray with an error, unlike a stop.
func TestRunDisconnected(t *testing.T) {
	address := privateBus(t)
	watcher := watch(t, address, 0)
	conn := connect(t, context.Background(), address)
	done := make(chan error, 1)
	go func() {
		done <- run(context.Background(), conn, time.Millisecond, func(context.Context) desktop.Status { return desktop.Status{State: "idle"} }, func() {})
	}()
	registration(t, watcher) // it serves
	conn.Close()
	select {
	case err := <-done:
		if err == nil || err.Error() != "tray: session bus disconnected" {
			t.Fatalf("disconnected: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not notice")
	}
}

// A change the bus is not told of ends the tray with an error: the menu, the
// status and the tooltip, each as property and as signal. The first sample is
// idle; the ones after are the status of the case.
func TestRunPublishFailures(t *testing.T) {
	working := desktop.Status{State: "working", Running: 1}
	waiting := desktop.Status{State: "waiting", Waiting: 1}
	for _, tc := range []struct {
		name, member string
		next         desktop.Status
		want         string
	}{
		{"menu", "LayoutUpdated", working, ""},
		{"status property", "PropertiesChanged", waiting, "tray: update Status: "},
		{"status signal", "NewStatus", waiting, ""},
		{"tooltip property", "PropertiesChanged", working, "tray: update ToolTip: "},
		{"tooltip signal", "NewToolTip", working, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			address := privateBus(t)
			watch(t, address, 0)
			// Invalid flags make the message fail to encode, so Emit fails.
			breaks := func(msg *dbus.Message) {
				if member, _ := msg.Headers[dbus.FieldMember].Value().(string); msg.Type == dbus.TypeSignal && member == tc.member {
					msg.Flags = 0xff
				}
			}
			conn := connect(t, context.Background(), address, dbus.WithOutgoingInterceptor(breaks))
			samples := 0
			sample := func(context.Context) desktop.Status {
				samples++
				if samples == 1 {
					return desktop.Status{State: "idle"}
				}
				return tc.next
			}
			done := make(chan error, 1)
			go func() { done <- run(context.Background(), conn, time.Millisecond, sample, func() {}) }()
			select {
			case err := <-done:
				var invalid dbus.InvalidMessageError
				if !errors.As(err, &invalid) || !strings.HasPrefix(err.Error(), tc.want) {
					t.Fatalf("broken %s: %v", tc.member, err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("the failure went unnoticed")
			}
		})
	}
}

// setProperty turns a failed signal into an error of Run. Any other panic is a
// bug of the tray and stays one.
func TestSetProperty(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	conn, err := dbus.NewConn(client)
	if err != nil {
		t.Fatal(err)
	}
	props, err := prop.Export(conn, itemPath, prop.Map{itemInterface: {
		"Status": {Value: "Active", Emit: prop.EmitTrue},
		"Broken": {Value: "", Emit: prop.EmitType(42)},
	}})
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if err := setProperty(props, "Status", "NeedsAttention"); !errors.Is(err, dbus.ErrClosed) || !strings.HasPrefix(err.Error(), "tray: update Status: ") {
		t.Fatalf("closed connection: %v", err)
	}
	defer func() {
		if failure := recover(); failure != "invalid value for EmitType" {
			t.Fatalf("panic: %v", failure)
		}
	}()
	_ = setProperty(props, "Broken", "x")
	t.Fatal("a bug of the tray was not passed on")
}
