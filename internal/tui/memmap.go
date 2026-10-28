package tui

import (
	"math"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"ouroboros/plugin"
)

// The memories are a star chart rather than a list: the mind in the middle,
// its memories as stars around it, on an orbit for each scope — the core
// ones closest, those of the project outermost — the orbit drawn as a faint
// dotted ring. Each star is named beside it where the chart has room for
// the name; the memory selected is always named, and the two lines below the
// chart say what it holds. Enter reads it in full, as the list did.

const (
	chartMinWidth = 56 // the dialog's content has to be this wide for a chart
	chartMinRows  = 10 // and the chart itself this high
	chartFootRows = 3  // the rule below it, and the two lines of the memory selected
)

// chartStyle is how a cell of the chart is drawn.
type chartStyle int

const (
	chartOrbit chartStyle = iota + 1
	chartMote
	chartName
	chartLabel
	chartLabelSel
	chartStarCore
	chartStarGlobal
	chartStarHere
	chartCentre
)

// chartCell is what the chart draws in one cell.
type chartCell struct {
	r     rune
	style chartStyle
	solid bool // what a name may not be drawn over
}

// chart is the grid the star chart is drawn into.
type chart struct {
	cells [][]chartCell
	w, h  int
	light bool
}

func newChart(w, h int, light bool) *chart {
	c := &chart{w: w, h: h, light: light, cells: make([][]chartCell, max(1, h))}
	for y := range c.cells {
		c.cells[y] = make([]chartCell, max(1, w))
	}
	return c
}

// put draws r at x and y, over whatever the orbit had there.
func (c *chart) put(x, y int, r rune, style chartStyle) bool {
	if x < 0 || x >= c.w || y < 0 || y >= c.h {
		return false
	}
	c.cells[y][x] = chartCell{r: r, style: style, solid: true}
	return true
}

// free says whether a name of w cells fits at x and y, over no star, no
// name and no centre: an orbit gives way to a name.
func (c *chart) free(x, y, w int) bool {
	if y < 0 || y >= c.h || x < 0 || x+w > c.w {
		return false
	}
	for i := x; i < x+w; i++ {
		if c.cells[y][i].solid {
			return false
		}
	}
	return true
}

// write draws text at x and y, and says whether all of it fit.
func (c *chart) write(x, y int, text string, style chartStyle) bool {
	for i, r := range []rune(text) {
		if !c.put(x+i, y, r, style) {
			return false
		}
	}
	return true
}

// air clears the cells just before and behind a name, so that no orbit is
// drawn against it: a name stands in the sky, not on a line. What it clears
// stays reserved, so that no star moves against the name either.
func (c *chart) air(x, y, width int) {
	for _, i := range []int{x - 1, x + width} {
		if i >= 0 && i < c.w && y >= 0 && y < c.h && !c.cells[y][i].solid {
			c.cells[y][i] = chartCell{solid: true}
		}
	}
}

// clear says whether the cells before and behind a name at x and y are free
// too, so that a name never stands against a star.
func (c *chart) clear(x, y, width int) bool {
	for _, i := range []int{x - 1, x + width} {
		if i >= 0 && i < c.w && y >= 0 && y < c.h && c.cells[y][i].solid {
			return false
		}
	}
	return true
}

// render draws the chart, a line at a time, each run of one style in one
// escape sequence.
func (c *chart) render() []string {
	out := make([]string, c.h)
	for y := 0; y < c.h; y++ {
		var b strings.Builder
		for x := 0; x < c.w; {
			style := c.cells[y][x].style
			end := x + 1
			for end < c.w && c.cells[y][end].style == style {
				end++
			}
			run := make([]rune, 0, end-x)
			for i := x; i < end; i++ {
				if r := c.cells[y][i].r; r != 0 {
					run = append(run, r)
				} else {
					run = append(run, ' ')
				}
			}
			text := string(run)
			if style == 0 {
				b.WriteString(text)
			} else {
				b.WriteString(c.style(style).Render(text))
			}
			x = end
		}
		out[y] = strings.TrimRight(b.String(), " ")
	}
	return out
}

// style is how a cell of the chart is painted: the orbit nearly gone into
// the background, the names in the colour of the text, and the memory
// selected as the accent chip the rest of the UI marks a selection with.
func (c *chart) style(s chartStyle) lipgloss.Style {
	switch s {
	case chartOrbit:
		return lipgloss.NewStyle().Foreground(fadeColor(c.light))
	case chartMote:
		return lipgloss.NewStyle().Foreground(blend(ui.muted, ui.background, 0.32))
	case chartName:
		return lipgloss.NewStyle().Foreground(ui.muted)
	case chartLabel:
		return lipgloss.NewStyle().Foreground(ui.text)
	case chartLabelSel:
		return lipgloss.NewStyle().Foreground(ui.background).Background(accent).Bold(true)
	case chartStarCore:
		return lipgloss.NewStyle().Foreground(ui.text).Bold(true)
	case chartStarGlobal, chartStarHere:
		return lipgloss.NewStyle().Foreground(accent)
	case chartCentre:
		return lipgloss.NewStyle().Foreground(ui.text).Bold(true)
	}
	return lipgloss.NewStyle()
}

// starGlyph is the star of a scope: a filled one for the core memories,
// which are given with every answer, a four-pointed one for the global
// ones, and a hollow one for those of the project.
func starGlyph(group int) rune {
	switch group {
	case groupCore:
		return '✸'
	case groupGlobal:
		return '✦'
	}
	return '✧'
}

// starStyle is the style of the star of a scope.
func starStyle(group int) lipgloss.Style {
	switch group {
	case groupCore:
		return lipgloss.NewStyle().Foreground(ui.text).Bold(true)
	case groupGlobal:
		return lipgloss.NewStyle().Foreground(accent)
	}
	return lipgloss.NewStyle().Foreground(accent)
}

// ringPoints are n points of the ring of radius rx and ry, evenly spaced by
// the length of its curve, from the angle phase on: the top of the ring is
// the angle zero.
func ringPoints(rx, ry float64, n int, phase float64) [][2]float64 {
	const samples = 2048
	at := func(a float64) [2]float64 {
		return [2]float64{rx * math.Sin(a), -ry * math.Cos(a)}
	}
	length := make([]float64, samples+1)
	prev := at(0)
	for i := 1; i <= samples; i++ {
		p := at(2 * math.Pi * float64(i) / samples)
		length[i] = length[i-1] + math.Hypot(p[0]-prev[0], p[1]-prev[1])
		prev = p
	}
	total := length[samples]
	out := make([][2]float64, n)
	for k := range out {
		want := math.Mod(phase/(2*math.Pi)+float64(k)/float64(n), 1) * total
		lo, hi := 0, samples
		for lo < hi {
			mid := (lo + hi) / 2
			if length[mid] < want {
				lo = mid + 1
			} else {
				hi = mid
			}
		}
		// between the samples lo-1 and lo, at the share of the way of want
		lo = max(1, lo)
		a := 2 * math.Pi * float64(lo-1) / samples
		if step := length[lo] - length[lo-1]; step > 0 {
			a += 2 * math.Pi / samples * (want - length[lo-1]) / step
		}
		out[k] = at(a)
	}
	return out
}

// chartView draws the memories as a star chart, in the box of the dialog,
// and says where its stars and names are clicked.
func (s *memoryScreen) chartView(m *Model, w, h, inner int, head []string, rows []plugin.Memory) (string, []hit) {
	chartRows := max(1, h-2-len(head)-chartFootRows)
	c := newChart(inner, chartRows, m.light)
	cx, cy := (inner-1)/2, (chartRows-1)/2
	rxMax, ryMax := float64(cx)-1, float64(cy)
	radii := [groupCount][2]float64{
		{rxMax * 0.30, ryMax * 0.30},
		{rxMax * 0.63, ryMax * 0.63},
		{rxMax, ryMax},
	}
	phases := [groupCount]float64{0, 0.55, 1.1}

	groups := [groupCount][]int{}
	for i, e := range rows {
		g := groupOf(e)
		groups[g] = append(groups[g], i)
	}
	stars := make([][2]int, len(rows))
	names := [groupCount]string{"core", "global", "here"}
	for g := 0; g < groupCount; g++ {
		idx := groups[g]
		if len(idx) == 0 {
			continue
		}
		// The name of a scope lies on the top of its ring, between two dots,
		// as a station of a line; the stars are placed after it, so that none
		// stands on its letters.
		rx, ry := radii[g][0], radii[g][1]
		orbit(c, float64(cx), float64(cy), rx, ry)
		name := "· " + names[g] + " ·"
		width := lipgloss.Width(name)
		at := [2]int{cx - width/2, int(math.Round(float64(cy) - ry))}
		if c.free(at[0], at[1], width) && c.clear(at[0], at[1], width) {
			c.write(at[0], at[1], name, chartName)
			c.air(at[0], at[1], width)
		}
		dense := ringPoints(rx, ry, max(8*len(idx), len(idx)+8), phases[g])
		for k, i := range idx {
			start := k * len(dense) / len(idx)
			x, y := cx, cy
			for d := 0; d < len(dense); d++ {
				p := dense[(start+d)%len(dense)]
				x, y = int(math.Round(float64(cx)+p[0])), int(math.Round(float64(cy)+p[1]))
				if !c.cells[y][x].solid {
					break
				}
			}
			stars[i] = [2]int{x, y}
			c.put(x, y, starGlyph(g), [...]chartStyle{chartStarCore, chartStarGlobal, chartStarHere}[g])
		}
	}
	c.put(cx, cy, '◉', chartCentre)
	chartDust(c)

	// The memory selected is named first, so that its name has room; the
	// others follow, and are left unnamed where the chart is full.
	order := []int{max(0, min(s.sel, len(rows)-1))}
	for i := range rows {
		if i != order[0] {
			order = append(order, i)
		}
	}
	var hits []hit
	for k, i := range order {
		x, y := stars[i][0], stars[i][1]
		name := ansi.Truncate(rows[i].Key, min(18, max(4, inner/3)), "…")
		width := lipgloss.Width(name)
		style := chartLabel
		if i == order[0] {
			style = chartLabelSel
		}
		hits = append(hits, hit{line: len(head) + y, from: x, to: x + 1, do: s.choose(i)})
		at, ok := chartSpot(c, x, y, width, cx, cy)
		if !ok && k == 0 {
			at, ok = chartSpotForced(c, x, y, width, cx, cy)
		}
		if !ok {
			continue
		}
		c.write(at[0], at[1], name, style)
		c.air(at[0], at[1], width)
		hits = append(hits, hit{line: len(head) + at[1], from: at[0], to: at[0] + width, do: s.choose(i)})
	}
	s.page = max(1, len(rows)/3)

	lines := append(head, c.render()...)
	lines = append(lines, s.chartFoot(m, rows, inner)...)
	return s.box(m, w, h, inner, lines), hits
}

// orbit draws the ring of radius rx and ry around cx and cy: a fine braille
// curve, a dot of an eighth of a cell across, so that the rings lie behind
// the stars as orbits do and not as lines of text do.
func orbit(c *chart, cx, cy, rx, ry float64) {
	const samples = 8192
	bits := make(map[[2]int]uint8)
	for i := 0; i < samples; i++ {
		a := 2 * math.Pi * float64(i) / samples
		// the dots of a cell of braille: two across and four down
		sx := int(math.Round((cx + rx*math.Sin(a)) * 2))
		sy := int(math.Round((cy - ry*math.Cos(a)) * 4))
		cell, dot := [2]int{sx / 2, sy / 4}, [2]int{sx % 2, sy % 4}
		if cell[0] < 0 || cell[0] >= c.w || cell[1] < 0 || cell[1] >= c.h {
			continue
		}
		if dot[0] < 0 || dot[1] < 0 {
			continue
		}
		bits[cell] |= brailleBit(dot[0], dot[1])
	}
	for cell, b := range bits {
		if c.cells[cell[1]][cell[0]].solid {
			continue // a star or a name stands before the orbit
		}
		c.cells[cell[1]][cell[0]] = chartCell{r: rune(0x2800 + int(b)), style: chartOrbit}
	}
}

// brailleBit is the dot of a cell of braille at column x and row y, as the
// rune of braille has them: the left column is 1, 2, 3, 7, the right 4, 5,
// 6, 8.
func brailleBit(x, y int) uint8 {
	if x == 0 {
		return [...]uint8{0x01, 0x02, 0x04, 0x40}[y]
	}
	return [...]uint8{0x08, 0x10, 0x20, 0x80}[y]
}

// chartDust sprinkles the cells no star, ring or name takes with a few
// motes, as a sky has: the same cells every time, so that the chart does not
// move under the keys.
func chartDust(c *chart) {
	for y := 0; y < c.h; y++ {
		for x := 0; x < c.w; x++ {
			if c.cells[y][x].solid || c.cells[y][x].r != 0 {
				continue
			}
			h := uint32(x)*73856093 ^ uint32(y)*19349663 ^ 0x9e3779b9
			h ^= h >> 13
			h *= 0x5bd1e995
			h ^= h >> 15
			if h%157 == 0 {
				c.cells[y][x] = chartCell{r: '·', style: chartMote}
			}
		}
	}
}

// choose is what clicking a star or its name does: it selects the memory,
// and on the one selected already reads it, as enter does.
func (s *memoryScreen) choose(i int) func() tea.Cmd {
	return choosing(i == s.sel, func() { s.sel = i }, func() tea.Cmd { return s.open(i) })
}

// openEdit edits the memory i of those the filter leaves.
func (s *memoryScreen) openEdit(m *Model, i int) tea.Cmd {
	rows := s.rows()
	if len(rows) == 0 {
		return nil
	}
	edit, cmd := editing(rows[max(0, min(i, len(rows)-1))], m.light)
	s.edit = edit
	return cmd
}

// open reads the memory i of those the filter leaves in full.
func (s *memoryScreen) open(i int) tea.Cmd {
	rows := s.rows()
	if len(rows) == 0 {
		return nil
	}
	e := rows[max(0, min(i, len(rows)-1))]
	s.detail, s.detailAt = &e, 0
	return nil
}

// chartSpot is where the name of a star fits best: on the side it lies of
// the centre first, then around it, and finally inside the ring where the
// chart leaves no room outside.
func chartSpot(c *chart, x, y, width, cx, cy int) ([2]int, bool) {
	up, right := y < cy, x >= cx
	spots := [][2]int{
		{x + 2, y}, {x - width - 1, y},
		{x - width/2, y - 1}, {x - width/2, y + 1},
		{x + 2, y - 1}, {x + 2, y + 1},
		{x - width - 1, y - 1}, {x - width - 1, y + 1},
	}
	if !right {
		spots[0], spots[1] = spots[1], spots[0]
	}
	if !up {
		spots[2], spots[3] = spots[3], spots[2]
	}
	for _, at := range spots {
		at[0] = max(0, min(at[0], c.w-width))
		if c.free(at[0], at[1], width) && c.clear(at[0], at[1], width) {
			return at, true
		}
	}
	return [2]int{}, false
}

// chartSpotForced is where the name of the memory selected goes when no
// spot is free: the nearest side outside its star, over what an orbit had.
func chartSpotForced(c *chart, x, y, width, cx, cy int) ([2]int, bool) {
	at := [2]int{x - width/2, y - 1}
	if y >= cy {
		at[1] = y + 1
	}
	at[0] = max(0, min(at[0], c.w-width))
	if at[1] < 0 || at[1] >= c.h {
		return [2]int{}, false
	}
	return at, true
}

// chartFoot are the lines below the chart: a rule, and the memory selected
// with where it holds and how old it is, and what it holds.
func (s *memoryScreen) chartFoot(m *Model, rows []plugin.Memory, inner int) []string {
	e := rows[max(0, min(s.sel, len(rows)-1))]
	g := groupOf(e)
	rule := lipgloss.NewStyle().Foreground(ui.line).Render(strings.Repeat("─", inner))
	line := starStyle(g).Render(string(starGlyph(g))) + " " +
		boldStyle.Render(e.Key) + mutedStyle.Render(" · "+holds(&e))
	if e.Archived {
		line += mutedStyle.Render(" · archived")
	}
	if age := s.age(e, m); age != "" {
		line += mutedStyle.Render(" · " + age)
	}
	content := lipgloss.NewStyle().Foreground(ui.text).Render(short(e.Content, max(1, inner-2)))
	return []string{rule, ansi.Truncate(line, inner, "…"), "  " + content}
}
