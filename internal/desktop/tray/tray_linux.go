//go:build linux

// Package tray exposes Ouroboros as a StatusNotifierItem on the session bus.
package tray

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/introspect"
	"github.com/godbus/dbus/v5/prop"
	"ouroboros/internal/desktop"
)

const (
	itemInterface                 = "org.kde.StatusNotifierItem"
	itemPath      dbus.ObjectPath = "/StatusNotifierItem"
	busName                       = "org.ouroboros.StatusNotifierItem"
	watcherName                   = "org.kde.StatusNotifierWatcher"
	watcherPath   dbus.ObjectPath = "/StatusNotifierWatcher"
)

// Run owns a private session-bus connection until ctx is canceled (returning
// nil), or the connection fails. A fixed bus name permits one tray per session
// bus, including across processes/profiles. A second Run returns an error.
// sample is called initially and every two seconds and must honor ctx. Sessions
// supplies the running names shown in the read-only menu. open is called only
// by the explicit Open Ouroboros menu action, from D-Bus handler goroutines,
// and should dispatch to the UI thread if needed. Nil callbacks are rejected.
// No node is started by this adapter.
// A missing watcher is harmless: registration is retried when one appears.
func Run(ctx context.Context, sample func(context.Context) desktop.Status, open func()) error {
	if sample == nil || open == nil {
		return errors.New("tray: sample and open callbacks are required")
	}
	if ctx.Err() != nil {
		return nil
	}
	conn, err := dbus.ConnectSessionBus(dbus.WithContext(ctx))
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("tray: connect session bus: %w", err)
	}
	defer conn.Close()
	return run(ctx, conn, sample, open)
}

func run(ctx context.Context, conn *dbus.Conn, sample func(context.Context) desktop.Status, open func()) error {
	reply, err := conn.RequestName(busName, dbus.NameFlagDoNotQueue)
	if err != nil {
		return fmt.Errorf("tray: acquire name: %w", err)
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		return errors.New("tray: another Ouroboros tray already owns the session bus name")
	}
	item := &itemMethods{}
	if err := conn.Export(item, itemPath, itemInterface); err != nil {
		return fmt.Errorf("tray: export item: %w", err)
	}
	initial := sample(ctx)
	menu, err := exportMenu(conn, initial, open)
	if err != nil {
		return fmt.Errorf("tray: export menu: %w", err)
	}
	previous := model(initial)
	props, err := prop.Export(conn, itemPath, properties(previous))
	if err != nil {
		return fmt.Errorf("tray: export properties: %w", err)
	}
	iface := introspect.Interface{
		Name: itemInterface, Methods: introspect.Methods(item), Properties: props.Introspection(itemInterface),
		Signals: []introspect.Signal{
			{Name: "NewTitle"}, {Name: "NewIcon"}, {Name: "NewAttentionIcon"}, {Name: "NewOverlayIcon"}, {Name: "NewToolTip"},
			{Name: "NewStatus", Args: []introspect.Arg{{Name: "status", Type: "s"}}},
		},
	}
	if err := conn.Export(introspect.NewIntrospectable(&introspect.Node{
		Name: string(itemPath), Interfaces: []introspect.Interface{iface, prop.IntrospectData},
	}), itemPath, "org.freedesktop.DBus.Introspectable"); err != nil {
		return err
	}
	signals := make(chan *dbus.Signal, 16)
	conn.Signal(signals)
	defer conn.RemoveSignal(signals)
	if err := conn.AddMatchSignalContext(ctx, dbus.WithMatchSender("org.freedesktop.DBus"),
		dbus.WithMatchObjectPath("/org/freedesktop/DBus"), dbus.WithMatchInterface("org.freedesktop.DBus"),
		dbus.WithMatchMember("NameOwnerChanged"), dbus.WithMatchArg(0, watcherName)); err != nil {
		return err
	}
	registered := register(ctx, conn)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-conn.Context().Done():
			if ctx.Err() != nil {
				return nil
			}
			return errors.New("tray: session bus disconnected")
		case sig, ok := <-signals:
			if !ok {
				if ctx.Err() != nil {
					return nil
				}
				return errors.New("tray: session bus signal stream closed")
			}
			if sig != nil && sig.Name == "org.freedesktop.DBus.NameOwnerChanged" && len(sig.Body) == 3 && sig.Body[0] == watcherName {
				registered = false
				if owner, ok := sig.Body[2].(string); ok && owner != "" {
					registered = register(ctx, conn)
				}
			}
		case <-ticker.C:
			status := sample(ctx)
			if err := menu.update(status); err != nil {
				return err
			}
			next := model(status)
			if next.status != previous.status {
				if err := setProperty(props, "Status", next.status); err != nil {
					return err
				}
				if err := conn.Emit(itemPath, itemInterface+".NewStatus", next.status); err != nil {
					return err
				}
			}
			if next.tooltip != previous.tooltip {
				if err := setProperty(props, "ToolTip", tooltip{Pixmap: []pixmap{}, Title: next.tooltip, Description: next.tooltip}); err != nil {
					return err
				}
				if err := conn.Emit(itemPath, itemInterface+".NewToolTip"); err != nil {
					return err
				}
			}
			previous = next
			if !registered {
				registered = register(ctx, conn)
			}
		}
	}
}

// godbus's only local property setter panics even on signal I/O errors.
// Turn those errors into Run errors (a bus disappearing is not a programmer bug).
func setProperty(props *prop.Properties, name string, value any) (err error) {
	defer func() {
		if failure := recover(); failure != nil {
			if cause, ok := failure.(error); ok {
				err = fmt.Errorf("tray: update %s: %w", name, cause)
			} else {
				panic(failure)
			}
		}
	}()
	props.SetMust(itemInterface, name, value)
	return nil
}

func register(ctx context.Context, conn *dbus.Conn) bool {
	callCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	// A unique service name uses the standard /StatusNotifierItem path.
	return conn.Object(watcherName, watcherPath).CallWithContext(callCtx,
		watcherName+".RegisterStatusNotifierItem", 0, conn.Names()[0]).Err == nil
}

// Menu-capable hosts display /Menu at the icon. Activation never opens the GUI.
type itemMethods struct{}

func (i *itemMethods) Activate(x, y int32) *dbus.Error          { return nil }
func (i *itemMethods) SecondaryActivate(x, y int32) *dbus.Error { return nil }
func (i *itemMethods) ContextMenu(x, y int32) *dbus.Error       { return nil }

// Scroll is deliberately a no-op; this item has no scrollable controls.
func (i *itemMethods) Scroll(delta int32, orientation string) *dbus.Error { return nil }
