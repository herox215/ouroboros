# Native Omarchy panel

This integration uses Omarchy's own Panel, BarIconButton and KeyboardPanel
components, including the popup border, palette, font, focus and positioning.
It requires the plugin-capable Omarchy Quickshell shell (not the old Waybar bar).

Copy Panel.qml and manifest.json from this directory into
~/.config/omarchy/plugins/local.ouroboros/. Run:

    omarchy-shell shell rescanPlugins
    # Once local.ouroboros appears in listPlugins:
    omarchy-shell shell enablePlugin local.ouroboros '{"section":"right","before":"omarchy.tray"}'

Preserve your existing ~/.config/omarchy/shell.json. The bar.layout.right entry
can specify settings inline:

    {"id":"local.ouroboros", "executable":"/absolute/path/to/ouro", "profile":"PROFILE"}

Specify a profile if the node has more than one. This panel samples that profile,
not an aggregate of all profiles. It polls every three seconds, never starts the
node, and offers an explicit GUI-open button. Session titles wrap instead of
being truncated. Enter opens the GUI; Escape dismisses the panel.

The explicit command ouro [--profile PROFILE] desktop panel returns local JSON
with live session titles. Do not publish or log this output. desktop status and
desktop waybar remain title-free. Offline/error snapshots clear displayed titles.

To avoid a duplicate icon, add ouroboros to the existing omarchy.tray entry's
hidden array and remove it from pinned. Keep all unrelated entries intact.
No files in /usr/share/omarchy need modification.

To uninstall, remove local.ouroboros from bar.layout and delete its user plugin
directory. Remove ouroboros from the tray hidden array to restore the standard
tray icon. The standard cross-desktop tray implementation remains available.
