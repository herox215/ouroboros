package plugin

import (
	"bufio"
	"cmp"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Terminal is how ouro talks to its user.
type Terminal struct {
	In  io.Reader
	Out io.Writer
	// ReadSecret reads a secret without echoing it. If nil, a plain line is read.
	ReadSecret func(prompt string) (string, error)
	// Interactive reports whether In is a terminal. Without it, ouro with no
	// command prints the usage instead of opening the terminal UI.
	Interactive bool
}

// Console is the line-based dialogue with the user. Commands read and write
// through it instead of os.Stdin and os.Stdout, so that tests can drive them.
type Console struct {
	// Out is where a command prints.
	Out io.Writer
	// Interactive reports whether the user sits at a terminal.
	Interactive bool

	in     *bufio.Reader
	secret func(prompt string) (string, error) // nil: a plain line is read
}

// NewConsole returns the console on t.
func NewConsole(t Terminal) *Console {
	return &Console{Out: t.Out, Interactive: t.Interactive, in: bufio.NewReader(t.In), secret: t.ReadSecret}
}

// ReadLine reads one trimmed line. It returns io.EOF only when no input is left.
func (c *Console) ReadLine() (string, error) {
	line, err := c.in.ReadString('\n')
	if err != nil && (line == "" || !errors.Is(err, io.EOF)) {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// Prompt prints prompt and reads an answer, returning def for an empty one.
func (c *Console) Prompt(prompt, def string) (string, error) {
	fmt.Fprint(c.Out, prompt)
	line, err := c.ReadLine()
	return cmp.Or(line, def), err
}

// ReadSecret reads an answer that should not be echoed.
func (c *Console) ReadSecret(prompt string) (string, error) {
	if c.secret != nil {
		return c.secret(prompt)
	}
	return c.Prompt(prompt, "")
}

// Converse answers the user line by line until the input ends. An answer that
// fails is reported, and the conversation goes on.
func (c *Console) Converse(intro string, answer func(line string) (string, error)) error {
	fmt.Fprintf(c.Out, "%s (Ctrl-D to quit)\n", intro)
	for {
		fmt.Fprint(c.Out, "› ")
		line, err := c.ReadLine()
		if errors.Is(err, io.EOF) {
			fmt.Fprintln(c.Out)
			return nil
		}
		if err != nil {
			return err
		}
		if line == "" {
			continue
		}
		reply, err := answer(line)
		if err != nil {
			reply = "✗ " + err.Error()
		}
		fmt.Fprintf(c.Out, "%s\n\n", reply)
	}
}
