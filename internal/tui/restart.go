package tui

import (
	"image/color"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Keep the alternate screen alive while the restart is announced. Only after
// the last frame may Bubble Tea restore the terminal and hand over the process.
const restartFrames = 26
const restartFrameInterval = 55 * time.Millisecond

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

// restartView is the window while ouro starts anew: what it showed, torn
// more with every frame until the picture breaks down.
func (m *Model) restartView() string {
	return glitchScreen(m.content(), m.width, m.height, m.restartFrame)
}

// restartRunes are what the restart breaks the screen into.
var restartRunes = []rune{'▓', '▒', '░', '█', '▚', '▞', '▙', '▟', '▀', '▄', '#', '¬', '/', '\\', '0', '1', '_', '%'}

// glitchScreen tears a screen of width by height as the frame of the restart
// has it: rows jump to the side and take the place of others, runs of cells
// and whole rows break into noise, and in the last frames the picture
// collapses onto the row in its middle, as a tube that is switched off.
func glitchScreen(content string, width, height, frame int) string {
	if width < 1 || height < 1 {
		return ""
	}
	fit := func(line string) string {
		line = ansi.Truncate(line, width, "")
		return line + strings.Repeat(" ", max(0, width-ansi.StringWidth(line)))
	}
	lines := strings.Split(content, "\n")
	for len(lines) < height {
		lines = append(lines, "")
	}
	lines = lines[:height]
	for i, line := range lines {
		lines[i] = fit(line)
	}
	p := min(1, float64(frame)/restartFrames)
	collapse := (p - 0.72) / 0.28 // from 0 to 1 over the last frames
	// Every third frame the picture is torn harder: it stutters, not fades.
	if frame%3 == 0 {
		p = min(1, p*1.5)
	}
	roll := func(row, salt int) int { return int(hash(row, frame, salt) % 1000) }
	chance := func(row, salt int, prob float64) bool { return float64(roll(row, salt)) < prob*1000 }
	colors := []color.Color{accent, bad, warn, muted}
	noise := func(row, salt, n int) string {
		var b strings.Builder
		for i := range n {
			b.WriteRune(restartRunes[int(hash(row*131+i, frame, salt))%len(restartRunes)])
		}
		style := lipgloss.NewStyle().Foreground(colors[roll(row, salt+1)%len(colors)])
		return style.Reverse(roll(row, salt+2)%6 == 0).Render(b.String())
	}
	out := make([]string, height)
	for y := range out {
		line := lines[y]
		if chance(y/2, 1, p*0.35) { // a band of rows shows what stands elsewhere
			line = lines[min(height-1, max(0, y+roll(y/2, 2)%9-4))]
		}
		switch {
		case chance(y, 3, p*p*0.75): // the row loses its colors and its letters
			cells := []rune(ansi.Strip(line))
			for i, r := range cells {
				if at := y*977 + i; r != ' ' && chance(at, 4, 0.2+p*0.6) || chance(at, 5, p*0.12) {
					cells[i] = restartRunes[int(hash(at, frame, 6))%len(restartRunes)]
				}
			}
			line = lipgloss.NewStyle().Foreground(colors[roll(y, 7)%len(colors)]).Render(string(cells))
		case chance(y, 8, 0.1+p*0.6): // a run of cells breaks into noise
			n := min(width, 2+roll(y, 9)%max(1, width/3))
			x := roll(y, 10) % (width - n + 1)
			line = ansi.Cut(line, 0, x) + noise(y, 11, n) + ansi.Cut(line, x+n, width)
		}
		if chance(y/2, 14, 0.08+p*0.6) { // the band jumps to the side
			if n := min(width, 1+roll(y/2, 15)%max(1, int(p*float64(width)/3))); roll(y/2, 16)%2 == 0 {
				line = strings.Repeat(" ", n) + ansi.Cut(line, 0, width-n)
			} else {
				line = ansi.Cut(line, n, width) + strings.Repeat(" ", n)
			}
		}
		if middle := height / 2; collapse > 0 {
			switch away := float64(max(y-middle, middle-y)) / float64(max(1, middle)); {
			case y == middle && collapse > 0.6: // the line the picture ends as
				n := max(3, int(float64(width)*(1-collapse)/0.4))
				line = lipgloss.NewStyle().Foreground(accent).Render(strings.Repeat(" ", max(0, (width-n)/2)) + strings.Repeat("━", min(width, n)))
			case away > 1-collapse:
				line = ""
				if chance(y, 17, 0.25*(1-collapse)) { // what is left of the row
					line = strings.Repeat(" ", roll(y, 18)%width) + noise(y, 19, 1+roll(y, 20)%6)
				}
			}
		}
		out[y] = fit(line)
	}
	return strings.Join(out, "\n")
}
