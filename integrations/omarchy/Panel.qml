import QtQuick
import QtQuick.Controls as Controls
import Quickshell
import Quickshell.Io
import qs.Commons
import qs.Ui

// Native Omarchy panel; no custom palette or border overrides.
Panel {
  id: root
  moduleName: "local.ouroboros"
  ipcTarget: "local.ouroboros"
  implicitWidth: button.implicitWidth
  implicitHeight: button.implicitHeight
  property var snapshot: ({ state: "offline", running: 0, waiting: 0, sessions: [] })
  readonly property color foreground: bar ? bar.foreground : Color.foreground
  readonly property string fontFamily: bar ? bar.fontFamily : Style.font.family
  readonly property string executable: String(setting("executable", "ouro"))
  readonly property string profile: String(setting("profile", ""))
  readonly property string statusText: {
    switch (snapshot.state) {
      case "working": return "Working · " + snapshot.running + (snapshot.running === 1 ? " session" : " sessions");
      case "waiting": return "Waiting for you · " + snapshot.waiting;
      case "idle": return "Ready";
      case "offline": return "Not running";
      default: return "Unavailable";
    }
  }
  function command(action) {
    return profile ? [executable, "--profile", profile, "desktop", action]
                   : [executable, "desktop", action];
  }
  function refresh() { if (!sample.running) sample.running = true; }
  function openGui() { if (!launcher.running) launcher.running = true; root.close(); }
  onOpenedChanged: if (opened) refresh()
  Component.onCompleted: refresh()

  Timer { interval: 3000; running: true; repeat: true; onTriggered: root.refresh() }
  Timer {
    interval: 5000; running: sample.running
    onTriggered: { sample.signal(15); root.snapshot = {state: "error", sessions: []}; }
  }
  Process {
    id: sample
    command: root.command("panel")
    stdout: StdioCollector {
      onStreamFinished: {
        try {
          var value = JSON.parse(text);
          if (value.version !== 1 || !Array.isArray(value.sessions)) throw new Error("Invalid snapshot");
          root.snapshot = value;
        } catch (e) { root.snapshot = {state: "error", sessions: []}; }
      }
    }
    onExited: function(code, status) {
      if (code !== 0 || status !== 0) root.snapshot = {state: "error", sessions: []};
    }
  }
  Process { id: launcher; command: root.command("open") }

  BarIconButton {
    id: button
    anchors.fill: parent
    bar: root.bar
    text: root.snapshot.state === "working" ? "◉" : root.snapshot.state === "waiting" ? "◉" : "◎"
    onPressed: function(buttonCode) { if (buttonCode === Qt.LeftButton) root.toggle(); }
  }
  KeyboardPanel {
    id: panel
    anchorItem: button
    owner: root
    bar: root.bar
    open: root.opened
    focusTarget: keys
    contentWidth: panel.fittedContentWidth(Style.space(380))
    contentHeight: panel.fittedContentHeight(column.implicitHeight, Style.space(500))
    PanelKeyCatcher {
      id: keys
      anchors.fill: parent
      onCloseRequested: root.close()
      onTabRequested: function(direction) { root.switchPanel(direction); }
      onActivateRequested: root.openGui()
      Flickable {
        anchors.fill: parent
        contentWidth: width
        contentHeight: column.implicitHeight
        clip: true
        boundsBehavior: Flickable.StopAtBounds
        Controls.ScrollBar.vertical: Controls.ScrollBar { policy: Controls.ScrollBar.AsNeeded }
        Column {
          id: column
          width: parent.width
          spacing: Style.space(12)
          Text {
            width: parent.width
            text: "Ouroboros"
            color: root.foreground
            font.family: root.fontFamily
            font.pixelSize: Style.font.body
            font.bold: true
          }
          Text {
            width: parent.width
            text: root.statusText
            color: root.foreground
            font.family: root.fontFamily
            font.pixelSize: Style.font.bodySmall
            wrapMode: Text.Wrap
          }
          Repeater {
            model: root.snapshot.sessions || []
            Text {
              required property string modelData
              width: column.width
              text: modelData
              textFormat: Text.PlainText
              wrapMode: Text.Wrap
              color: root.foreground
              font.family: root.fontFamily
              font.pixelSize: Style.font.bodySmall
            }
          }
          Controls.Button {
            width: parent.width
            text: "Open GUI"
            onClicked: root.openGui()
            contentItem: Text {
              text: "Open GUI"
              color: root.foreground
              font.family: root.fontFamily
              font.pixelSize: Style.font.bodySmall
              horizontalAlignment: Text.AlignHCenter
            }
            background: Rectangle {
              color: parent.hovered ? Style.hoverFillFor(root.foreground, root.foreground) : "transparent"
              radius: Style.cornerRadius
              implicitHeight: Style.space(34)
            }
          }
        }
      }
    }
  }
}
