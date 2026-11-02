package gui

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

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
	// The error counts the profiles from 1, as the picker shows them.
	Together([]*Profile{a, broken})
	if _, err := a.page(); err == nil || err.Error() != "GUI profile 2: bad config" {
		t.Fatalf("second profile unreadable: %v", err)
	}
}

func TestCommand(t *testing.T) {
	var ctx *plugin.Context
	var p *Profile
	k := kernel.New()
	if err := k.Start(sessionProvider(&testSessions{}), Plugin(nil, func(g *Profile) { p = g }), plugin.New("spy", func(c *plugin.Context, _ struct{}) error { ctx = c; return nil })); err != nil {
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

func TestWindowState(t *testing.T) {
	p := page{choices: []choice{{"Luna", "aaa"}, {"Other", "bbb"}}, selected: 1}
	state := newWindow(p).InitialState()
	if state.Selected != 1 || len(state.Profiles) != 2 || state.Profiles[0].Name != "Luna" || state.Profiles[1].ID != "bbb" {
		t.Fatalf("state: %+v", state)
	}
	// The window owns its snapshot, not the frame's slice.
	p.choices[0].name = "Changed"
	if state.Profiles[0].Name != "Luna" {
		t.Fatal("window retained the frame's mutable choices")
	}
}

// The window of the process this one makes room for goes on with the profile
// and the session that process showed.
func TestRunGoesOnWithTheHandedOverSession(t *testing.T) {
	a, b := fake("Luna", "aaa"), fake("Other", "bbb")
	Together([]*Profile{a, b})
	old := show
	t.Cleanup(func() { show = old })
	var got page
	called := false
	show = func(_ context.Context, p page) error { called, got = true, p; return nil }
	t.Setenv(resumeEnv, `{"profile":"bbb","session":"owner/s1"}`)
	if err := a.run(context.Background(), nil); err != nil || !called {
		t.Fatalf("run: %v, shown %v", err, called)
	}
	if got.session != "owner/s1" || got.selected != 1 {
		t.Fatalf("window: %+v", got)
	}
	t.Setenv(resumeEnv, `{"profile":"unknown","session":"owner/s2"}`)
	if err := a.run(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if got.session != "owner/s2" || got.selected != 0 {
		t.Fatalf("a profile that is gone keeps the selection: %+v", got)
	}
}

func TestResumed(t *testing.T) {
	if r, err := resumed(); err != nil || r != nil {
		t.Fatalf("nothing handed over: %+v %v", r, err)
	}
	t.Setenv(resumeEnv, `{"profile":"bbb","session":"owner/s1"}`)
	r, err := resumed()
	if err != nil || r.Profile != "bbb" || r.Session != "owner/s1" {
		t.Fatalf("handover: %+v %v", r, err)
	}
	if v, ok := os.LookupEnv(resumeEnv); ok {
		t.Fatalf("the node would see %q", v)
	}
	for _, bad := range []string{"not json", `{"profile":"bbb"}`, `{"session":""}`} {
		t.Setenv(resumeEnv, bad)
		if _, err := resumed(); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestHandOver(t *testing.T) {
	err := handOver("/new/ouro", resume{Profile: "aaa", Session: "owner/s1"})
	r, ok := errors.AsType[*plugin.Restarting](err)
	if !ok || r.Exe != "/new/ouro" {
		t.Fatalf("handover: %v", err)
	}
	if !slices.Contains(r.Env, resumeEnv+`={"profile":"aaa","session":"owner/s1"}`) {
		t.Fatalf("env: %v", r.Env)
	}
}

// The command fails before any window when a profile cannot be read, or when
// what the process before handed over is no session.
func TestRunFailsBeforeTheWindow(t *testing.T) {
	old := show
	t.Cleanup(func() { show = old })
	show = func(context.Context, page) error { t.Fatal("window shown"); return nil }
	broken := &Profile{status: func() (plugin.NodeStatus, error) { return plugin.NodeStatus{}, errors.New("bad config") }}
	if err := broken.run(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "bad config") {
		t.Fatalf("unreadable profile: %v", err)
	}
	t.Setenv(resumeEnv, "not json")
	if err := fake("Luna", "aaa").run(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "the session to go on with") {
		t.Fatalf("handed over no session: %v", err)
	}
}
