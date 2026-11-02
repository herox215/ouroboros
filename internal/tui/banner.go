package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"ouroboros/plugin"
)

// threadBanners are the name of the thread as ASCII art, the widest first:
// in the "small" font of figlet and, for a narrow sidebar, in its "mini"
// font. Each is drawn once by hand, so every row lines up.
var threadBanners = [][]string{
	{
		` _____  _                        _ `,
		`|_   _|| |_   _ _  ___  __ _  __| |`,
		`  | |  | ' \ | '_|/ -_)/ _` + "`" + ` |/ _` + "`" + ` |`,
		`  |_|  |_||_||_|  \___|\__,_|\__,_|`,
	},
	{
		`___`,
		` | |_  ._  _   _.  _|`,
		` | | | |  (/_ (_| (_|`,
	},
}

// threadArt is the name of the thread as ASCII art, the widest banner that
// fits in w cells: the sidebar leads with it above the sessions that run
// while the thread is shown. A window too low for it, a conversation of its
// own, or a sidebar too narrow for every banner has none.
func (m *Model) threadArt(w int) ([]string, bool) {
	if m.chat.contextID != plugin.ThreadID || m.compact() {
		return nil, false
	}
	for _, art := range threadBanners {
		if bannerWidth(art) <= w {
			return art, true
		}
	}
	return nil, false
}

// bannerWidth is how many cells the widest row of a banner takes.
func bannerWidth(art []string) int {
	width := 0
	for _, row := range art {
		width = max(width, ansi.StringWidth(row))
	}
	return width
}

// styledBanner colors the banner with the gradient of the splash, running
// across it from left to right and a little down, in bold.
func (m *Model) styledBanner(art []string) []string {
	p := colors(m.light)
	width := bannerWidth(art)
	lines := make([]string, len(art))
	for y, row := range art {
		var b strings.Builder
		for x, r := range row {
			if r == ' ' {
				b.WriteByte(' ')
				continue
			}
			f := (float64(x)/float64(width) + float64(y)*0.04) * 0.75
			b.WriteString(lipgloss.NewStyle().Bold(true).Foreground(p.gradient(f)).Render(string(r)))
		}
		lines[y] = b.String()
	}
	return lines
}
