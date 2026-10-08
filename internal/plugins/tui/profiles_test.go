package tui_test

import (
	"context"
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"ouroboros/internal/cli"
	"ouroboros/internal/cli/clitest"
	"ouroboros/internal/config"
	"ouroboros/internal/daemon"
	"ouroboros/internal/home"
	tuiplugin "ouroboros/internal/plugins/tui"
	"ouroboros/internal/session"
	"ouroboros/internal/tui"
)

// shiftTab is the press of shift+tab.
var shiftTab = tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}

// The UI of a node with several profiles shows them all: it opens on the one
// named, or on the first, and shift+tab shows the next. Each answers in its
// own sessions, and a new build goes on with the profile shown.
func TestProfilesInOneUI(t *testing.T) {
	s := clitest.Settings(t)
	terra := home.Dir(s.Home)
	t.Cleanup(func() { os.RemoveAll(s.Home + "-workspaces") })
	cfg := clitest.NodeConfig(s)
	cfg.Name = "Terra"
	config.Save(s.Home, cfg)
	luna, err := terra.AddProfile("Luna")
	if err != nil {
		t.Fatal(err)
	}
	lunaID, _ := luna.Identity()
	term := cli.Terminal{Interactive: true, In: strings.NewReader(""), Out: &strings.Builder{}}
	node, err := cli.New(s, term)
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- node.Run(ctx, []string{"serve"}) }()
	t.Cleanup(func() { stop(); <-served })
	if _, ok := daemon.WaitRunning(terra.Service().PIDFile, clitest.Patience); !ok {
		t.Fatal("the node did not start")
	}

	shows := func(m *tui.Model, text string) func() bool {
		return func() bool { return strings.Contains(ansi.Strip(m.View().Content), text) }
	}
	tuiplugin.Use(t, func(m *tui.Model) error {
		m.Update(tea.WindowSizeMsg{Width: 200, Height: 30})
		m.Update(key(' ')) // skips the splash
		drive(t, m, m.Init(), shows(m, "Terra"))
		_, cmd := m.Update(shiftTab)
		if !shows(m, "profile Luna ("+lunaID.ID()+") · shift+tab shows the next")() {
			t.Errorf("no word of the profile:\n%s", ansi.Strip(m.View().Content))
		}
		drive(t, m, cmd, shows(m, lunaID.ID()+" · Luna · "))
		drive(t, m, typed(m, "hello Luna"), shows(m, "last=hello Luna"))
		if m.Profile() != lunaID.ID() {
			t.Errorf("the UI shows %q", m.Profile())
		}
		return nil
	})
	ui, err := cli.New(s, term)
	if err != nil {
		t.Fatal(err)
	}
	if err := ui.Run(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if list, err := session.NewClient(luna).List(context.Background()); err != nil || len(list) != 1 {
		t.Fatalf("Luna has %+v, %v", list, err)
	}
	if list, err := session.NewClient(terra).List(context.Background()); err != nil || len(list) != 0 {
		t.Fatalf("Terra has %+v, %v", list, err)
	}

	// Named, a profile is shown first.
	tuiplugin.Use(t, func(m *tui.Model) error {
		if m.Profile() != lunaID.ID() {
			t.Errorf("the UI opens on %q", m.Profile())
		}
		return nil
	})
	if err := ui.Run(context.Background(), []string{"--profile", "Luna", "tui"}); err != nil {
		t.Fatal(err)
	}
	// So is the one a UI went on from.
	t.Setenv(tuiplugin.ResumeEnv, `{"profile":"`+lunaID.ID()+`","session":"s1","was_running":true}`)
	tuiplugin.Use(t, func(m *tui.Model) error {
		if m.Profile() != lunaID.ID() || m.Current() != "s1" {
			t.Errorf("the UI goes on with %q in %q", m.Current(), m.Profile())
		}
		return nil
	})
	if err := ui.Run(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
}
