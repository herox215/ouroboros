package tui

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// meshCount counts in rows of a mesh the nerve cells, those a signal
// reaches, and the cells of braille.
func meshCount(rows []string) (nodes, firing, braille int) {
	for _, r := range ansi.Strip(strings.Join(rows, "")) {
		switch {
		case r == '•':
			nodes++
		case r == '●':
			nodes++
			firing++
		case r > 0x2800 && r <= 0x28ff:
			braille++
		}
	}
	return nodes, firing, braille
}

// The mesh grows when the dialog opens, in six ticks, to the size of the
// effort. Moved to another effort, it grows on or dies back in six ticks
// from where it is, the nerve cells it keeps where they were, and those
// that grow anew placed anew. Its first few nerve cells make a tree, those
// after them a mesh.
func TestMeshGrowsAndDiesBack(t *testing.T) {
	m, _ := newModel(t)
	m.ticks = 100
	run(m, "/effort") // at auto, as big as medium
	s := &m.effort
	nodes := func(after int) int {
		n, _, _ := meshCount(s.net.view(s.choices[s.at], m.ticks+after))
		return n
	}
	if got := []int{nodes(0), nodes(1), nodes(3), nodes(5), nodes(6), nodes(99)}; !slices.Equal(got, []int{0, 1, 3, 5, 7, 7}) {
		t.Fatalf("auto grows %v", got)
	}
	if len(s.net.nodes) != 18 || len(s.net.parents) != 18 {
		t.Fatalf("%d nerve cells placed, %d with parents", len(s.net.nodes), len(s.net.parents))
	}

	m.ticks += 99
	kept := slices.Clone(s.net.nodes)
	press(m, "right") // none: 2
	if got := []int{nodes(0), nodes(3), nodes(6), nodes(99)}; !slices.Equal(got, []int{7, 4, 2, 2}) {
		t.Fatalf("none dies back %v", got)
	}
	m.ticks += 3
	press(m, "right", "right", "right", "right") // max, from 4.5 on
	if got := []int{nodes(0), nodes(3), nodes(6)}; !slices.Equal(got, []int{4, 11, 18}) {
		t.Fatalf("max grows %v", got)
	}
	if !slices.Equal(s.net.nodes[:5], kept[:5]) || slices.Equal(s.net.nodes[5:], kept[5:]) {
		t.Errorf("what grew stays, what grows anew is placed anew:\n%v\n%v", kept, s.net.nodes)
	}
	loops := 0
	for i, parents := range s.net.parents {
		if i < treeCells && len(parents) != min(i, 1) {
			t.Fatalf("nerve cell %d of the tree grows from %v", i, parents)
		}
		loops += max(0, len(parents)-1)
	}
	if loops == 0 {
		t.Error("the mesh has no loops")
	}
	for i, a := range s.net.nodes {
		for _, b := range s.net.nodes[:i] {
			if int(a.x/2) == int(b.x/2) && int(a.y/4) == int(b.y/4) {
				t.Fatalf("two nerve cells in one cell of the terminal: %v", s.net.nodes)
			}
		}
	}
	xs := make([]float64, len(s.net.nodes))
	for i, d := range s.net.nodes {
		xs[i] = d.x
	}
	if spread := slices.Max(xs) - slices.Min(xs); spread < float64(s.width()) {
		t.Errorf("the nerve cells keep to %v of %d cells", spread/2, s.width())
	}
	if rows := s.net.view("max", m.ticks); len(rows) != meshRows || ansi.StringWidth(rows[0]) != s.width() {
		t.Errorf("the mesh is %d rows of %d cells, the slider %d", len(rows), ansi.StringWidth(rows[0]), s.width())
	}
}

// A connection grows before the nerve cell it leads to, its tip in the
// color of the signals. Once grown, signals run along it, there and back,
// and light up each end they reach.
func TestMeshConnectionsGrowAndFire(t *testing.T) {
	n := neuralNet{w: 20, nodes: []dot{{0.5, 1.5}, {38.5, 1.5}}, parents: [][]int{nil, {0}}, from: 1, to: 2}
	half := n.view("max", 3) // grown 1.5
	nodes, _, braille := meshCount(half)
	if nodes != 1 || braille < 6 || braille > 15 || !strings.Contains(half[0], "\x1b[31m") {
		t.Fatalf("half a connection: %d nerve cells, %d cells of it:\n%q", nodes, braille, half)
	}
	if nodes, _, braille = meshCount(n.view("none", 6)); nodes != 2 || braille != 18 {
		t.Fatalf("grown: %d nerve cells, %d cells of the connection", nodes, braille)
	}
	reached := map[int]bool{}
	for tick := range 200 {
		row := []rune(ansi.Strip(n.view("max", tick)[0]))
		reached[0] = reached[0] || row[0] == '●'
		reached[19] = reached[19] || row[19] == '●'
	}
	if !reached[0] || !reached[19] {
		t.Errorf("the signals reach %v", reached)
	}
	for tick := range 200 {
		if _, firing, _ := meshCount(n.view("none", tick)); firing != 0 {
			t.Fatalf("none fires at %d", tick)
		}
	}
}

// none is a mesh that nothing runs through. max fires much more than
// minimal does. auto fires as much as the model thinks, more and less in
// turn: its signals take the accent at times, and are yellow at others.
func TestMeshThinksWithTheEffort(t *testing.T) {
	fired := map[string]int{}
	for _, effort := range []string{"none", "minimal", "max"} {
		n := neuralNet{w: 44}
		n.growTo(effort, 0)
		still := strings.Join(n.view(effort, 100), "")
		for tick := 100; tick < 400; tick++ {
			rows := n.view(effort, tick)
			_, firing, _ := meshCount(rows)
			fired[effort] += firing
			if effort == "none" && strings.Join(rows, "") != still {
				t.Fatalf("none moves at %d", tick)
			}
		}
	}
	if fired["none"] != 0 || fired["max"] <= 2*fired["minimal"] {
		t.Errorf("fired %v", fired)
	}

	colors := map[string]bool{}
	for tick := range 200 {
		colors[fmt.Sprint(signalColor(fire("", float64(tick))))] = true
	}
	if len(colors) != 2 || signalColor(fire("xhigh", 0)) != bad || signalColor(fire("high", 0)) != warn {
		t.Errorf("auto fires in %d colors", len(colors))
	}
}

// A mesh too narrow for all its nerve cells has as many as fit, one to a
// cell of the terminal.
func TestNarrowMesh(t *testing.T) {
	n := neuralNet{w: 2}
	n.growTo("max", 0)
	if nodes, _, _ := meshCount(n.view("max", 99)); nodes != 2*meshRows || len(n.nodes) != 2*meshRows {
		t.Fatalf("%d nerve cells in:\n%s", nodes, ansi.Strip(strings.Join(n.view("max", 99), "\n")))
	}
}

// The slider and the mesh above it are in the middle of the dialog, as is
// what the effort does; a line too wide for the dialog stays at its left.
func TestEffortSliderIsCentered(t *testing.T) {
	m, _ := newModel(t) // 100 wide: a dialog of 80, 76 inside it
	run(m, "/effort")
	view := ansi.Strip(m.View().Content)
	for _, want := range []string{
		"│ " + strings.Repeat(" ", 16) + "auto    none    low    medium    high    max" + strings.Repeat(" ", 16) + " │",
		"│ " + strings.Repeat(" ", 16) + "━━●───",
		"│ " + strings.Repeat(" ", 15) + efforts["auto"] + strings.Repeat(" ", 16) + " │",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("not in the middle: %q\n%s", want, view)
		}
	}

	if got := m.centered("ab", "abcd"); got[0] != strings.Repeat(" ", 36)+"ab" || got[1] != strings.Repeat(" ", 36)+"abcd" {
		t.Errorf("centered as a block: %q", got)
	}
	m.Update(tea.WindowSizeMsg{Width: 30, Height: 30})
	if got := m.centered(strings.Repeat("x", 26)); got[0] != strings.Repeat("x", 26) {
		t.Errorf("too wide: %q", got)
	}
}

// A net of five nerve cells that is grown, as it is drawn: its connections,
// the signals that run along them, there and back, and the cells they reach,
// for an effort that fires little, one that fires all the time, and auto,
// which fires more and less in turn.
func TestGoldenMesh(t *testing.T) {
	n := neuralNet{w: 20, from: 5, to: 5,
		nodes:   []dot{{0.5, 1.5}, {12.5, 9.5}, {24.5, 1.5}, {30.5, 9.5}, {38.5, 5.5}},
		parents: [][]int{nil, {0}, {1}, {0, 2}, {3}}}
	var b strings.Builder
	for _, effort := range []string{"", "low", "max"} {
		for _, tick := range []int{7, 20, 41, 66} {
			fmt.Fprintf(&b, "--- %q at %d\n%s\n", effort, tick, strings.Join(n.view(effort, tick), "\n"))
		}
	}
	// On its way from three nerve cells to five, both connections of the
	// fourth grow at once.
	n.from, n.to = 3, 5
	for _, tick := range []int{2, 4} {
		fmt.Fprintf(&b, "--- growing at %d\n%s\n", tick, strings.Join(n.view("max", tick), "\n"))
	}
	golden(t, "mesh", b.String())
}

// Where the nerve cells are placed is left to chance, but not how many: a
// cell that is taken is tried again until all of them have theirs. From the
// first that is no part of the tree on, one grows from two others now and
// then.
func TestMeshPlacesAllItsCells(t *testing.T) {
	loops := 0
	for range 50 {
		narrow := neuralNet{w: 1}
		narrow.growTo("max", 0)
		if len(narrow.nodes) != meshRows {
			t.Fatalf("%d nerve cells in %d cells", len(narrow.nodes), meshRows)
		}
		wide := neuralNet{w: 44}
		wide.growTo("max", 0)
		for i, parents := range wide.parents[1:treeCells] {
			if len(parents) != 1 {
				t.Fatalf("the nerve cell %d of the tree grows from %d others", i+1, len(parents))
			}
		}
		if len(wide.parents[treeCells]) == 2 {
			loops++
		}
	}
	if loops == 0 || loops == 50 {
		t.Fatalf("the first nerve cell after the tree grew from two others %d times of 50", loops)
	}
}
