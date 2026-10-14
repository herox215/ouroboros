package gui

import (
	"context"
	"errors"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/widget"

	"ouroboros/internal/kernel"
	"ouroboros/plugin"
)

func fake(name, id string) *Profile {
	return &Profile{status: func() (plugin.NodeStatus, error) {
		return plugin.NodeStatus{Name: name, ID: id}, nil
	}}
}

func TestProfiles(t *testing.T) {
	a, b := fake("Luna", "aaa"), fake("Other", "bbb")
	Together([]*Profile{a, nil, b})
	p, err := b.page()
	if err != nil || p.selected != 1 || len(p.choices) != 2 || p.choices[0].name != "Luna" {
		t.Fatalf("page: %+v, %v", p, err)
	}
	single, err := fake("", "").page()
	if err != nil || single.choices[0].name != "Not set up" {
		t.Fatalf("unnamed: %+v, %v", single, err)
	}
	broken := &Profile{status: func() (plugin.NodeStatus, error) { return plugin.NodeStatus{}, errors.New("bad config") }}
	if _, err := broken.page(); err == nil {
		t.Fatal("ignored unreadable profile")
	}
}

func TestCommand(t *testing.T) {
	var ctx *plugin.Context
	var p *Profile
	k := kernel.New()
	if err := k.Start(Plugin(nil, func(g *Profile) { p = g }), plugin.New("spy", func(c *plugin.Context, _ struct{}) error { ctx = c; return nil })); err != nil {
		t.Fatal(err)
	}
	commands := plugin.All[plugin.Command](ctx)
	if len(commands) != 1 || commands[0].Name != "gui" || p == nil {
		t.Fatalf("command: %+v, profile: %v", commands, p)
	}
	if len(plugin.All[plugin.FrontEnd](ctx)) != 0 {
		t.Fatal("GUI replaced the default front end")
	}
	p.status = fake("Luna", "aaa").status
	old := show
	t.Cleanup(func() { show = old })
	called := false
	sentinel := errors.New("window error")
	show = func(_ context.Context, p page) error {
		called = true
		if p.choices[0].id != "aaa" {
			t.Fatalf("page: %+v", p)
		}
		return sentinel
	}
	if err := commands[0].Run(context.Background(), nil); !errors.Is(err, sentinel) || !called {
		t.Fatalf("run: %v, called: %v", err, called)
	}
	called = false
	if err := p.run(context.Background(), []string{"extra"}); err == nil || called {
		t.Fatal("accepted extra argument")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.run(canceled, nil); !errors.Is(err, context.Canceled) || called {
		t.Fatalf("canceled: %v", err)
	}
}

func TestPicker(t *testing.T) {
	a := test.NewApp()
	defer a.Quit()
	root := content(page{choices: []choice{{"Luna", "aaa"}, {"Other", "bbb"}}, selected: 1})
	var picker *widget.Select
	var labels []*widget.Label
	var walk func(fyne.CanvasObject)
	walk = func(o fyne.CanvasObject) {
		switch v := o.(type) {
		case *fyne.Container:
			for _, child := range v.Objects {
				walk(child)
			}
		case *widget.Select:
			picker = v
		case *widget.Label:
			labels = append(labels, v)
		}
	}
	walk(root)
	has := func(text string) bool {
		for _, l := range labels {
			if l.Text == text {
				return true
			}
		}
		return false
	}
	if picker.SelectedIndex() != 1 || !has("Active profile: Other") || !has("bbb") {
		t.Fatal("wrong initial profile")
	}
	picker.SetSelectedIndex(0)
	if !has("Active profile: Luna") || !has("aaa") {
		t.Fatal("profile did not switch")
	}
	labels = nil
	walk(content(page{choices: []choice{{"Not set up", ""}}}))
	if !picker.Disabled() || !has("Profile not set up yet") {
		t.Fatal("single unset profile")
	}
}
