package tui

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"ouroboros/plugin"
)

func TestTerminalRestartAnimation(t *testing.T) {
	m, _ := newModel(t)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	cmd := m.beginRestart("/new/ouro")
	if cmd == nil || m.ctx.Err() != nil {
		t.Fatal("restart must animate before closing")
	}
	if _, ok := cmd().(restartFrameMsg); !ok {
		t.Fatal("restart did not schedule a frame")
	}
	first := ansi.Strip(m.View().Content)
	if !strings.Contains(first, "RESTARTING // PLEASE WAIT") || !m.View().AltScreen {
		t.Fatalf("no terminal transition: %q", first)
	}
	if duplicate := m.beginRestart("/other/ouro"); duplicate != nil || m.restarts != "/new/ouro" {
		t.Fatal("duplicate restart reset the transition")
	}
	for frame := 1; frame < restartFrames; frame++ {
		_, cmd = m.Update(restartFrameMsg{})
		if cmd == nil || m.ctx.Err() != nil {
			t.Fatal("quit before the last frame")
		}
	}
	if first == ansi.Strip(m.View().Content) {
		t.Fatal("ASCII frames did not change")
	}
	_, cmd = m.Update(restartFrameMsg{})
	if _, ok := cmd().(tea.QuitMsg); !ok || m.ctx.Err() == nil {
		t.Fatal("finished transition did not quit")
	}
}

func TestTerminalRestartResizeAndProfile(t *testing.T) {
	root, terra, _, _ := profiles(t)
	terra.beginRestart("/new/ouro")
	root.profiles.shown = 1 // A background profile restarting must still be visible.
	root.Update(tea.WindowSizeMsg{Width: 20, Height: 4})
	content := root.View().Content
	if !strings.Contains(ansi.Strip(content), "0UR0B0R0S") && !strings.Contains(ansi.Strip(content), "OUROBOROS") {
		t.Fatalf("profile restart hidden: %q", content)
	}
	for _, line := range strings.Split(content, "\n") {
		if ansi.StringWidth(line) > 20 {
			t.Fatalf("transition exceeds terminal width: %q", line)
		}
	}
	if strings.Count(content, "\n") >= 4 {
		t.Fatalf("transition exceeds the terminal: %q", content)
	}
	root.Update(profileMsg{0, restartFrameMsg{}})
	if terra.restartFrame != 2 {
		t.Fatal("profile frame was not routed")
	}
}

func TestHistoricalRestartDoesNotAnimate(t *testing.T) {
	m, _ := newModel(t)
	m.apply(m.chat, Event{Kind: plugin.SessionRestart, Text: "/old/ouro"}, false)
	if m.restartFrame != 0 {
		t.Fatal("archived restart animated")
	}
}

// Exercise Bubble Tea's renderer and timer loop, not only Model.View.
type restartProgram struct{ *Model }

func (m restartProgram) Init() tea.Cmd { return m.beginRestart("/new/ouro") }

func TestRestartReachesTerminalOutput(t *testing.T) {
	m, _ := newModel(t)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	var output bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	program := tea.NewProgram(restartProgram{m}, tea.WithContext(ctx), tea.WithInput(nil), tea.WithOutput(&output), tea.WithWindowSize(80, 24), tea.WithEnvironment([]string{"TERM=xterm-256color"}))
	if _, err := program.Run(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ansi.Strip(output.String()), "RESTARTING // PLEASE WAIT") {
		t.Fatalf("no restart in terminal output: %q", output.String())
	}
	if m.restartFrame <= restartFrames {
		t.Fatal("renderer stopped before the animation completed")
	}
}
