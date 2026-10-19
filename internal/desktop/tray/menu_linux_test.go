//go:build linux

package tray

import (
	"context"
	"encoding/xml"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
	"ouroboros/internal/desktop"
)

// Exercise the exported wire protocol, not just local method calls.
func testMenuProtocol(t *testing.T, client *dbus.Conn, service string, opened <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	object := client.Object(service, menuPath)
	call := func(method string, args ...any) *dbus.Call {
		t.Helper()
		result := object.CallWithContext(ctx, menuInterface+"."+method, 0, args...)
		if result.Err != nil {
			t.Fatalf("%s: %v", method, result.Err)
		}
		return result
	}
	var text string
	if err := object.CallWithContext(ctx, "org.freedesktop.DBus.Introspectable.Introspect", 0).Store(&text); err != nil {
		t.Fatal(err)
	}
	var node introspect.Node
	if err := xml.Unmarshal([]byte(text), &node); err != nil {
		t.Fatal(err)
	}
	expected := map[string]string{
		"GetLayout": "iias>u(ia{sv}av)", "GetGroupProperties": "aias>a(ia{sv})",
		"GetProperty": "is>v", "Event": "isvu>", "EventGroup": "a(isvu)>ai",
		"AboutToShow": "i>b", "AboutToShowGroup": "ai>aiai",
	}
	for _, iface := range node.Interfaces {
		if iface.Name != menuInterface {
			continue
		}
		for _, method := range iface.Methods {
			in, out := "", ""
			for _, a := range method.Args {
				if a.Direction == "out" {
					out += a.Type
				} else {
					in += a.Type
				}
			}
			if want, ok := expected[method.Name]; !ok || in+">"+out != want {
				t.Errorf("%s signature %s>%s (want %s)", method.Name, in, out, want)
			}
			delete(expected, method.Name)
		}
	}
	if len(expected) != 0 {
		t.Fatalf("missing menu methods: %v", expected)
	}
	var version dbus.Variant
	if err := object.CallWithContext(ctx, "org.freedesktop.DBus.Properties.Get", 0, menuInterface, "Version").Store(&version); err != nil || version.Value() != uint32(3) {
		t.Fatalf("version: %v %v", version, err)
	}
	var revision uint32
	var layout menuLayout
	result := call("GetLayout", int32(0), int32(-1), []string{})
	if err := result.Store(&revision, &layout); err != nil {
		t.Fatal(err)
	}
	if revision != 1 || layout.ID != 0 || len(layout.Children) != 4 {
		t.Fatalf("initial layout: %+v rev=%d", layout, revision)
	}
	var label dbus.Variant
	if err := call("GetProperty", openID, "label").Store(&label); err != nil || label.Value() != "Open Ouroboros" {
		t.Fatalf("label: %v %v", label, err)
	}
	var rows []menuProperties
	if err := call("GetGroupProperties", []int32{1, openID, 999}, []string{"label"}).Store(&rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || len(rows[0].Properties) != 1 {
		t.Fatalf("group properties: %+v", rows)
	}
	layout = menuLayout{} // godbus Store merges maps into existing destinations.
	if err := call("GetLayout", int32(0), int32(0), []string{"label"}).Store(&revision, &layout); err != nil {
		t.Fatal(err)
	}
	if len(layout.Children) != 0 || len(layout.Properties) != 0 {
		t.Fatal("depth/property filtering ignored")
	}
	var update bool
	if err := call("AboutToShow", int32(0)).Store(&update); err != nil || update {
		t.Fatalf("AboutToShow: %v %v", update, err)
	}
	var updates, failures []int32
	if err := call("AboutToShowGroup", []int32{0, 999}).Store(&updates, &failures); err != nil || len(updates) != 0 || len(failures) != 1 || failures[0] != 999 {
		t.Fatalf("AboutToShowGroup: %v %v %v", updates, failures, err)
	}
	if err := object.CallWithContext(ctx, menuInterface+".GetProperty", 0, int32(999), "label").Err; err == nil {
		t.Fatal("invalid ID accepted")
	}
	if err := object.CallWithContext(ctx, menuInterface+".GetProperty", 0, openID, "nonexistent").Err; err == nil {
		t.Fatal("invalid property accepted")
	}
	// Read-only rows, opening the menu, and non-click events cannot open the GUI.
	call("Event", int32(1), "clicked", dbus.MakeVariant(int32(0)), uint32(0))
	call("Event", openID, "hovered", dbus.MakeVariant(int32(0)), uint32(0))
	select {
	case <-opened:
		t.Fatal("non-action event opened GUI")
	default:
	}
	call("Event", openID, "clicked", dbus.MakeVariant(int32(0)), uint32(0))
	select {
	case <-opened:
	case <-ctx.Done():
		t.Fatal("explicit action did not open GUI")
	}
	if err := call("EventGroup", []menuEvent{{1, "clicked", dbus.MakeVariant(int32(0)), 0}, {999, "clicked", dbus.MakeVariant(int32(0)), 0}, {openID, "clicked", dbus.MakeVariant(int32(0)), 0}}).Store(&failures); err != nil || len(failures) != 1 || failures[0] != 999 {
		t.Fatalf("EventGroup: %v %v", failures, err)
	}
	select {
	case <-opened:
	case <-ctx.Done():
		t.Fatal("group action did not open GUI")
	}
}

// The menu answers from its rows: an unknown item is an error, no IDs ask for
// all items, a property a row leaves out has the default of DBusMenu, and an
// unchanged status publishes nothing.
func TestMenuMethods(t *testing.T) {
	status := desktop.Status{State: "working", Running: 2, Sessions: []string{"Research", "Review"}}
	m := &menuMethods{revision: 1, rows: menuRows(status)} // without a connection: publishing would panic
	if err := m.update(status); err != nil || m.revision != 1 {
		t.Fatalf("unchanged status: %v, revision %d", err, m.revision)
	}
	if _, _, err := m.GetLayout(999, -1, nil); err == nil || err.Name != menuInterface+".Error.InvalidMenuItem" {
		t.Fatalf("layout of an unknown item: %v", err)
	}
	// Only the root has children.
	if _, layout, err := m.GetLayout(openID, -1, nil); err != nil || layout.ID != openID || len(layout.Children) != 0 || layout.Properties["label"].Value() != "Open Ouroboros" {
		t.Fatalf("layout of an item: %+v %v", layout, err)
	}
	rows, err := m.GetGroupProperties(nil, []string{"label"})
	if err != nil {
		t.Fatal(err)
	}
	var ids []int32
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	if !slices.Equal(ids, []int32{0, 1, 2, 100, 101, 3, openID}) || rows[4].Properties["label"].Value() != "Review" || len(rows[0].Properties) != 0 {
		t.Fatalf("all items: %+v", rows)
	}
	for _, tc := range []struct {
		id   int32
		name string
		want any
	}{
		{3, "type", "separator"}, {3, "label", ""}, {3, "enabled", true}, {3, "visible", true},
		{openID, "type", "standard"}, {openID, "toggle-state", int32(-1)}, {openID, "disposition", "normal"},
		{0, "children-display", "submenu"}, {1, "children-display", ""},
	} {
		got, err := m.GetProperty(tc.id, tc.name)
		if err != nil || !reflect.DeepEqual(got.Value(), tc.want) {
			t.Errorf("%d %s: %v %v, want %v", tc.id, tc.name, got, err, tc.want)
		}
	}
}
