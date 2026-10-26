package tui

import (
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Keep the alternate screen alive while the restart is announced. Only after
// the last frame may Bubble Tea restore the terminal and hand over the process.
const restartFrames = 18
const restartFrameInterval = 70 * time.Millisecond

type restartFrameMsg struct{}

func restartTick() tea.Cmd {
	return tea.Tick(restartFrameInterval, func(time.Time) tea.Msg { return restartFrameMsg{} })
}

func (m *Model) beginRestart(exe string) tea.Cmd {
	if m.restarts != "" {
		return nil
	}
	m.restarts = exe
	m.restartFrame = 1
	return restartTick()
}

func (m *Model) advanceRestart() tea.Cmd {
	m.restartFrame++
	if m.restartFrame > restartFrames {
		return m.leave()
	}
	return restartTick()
}

func (m *Model) restartView() string {
	noise := []string{
		"0101 // :: __ // 1010",
		"0#01 /_ :# __ // 1_10",
		"01_1 // :: ## /_ 1010",
		"0101 // :: __ // 1010",
	}
	frame := m.restartFrame
	bar := strings.Repeat("#", min(16, frame)) + strings.Repeat(".", max(0, 16-frame))
	art := fmt.Sprintf("+--------------------------------+\n| OUROBOROS :: RESTART            |\n| > /dev/ouro  [%-16s] |\n| %-30s |\n+--------------------------------+\n\n  RESTARTING // PLEASE WAIT_", bar, noise[frame%len(noise)])
	if frame%3 == 0 {
		art = strings.ReplaceAll(art, "OUROBOROS", "0UR0B0R0S")
	}
	lines := strings.Split(art, "\n")
	for i, line := range lines {
		lines[i] = ansi.Truncate(line, max(1, m.width), "")
	}
	if len(lines) > m.height {
		lines = lines[:max(0, m.height)]
	}
	art = lipgloss.NewStyle().Foreground(accent).Render(strings.Join(lines, "\n"))
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, art)
}
