package tui

import (
	"cmp"
	"math"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
)

const (
	headerMorphDuration = 520 * time.Millisecond
	headerMorphFrame    = 33 * time.Millisecond
)

// headerMorph belongs to the row shared by the profiles, not to a profile:
// switching again continues from the name on screen, even midway through.
type headerMorph struct {
	active     bool
	generation uint64 // a frame of an earlier switch must not move a new one
	from, to   string
	start      time.Time
	elapsed    time.Duration
}

type headerMorphMsg struct {
	generation uint64
	at         time.Time
}

func headerMorphTick(generation uint64) tea.Cmd {
	return tea.Tick(headerMorphFrame, func(at time.Time) tea.Msg {
		return headerMorphMsg{generation: generation, at: at}
	})
}

func (a *headerMorph) begin(from, to string, at time.Time) tea.Cmd {
	a.generation++
	a.active = from != to
	a.from, a.to = from, to
	a.start, a.elapsed = at, 0
	if !a.active {
		return nil
	}
	return headerMorphTick(a.generation)
}

func (a *headerMorph) update(msg headerMorphMsg) tea.Cmd {
	if !a.active || msg.generation != a.generation {
		return nil
	}
	a.elapsed = max(a.elapsed, msg.at.Sub(a.start))
	if a.elapsed >= headerMorphDuration {
		a.active = false
		return nil
	}
	return headerMorphTick(a.generation)
}

func (a *headerMorph) name(name string) string {
	if !a.active || name != a.to {
		return name
	}
	return morphName(a.from, a.to, a.elapsed)
}

// headerName moves only the name in the shared header. The terminal title,
// the profile counter and everything in the chat name the new profile at once.
func (m *Model) headerName() string {
	name := cmp.Or(m.status.Name, "ouroboros")
	if m.among != nil && m.shown() {
		return m.among.morph.name(name)
	}
	return name
}

var headerMorphMarks = [...]string{"·", "⠂", "⠒", "⠲", "⠶", "⠿", "⠶", "⠒"}

// morphName dissolves the old letters into dots and assembles the new ones,
// in a wave from left to right. The width eases toward that of the new name:
// the rest of the header slides, rather than jumping or flickering in size.
func morphName(from, to string, elapsed time.Duration) string {
	if elapsed <= 0 || from == to {
		return from
	}
	if elapsed >= headerMorphDuration {
		return to
	}
	before, after := nameGraphemes(from), nameGraphemes(to)
	n := max(len(before), len(after))
	progress := float64(elapsed) / float64(headerMorphDuration)
	frame := int(elapsed / headerMorphFrame)
	var text strings.Builder
	for i := range n {
		var old, next string
		if i < len(before) {
			old = before[i]
		}
		if i < len(after) {
			next = after[i]
		}
		phase := (progress - 0.3*float64(i)/float64(max(1, n-1))) / 0.7
		switch {
		case old == next || phase <= 0.15:
			text.WriteString(old)
		case phase >= 0.85:
			text.WriteString(next)
		default:
			mark := headerMorphMarks[(frame+i*3)%len(headerMorphMarks)]
			text.WriteString(strings.Repeat(mark, max(ansi.StringWidth(old), ansi.StringWidth(next))))
		}
	}
	ease := progress * progress * (3 - 2*progress)
	width := int(math.Round(float64(ansi.StringWidth(from)) + float64(ansi.StringWidth(to)-ansi.StringWidth(from))*ease))
	name := ansi.Truncate(text.String(), width, "")
	return name + strings.Repeat(" ", max(0, width-ansi.StringWidth(name)))
}

// A letter may span several runes or terminal columns: accents, CJK and
// emoji must stay whole while they dissolve and when the row is truncated.
func nameGraphemes(name string) []string {
	var letters []string
	g := uniseg.NewGraphemes(name)
	for g.Next() {
		letters = append(letters, g.Str())
	}
	return letters
}
