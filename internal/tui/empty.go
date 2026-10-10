package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// nameScales are the sizes an empty chat draws the name of the agent in, the
// largest first: twice the title of the splash, and the title itself.
var nameScales = [...]int{2, 1}

const emptyTagline = "keep on building"

var emptyTaglines = [...]string{
	emptyTagline, "stay curious", "make it happen", "build something great",
	"one idea at a time", "turn ideas into reality", "keep moving forward",
}

// taglineText defaults to the original line for callers without a selection.
func taglineText(selected []string) string {
	if len(selected) > 0 {
		return selected[0]
	}
	return emptyTagline
}

// emptyTaglineRow leaves a blank row below the name. Small windows keep
// the name alone rather than clipping either it or the tagline.
func emptyTaglineRow(width, height, nameHeight int, selected ...string) (int, bool) {
	row := (height-nameHeight)/2 + nameHeight + 1
	return row, width >= len(taglineText(selected)) && row < height
}

// nameArt draws name in the font of the splash title, scale times its size.
// A name with a character the font lacks has no rows: it is shown as it is
// rather than as another name.
func nameArt(name string, scale int) []string {
	text := strings.ToLower(name)
	for _, r := range text {
		if _, ok := titleFont[r]; !ok {
			return nil
		}
	}
	return scaledHalfBlocks(text, scale)
}

// fitName draws name in the largest size all of which width by height
// show. It has no rows where even the smallest would be cut off.
func fitName(name string, width, height int) []string {
	for _, scale := range nameScales {
		art := nameArt(name, scale)
		if len(art) > 0 && len(art) <= height && len([]rune(art[0])) <= width {
			return art
		}
	}
	return nil
}

// emptyChat fills the viewport so the name stays centered on resize. A
// terminal too small for the artwork shows the actual name on one line
// instead of clipping it.
func emptyChat(name string, width, height int, selected ...string) string {
	text := ansi.Truncate(name, width, "")
	if art := fitName(name, width, height); art != nil {
		text = strings.Join(art, "\n")
	}
	view := lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, mutedStyle.Render(text))
	if row, ok := emptyTaglineRow(width, height, strings.Count(text, "\n")+1, selected...); ok {
		lines := strings.Split(view, "\n")
		lines[row] = lipgloss.PlaceHorizontal(width, lipgloss.Center, mutedStyle.Render(taglineText(selected)))
		view = strings.Join(lines, "\n")
	}
	return view
}
