package node

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"sync"

	"ouroboros/internal/config"
	"ouroboros/internal/home"
	"ouroboros/internal/llm"
	"ouroboros/internal/setup"
	"ouroboros/plugin"
)

// wizard is the interactive setup of the node on the terminal.
type wizard struct {
	term      *plugin.Console
	home      home.Dir
	providers func() []plugin.Provider // of this ouro, read when the setup runs
}

// run asks for the access, the model and the node name and saves the
// configuration.
func (w *wizard) run(ctx context.Context, cfg config.Config) (config.Config, error) {
	s := setup.New(w.home, w.providers, cfg)
	d := &lines{term: w.term, out: &serial{w: w.term.Out}}
	models, err := w.access(ctx, s, d)
	if err != nil {
		return cfg, err
	}
	model, err := w.askModel(ctx, d, models)
	if err != nil {
		return cfg, err
	}
	name, err := d.prompt(ctx, fmt.Sprintf("Node name [%s]: ", s.Name()), s.Name(), false)
	if err != nil {
		return cfg, err
	}
	if err := s.Save(model, name); err != nil {
		return cfg, err
	}
	return w.home.Config()
}

// access lets the user pick the access the agents answer through and returns
// its models. A node without one signs in to its first. One with accesses
// shows them, to keep one, add one, sign in to one again, rename or remove
// one.
func (w *wizard) access(ctx context.Context, s *setup.Session, d *lines) (setup.Models, error) {
	for {
		accesses, err := s.Accesses()
		if err != nil {
			return setup.Models{}, err
		}
		if len(accesses) == 0 {
			return w.add(ctx, s, d)
		}
		fmt.Fprintf(d.out, "\nAccesses:\n\n")
		active := 1
		for i, a := range accesses {
			line := fmt.Sprintf("  %3d) %s", i+1, a.Label)
			if a.About != "" {
				line += " · " + a.About
			}
			if a.Active {
				active = i + 1
				line += "  (in use)"
			}
			if a.Missing {
				line += "  (its provider " + a.Provider + " is not part of this ouro)"
			}
			fmt.Fprintln(d.out, line)
		}
		fmt.Fprintf(d.out, "\n  a = add · l N = sign in again · r N NAME = rename · d N = remove\n")
		pick, err := d.prompt(ctx, fmt.Sprintf("\nAccess [number, Enter = %d]: ", active), strconv.Itoa(active), false)
		if err != nil {
			return setup.Models{}, err
		}
		verb, rest, _ := strings.Cut(pick, " ")
		number, name, _ := strings.Cut(strings.TrimSpace(rest), " ")
		if n, err := strconv.Atoi(verb); err == nil {
			verb, number = "use", strconv.Itoa(n)
		}
		if verb == "a" {
			return w.add(ctx, s, d)
		}
		n, _ := strconv.Atoi(number)
		if n < 1 || n > len(accesses) || !slices.Contains([]string{"use", "l", "r", "d"}, verb) {
			fmt.Fprintln(d.out, "✗ unknown choice")
			continue
		}
		id := accesses[n-1].ID
		switch verb {
		case "use":
			models, err := s.Use(ctx, id)
			if !errors.Is(err, setup.ErrKeyRejected) {
				return models, err
			}
			fmt.Fprintln(d.out, "✗ API key rejected: sign in again")
			return s.Again(ctx, id, d)
		case "l":
			return s.Again(ctx, id, d)
		case "r":
			if err := s.Rename(id, strings.TrimSpace(name)); err != nil {
				return setup.Models{}, err
			}
		case "d":
			note, err := s.Remove(ctx, id)
			if err != nil {
				fmt.Fprintf(d.out, "✗ %v\n", err)
			} else if note != "" {
				fmt.Fprintln(d.out, note)
			}
		}
	}
}

// add signs in to a new access: with the only provider there is, or the one
// the user picks.
func (w *wizard) add(ctx context.Context, s *setup.Session, d *lines) (setup.Models, error) {
	providers := s.Providers()
	switch len(providers) {
	case 0:
		return setup.Models{}, errors.New("this ouro has no provider to sign in with: turn the plugin of one on in config.json")
	case 1:
		return s.Login(ctx, providers[0].Name, d)
	}
	fmt.Fprintf(d.out, "\nProviders:\n\n")
	for i, p := range providers {
		fmt.Fprintf(d.out, "  %3d) %s\n", i+1, p.Title)
	}
	for {
		pick, err := d.prompt(ctx, "\nProvider [number, Enter = 1]: ", "1", false)
		if err != nil {
			return setup.Models{}, err
		}
		if n, err := strconv.Atoi(pick); err == nil && n >= 1 && n <= len(providers) {
			return s.Login(ctx, providers[n-1].Name, d)
		}
		fmt.Fprintln(d.out, "✗ unknown provider")
	}
}

// askModel lets the user pick a model by number or name.
func (w *wizard) askModel(ctx context.Context, d *lines, models setup.Models) (string, error) {
	fmt.Fprintf(d.out, "\nAvailable models:\n\n")
	printColumns(d.out, models.Chat)
	for {
		pick, err := d.prompt(ctx, fmt.Sprintf("\nChoose model [number or name, Enter = %s]: ", models.Default), models.Default, false)
		if err != nil {
			return "", err
		}
		if m, ok := models.Resolve(pick); ok {
			return m, nil
		}
		fmt.Fprintln(d.out, "✗ unknown model")
	}
}

func printColumns(w io.Writer, items []string) {
	if len(items) == 0 {
		return
	}
	width := len(slices.MaxFunc(items, func(a, b string) int { return cmp.Compare(len(a), len(b)) }))
	cols := max(1, 96/(width+7))
	for i, item := range items {
		fmt.Fprintf(w, "  %3d) %-*s", i+1, width, item)
		if (i+1)%cols == 0 || i == len(items)-1 {
			fmt.Fprintln(w)
		}
	}
}

// lines is the dialog of the setup on the terminal: it asks line by line.
//
// A provider may give up a question, as when the browser brought what it
// asked to be pasted, and it may ask beside its work. The terminal cannot
// take a read back, so the line it is still waiting for answers the next
// question: every question of the setup is asked through here, and all the
// setup prints goes through out, one writer at a time.
type lines struct {
	term *plugin.Console
	out  *serial

	mu      sync.Mutex
	waiting chan answer // the read that is not answered yet, nil if none
}

// serial is a writer for several goroutines: one write at a time.
type serial struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *serial) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

// answer is what a read of the terminal brought.
type answer struct {
	line string
	err  error
}

// prompt prints prompt and reads an answer, returning def for an empty one.
// It ends with the error of ctx when the question is given up; the line the
// terminal is waiting for then goes to the next question.
func (l *lines) prompt(ctx context.Context, prompt, def string, secret bool) (string, error) {
	fmt.Fprint(l.out, prompt)
	l.mu.Lock()
	read := l.waiting
	if read == nil {
		read = make(chan answer, 1)
		l.waiting = read
		go func() {
			var a answer
			if secret {
				a.line, a.err = l.term.ReadSecret("") // the prompt is printed
			} else {
				a.line, a.err = l.term.ReadLine()
			}
			read <- a
		}()
	}
	l.mu.Unlock()
	select {
	case a := <-read:
		// A question that was given up at the same moment leaves the line
		// to the next one.
		if ctx.Err() != nil {
			read <- a
			return "", ctx.Err()
		}
		l.mu.Lock()
		l.waiting = nil
		l.mu.Unlock()
		return cmp.Or(a.line, def), a.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// Ask asks q on a line of its own. An answer the provider already has is
// taken without asking.
func (l *lines) Ask(ctx context.Context, q llm.Question) (string, error) {
	if q.Known {
		return q.Default, nil
	}
	text := ""
	for _, line := range q.Text {
		text += line + "\n"
	}
	prompt := q.Title
	switch {
	case q.Default != "" && !q.Secret:
		prompt += " [" + q.Default + "]"
	case q.Hint != "":
		prompt += " (" + q.Hint + ")"
	}
	return l.prompt(ctx, text+prompt+": ", q.Default, q.Secret)
}

// Tell prints text.
func (l *lines) Tell(text string) { fmt.Fprintln(l.out, text) }

// Reject prints why the last answer was not taken.
func (l *lines) Reject(why string) { fmt.Fprintln(l.out, "✗ "+why) }

// Open prints url for the user to open, and opens it in the browser if
// there is one here.
func (l *lines) Open(url string) {
	fmt.Fprintf(l.out, "Open this page in your browser:\n\n  %s\n\n", url)
	llm.Browse(url) // without a browser here, the URL above is what there is
}
