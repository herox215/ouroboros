package node

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"

	"ouroboros/internal/config"
	"ouroboros/internal/home"
	"ouroboros/internal/setup"
	"ouroboros/plugin"
)

// wizard is the interactive setup of the node on the terminal.
type wizard struct {
	term  *plugin.Console
	home  home.Dir
	offer setup.Offer // the endpoint, and the key that belongs to it
}

// run asks for endpoint, API key, model and node name and saves the configuration.
func (w *wizard) run(ctx context.Context, cfg config.Config) (config.Config, error) {
	s := setup.New(w.home, w.offer, cfg)
	endpoint, err := w.term.Prompt(fmt.Sprintf("API endpoint (OpenAI-compatible) [%s]: ", s.Endpoint()), s.Endpoint())
	if err != nil {
		return cfg, err
	}
	models, err := w.connect(ctx, s, endpoint)
	if err != nil {
		return cfg, err
	}
	model, err := w.askModel(models)
	if err != nil {
		return cfg, err
	}
	name, err := w.term.Prompt(fmt.Sprintf("Node name [%s]: ", s.Name()), s.Name())
	if err != nil {
		return cfg, err
	}
	if err := s.Save(model, name); err != nil {
		return cfg, err
	}
	return w.home.Config()
}

// connect tries the key known for endpoint, then asks for one until the
// endpoint accepts it (possibly none), and returns the endpoint's models.
func (w *wizard) connect(ctx context.Context, s *setup.Session, endpoint string) (setup.Models, error) {
	if s.HasKey(endpoint) {
		models, err := s.Saved(ctx, endpoint)
		if !errors.Is(err, setup.ErrKeyRejected) {
			return models, err
		}
		fmt.Fprintln(w.term.Out, "✗ API key rejected, try again")
	}
	for {
		key, err := w.term.ReadSecret("API key (Enter for none): ")
		if err != nil {
			return setup.Models{}, err
		}
		models, err := s.Connect(ctx, endpoint, key)
		if !errors.Is(err, setup.ErrKeyRejected) {
			return models, err
		}
		fmt.Fprintln(w.term.Out, "✗ API key rejected, try again")
	}
}

// askModel lets the user pick a model by number or name.
func (w *wizard) askModel(models setup.Models) (string, error) {
	fmt.Fprintf(w.term.Out, "\nAvailable models:\n\n")
	printColumns(w.term.Out, models.Chat)
	for {
		pick, err := w.term.Prompt(fmt.Sprintf("\nChoose model [number or name, Enter = %s]: ", models.Default), models.Default)
		if err != nil {
			return "", err
		}
		if m, ok := models.Resolve(pick); ok {
			return m, nil
		}
		fmt.Fprintln(w.term.Out, "✗ unknown model")
	}
}

func printColumns(w io.Writer, items []string) {
	width := len(slices.MaxFunc(items, func(a, b string) int { return cmp.Compare(len(a), len(b)) }))
	cols := max(1, 96/(width+7))
	for i, item := range items {
		fmt.Fprintf(w, "  %3d) %-*s", i+1, width, item)
		if (i+1)%cols == 0 || i == len(items)-1 {
			fmt.Fprintln(w)
		}
	}
}
