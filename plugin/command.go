package plugin

import "context"

// Command is one ouro command. Plugins contribute commands with Add; the
// usage text is generated from them.
type Command struct {
	Name string // what the user types: ouro NAME
	Args string // its arguments, as shown in the usage
	Help string // one line for the usage
	// Run gets the arguments after the name. It prints to the console and
	// returns an error to fail with exit code 1.
	Run func(ctx context.Context, args []string) error
}
