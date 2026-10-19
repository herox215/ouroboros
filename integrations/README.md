# Desktop integration

The core is independent of Omarchy, Waybar and native tray libraries. It provides:

- `ouro desktop status`: a versioned, single-line JSON snapshot:
  `{"version":1,"state":"working","running":2,"waiting":0}`.
- `ouro desktop waybar`: the Waybar custom-module JSON protocol.
- `ouro desktop open`: open the existing graphical UI, without a terminal.

States are `idle`, `working`, `waiting`, `offline` and `error`. Waiting for an
owner reply takes priority over working. Running counts owner sessions with an
answer or delegated tasks in flight; waiting counts those needing an owner reply.
Persistent services and peer requests are not counted. No chat content, titles,
paths, profile names or raw errors are included. A snapshot does not start or
retain the node, load archives, or change conversation state. A failed query is
not reported as idle. Polls have a two-second deadline.

## Linux tray (including Omarchy / Quickshell)

While the node runs, the desktop plugin automatically registers a real
StatusNotifierItem on the session D-Bus. Quickshell, Waybar with a tray module,
and other compatible panels can display it without a custom module. The icon
is a generated ring, with an attention state and a content-free status tooltip. Clicking
opens a small standard tray menu, anchored by the panel. It lists running owner
sessions by title (or the first-message title fallback), including delegated work
and sessions waiting for a reply. These names are exposed only on the local session
bus menu, not in CLI JSON or tooltips. “Open Ouroboros” explicitly opens the
existing GUI (one launch at a time). The tray disappears when the node
stops and reconnects when the tray host restarts. On a headless system a missing
session bus is logged but does not prevent the node from running.

There is one tray for the node. It aggregates owner-session activity across
profiles whose desktop plugin is enabled; the CLI JSON commands below remain
per-profile. A status-query failure is shown as unavailable, not ready.
No desktop configuration, automatic startup, or close-to-tray behavior is added.
The panel must support DBusMenu to show the popup. The GUI action additionally
requires the GUI plugin and native build dependencies.

## Optional Omarchy / Waybar custom module

1. Build and install a native GUI-capable Ouroboros (see docs/manual.md).
   Ensure `ouro` is on the PATH inherited by Waybar; otherwise replace it below
   and in the module with its absolute installed path.
2. Copy `waybar/ouroboros.jsonc` and `waybar/ouroboros.css` to
   `~/.config/waybar/`. Do not replace your existing configuration.
3. Add `"~/.config/waybar/ouroboros.jsonc"` to your configuration's
   `include` list and `"custom/ouroboros"` to the desired module list,
   for example `modules-right`. Preserve existing entries.
4. Add `@import "ouroboros.css";` to your Waybar `style.css` and reload
   Waybar using your desktop's usual mechanism. If your theme lacks
   `@warning_color` or `@error_color`, substitute its equivalent colors.

The symbol uses plain Unicode, without a Nerd Font dependency. Hover shows
status; left-click opens the GUI. Polling does not start Ouroboros when offline.
Opening the GUI retains the existing GUI's node-lifecycle behavior: there is no
new daemon, auto-start, or close-to-tray behavior.

### Profiles

Snapshots describe **one profile**, not the whole node. With several profiles,
choose explicitly in both module commands:

```json
"exec": "ouro --profile PROFILE desktop waybar",
"on-click": "ouro --profile PROFILE desktop open"
```

Use an ID from `ouro profile` (or correctly shell-quote a name). The GUI still
has its existing profile picker. Without an explicit profile a multi-profile
installation rejects the command rather than silently displaying the wrong one.
If the desktop plugin was disabled, enable it using Ouroboros's plugin controls.

## macOS menu bar

On macOS, builds with cgo automatically show a native menu-bar icon while the
node runs and the desktop plugin is enabled. Its menu shows the aggregate
status, running session titles and “Open Ouroboros”. Status is refreshed every
two seconds; waiting or unavailable status adds an attention mark. The GUI
action uses the existing GUI and its profile picker.

A private child process owns the AppKit main thread; it exits when the node
stops or its input pipe closes. No Dock icon, notifications, automatic startup,
approval buttons or desktop configuration changes are introduced. A graphical
macOS login session is required.

## Other desktops

Windows has no native tray adapter yet; external adapters can poll
`desktop status` and invoke `desktop open`.

Linux/Omarchy configuration is opt-in: no user desktop files are modified by a
build or by querying status. Remove the custom module, include and CSS import to
undo the integration.
