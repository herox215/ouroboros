package tui

import (
	"cmp"
	"image/color"
	"math"
	"math/rand/v2"
	"slices"
	"strings"

	"charm.land/lipgloss/v2"

	"ouroboros/plugin"
)

// The mesh above the slider of the effort is a net of nerve cells that
// thinks as hard as the effort the slider is at. It grows when the dialog
// opens, cell after cell, each with its connections to the nearest of those
// before it, which grow first: a few cells make a simple tree, more of them
// a mesh. Moved to another effort, it grows on, or dies back, to the size of
// that one, in half a second, and the cells that grow anew are placed anew,
// at random. Signals run along the connections from cell to cell, and
// a cell a signal reaches lights up. none is a small net that nothing runs
// through. Each effort more makes the net bigger, and sends more signals,
// faster, and hotter in color: the accent, yellow, red. auto is as big as
// medium and thinks more and less in turn, as the model thinks as much as it
// does by itself.

const (
	meshRows  = 3 // how high the mesh is
	growTicks = 6 // how long it takes to grow, or to die back, to another size
	treeCells = 4 // how many nerve cells it has before it has loops
)

// meshCells is how many nerve cells the mesh of an effort has: auto as many
// as medium.
var meshCells = map[string]int{"": 7, "none": 2, "minimal": 3, "low": 4, "medium": 7, "high": 10, "xhigh": 13, "max": 18}

// meshSize is how big the mesh of effort is, from 0 for none to 1 for max:
// auto is as big as medium.
func meshSize(effort string) float64 {
	if effort == "" {
		return 0.5
	}
	return float64(slices.Index(plugin.AllEfforts, effort)-1) / float64(len(plugin.AllEfforts)-2)
}

// fire is how much of the mesh of effort fires at the tick t, from 0 for
// none to 1 for max.
func fire(effort string, t float64) float64 {
	if effort == "" {
		return 0.45 + 0.3*math.Sin(t/15)
	}
	return meshSize(effort)
}

// signalColor is the color of the signals, and of the cells they reach, at
// fire.
func signalColor(fire float64) color.Color {
	if fire < 2.0/3 {
		return accent
	}
	if fire < 5.0/6 {
		return warn
	}
	return bad
}

// dot is a point of the mesh, in the dots of braille: two across a cell of
// the terminal, four down.
type dot struct{ x, y float64 }

// distance is how far two dots are apart.
func distance(a, b dot) float64 { return math.Hypot(b.x-a.x, b.y-a.y) }

// along is the dot the part p of the way from a to b.
func along(a, b dot, p float64) dot { return dot{a.x + (b.x-a.x)*p, a.y + (b.y-a.y)*p} }

// neuralNet is the mesh: where its nerve cells are, and how far it grew.
type neuralNet struct {
	w       int     // the cells of the terminal it is drawn over, across
	nodes   []dot   // the nerve cells, in the order they grow
	parents [][]int // of each nerve cell, those before it it grows from
	from    float64 // how many nerve cells had grown at the tick since
	to      float64 // how many are to grow
	since   int
}

// grown is how many of its nerve cells the net has grown at the tick t,
// on its way from what it had to what it is to have.
func (n *neuralNet) grown(t int) float64 {
	return n.from + (n.to-n.from)*min(1, float64(t-n.since)/growTicks)
}

// growTo has the net grow, or die back, from the tick t on, to the size of
// effort.
func (n *neuralNet) growTo(effort string, t int) {
	n.from, n.since = n.grown(t), t
	n.to = float64(min(meshCells[effort], n.room()))
	n.place(int(math.Ceil(n.from)))
}

// room is how many nerve cells fit: one to a cell of the terminal.
func (n *neuralNet) room() int { return min(meshCells["max"], n.w*meshRows) }

// place places the nerve cells anew from the first one that did not begin
// to grow yet, each in the middle of a cell of the terminal. The cell is
// the best of a few taken at random that are free: the one farthest from
// the nerve cells before it, so that they spread. It grows from the one of
// those nearest to it, and once there are a few, now and then from the
// second nearest too.
func (n *neuralNet) place(from int) {
	n.nodes, n.parents = n.nodes[:from], n.parents[:from]
	for len(n.nodes) < n.room() {
		best, far := dot{}, 0.0
		for range 3 {
			at := dot{float64(rand.IntN(n.w))*2 + 0.5, float64(rand.IntN(meshRows))*4 + 1.5}
			near := math.Inf(1)
			for _, d := range n.nodes {
				near = min(near, distance(at, d))
			}
			if near > far {
				best, far = at, near
			}
		}
		if far == 0 {
			continue // all of them taken: other ones
		}
		before := make([]int, len(n.nodes))
		for i := range before {
			before[i] = i
		}
		slices.SortStableFunc(before, func(i, j int) int {
			return cmp.Compare(distance(best, n.nodes[i]), distance(best, n.nodes[j]))
		})
		parents := 1
		if len(n.nodes) >= treeCells {
			parents += rand.IntN(2)
		}
		n.nodes = append(n.nodes, best)
		n.parents = append(n.parents, before[:min(len(before), parents)])
	}
}

// brailleBits are the bits of the dots of braille in a cell, by their
// column and row.
var brailleBits = [2][4]rune{{0x01, 0x02, 0x04, 0x40}, {0x08, 0x10, 0x20, 0x80}}

// What a cell of the mesh shows, the one of them that is drawn over the
// others.
const (
	meshEmpty  = iota
	meshLink   // a connection between two nerve cells
	meshSignal // a signal on its way along one, or the tip of one that grows
	meshNode   // a nerve cell
	meshFiring // a nerve cell a signal reaches
)

// meshCell is a cell of the terminal the mesh is drawn in.
type meshCell struct {
	dots rune // of braille
	kind int
}

// view draws the net as it thinks for effort at the tick t, a line for each
// of its rows from the top.
func (n *neuralNet) view(effort string, t int) []string {
	grown, f := n.grown(t), fire(effort, float64(t))
	cells := make([][]meshCell, meshRows)
	for r := range cells {
		cells[r] = make([]meshCell, n.w)
	}
	draw := func(d dot, kind int) {
		c := &cells[int(d.y/4)][int(d.x/2)]
		c.dots |= brailleBits[int(d.x)%2][int(d.y)%4]
		c.kind = max(c.kind, kind)
	}

	// The connections of a nerve cell grow before it does. Along each that
	// grew runs a signal now and then, there and back, the more often the
	// more the net fires; the cell it reaches lights up.
	speed := 0.4 + 1.2*meshSize(effort) // dots a tick
	firing := map[int]bool{}
	for i, parents := range n.parents {
		part := min(1, grown-float64(i)) // of the way the connections of i grew
		if part <= 0 {
			break
		}
		for k, p := range parents {
			from, to, target := n.nodes[p], n.nodes[i], i
			long := distance(from, to)
			for s := range int(long * 2 * part) {
				draw(along(from, to, float64(s)/2/long), meshLink)
			}
			if part < 1 {
				draw(along(from, to, part), meshSignal)
				continue
			}
			seed := float64(i*2 + k)
			run := float64(t)*speed/long + noise(seed)
			if noise(seed*7.3+math.Floor(run)) >= f {
				continue
			}
			if int(run)%2 == 1 {
				from, to, target = to, from, p
			}
			at := along(from, to, frac(run))
			for _, d := range []dot{at, {at.x + 1, at.y}, {at.x, at.y + 1}, {at.x + 1, at.y + 1}} {
				draw(d, meshSignal)
			}
			if (1-frac(run))*long < 3 {
				firing[target] = true // it is a few dots from there
			}
		}
	}
	for i, d := range n.nodes[:int(grown)] {
		c := &cells[int(d.y/4)][int(d.x/2)]
		c.kind = meshNode
		if firing[i] {
			c.kind = meshFiring
		}
	}

	quiet := lipgloss.NewStyle().Foreground(muted)
	hot := lipgloss.NewStyle().Foreground(signalColor(f))
	styles := map[int]lipgloss.Style{meshEmpty: lipgloss.NewStyle(), meshLink: quiet, meshSignal: hot, meshNode: quiet, meshFiring: hot.Bold(true)}
	glyphs := map[int]rune{meshNode: '•', meshFiring: '●'}
	rows := make([]string, meshRows)
	for r, row := range cells {
		var b strings.Builder
		for x := 0; x < len(row); {
			kind, run := row[x].kind, ""
			for ; x < len(row) && row[x].kind == kind; x++ {
				glyph, ok := glyphs[kind]
				if !ok {
					glyph = ' '
					if row[x].dots != 0 {
						glyph = 0x2800 + row[x].dots
					}
				}
				run += string(glyph)
			}
			b.WriteString(styles[kind].Render(run))
		}
		rows[r] = b.String()
	}
	return rows
}
