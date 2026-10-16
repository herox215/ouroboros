package tui

import "math"

// laserPath visits every ink cell once, letter by letter. The nearest uncut
// cell continues each stroke; disconnected strokes are reached with the beam
// off, rather than burning a line across the gaps of the font.
func laserPath(art []string) [][2]int {
	var path [][2]int
	for _, span := range letterSpans(art) {
		var todo [][2]int
		for y, row := range art {
			for x, r := range []rune(row) {
				if x >= span[0] && x <= span[1] && r != ' ' {
					todo = append(todo, [2]int{x, y})
				}
			}
		}
		at := [2]int{span[0], 0}
		for len(todo) > 0 {
			best, distance := 0, math.MaxInt
			for i, point := range todo {
				dx, dy := point[0]-at[0], point[1]-at[1]
				if d := dx*dx + 4*dy*dy; d < distance {
					best, distance = i, d
				}
			}
			at = todo[best]
			path = append(path, at)
			todo = append(todo[:best], todo[best+1:]...)
		}
	}
	return path
}

const (
	laserStep = 0.0225 // seconds per engraved cell
	laserLife = 0.55   // seconds a spark remains airborne
)

// drawNameLaser reveals only the cells already cut. Fresh cuts cool from
// white through amber into the flowing name gradient. Each letter has its
// own off-screen source. The finished engraving never starts over.
func drawNameLaser(c *canvas, p palette, art []string, left, top int, t, shown float64) {
	path := laserPath(art)
	if len(path) == 0 {
		return
	}
	cutFor := float64(len(path)) * laserStep
	into := max(0, t)
	count := min(len(path), int(into/laserStep)+1)
	// The ray is behind the engraved material, not painted over it.
	if into < cutFor {
		head := path[count-1]
		hx, hy := left+head[0], top+head[1]
		letter := 0
		for n, span := range letterSpans(art) {
			if head[0] >= span[0] && head[0] <= span[1] {
				letter = n
				break
			}
		}
		sx, sy := laserSource(c.w, c.h, letter)
		drawLaserRay(c, p, sx, sy, float64(hx)+0.5, float64(hy)*2+1, shown)
	}
	for i, point := range path[:count] {
		age := into - float64(i)*laserStep
		heat := math.Exp(-age * 5)
		c.alpha = shown * (nameMatte + (1-nameMatte)*heat)
		col := p.gradient(float64(point[0])/float64(len([]rune(art[0])))*0.8 - t*0.36)
		c.block(left+point[0], top+point[1], mix(col, p.spark, heat))
	}
	// Reconstruct recent emissions from their birth times: particles move
	// continuously, also across skipped frames, without a mutable random pool.
	for born := max(0, int((into-laserLife)/laserStep)); born < count; born++ {
		age := into - float64(born)*laserStep
		if age < 0 || age >= laserLife {
			continue
		}
		point := path[born]
		for n := 0; n < 3; n++ {
			h := hash(born, n, 41)
			vx := (float64(h%1000)/999 - 0.5) * 26
			vy := -3 - float64(h>>12%1000)/999*6
			x := left + point[0] + int(math.Round(vx*age))
			y := top + point[1] + int(math.Round(vy*age+14*age*age))
			if x < 0 || x >= c.w || y < 0 || y >= c.h || c.cells[y*c.w+x].r == '█' {
				continue
			}
			c.alpha = shown * (1 - age/laserLife)
			c.set(x, y, sparkArt[min(2, int(age/laserLife*3))], p.spark, false)
		}
	}
	if into < cutFor {
		head := path[count-1]
		c.alpha = shown
		c.block(left+head[0], top+head[1], p.glow)
	}
}

// laserSource rotates by the golden angle between letters. Coordinates use
// half-height cells, so the ray has the same proportions in every direction.
func laserSource(w, h, letter int) (float64, float64) {
	angle := -0.8 + float64(letter)*math.Pi*(3-math.Sqrt(5))
	radius := math.Hypot(float64(w), float64(h)*2) + 8
	return float64(w)/2 + radius*math.Cos(angle), float64(h) + radius*math.Sin(angle)
}

// drawLaserRay rasterizes light, not a chain of line glyphs. Each terminal
// cell carries two hard-edged half pixels in one muted yellow hue,
// with no halo or antialiasing.
func drawLaserRay(c *canvas, _ palette, sx, sy, hx, hy, shown float64) {
	dx, dy := hx-sx, hy-sy
	length := dx*dx + dy*dy
	if length == 0 {
		return
	}
	for y := 0; y < c.h; y++ {
		for x := 0; x < c.w; x++ {
			var shades [2]float64
			for half := 0; half < 2; half++ {
				px, py := float64(x)+0.5, float64(2*y+half)+0.5
				f := clamp(((px-sx)*dx + (py-sy)*dy) / length)
				distance := math.Hypot(px-sx-f*dx, py-sy-f*dy)
				if distance <= 0.6 {
					shades[half] = 1
				}
			}
			if max(shades[0], shades[1]) == 0 {
				continue
			}
			glyph := '█'
			if shades[0] == 0 {
				glyph = '▄'
			} else if shades[1] == 0 {
				glyph = '▀'
			}
			// Leave unused halves transparent to the actual terminal background.
			c.cells[y*c.w+x] = cell{r: glyph, fg: mix(c.back, rgb(0xe6d477), shown*0.65)}
		}
	}
}
