package tui

import (
	"bytes"
	"context"
	"slices"
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
	if !strings.Contains(first, "Write a message") || !m.View().AltScreen {
		t.Fatalf("the transition does not begin with the screen as it was: %q", first)
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
	last := ansi.Strip(m.View().Content)
	if strings.TrimSpace(last) != "━━━" || strings.Count(last, "\n") != 23 {
		t.Fatalf("the screen did not collapse onto a line: %q", last)
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
	if content != terra.restartView() {
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

// The restart tears the screen more with every frame, and never beyond the
// window: every frame has its rows and its width, whatever the size.
func TestGlitchScreenKeepsTheWindow(t *testing.T) {
	screen := strings.Repeat("\x1b[31mouro builds itself anew, 世界\x1b[0m and more\n", 30)
	for _, size := range [][2]int{{80, 24}, {20, 4}, {1, 1}, {7, 40}} {
		w, h := size[0], size[1]
		torn := 0
		for frame := 1; frame <= restartFrames; frame++ {
			got := glitchScreen(screen, w, h, frame)
			lines := strings.Split(got, "\n")
			if len(lines) != h {
				t.Fatalf("%dx%d frame %d has %d rows", w, h, frame, len(lines))
			}
			for _, line := range lines {
				if ansi.StringWidth(line) != w {
					t.Fatalf("%dx%d frame %d: a row of %d cells: %q", w, h, frame, ansi.StringWidth(line), line)
				}
			}
			if got != glitchScreen(screen, w, h, 0) {
				torn++
			}
		}
		if w > 1 && torn < restartFrames/2 {
			t.Fatalf("%dx%d: only %d frames are torn", w, h, torn)
		}
	}
	if glitchScreen(screen, 0, 0, 3) != "" {
		t.Fatal("a window of no size shows something")
	}
}

func TestHistoricalRestartDoesNotAnimate(t *testing.T) {
	m, _ := newModel(t)
	m.apply(m.chat, Event{Kind: plugin.SessionRestart, Text: "/old/ouro"}, false)
	if m.restartFrame != 0 {
		t.Fatal("archived restart animated")
	}
}

// What was typed into the input before ouro started anew is put back into it,
// in the profile the UI shows, with the files its labels stand for.
func TestDraft(t *testing.T) {
	m, _ := newModel(t)
	d := Draft{Text: "half a thought\nand another", Images: []string{"/tmp/picture.png"}, Files: []string{"/tmp/notes.pdf"}}
	m.SetDraft(d)
	if got := m.Draft(); got.Text != d.Text || !slices.Equal(got.Images, d.Images) || !slices.Equal(got.Files, d.Files) {
		t.Fatalf("the draft is %+v", got)
	}
	root, terra, luna, _ := profiles(t)
	root.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	root.SetDraft(Draft{Text: "for terra"})
	root.Update(shiftTab)
	root.SetDraft(Draft{Text: "for luna"})
	if terra.Draft().Text != "for terra" || luna.Draft().Text != "for luna" || root.Draft().Text != "for luna" {
		t.Fatalf("terra %q, luna %q, shown %q", terra.Draft().Text, luna.Draft().Text, root.Draft().Text)
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
	if !strings.Contains(ansi.Strip(output.String()), "━━━") {
		t.Fatalf("no restart in terminal output: %q", output.String())
	}
	if m.restartFrame <= restartFrames {
		t.Fatal("renderer stopped before the animation completed")
	}
}
