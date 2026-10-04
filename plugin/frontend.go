package plugin

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
