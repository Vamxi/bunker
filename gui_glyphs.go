// gui_glyphs.go - procedurally drawn box, block, and braille glyphs.
//
// Fonts rarely make line-drawing characters meet exactly at cell edges, and
// fallback fonts give them foreign widths. Drawing them as rectangles keeps
// TUI borders (btop, Claude Code, vim splits) seamless at any font size.
package main

import (
	"math"

	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/gdamore/tcell/v2"
)

// Arm weights: 0 none, 1 light, 2 heavy. Packed as up | right<<2 | down<<4 | left<<6.
var boxArms = func() (t [0x80]uint8) {
	spec := []string{
		0x00: "L1R1", 0x01: "L2R2", 0x02: "U1D1", 0x03: "U2D2",
		0x0C: "D1R1", 0x0D: "D1R2", 0x0E: "D2R1", 0x0F: "D2R2",
		0x10: "D1L1", 0x11: "D1L2", 0x12: "D2L1", 0x13: "D2L2",
		0x14: "U1R1", 0x15: "U1R2", 0x16: "U2R1", 0x17: "U2R2",
		0x18: "U1L1", 0x19: "U1L2", 0x1A: "U2L1", 0x1B: "U2L2",
		0x1C: "U1D1R1", 0x1D: "U1D1R2", 0x1E: "U2D1R1", 0x1F: "U1D2R1",
		0x20: "U2D2R1", 0x21: "U2D1R2", 0x22: "U1D2R2", 0x23: "U2D2R2",
		0x24: "U1D1L1", 0x25: "U1D1L2", 0x26: "U2D1L1", 0x27: "U1D2L1",
		0x28: "U2D2L1", 0x29: "U2D1L2", 0x2A: "U1D2L2", 0x2B: "U2D2L2",
		0x2C: "D1L1R1", 0x2D: "D1L2R1", 0x2E: "D1L1R2", 0x2F: "D1L2R2",
		0x30: "D2L1R1", 0x31: "D2L2R1", 0x32: "D2L1R2", 0x33: "D2L2R2",
		0x34: "U1L1R1", 0x35: "U1L2R1", 0x36: "U1L1R2", 0x37: "U1L2R2",
		0x38: "U2L1R1", 0x39: "U2L2R1", 0x3A: "U2L1R2", 0x3B: "U2L2R2",
		0x3C: "U1D1L1R1", 0x3D: "U1D1L2R1", 0x3E: "U1D1L1R2", 0x3F: "U1D1L2R2",
		0x40: "U2D1L1R1", 0x41: "U1D2L1R1", 0x42: "U2D2L1R1", 0x43: "U2D1L2R1",
		0x44: "U2D1L1R2", 0x45: "U1D2L2R1", 0x46: "U1D2L1R2", 0x47: "U2D1L2R2",
		0x48: "U1D2L2R2", 0x49: "U2D2L2R1", 0x4A: "U2D2L1R2", 0x4B: "U2D2L2R2",
		0x74: "L1", 0x75: "U1", 0x76: "R1", 0x77: "D1",
		0x78: "L2", 0x79: "U2", 0x7A: "R2", 0x7B: "D2",
		0x7C: "L1R2", 0x7D: "U1D2", 0x7E: "L2R1", 0x7F: "U2D1",
	}
	shift := map[byte]uint{'U': 0, 'R': 2, 'D': 4, 'L': 6}
	for i, s := range spec {
		if s == "" {
			continue
		}
		var v uint8
		for j := 0; j+1 < len(s); j += 2 {
			v |= (s[j+1] - '0') << shift[s[j]]
		}
		t[i] = v
	}
	return t
}()

// drawSpecial paints ch procedurally at column col of a row snapshot whose
// origin is the row's top-left. It returns false for glyphs the font draws.
func (v *termView) drawSpecial(s *gtk.Snapshot, ch rune, col, wcols int, fg tcell.Color) bool {
	x0 := v.colX(col)
	w := v.colX(col+wcols) - x0
	h := v.cellH
	c := rgba(fg, 1)

	switch {
	case ch >= 0x2500 && ch <= 0x257F:
		idx := ch - 0x2500
		if arms := boxArms[idx]; arms != 0 {
			v.drawBoxArms(s, x0, w, arms, c)
			return true
		}
		if idx >= 0x6D && idx <= 0x70 { // ╭ ╮ ╯ ╰
			v.drawArc(s, x0, w, int(idx-0x6D), c)
			return true
		}
		return false

	case ch >= 0x2580 && ch <= 0x259F:
		return v.drawBlock(s, ch, x0, w, h, fg)

	case ch >= 0x2800 && ch <= 0x28FF:
		bits := ch - 0x2800
		if bits == 0 {
			return true
		}
		// Dot order: 1 2 3 7 down the left column, 4 5 6 8 down the right.
		order := [8][2]int{{0, 0}, {0, 1}, {0, 2}, {1, 0}, {1, 1}, {1, 2}, {0, 3}, {1, 3}}
		dot := max(1, math.Round(min(w/4, h/8)))
		for i, pos := range order {
			if bits&(1<<i) == 0 {
				continue
			}
			cx := x0 + w*(0.25+0.5*float64(pos[0]))
			cy := h * (0.125 + 0.25*float64(pos[1]))
			v.fillRect(s, math.Round(cx-dot/2), math.Round(cy-dot/2), dot, dot, c)
		}
		return true
	}
	return false
}

func (v *termView) lineThickness(weight uint8) float64 {
	light := max(1, math.Round(v.cellH/18))
	if weight == 2 {
		return light * 2
	}
	return light
}

func (v *termView) drawBoxArms(s *gtk.Snapshot, x0, w float64, arms uint8, c gdk.RGBA) {
	h := v.cellH
	up, right, down, left := arms&3, arms>>2&3, arms>>4&3, arms>>6&3
	cx := x0 + math.Floor(w/2)
	cy := math.Floor(h / 2)
	vt := v.lineThickness(max(up, down)) // vertical stroke width at the centre
	ht := v.lineThickness(max(left, right))
	if up != 0 {
		t := v.lineThickness(up)
		v.fillRect(s, cx-math.Floor(t/2), 0, t, cy+math.Ceil(ht/2), c)
	}
	if down != 0 {
		t := v.lineThickness(down)
		top := cy - math.Floor(ht/2)
		if up == 0 && left == 0 && right == 0 {
			top = cy
		}
		v.fillRect(s, cx-math.Floor(t/2), top, t, h-top, c)
	}
	if left != 0 {
		t := v.lineThickness(left)
		v.fillRect(s, x0, cy-math.Floor(t/2), cx-x0+math.Ceil(vt/2), t, c)
	}
	if right != 0 {
		t := v.lineThickness(right)
		start := cx - math.Floor(vt/2)
		if up == 0 && down == 0 && left == 0 {
			start = cx
		}
		v.fillRect(s, start, cy-math.Floor(t/2), x0+w-start, t, c)
	}
}

// drawArc draws a rounded corner: 0 ╭, 1 ╮, 2 ╯, 3 ╰.
func (v *termView) drawArc(s *gtk.Snapshot, x0, w float64, which int, c gdk.RGBA) {
	h := v.cellH
	t := v.lineThickness(1)
	cx := x0 + math.Floor(w/2) - math.Floor(t/2)
	cy := math.Floor(h/2) - math.Floor(t/2)
	r := math.Floor(min(w, h) / 2)
	// Arc centre sits r away from the cell centre towards the open side.
	sx, sy := 1.0, 1.0 // direction of the arms: +x right, +y down
	switch which {
	case 1:
		sx = -1
	case 2:
		sx, sy = -1, -1
	case 3:
		sy = -1
	}
	ax, ay := cx+sx*r, cy+sy*r
	// Straight arm towards the edge beyond the arc.
	if sx > 0 {
		v.fillRect(s, ax, cy, x0+w-ax, t, c)
	} else {
		v.fillRect(s, x0, cy, ax-x0+t, t, c)
	}
	if sy > 0 {
		v.fillRect(s, cx, ay, t, h-ay, c)
	} else {
		v.fillRect(s, cx, 0, t, ay+t, c)
	}
	// Quarter circle stamped with thickness-sized squares.
	steps := int(math.Max(8, r*3))
	for i := 0; i <= steps; i++ {
		a := float64(i) / float64(steps) * math.Pi / 2
		px := ax - sx*r*math.Cos(a)
		py := ay - sy*r*math.Sin(a)
		v.fillRect(s, math.Round(px), math.Round(py), t, t, c)
	}
}

// blockQuadrants maps ▖..▟ to bits: upper-left 1, upper-right 2,
// lower-left 4, lower-right 8.
var blockQuadrants = [10]uint8{4, 8, 1, 1 | 4 | 8, 1 | 8, 1 | 2 | 4, 1 | 2 | 8, 2, 2 | 4, 2 | 4 | 8}

func (v *termView) drawBlock(s *gtk.Snapshot, ch rune, x0, w, h float64, fg tcell.Color) bool {
	c := rgba(fg, 1)
	eighthH := func(n float64) float64 { return math.Round(h * n / 8) }
	eighthW := func(n float64) float64 { return math.Round(w * n / 8) }
	switch {
	case ch == 0x2580: // ▀
		v.fillRect(s, x0, 0, w, eighthH(4), c)
	case ch >= 0x2581 && ch <= 0x2588: // ▁..█ lower n/8
		n := float64(ch - 0x2580)
		top := h - eighthH(n)
		v.fillRect(s, x0, top, w, h-top, c)
	case ch >= 0x2589 && ch <= 0x258F: // ▉..▏ left (8-n)/8
		n := float64(0x2590 - ch)
		v.fillRect(s, x0, 0, eighthW(n), h, c)
	case ch == 0x2590: // ▐
		half := eighthW(4)
		v.fillRect(s, x0+half, 0, w-half, h, c)
	case ch >= 0x2591 && ch <= 0x2593: // ░▒▓
		v.fillRect(s, x0, 0, w, h, rgba(fg, float32(ch-0x2590)*0.25))
	case ch == 0x2594: // ▔
		v.fillRect(s, x0, 0, w, eighthH(1), c)
	case ch == 0x2595: // ▕
		e := eighthW(1)
		v.fillRect(s, x0+w-e, 0, e, h, c)
	default: // quadrants ▖..▟
		if ch < 0x2596 {
			return false
		}
		q := blockQuadrants[ch-0x2596]
		hw, hh := eighthW(4), eighthH(4)
		if q&1 != 0 {
			v.fillRect(s, x0, 0, hw, hh, c)
		}
		if q&2 != 0 {
			v.fillRect(s, x0+hw, 0, w-hw, hh, c)
		}
		if q&4 != 0 {
			v.fillRect(s, x0, hh, hw, h-hh, c)
		}
		if q&8 != 0 {
			v.fillRect(s, x0+hw, hh, w-hw, h-hh, c)
		}
	}
	return true
}
