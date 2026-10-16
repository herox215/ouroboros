//go:build linux

package tray

import (
	"bufio"
	"context"
	"encoding/xml"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
	"ouroboros/internal/desktop"
)

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
}

type fakeWatcher struct{ registrations chan string }

func (w *fakeWatcher) RegisterStatusNotifierItem(service string) *dbus.Error {
	w.registrations <- service
	return nil
}

func TestPrivateBusLifecycle(t *testing.T) {
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
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", scanner.Text())
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
	for _, method := range []string{"Activate", "SecondaryActivate", "ContextMenu"} {
		if err := object.CallWithContext(callCtx, itemInterface+"."+method, 0, int32(0), int32(0)).Err; err != nil {
			t.Fatal(err)
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
	if err := Run(context.Background(), nil, func() {}); err == nil {
		t.Fatal("nil sampler accepted")
	}
	if err := Run(context.Background(), func(context.Context) desktop.Status { return desktop.Status{} }, nil); err == nil {
		t.Fatal("nil open accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Run(ctx, func(context.Context) desktop.Status { t.Fatal("sample called"); return desktop.Status{} }, func() {}); err != nil {
		t.Fatal(err)
	}
}
