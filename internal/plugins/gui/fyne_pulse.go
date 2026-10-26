//go:build cgo

package gui

import (
	"fmt"
	"math"
	"math/rand/v2"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// The sign that the model thinks is a ring of dots a head runs around, its
// tail fading behind it, as the snake of the name eats its own: below it who
// thinks and for how long, and the last words of the thought, which decode
// from noise as they arrive.
const (
	pulseDots    = 12
	pulseRadius  = 8
	pulseGap     = 8 // between the ring and the label
	pulsePeriod  = 1400 * time.Millisecond
	pulseTail    = 0.7
	pulseHeight  = 64
	pulseWords   = 64
	pulseDecode  = 2
	pulseNoise   = 5
	pulseGlyphs  = "·:+*#/\\<>=_"
	pulseLineMax = 420
)

// thinkingPulse is the animation the transcript shows while the model
// thinks, apart from the messages, until the round goes on.
type thinkingPulse struct {
	widget.BaseWidget
	agent   string
	started time.Time

	dots  []*canvas.Circle
	label *canvas.Text
	line  *canvas.Text

	// target is the end of the thought that shows, decoded as far as shown.
	target []rune
	shown  int

	phase     float32
	animation *fyne.Animation
	stopped   bool
}

func newThinkingPulse(agent string, started time.Time) *thinkingPulse {
	p := &thinkingPulse{agent: agent, started: started}
	p.ExtendBaseWidget(p)
	return p
}

// add takes the next piece of the thought, and lets it decode in.
func (p *thinkingPulse) add(thought, piece string) {
	words := []rune(strings.Join(strings.Fields(thought), " "))
	if len(words) > pulseWords {
		// The end begins with a whole word.
		tail := words[len(words)-pulseWords:]
		if i := strings.IndexRune(string(tail), ' '); i >= 0 && i < len(string(tail))-1 {
			tail = []rune(string(tail)[i+1:])
		}
		words = append([]rune("… "), tail...)
	}
	fresh := len([]rune(strings.Join(strings.Fields(piece), " ")))
	p.target = words
	p.shown = max(0, min(p.shown, len(words)-fresh))
}

// stop ends the animation.
func (p *thinkingPulse) stop() {
	p.stopped = true
	if p.animation != nil {
		p.animation.Stop()
	}
}

func (p *thinkingPulse) CreateRenderer() fyne.WidgetRenderer {
	for range pulseDots {
		p.dots = append(p.dots, canvas.NewCircle(accentColor()))
	}
	p.label = canvas.NewText("", mutedColor())
	p.label.TextSize = theme.Size(sizeSmall)
	p.label.TextStyle = fyne.TextStyle{Monospace: true, Bold: true}
	p.label.Alignment = fyne.TextAlignCenter
	p.line = canvas.NewText("", mutedColor())
	p.line.TextSize = theme.Size(sizeSmall)
	p.line.TextStyle = fyne.TextStyle{Monospace: true, Italic: true}
	p.line.Alignment = fyne.TextAlignCenter
	p.frame(0)
	if !p.stopped {
		p.animation = fyne.NewAnimation(pulsePeriod, p.frame)
		p.animation.Curve = fyne.AnimationLinear
		p.animation.RepeatCount = fyne.AnimationRepeatForever
		p.animation.Start()
	}
	return &pulseRenderer{pulse: p}
}

// frame draws the ring at the point f of a turn of its head, who thinks for
// how long, and the thought decoded one step further.
func (p *thinkingPulse) frame(f float32) {
	p.phase = f
	accent := accentColor()
	for i, dot := range p.dots {
		// How far the dot lies behind the head, as a part of the ring.
		behind := math.Mod(float64(f)-float64(i)/pulseDots+1, 1)
		glow := 0.0
		if behind < pulseTail {
			glow = math.Pow(1-behind/pulseTail, 1.6)
		}
		radius := float32(1.1 + 1.3*glow)
		dot.FillColor = withAlpha(accent, uint8(30+225*glow))
		dot.Resize(fyne.NewSize(radius*2, radius*2))
		dot.Refresh()
	}
	p.layoutDots(p.Size().Width)

	elapsed := time.Since(p.started)
	if p.started.IsZero() || elapsed < 0 {
		elapsed = 0
	}
	label := strings.ToUpper(p.agent+" · "+tr("pulse.thinking", nil)) +
		fmt.Sprintf(" · %d:%02d", int(elapsed.Minutes()), int(elapsed.Seconds())%60)
	if p.label.Text != label {
		p.label.Text = label
		p.label.Refresh()
	}

	if p.shown < len(p.target) {
		p.shown = min(len(p.target), p.shown+pulseDecode)
	}
	line := string(p.target[:p.shown])
	if rest := len(p.target) - p.shown; rest > 0 {
		noise := make([]rune, min(rest, pulseNoise))
		glyphs := []rune(pulseGlyphs)
		for i := range noise {
			noise[i] = glyphs[rand.IntN(len(glyphs))]
		}
		line += string(noise)
	}
	if p.line.Text != line {
		p.line.Text = line
		p.line.Refresh()
	}
}

// layoutDots sets the dots on their ring, centred in width.
func (p *thinkingPulse) layoutDots(width float32) {
	cx, cy := width/2, float32(6+pulseRadius+3)
	for i, dot := range p.dots {
		angle := 2*math.Pi*float64(i)/pulseDots - math.Pi/2
		size := dot.Size()
		x := cx + pulseRadius*float32(math.Cos(angle))
		y := cy + pulseRadius*float32(math.Sin(angle))
		dot.Move(fyne.NewPos(x-size.Width/2, y-size.Height/2))
	}
}

type pulseRenderer struct{ pulse *thinkingPulse }

func (r *pulseRenderer) Layout(size fyne.Size) {
	p := r.pulse
	p.layoutDots(size.Width)
	labelHeight := p.label.MinSize().Height
	p.label.Move(fyne.NewPos(0, 6+2*pulseRadius+pulseGap))
	p.label.Resize(fyne.NewSize(size.Width, labelHeight))
	width := fyne.Min(size.Width, pulseLineMax)
	p.line.Move(fyne.NewPos((size.Width-width)/2, 6+2*pulseRadius+pulseGap+labelHeight+2))
	p.line.Resize(fyne.NewSize(width, p.line.MinSize().Height))
}

func (r *pulseRenderer) MinSize() fyne.Size { return fyne.NewSize(0, pulseHeight) }

func (r *pulseRenderer) Refresh() {
	p := r.pulse
	p.label.Color = mutedColor()
	p.line.Color = mutedColor()
	p.frame(p.phase)
}

func (r *pulseRenderer) Objects() []fyne.CanvasObject {
	p := r.pulse
	objects := make([]fyne.CanvasObject, 0, len(p.dots)+2)
	for _, dot := range p.dots {
		objects = append(objects, dot)
	}
	return append(objects, p.label, p.line)
}

func (r *pulseRenderer) Destroy() { r.pulse.stop() }
