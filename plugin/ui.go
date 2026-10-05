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
	// Screen is a view of its own in the terminal UI, next to the chat. The
	// owner opens it with its Command and leaves it with esc.
	Screen struct {
		Command string // what opens it: "greetings" for /greetings, see Slash
		Title   string // its heading
		// Body returns the text the screen shows; what does not fit the
		// window is cut off. It runs when the screen is opened, after one of
		// its Actions ran, and every few seconds while the screen is open. It
		// does not hold up the UI, so it may ask the network. ctx ends when
		// the user quits. An error is shown in place of the text.
		Body func(ctx context.Context) (string, error)
		// Actions are offered below the text: the arrow keys select one,
		// enter runs it.
		Actions []Action
	}

	// Action is something a Screen offers to run.
	Action struct {
		Name string // what it does, in a word or two
		// Run is what the action does. It does not hold up the UI. The text
		// it returns is shown in the footer until the next key, an error in
		// red.
		Run func(ctx context.Context) (string, error)
	}

	// Slash is a command of the terminal UI: the owner types it into the
	// message input with a slash in front, /Name, and it runs instead of
	// going to the agent. Only the owner runs it: the agents never see it.
	//
	// A name belongs to one contribution. The UI keeps the names it uses
	// itself, and of two contributions that claim the same name the one
	// that was contributed first gets it, a Screen before a Slash. The other
	// is left out, and the UI says so when it opens: ouro still starts, and
	// whatever else its plugin contributed still works.
	Slash struct {
		Name string // one word, without the slash: "greet" for /greet
		Help string // what it does, in a few words, for /help
		// Run is what the command does. It does not hold up the UI. The text
		// it returns is shown in the footer until the next key, an error in
		// red.
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
		// Detail returns what the owner is shown of a call once it ended,
		// after the line of Show: how long it took, the end of an error. It
		// is only asked for a call whose line Show gives. May be nil, which
		// leaves the line as it is.
		Detail func(s Step) string
	}
)

type (
	// Assistant is the agent of the owner of the node, a service for front
	// ends: Ready reports whether it can answer, Ask lets it.
	Assistant = assistant.Assistant
	// Step is a tool call the Assistant reports on the way to its answer
	// and, with Done set, that its result is in. ToolLine turns it into what
	// the owner is shown, ToolDetail adds what became of the call.
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

// ToolDetail returns what the owner is shown of the tool call s once it
// ended: line, as ToolLine gives it, with what the first ToolView of its tool
// has to add. Without a Detail for that tool, line comes back as it is.
func ToolDetail(ctx *Context, s Step, line string) string {
	for _, v := range All[ToolView](ctx) {
		if v.Tool != s.Tool || v.Detail == nil {
			continue
		}
		if more := v.Detail(s); more != "" {
			return line + " " + more
		}
		return line
	}
	return line
}
