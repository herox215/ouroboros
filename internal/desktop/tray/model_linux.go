//go:build linux

package tray

import (
	"math"

	"github.com/godbus/dbus/v5/prop"
	"ouroboros/internal/desktop"
)

// D-Bus structs must retain this field order. Pixel bytes are network-order
// ARGB32, not native-endian image.RGBA storage.
type pixmap struct {
	Width, Height int32
	Data          []byte
}
type tooltip struct {
	IconName           string
	Pixmap             []pixmap
	Title, Description string
}
type view struct{ status, tooltip string }

func model(s desktop.Status) view {
	status := "Active"
	if s.State == "waiting" || s.State == "error" {
		status = "NeedsAttention"
	}
	// Offline stays Active so the user can still open/start Ouroboros.
	return view{status: status, tooltip: s.Label()}
}

func properties(v view) prop.Map {
	values := map[string]any{
		"Category": "ApplicationStatus", "Id": "ouroboros", "Title": "Ouroboros", "Status": v.status,
		"WindowId": uint32(0), "IconName": "", "IconPixmap": []pixmap{ring(22), ring(32)},
		"OverlayIconName": "", "OverlayIconPixmap": []pixmap{},
		"AttentionIconName": "", "AttentionIconPixmap": []pixmap{ring(22), ring(32)}, "AttentionMovieName": "",
		"ToolTip":    tooltip{Pixmap: []pixmap{}, Title: v.tooltip, Description: v.tooltip},
		"ItemIsMenu": true, "Menu": menuPath,
	}
	result := make(map[string]*prop.Prop, len(values))
	for key, value := range values {
		result[key] = &prop.Prop{Value: value, Emit: prop.EmitConst}
	}
	result["Status"].Emit = prop.EmitTrue
	result["ToolTip"].Emit = prop.EmitTrue
	return prop.Map{itemInterface: result}
}

// ring draws a small antialiased Ouro ring with a tapered tail and a head/eye.
// Supersampling keeps it legible at both common tray sizes without assets.
func ring(size int) pixmap {
	p := pixmap{Width: int32(size), Height: int32(size), Data: make([]byte, size*size*4)}
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			coverage, eye := 0, 0
			for sy := 0; sy < 4; sy++ {
				for sx := 0; sx < 4; sx++ {
					px := (float64(x)+(float64(sx)+0.5)/4)/float64(size)*2 - 1
					py := (float64(y)+(float64(sy)+0.5)/4)/float64(size)*2 - 1
					radius, angle := math.Hypot(px, py), math.Atan2(py, px)
					body := math.Abs(radius-0.64) < 0.13 && !(angle > -0.45 && angle < 0.12)
					head := math.Hypot(px-0.61, py+0.22) < 0.20
					if body || head {
						coverage++
						if math.Hypot(px-0.66, py+0.27) < 0.045 {
							eye++
						}
					}
				}
			}
			at := 4 * (y*size + x)
			p.Data[at] = byte(coverage * 255 / 16)
			if coverage > 0 {
				p.Data[at+1] = byte(80 - eye*55/coverage)
				p.Data[at+2] = byte(205 - eye*170/coverage)
				p.Data[at+3] = byte(170 - eye*135/coverage)
			}
		}
	}
	return p
}
