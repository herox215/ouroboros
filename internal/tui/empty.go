package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// nameScale is the size an empty chat draws the name of the agent in: the
// size of the title of the splash, at which a pixel of the font is a block of
// two cells.
const nameScale = 1

const emptyTagline = "keep on dreaming"

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
	return blockArt(text, scale)
}

// fitName draws name where the size of the empty chat shows all of it. It has
// no rows where the artwork would be cut off.
func fitName(name string, width, height int) []string {
	art := nameArt(name, nameScale)
	if len(art) > 0 && len(art) <= height && len([]rune(art[0])) <= width {
		return art
	}
	return nil
}

// blockStyle draws the blocks of the artwork of an empty chat in the muted
// color of its text.
var blockStyle = lipgloss.NewStyle().Foreground(muted).Background(muted)

// artText draws the rows of the artwork of the name as the chat shows them:
// a block is a cell the terminal fills in the color of the artwork.
func artText(rows []string) string {
	var b strings.Builder
	for i, row := range rows {
		if i > 0 {
			b.WriteByte('\n')
		}
		runes := []rune(row)
		for x := 0; x < len(runes); {
			end := x
			for end < len(runes) && runes[end] == runes[x] {
				end++
			}
			run := string(runes[x:end])
			if runes[x] == '█' {
				run = blockStyle.Render(run)
			}
			b.WriteString(run)
			x = end
		}
	}
	return b.String()
}

// emptyChat fills the viewport so the name stays centered on resize. A
// terminal too small for the artwork shows the actual name on one line
// instead of clipping it.
func emptyChat(name string, width, height int, selected ...string) string {
	text := mutedStyle.Render(ansi.Truncate(name, width, ""))
	if art := fitName(name, width, height); art != nil {
		text = artText(art)
	}
	view := lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, text)
	if row, ok := emptyTaglineRow(width, height, strings.Count(text, "\n")+1, selected...); ok {
		lines := strings.Split(view, "\n")
		lines[row] = lipgloss.PlaceHorizontal(width, lipgloss.Center, mutedStyle.Render(taglineText(selected)))
		view = strings.Join(lines, "\n")
	}
	return view
}
