package tui

import "math"

// drawNameRain reconstructs a shower from the clock, not the preceding frame.
// Each lane has its own speed and phase; short, dim tails give the drops depth.
// The name and tagline are painted afterwards, in front of the rain.
func drawNameRain(c *canvas, t, shown float64) {
	if c.w <= 0 || c.h <= 0 {
		return
	}
	ink := rgb(0x739eb8)
	for lane := 0; lane < (c.w+2)/3; lane++ {
		h := hash(lane, 0, 73)
		x := lane*3 + int(h%3)
		speed := 7 + float64(h>>8%700)/100
		period := float64(c.h + 6)
		into := math.Mod(max(0, t)*speed+float64(h>>16%1000)/1000*period, period)
		y := int(into) - 3
		for tail := 2; tail >= 0; tail-- {
			c.alpha = shown * (0.48 - float64(tail)*0.14)
			r := '│'
			if tail > 0 {
				r = '╎'
			}
			c.set(x, y-tail, r, ink, false)
		}
		// Once the head reaches the floor, a small spray opens sideways.
		if age := into - float64(c.h+2); age >= 0 && age < 2 {
			c.alpha = shown * 0.4 * (1 - age/2)
			reach := 1 + int(age)
			c.set(x-reach, c.h-1, '·', ink, false)
			c.set(x+reach, c.h-1, '·', ink, false)
		}
	}
}
