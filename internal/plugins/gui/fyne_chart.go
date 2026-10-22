//go:build cgo

package gui

import (
	"image/color"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
)

// The geometry of the chart, as frontend/app.js writes it.
const (
	chartWidth  = float32(240)
	chartLabels = float32(64)
	chartCounts = float32(16)
	chartRow    = float32(12)
	chartPad    = float32(2)
	chartLanes  = 6
)

type chartLane struct {
	name  string
	count int
}

type chartTick struct {
	lane int
	at   float64
}

// toolChart is the picture of the tools of a session: one lane per tool, its
// calls where their time puts them, so that its mix and its rhythm read under
// the state.
type toolChart struct {
	lanes   []chartLane
	ticks   []chartTick
	running bool
}

func (c toolChart) height() float32 { return float32(len(c.lanes))*chartRow + 2*chartPad }

// chartView is the chart as a widget: the lanes, their names and counts, the
// ticks of the calls and the line where now is.
func chartView(chart toolChart) fyne.CanvasObject {
	objects := make([]fyne.CanvasObject, 0, len(chart.lanes)*3+len(chart.ticks)+8)
	names := make([]*canvas.Text, len(chart.lanes))
	counts := make([]*canvas.Text, len(chart.lanes))
	for i, lane := range chart.lanes {
		names[i] = chartText(chartLaneName(lane.name), 0, 0, accentColor())
		counts[i] = chartText(itoa(lane.count), 0, 0, accentColor())
		objects = append(objects, names[i], counts[i])
	}
	lines := make([]*canvas.Line, len(chart.lanes))
	for i := range chart.lanes {
		lines[i] = chartLine(withAlpha(accentColor(), 0x38))
		objects = append(objects, lines[i])
	}
	rects := make([]*canvas.Rectangle, len(chart.ticks))
	for i, tick := range chart.ticks {
		shade := chart.lanes[tick.lane]
		fill := withAlpha(accentColor(), chartShade(lanesIndex(chart.lanes, shade.name)))
		rects[i] = canvas.NewRectangle(fill)
		objects = append(objects, rects[i])
	}
	var now []*canvas.Line
	if chart.running {
		for i := float32(chartPad); i < chart.height()-chartPad; i += 5 {
			line := chartLine(withAlpha(accentColor(), 0x80))
			now = append(now, line)
			objects = append(objects, line)
		}
	}
	view := container.New(&chartLayout{
		chart: chart, names: names, counts: counts, lanes: lines, ticks: rects, now: now,
	}, objects...)
	return view
}

func lanesIndex(lanes []chartLane, name string) int {
	for i, lane := range lanes {
		if lane.name == name {
			return i
		}
	}
	return 0
}

// chartShade is the strength of a lane: strongest for the most used, as the
// stylesheet shades them.
func chartShade(index int) uint8 {
	switch index {
	case 0:
		return 0xff
	case 1:
		return 0xbd
	case 2:
		return 0x8c
	case 3:
		return 0x66
	case 4:
		return 0x47
	case 5:
		return 0x2e
	}
	return 0x2e
}

// chartLaneName shortens a tool name to what the lane has room for.
func chartLaneName(name string) string {
	runes := []rune(name)
	if len(runes) > 12 {
		return string(runes[:11]) + "…"
	}
	return name
}

func chartText(value string, x, y float32, fill color.Color) *canvas.Text {
	t := canvas.NewText(value, fill)
	t.TextSize = 8
	t.TextStyle = fyne.TextStyle{Monospace: true}
	t.Move(fyne.NewPos(x, y))
	return t
}

func chartLine(fill color.Color) *canvas.Line {
	line := canvas.NewLine(fill)
	line.StrokeWidth = 1
	return line
}

// chartLayout places the chart of a session in the width of the rail: the
// view box is 240 pixels wide, and the chart scales with the room it gets.
type chartLayout struct {
	chart  toolChart
	names  []*canvas.Text
	counts []*canvas.Text
	lanes  []*canvas.Line
	ticks  []*canvas.Rectangle
	now    []*canvas.Line
}

func (l *chartLayout) MinSize([]fyne.CanvasObject) fyne.Size {
	return fyne.NewSize(0, l.chart.height())
}

func (l *chartLayout) Layout(objects []fyne.CanvasObject, size fyne.Size) {
	scale := fyne.Min(1, size.Width/chartWidth)
	plot := chartWidth - chartCounts - chartLabels
	centre := func(index int) float32 { return chartPad + float32(index)*chartRow + chartRow/2 }
	for i := range l.chart.lanes {
		name := l.names[i]
		name.TextSize = 8 * scale
		name.Move(fyne.NewPos(0, (centre(i)-3.4)*scale))
		count := l.counts[i]
		count.TextSize = 8 * scale
		count.Move(fyne.NewPos(size.Width-count.MinSize().Width, (centre(i)-3.4)*scale))
		line := l.lanes[i]
		y := centre(i) * scale
		line.Position1 = fyne.NewPos(chartLabels*scale, y)
		line.Position2 = fyne.NewPos((chartLabels+plot)*scale, y)
	}
	for i, tick := range l.chart.ticks {
		rect := l.ticks[i]
		x := (chartLabels + float32(tick.at)*plot - 1.2) * scale
		rect.Move(fyne.NewPos(x, (centre(tick.lane)-3.5)*scale))
		rect.Resize(fyne.NewSize(2.4*scale, 7*scale))
	}
	x := (chartLabels + plot - 1.2) * scale
	for i, line := range l.now {
		y := (chartPad + float32(i)*5) * scale
		line.Position1 = fyne.NewPos(x, y)
		line.Position2 = fyne.NewPos(x, y+3*scale)
	}
	_ = objects
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits [20]byte
	i := len(digits)
	for n > 0 {
		i--
		digits[i] = byte('0' + n%10)
		n /= 10
	}
	return string(digits[i:])
}
