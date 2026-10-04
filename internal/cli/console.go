package cli

import (
	"bufio"
	"cmp"
	"errors"
	"fmt"
	"io"
	"strings"
)

// console is the line-based dialogue with the user. Commands read and write
// through it instead of os.Stdin and os.Stdout, so that tests can drive them.
type console struct {
	in     *bufio.Reader
	out    io.Writer
	secret func(prompt string) (string, error) // nil: a plain line is read
}

func newConsole(t Terminal) *console {
	return &console{in: bufio.NewReader(t.In), out: t.Out, secret: t.ReadSecret}
}

// readLine reads one trimmed line. It returns io.EOF only when no input is left.
func (c *console) readLine() (string, error) {
	line, err := c.in.ReadString('\n')
	if err != nil && (line == "" || !errors.Is(err, io.EOF)) {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// prompt prints prompt and reads an answer, returning def for an empty one.
func (c *console) prompt(prompt, def string) (string, error) {
	fmt.Fprint(c.out, prompt)
	line, err := c.readLine()
	return cmp.Or(line, def), err
}

// readSecret reads an answer that should not be echoed.
func (c *console) readSecret(prompt string) (string, error) {
	if c.secret != nil {
		return c.secret(prompt)
	}
	return c.prompt(prompt, "")
}

// converse answers the user line by line until the input ends. An answer that
// fails is reported, and the conversation goes on.
func (c *console) converse(intro string, answer func(line string) (string, error)) error {
	fmt.Fprintf(c.out, "%s (Ctrl-D to quit)\n", intro)
	for {
		fmt.Fprint(c.out, "› ")
		line, err := c.readLine()
		if errors.Is(err, io.EOF) {
			fmt.Fprintln(c.out)
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
		fmt.Fprintf(c.out, "%s\n\n", reply)
	}
}
