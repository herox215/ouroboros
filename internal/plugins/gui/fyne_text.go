//go:build cgo

package gui

import (
	"fmt"
	"strings"
	"time"

	"ouroboros/plugin"
)

// The texts of the window, ported from frontend/i18n.js: English is what it
// opens with, German stays beside it so that both wordings live in one place.
// A text that is not in the table is shown as its key, which is what a
// missing translation then looks like.
var fyneTexts = map[string]map[string]string{
	"en": {
		"nav.backToThread":   "To the thread",
		"session.select":     "Select a session",
		"context.eyebrow":    "Context",
		"context.title":      "[Context]",
		"context.stand":      "[State]",
		"context.hint":       "Open a tool result in the transcript to read it here.",
		"context.toolResult": "Tool result · {id}",

		"date.undated":   "No date",
		"date.today":     "Today",
		"date.yesterday": "Yesterday",
		"date.older":     "Older",
		"date.active":    "Active",

		"speaker.you":            "You",
		"speaker.tool":           "Tool",
		"speaker.question":       "Question",
		"speaker.answered":       "Answered",
		"speaker.queue":          "Queue",
		"speaker.status":         "Status",
		"speaker.task":           "↳ Task",
		"common.none":            "None yet",
		"common.tool":            "tool",
		"transcript.expand":      "Show the full text",
		"transcript.expandCount": "Show the full text ({n} characters)",
		"transcript.collapse":    "Collapse the text",
		"transcript.copy":        "Copy",
		"transcript.writing":     "writing …",
		"pulse.thinking":         "thinking",
		"transcript.thought":     "thought",
		"transcript.showThought": "Show what it thought ({n} characters)",
		"transcript.empty":       "Nothing here yet. Say below what is to be done.",
		"tool.loading":           "Loading the tool result …",
		"tool.empty":             "(Empty result)",
		"tool.missing":           "No stored result yet. Open and close it to load it again.",
		"tool.failed":            "The tool result could not be loaded: {error}. Open and close it to retry.",
		"tool.expandHint":        " · show the result",
		"queue.text":             "Message queued",
		"end.stopped":            "Answer stopped",
		"end.failed":             "Answer failed",

		"phase.asked":       "asks you",
		"phase.calling":     "calls",
		"phase.ready":       "ready",
		"phase.thinking":    "thinks",
		"phase.answering":   "writes the answer",
		"phase.done":        "done",
		"phase.failed":      "failed",
		"phase.canceled":    "canceled",
		"phase.empty":       "nothing yet",
		"phase.nextAnswer":  "your answer",
		"phase.nextResult":  "the result of {tool}",
		"phase.nextEnd":     "the end of the answer",
		"phase.nextTool":    "a tool or the answer",
		"phase.nextNothing": "nothing",

		"chart.running": "Running · ",
		"chart.done":    "Done · ",
		"chart.oneTool": " tool: ",
		"chart.tools":   " tools: ",
		"chart.more":    " and {n} more",
		"chart.none":    "No tools yet",

		"artefact.working":  " · working",
		"artefact.question": "question",
		"artefact.last":     "last",
		"artefact.next":     "next",
		"artefact.running":  "running",
		"artefact.noCall":   "no call yet",
		"artefact.oneTool":  "{n} tool · ",
		"artefact.tools":    "{n} tools · ",
		"artefact.oneKind":  " kind",
		"artefact.kinds":    " kinds",
		"artefact.lanes":    " · {n} lanes",

		"status.peer":        "Peer session · read only",
		"status.needsAnswer": "Needs your answer",
		"status.working":     "Working …",
		"status.ready":       "Ready",
		"status.oneActive":   " · {n} active session",
		"status.active":      " · {n} active sessions",

		"question.heading":     "Your decision",
		"question.recommended": " · Recommended",
		"question.sent":        "Answer delivered – the assistant goes on …",
		"question.hint":        "Choose an answer or write your own below.",

		"composer.label":       "Write a message",
		"composer.draftLabel":  "Message / own answer",
		"composer.placeholder": "Write a message",
		"composer.stop":        "Stop",
		"composer.send":        "Send",
		"composer.enterHint":   "Enter sends · Shift+Enter for a new line",
		"composer.sending":     "Sending …",
		"composer.sent":        "Message delivered",
		"composer.sendFailed":  "The message could not be sent: {error}",
		"composer.stopFailed":  "The answer could not be stopped: {error}",

		"sessions.title":           "Sessions",
		"sessions.subtitle":        "Conversations of the node",
		"sessions.loading":         "Loading sessions …",
		"sessions.choose":          "Choose a session.",
		"sessions.none":            "No sessions yet.",
		"sessions.loadFailed":      "The sessions could not be loaded",
		"profiles.loadFailed":      "The profiles could not be loaded: {error}",
		"session.state":            "State: {state} · {owner}",
		"session.own":              "own",
		"session.peer":             "peer",
		"session.loading":          "Loading the transcript …",
		"session.loadFailed":       "The transcript could not be loaded",
		"session.workspace":        "Profile workspace",
		"session.disconnected":     "Connection interrupted",
		"session.disconnectedHint": "The connection to the session was interrupted. Please refresh",
		"session.ended":            "Session connection ended",
		"session.working":          "working",

		"dialog.model.title": "Set up the model",
		"dialog.back":        "Back",
		"dialog.close":       "Close",
		"dialog.cancel":      "Cancel",
		"dialog.save":        "Save",
		"dialog.discard":     "Discard",
		"dialog.loading":     "Loading …",
		"dialog.failed":      "That did not work. Check the settings and try again.",
		"dialog.unsaved":     "The change is not saved yet.",
		"dialog.keepEditing": "Keep editing",
		"model.access":       "Access",
		"model.model":        "Model",
		"model.none":         "No models are available for this access.",
		"model.saving":       "Saving the model …",
		"model.refreshing":   "Updating the list of models …",
		"model.thisProfile":  "this profile",
		"model.default":      "Default model for {name}. It applies from the next message on, in other sessions too.",
		"model.noAccess":     "No access is set up yet. Set it up in the terminal UI first.",

		"settings.title":            "Settings",
		"settings.appearance":       "Appearance",
		"settings.system":           "System",
		"settings.light":            "Light",
		"settings.dark":             "Dark",
		"settings.model":            "Model",
		"settings.gateways":         "Gateways",
		"settings.gatewayFailed":    "The gateway operation failed. Check the settings and try again.",
		"settings.noGateways":       "No gateway plugins available for this profile.",
		"settings.enabled":          "Enabled",
		"settings.loginSave":        "Login / Save",
		"settings.logout":           "Logout",
		"settings.loggedIn":         "Logged in",
		"settings.loggedOut":        "Not logged in",
		"settings.homeserver":       "Homeserver",
		"settings.user":             "User ID",
		"settings.password":         "Password (empty keeps the current login)",
		"settings.allowedUsers":     "Allowed user IDs (comma separated)",
		"settings.allowlistWarning": "Allowlisted users are delegated this profile’s agent tools, INCLUDING permission answers, not just chat. An empty allowlist denies all users.",
	},
	"de": {
		"nav.backToThread":   "Zum Thread",
		"session.select":     "Sitzung auswählen",
		"context.eyebrow":    "Kontext",
		"context.title":      "[Kontext]",
		"context.stand":      "[Stand]",
		"context.hint":       "Öffne ein Werkzeug-Ergebnis im Verlauf, um es hier zu lesen.",
		"context.toolResult": "Werkzeug-Ergebnis · {id}",

		"date.undated":   "Ohne Datum",
		"date.today":     "Heute",
		"date.yesterday": "Gestern",
		"date.older":     "Älter",
		"date.active":    "Aktiv",

		"speaker.you":            "Du",
		"speaker.tool":           "Tool",
		"speaker.question":       "Frage",
		"speaker.answered":       "Beantwortet",
		"speaker.queue":          "Warteschlange",
		"speaker.status":         "Status",
		"speaker.task":           "↳ Aufgabe",
		"common.none":            "Noch keine",
		"common.tool":            "Werkzeug",
		"transcript.expand":      "Vollständigen Text anzeigen",
		"transcript.expandCount": "Vollständigen Text anzeigen ({n} Zeichen)",
		"transcript.collapse":    "Text einklappen",
		"transcript.copy":        "Kopieren",
		"transcript.writing":     "schreibt …",
		"pulse.thinking":         "denkt",
		"transcript.thought":     "dachte nach",
		"transcript.showThought": "Gedanken anzeigen ({n} Zeichen)",
		"transcript.empty":       "Noch nichts hier. Schreib unten, was zu tun ist.",
		"tool.loading":           "Tool-Ergebnis wird geladen …",
		"tool.empty":             "(Leeres Ergebnis)",
		"tool.missing":           "Noch kein gespeichertes Ergebnis. Zum erneuten Laden auf- und zuklappen.",
		"tool.failed":            "Tool-Ergebnis konnte nicht geladen werden: {error}. Zum Wiederholen auf- und zuklappen.",
		"tool.expandHint":        " · Ergebnis aufklappen",
		"queue.text":             "Nachricht eingereiht",
		"end.stopped":            "Antwort gestoppt",
		"end.failed":             "Antwort fehlgeschlagen",

		"phase.asked":       "fragt dich",
		"phase.calling":     "ruft",
		"phase.ready":       "bereit",
		"phase.thinking":    "denkt",
		"phase.answering":   "schreibt die Antwort",
		"phase.done":        "fertig",
		"phase.failed":      "fehlgeschlagen",
		"phase.canceled":    "abgebrochen",
		"phase.empty":       "noch nichts",
		"phase.nextAnswer":  "deine Antwort",
		"phase.nextResult":  "das Ergebnis von {tool}",
		"phase.nextEnd":     "das Ende der Antwort",
		"phase.nextTool":    "ein Werkzeug oder die Antwort",
		"phase.nextNothing": "nichts",

		"chart.running": "Läuft · ",
		"chart.done":    "Fertig · ",
		"chart.oneTool": " Werkzeug: ",
		"chart.tools":   " Werkzeuge: ",
		"chart.more":    " und {n} weitere",
		"chart.none":    "Noch keine Werkzeuge",

		"artefact.working":  " · arbeitet",
		"artefact.question": "frage",
		"artefact.last":     "zuletzt",
		"artefact.next":     "danach",
		"artefact.running":  "läuft",
		"artefact.noCall":   "noch kein Aufruf",
		"artefact.oneTool":  "{n} Werkzeug · ",
		"artefact.tools":    "{n} Werkzeuge · ",
		"artefact.oneKind":  " Art",
		"artefact.kinds":    " Arten",
		"artefact.lanes":    " · {n} Spuren",

		"status.peer":        "Peer-Session · Nur lesen",
		"status.needsAnswer": "Braucht deine Antwort",
		"status.working":     "Arbeitet …",
		"status.ready":       "Bereit",
		"status.oneActive":   " · {n} aktive Session",
		"status.active":      " · {n} aktive Sessions",

		"question.heading":     "Deine Entscheidung",
		"question.recommended": " · Empfohlen",
		"question.sent":        "Antwort übergeben – der Assistent fährt fort …",
		"question.hint":        "Wähle eine Antwort oder antworte unten in eigenen Worten.",

		"composer.label":       "Nachricht verfassen",
		"composer.draftLabel":  "Nachricht / eigene Antwort",
		"composer.placeholder": "Nachricht schreiben",
		"composer.stop":        "Stoppen",
		"composer.send":        "Senden",
		"composer.enterHint":   "Enter sendet · Shift+Enter für eine neue Zeile",
		"composer.sending":     "Wird gesendet …",
		"composer.sent":        "Nachricht übergeben",
		"composer.sendFailed":  "Nachricht konnte nicht gesendet werden: {error}",
		"composer.stopFailed":  "Antwort konnte nicht gestoppt werden: {error}",

		"sessions.title":           "Sessions",
		"sessions.subtitle":        "Unterhaltungen des Nodes",
		"sessions.loading":         "Sessions werden geladen …",
		"sessions.choose":          "Wähle eine Session.",
		"sessions.none":            "Noch keine Sessions.",
		"sessions.loadFailed":      "Sessions konnten nicht geladen werden",
		"profiles.loadFailed":      "Profile konnten nicht geladen werden: {error}",
		"session.state":            "Zustand: {state} · {owner}",
		"session.own":              "eigene",
		"session.peer":             "peer",
		"session.loading":          "Verlauf wird geladen …",
		"session.loadFailed":       "Verlauf konnte nicht geladen werden",
		"session.workspace":        "Profil-Workspace",
		"session.disconnected":     "Verbindung unterbrochen",
		"session.disconnectedHint": "Verbindung zur Session unterbrochen. Bitte aktualisieren",
		"session.ended":            "Session-Verbindung beendet",
		"session.working":          "arbeitet",

		"dialog.model.title": "Modell einstellen",
		"dialog.back":        "Zurück",
		"dialog.close":       "Schließen",
		"dialog.cancel":      "Abbrechen",
		"dialog.save":        "Speichern",
		"dialog.discard":     "Verwerfen",
		"dialog.loading":     "Wird geladen …",
		"dialog.failed":      "Das hat nicht geklappt. Prüfe die Einstellungen und versuche es erneut.",
		"dialog.unsaved":     "Die Änderung ist noch nicht gespeichert.",
		"dialog.keepEditing": "Weiter bearbeiten",
		"model.access":       "Zugang",
		"model.model":        "Modell",
		"model.none":         "Für diesen Zugang sind keine Modelle verfügbar.",
		"model.saving":       "Modell wird gespeichert …",
		"model.refreshing":   "Modellliste wird aktualisiert …",
		"model.thisProfile":  "dieses Profil",
		"model.default":      "Standardmodell für {name}. Gilt ab der nächsten Nachricht, auch in anderen Sitzungen.",
		"model.noAccess":     "Keine Zugänge eingerichtet. Bitte zuerst die Einrichtung in der TUI durchführen.",

		"settings.title":            "Einstellungen",
		"settings.appearance":       "Darstellung",
		"settings.system":           "System",
		"settings.light":            "Hell",
		"settings.dark":             "Dunkel",
		"settings.model":            "Modell",
		"settings.gateways":         "Gateways",
		"settings.gatewayFailed":    "Der Gateway-Vorgang ist fehlgeschlagen. Prüfe die Einstellungen und versuche es erneut.",
		"settings.noGateways":       "Für dieses Profil gibt es keine Gateway-Plugins.",
		"settings.enabled":          "Aktiviert",
		"settings.loginSave":        "Anmelden / Speichern",
		"settings.logout":           "Abmelden",
		"settings.loggedIn":         "Angemeldet",
		"settings.loggedOut":        "Nicht angemeldet",
		"settings.homeserver":       "Homeserver",
		"settings.user":             "Benutzer-ID",
		"settings.password":         "Passwort (leer behält die aktuelle Anmeldung)",
		"settings.allowedUsers":     "Erlaubte Benutzer-IDs (durch Komma getrennt)",
		"settings.allowlistWarning": "Erlaubte Benutzer bekommen die Agent-Werkzeuge dieses Profils, EINSCHLIESSLICH der Antworten auf Berechtigungsfragen, nicht nur den Chat. Eine leere Liste verweigert allen Benutzern den Zugang.",
	},
}

// fyneLanguage is the language the window shows, English unless picked.
var fyneLanguage = "en"

// tr is the text of key in the language shown, with {name} filled in from
// vars.
func tr(key string, vars map[string]any) string {
	text, ok := fyneTexts[fyneLanguage][key]
	if !ok {
		text = fyneTexts["en"][key]
	}
	if text == "" {
		text = key
	}
	for name, value := range vars {
		text = strings.ReplaceAll(text, "{"+name+"}", fmt.Sprint(value))
	}
	return text
}

// datePattern is the medium date with the short time of the locale, as the
// window writes a date.
func datePattern() string {
	if fyneLanguage == "de" {
		return "02.01.2006, 15:04"
	}
	return "02 Jan 2006, 15:04"
}

// clockPattern is the time of a message today: the clock without the date.
func clockPattern() string { return "15:04" }

// dateGroup is the group of the sidebar a session with the time value belongs
// to.
func dateGroup(value, now time.Time) string {
	if value.IsZero() {
		return tr("date.undated", nil)
	}
	year, month, day := now.Date()
	today := time.Date(year, month, day, 0, 0, 0, 0, now.Location())
	yesterday := today.AddDate(0, 0, -1)
	switch {
	case !value.Before(today):
		return tr("date.today", nil)
	case !value.Before(yesterday):
		return tr("date.yesterday", nil)
	}
	return tr("date.older", nil)
}

// clock is the time above a message, as the terminal UI writes it: the clock
// today, the date with it before.
func clock(value, now time.Time) string {
	if value.IsZero() {
		return ""
	}
	if value.In(now.Location()).Format("2006-01-02") == now.Format("2006-01-02") {
		return value.In(now.Location()).Format(clockPattern())
	}
	return value.In(now.Location()).Format(datePattern())
}

// formatDate writes a date as the window writes it, "" without one.
func formatDate(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.Local().Format(datePattern())
}

// sessionDirectory is the kind that tells the workspace of a session, as the
// window shows it under its title.
const sessionDirectory = plugin.SessionKind("directory")
