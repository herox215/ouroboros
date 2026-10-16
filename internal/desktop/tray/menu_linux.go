//go:build linux

package tray

import (
	"fmt"
	"reflect"
	"sync"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
	"github.com/godbus/dbus/v5/prop"
	"ouroboros/internal/desktop"
)

const (
	menuInterface                 = "com.canonical.dbusmenu"
	menuPath      dbus.ObjectPath = "/Menu"
	openID        int32           = 4
)

// Field order is the DBusMenu wire format (ia{sv}av).
type menuLayout struct {
	ID         int32
	Properties map[string]dbus.Variant
	Children   []dbus.Variant
}
type menuProperties struct {
	ID         int32
	Properties map[string]dbus.Variant
}
type menuEvent struct {
	ID        int32
	Event     string
	Data      dbus.Variant
	Timestamp uint32
}
type menuMethods struct {
	mu       sync.RWMutex
	revision uint32
	rows     []menuProperties
	open     func()
	conn     *dbus.Conn
}

func menuRows(s desktop.Status) []menuProperties {
	row := func(id int32, label string, enabled bool) menuProperties {
		return menuProperties{id, map[string]dbus.Variant{"label": dbus.MakeVariant(label), "enabled": dbus.MakeVariant(enabled), "visible": dbus.MakeVariant(true)}}
	}
	rows := []menuProperties{row(1, s.Label(), false), row(2, fmt.Sprintf("Running sessions (%d)", s.Running), false)}
	for i, name := range s.Sessions {
		rows = append(rows, row(int32(100+i), name, false))
	}
	rows = append(rows, menuProperties{3, map[string]dbus.Variant{"type": dbus.MakeVariant("separator")}}, row(openID, "Open Ouroboros", true))
	return rows
}

func exportMenu(conn *dbus.Conn, s desktop.Status, open func()) (*menuMethods, error) {
	m := &menuMethods{revision: 1, rows: menuRows(s), open: open, conn: conn}
	if err := conn.Export(m, menuPath, menuInterface); err != nil {
		return nil, err
	}
	props, err := prop.Export(conn, menuPath, prop.Map{menuInterface: {
		"Version":       {Value: uint32(3), Emit: prop.EmitConst},
		"TextDirection": {Value: "ltr", Emit: prop.EmitConst},
		"Status":        {Value: "normal", Emit: prop.EmitConst},
		"IconThemePath": {Value: []string{}, Emit: prop.EmitConst},
	}})
	if err != nil {
		return nil, err
	}
	iface := introspect.Interface{Name: menuInterface, Methods: introspect.Methods(m), Properties: props.Introspection(menuInterface), Signals: []introspect.Signal{
		{Name: "LayoutUpdated", Args: []introspect.Arg{{Name: "revision", Type: "u"}, {Name: "parent", Type: "i"}}},
		{Name: "ItemsPropertiesUpdated", Args: []introspect.Arg{{Name: "updatedProps", Type: "a(ia{sv})"}, {Name: "removedProps", Type: "a(ias)"}}},
	}}
	err = conn.Export(introspect.NewIntrospectable(&introspect.Node{Name: string(menuPath), Interfaces: []introspect.Interface{iface, prop.IntrospectData}}), menuPath, "org.freedesktop.DBus.Introspectable")
	return m, err
}

func (m *menuMethods) update(s desktop.Status) error {
	rows := menuRows(s)
	m.mu.Lock()
	defer m.mu.Unlock()
	if reflect.DeepEqual(rows, m.rows) {
		return nil
	}
	m.rows = rows
	m.revision++
	return m.conn.Emit(menuPath, menuInterface+".LayoutUpdated", m.revision, int32(0))
}

func menuError() *dbus.Error {
	return dbus.NewError(menuInterface+".Error.InvalidMenuItem", []any{"Unknown menu item"})
}

// Callers hold mu; returned maps are immutable snapshots.
func (m *menuMethods) find(id int32) (map[string]dbus.Variant, bool) {
	if id == 0 {
		return map[string]dbus.Variant{"children-display": dbus.MakeVariant("submenu")}, true
	}
	for _, r := range m.rows {
		if r.ID == id {
			return r.Properties, true
		}
	}
	return nil, false
}
func filterProperties(p map[string]dbus.Variant, names []string) map[string]dbus.Variant {
	result := make(map[string]dbus.Variant)
	for k, v := range p {
		if len(names) == 0 {
			result[k] = v
			continue
		}
		for _, name := range names {
			if name == k {
				result[k] = v
				break
			}
		}
	}
	return result
}
func (m *menuMethods) GetLayout(parent int32, depth int32, names []string) (uint32, menuLayout, *dbus.Error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	p, ok := m.find(parent)
	if !ok {
		return 0, menuLayout{}, menuError()
	}
	layout := menuLayout{parent, filterProperties(p, names), []dbus.Variant{}}
	if parent == 0 && depth != 0 {
		for _, row := range m.rows {
			layout.Children = append(layout.Children, dbus.MakeVariant(menuLayout{row.ID, filterProperties(row.Properties, names), []dbus.Variant{}}))
		}
	}
	return m.revision, layout, nil
}
func (m *menuMethods) GetGroupProperties(ids []int32, names []string) ([]menuProperties, *dbus.Error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := []menuProperties{}
	if len(ids) == 0 {
		ids = []int32{0}
		for _, row := range m.rows {
			ids = append(ids, row.ID)
		}
	}
	for _, id := range ids {
		if p, ok := m.find(id); ok {
			result = append(result, menuProperties{id, filterProperties(p, names)})
		}
	}
	return result, nil
}
func (m *menuMethods) GetProperty(id int32, name string) (dbus.Variant, *dbus.Error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	p, ok := m.find(id)
	if !ok {
		return dbus.Variant{}, menuError()
	}
	if v, ok := p[name]; ok {
		return v, nil
	}
	// Standard DBusMenu defaults for properties omitted from the layout.
	defaults := map[string]any{"type": "standard", "label": "", "enabled": true, "visible": true, "icon-name": "", "icon-data": []byte{}, "shortcut": [][]string{}, "toggle-type": "", "toggle-state": int32(-1), "children-display": "", "disposition": "normal"}
	if v, ok := defaults[name]; ok {
		return dbus.MakeVariant(v), nil
	}
	return dbus.Variant{}, dbus.NewError(menuInterface+".Error.InvalidProperty", []any{"Unknown menu property"})
}
func (m *menuMethods) Event(id int32, event string, data dbus.Variant, timestamp uint32) *dbus.Error {
	m.mu.RLock()
	_, ok := m.find(id)
	m.mu.RUnlock()
	if !ok {
		return menuError()
	}
	if id == openID && event == "clicked" {
		m.open()
	}
	return nil
}
func (m *menuMethods) EventGroup(events []menuEvent) ([]int32, *dbus.Error) {
	failures := []int32{}
	for _, e := range events {
		if m.Event(e.ID, e.Event, e.Data, e.Timestamp) != nil {
			failures = append(failures, e.ID)
		}
	}
	return failures, nil
}
func (m *menuMethods) AboutToShow(id int32) (bool, *dbus.Error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if _, ok := m.find(id); !ok {
		return false, menuError()
	}
	return false, nil
}
func (m *menuMethods) AboutToShowGroup(ids []int32) ([]int32, []int32, *dbus.Error) {
	failures := []int32{}
	for _, id := range ids {
		if _, err := m.AboutToShow(id); err != nil {
			failures = append(failures, id)
		}
	}
	return []int32{}, failures, nil
}
