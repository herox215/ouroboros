package plugin

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"ouroboros/internal/assistant"
	"ouroboros/internal/pasteboard"
	"ouroboros/internal/session"
)

// FrontEnd is what opens when ouro is started in a terminal without a
// command, and what "ouro chat" opens without a message: the terminal UI of
// the standard ouro. A plugin contributes it with Add, next to the Command
// that opens it by name.
//
// ouro opens a front end only if exactly one was contributed. With none it
// prints the usage, and "ouro chat" talks line by line. With more than one it
// does not pick: it fails and names them, see FrontEndFor.
type FrontEnd struct {
	Name string // what it is called: the name of the command that opens it
	Help string // one line for the usage
	// Run shows the front end until the user leaves it.
	Run func(ctx context.Context) error
}

var (
	// ErrNoFrontEnd means there is no front end to open: no plugin
	// contributed one, or the user does not sit at a terminal.
	ErrNoFrontEnd = errors.New("no front end")
	// ErrFrontEnds means more than one front end was contributed.
	ErrFrontEnds = errors.New("more than one front end")
)

// FrontEndFor returns the front end to open for the user at term. It fails
// with ErrNoFrontEnd if there is none to open, and with ErrFrontEnds, naming
// them, if the plugins contributed more than one: which of them the owner
// wants is not for ouro to guess.
func FrontEndFor(ctx *Context, term *Console) (FrontEnd, error) {
	all := All[FrontEnd](ctx)
	if !term.Interactive || len(all) == 0 {
		return FrontEnd{}, ErrNoFrontEnd
	}
	if len(all) > 1 {
		names := make([]string, len(all))
		for i, f := range all {
			names[i] = "ouro " + f.Name
		}
		return FrontEnd{}, fmt.Errorf("%w: open one by its command (%s), or take all but one out of the list of plugins",
			ErrFrontEnds, strings.Join(names, ", "))
	}
	return all[0], nil
}

// WithRestart returns ctx in which a tool can ask for ouro to start anew:
// restart is told the binary to start, see Restart. The node hands such a ctx
// to the answers of the sessions of the owner.
func WithRestart(ctx context.Context, restart func(exe string)) context.Context {
	return session.WithRestart(ctx, restart)
}

// Restart asks the node that holds the conversation of ctx to start exe in
// its place once the answer is given, and to go on with the conversation
// there: the node hands over its sessions, and a front end that shows one
// starts anew as well. It reports false if nobody does that, like for a peer.
func Restart(ctx context.Context, exe string) bool { return session.AskRestart(ctx, exe) }

// Restarting is the error a command ends with whose process is to be
// started anew, like a front end or a node that is replaced by a new build:
// the process runs Exe in its place, with its arguments and Env as its
// environment, see cmd/ouro.
type Restarting struct {
	Exe string
	Env []string
}

func (r *Restarting) Error() string { return "ouro is to be started anew from " + r.Exe }

// Sessions are the conversations of the owner with its agent, a service for
// front ends. They belong to the node: it runs their answers and keeps what
// happens in them, and a front end shows that as it is told. An answer goes
// on when the front end is closed. Await asks and waits for one answer.
type Sessions = session.API

type (
	// SessionEvent is one thing that happened in a session, see Sessions.
	SessionEvent = session.Event
	// SessionKind is what a SessionEvent tells.
	SessionKind = session.Kind
	// SessionInfo describes a session for a list of them.
	SessionInfo = session.Info
	// ContextLine is a message of the context of a session, see
	// Sessions.Contents.
	ContextLine = session.Line
	// SessionCache is how much of what the LLM was sent in a session the
	// provider had cached, see SessionEvent.
	SessionCache = assistant.Cache
	// OwnerQuestion is what the agent asks the owner on its way to an
	// answer, with the answers to choose from: a front end offers them, and
	// to answer in own words instead. See SessionAsked.
	OwnerQuestion = assistant.Question
)

// What a SessionEvent tells.
const (
	SessionMessage  = session.Message  // a message of the owner: an answer starts, or goes on with it
	SessionQueued   = session.Queued   // a message that waits for the answer on its way to take it
	SessionUnqueued = session.Unqueued // the last message that waits, taken back
	SessionSay      = session.Say      // what the agent says along with the tools it asks for
	SessionStep     = session.Step     // a call of a tool, see SessionEvent.AsStep and ToolLine
	SessionAnswer   = session.Answer   // the answer
	SessionFailed   = session.Failed   // an answer that failed
	SessionCanceled = session.Canceled // an answer that was stopped
	SessionRestart  = session.Restart  // ouro starts anew from a new build, the binary in Text
	SessionTitle    = session.Title    // the session is named for its topic, in Text
	SessionEffort   = session.Effort   // how much the model thinks in the session, in Text
	// SessionAsked is a question the agent asks the owner and waits for,
	// see SessionEvent.AsQuestion: what the owner says next in the session,
	// with Sessions.Steer, answers it. With Done the wait is over, and Text
	// is what they answered, "" if they did not.
	SessionAsked = session.Asked
)

var (
	// ErrBusy means that an answer is on its way in the session already.
	ErrBusy = session.ErrBusy
	// ErrNotRunning means that the node that holds the sessions does not
	// run: Sessions.Open starts it.
	ErrNotRunning = session.ErrNotRunning
	// ErrNoSession means that there is no session of that ID.
	ErrNoSession = session.ErrNoSession
)

// Await asks for the answer to text in the session id and waits for it, see
// session.Await.
func Await(ctx context.Context, s Sessions, id, text string) (string, error) {
	return session.Await(ctx, s, id, text)
}

// Picture is a picture of the clipboard of the system: the file it is, if a
// file was copied, or else its data and the extension of its kind.
type Picture = pasteboard.Picture

// ErrNoPicture means that the clipboard holds no picture, or none that can
// be read on this desktop.
var ErrNoPicture = pasteboard.ErrNone

// ClipboardPicture returns the picture on the clipboard of the system, for a
// front end to paste: a terminal hands it the text of the clipboard only.
// It asks the tools of the desktop, see the package pasteboard.
func ClipboardPicture(ctx context.Context) (Picture, error) {
	return pasteboard.System(os.Getenv).Picture(ctx)
}

// IsPicture reports whether a file named path is a picture, by its
// extension, and LocalPath is the path of the file a URI of the scheme file
// names, or name as it is.
func IsPicture(path string) bool { return pasteboard.IsPicture(path) }

// LocalPath is the path of the file a URI of the scheme file names; what is
// no such URI is returned as it is.
func LocalPath(name string) string { return pasteboard.Local(name) }
