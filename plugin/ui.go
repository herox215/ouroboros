package plugin

import (
	"context"
	"encoding/json"

	"ouroboros/internal/assistant"
)

// What a plugin contributes to the terminal UI with Add. None of it knows
// how the UI is drawn: a plugin hands over text and functions, and the UI
// decides where they go.
type (
	// Screen is a view of its own in the terminal UI, next to the chat. Key
	// opens it where the commands are, and Key, esc or q lead back.
	Screen struct {
		Key   string // the key that opens it, see Key
		Title string // its heading, and its name next to the key in the footer
		// Body returns the text the screen shows; what does not fit the
		// window is cut off. It runs when the screen is opened, after one of
		// its Keys ran, and every few seconds while the screen is open. It
		// does not hold up the UI, so it may ask the network. ctx ends when
		// the user quits. An error is shown in place of the text.
		Body func(ctx context.Context) (string, error)
		// Keys work while the screen is open.
		Keys []Key
	}

	// Key is a key of the terminal UI that runs something. Contributed as it
	// is, it works where the commands are (esc leaves the message input);
	// listed in Screen.Keys, it works on that screen.
	//
	// A key belongs to one contribution. The UI keeps the keys it uses
	// itself, and of two contributions that claim the same key the one that
	// was contributed first gets it, a Screen before a Key. The other is
	// left out, and the UI says so when it opens: ouro still starts, and
	// whatever else its plugin contributed still works.
	Key struct {
		// Key is the key as the terminal reports it: a character like "g",
		// where "G" is a key of its own, or a combination like "ctrl+g".
		Key  string
		Help string // what it does, in a word or two, for the footer
		// Run is what the key does. It does not hold up the UI. The text it
		// returns is shown in the footer until the next key, an error in red.
		Run func(ctx context.Context) (string, error)
	}

	// ToolView says what the owner is shown of a call of a tool while the
	// agent waits for its result. Without one, a call shows as the agent
	// thinking.
	ToolView struct {
		Tool string // the name of the Tool
		// Show returns the line for a call with the arguments args, as JSON:
		// "asking beta" for the tool that asks a peer. Nothing is shown of a
		// call it returns "" for.
		Show func(args json.RawMessage) string
	}
)

type (
	// Assistant is the agent of the owner of the node, a service for front
	// ends: Ready reports whether it can answer, Ask lets it.
	Assistant = assistant.Assistant
	// Step is a tool call the Assistant reports on the way to its answer
	// and, with Done set, that its result is in. ToolLine turns it into what
	// the owner is shown.
	Step = assistant.Step
)

// ToolLine returns what the owner is shown of the tool call s: the line of
// the first ToolView contributed for the tool that has one for these
// arguments, or "" if nothing is to be shown.
func ToolLine(ctx *Context, s Step) string {
	for _, v := range All[ToolView](ctx) {
		if v.Tool != s.Tool || v.Show == nil {
			continue
		}
		if line := v.Show(s.Args); line != "" {
			return line
		}
	}
	return ""
}
