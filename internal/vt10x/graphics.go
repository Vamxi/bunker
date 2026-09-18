package vt10x

import (
	"bunk/internal/graphics"
	"context"
	"image"
	"image/color"
	"log/slog"
)

// ImagePlacement identifies one paint operation, even when Kitty placement IDs
// are omitted. Immutable pointers let deletion select whole visible placements.
type ImagePlacement struct{ ID, PlacementID uint32 }

// ImageCell stores two samples rather than a reference to the source bitmap.
// Scrollback therefore retains at most a fixed amount of image data per cell.
type ImageCell struct {
	Top, Bottom color.NRGBA
	Placement   *ImagePlacement
}

// CellPixels reports the virtual raster resolution used for placement.
func (t *State) CellPixels() (int, int) { return t.cellWidth, t.cellHeight }

// GraphicsState returns an isolated snapshot of reusable image data.
func (t *State) GraphicsState() *graphics.Decoder { return t.graphics.Clone() }

func (t *State) handleGraphics(typ rune, body []byte) {
	if t.graphics == nil {
		t.graphics = &graphics.Decoder{}
	}
	rows := t.rows
	if t.graphicsRows > 0 {
		rows = t.graphicsRows
	}
	result, err := t.graphics.Handle(typ, body, graphics.Context{Cols: t.cols, Rows: rows, CellWidth: t.cellWidth, CellHeight: t.cellHeight})
	if result.Reply != "" {
		if t.graphicsReply != nil {
			t.graphicsReply([]byte(result.Reply))
		} else {
			if _, err := t.w.Write([]byte(result.Reply)); err != nil {
				slog.Log(context.Background(), slog.LevelDebug-4, "graphics: write reply", "err", err)
			}
		}
	}
	if err != nil {
		slog.Log(context.Background(), slog.LevelDebug-4, "graphics: rejected image", "err", err)
		return
	}
	if result.Delete != nil {
		t.deleteGraphics(result.Delete)
	}
	if result.Paint != nil {
		t.paintGraphics(result.Paint)
	}
}

func (t *State) paintGraphics(p *graphics.Placement) {
	t.clusterOpen = false
	origin := t.cur
	identity := &ImagePlacement{ID: p.ID, PlacementID: p.PlacementID}
	for row := 0; row < p.Rows; row++ {
		y := origin.Y + row
		if p.Move {
			y = t.cur.Y
		} else if y >= t.rows {
			break
		}
		for col := 0; col < min(p.Cols, t.cols-origin.X); col++ {
			x := origin.X + col
			px, py := col*t.cellWidth, row*t.cellHeight
			top := p.Sample(image.Rect(px, py, px+t.cellWidth, py+t.cellHeight/2))
			bottom := p.Sample(image.Rect(px, py+t.cellHeight/2, px+t.cellWidth, py+t.cellHeight))
			if top.A == 0 && bottom.A == 0 {
				continue
			}
			old := t.lines[y][x]
			t.eraseWideAt(x, y)
			// Composite over previous image samples without retaining a chain
			// of old images. Remaining alpha is blended against the theme later.
			if old.Image != nil {
				top, bottom = over(top, old.Image.Top), over(bottom, old.Image.Bottom)
			}
			t.lines[y][x] = Glyph{Char: '▀', Width: 1, FG: DefaultFG, BG: old.BG, UL: DefaultUL, Image: &ImageCell{Top: top, Bottom: bottom, Placement: identity}}
			t.markDirty(y)
		}
		if p.Move && row+1 < p.Rows {
			t.newline(false)
		}
	}
	if p.Move {
		t.newline(false)
		x := origin.X + p.Cols
		if p.Inline {
			x = origin.X
		}
		t.moveTo(min(x, t.cols-1), t.cur.Y)
	} else {
		t.cur = origin
	}
	t.changed |= ChangedScreen
}

func over(front, back color.NRGBA) color.NRGBA {
	a := uint32(front.A)*255 + uint32(back.A)*(255-uint32(front.A))
	if a == 0 {
		return color.NRGBA{}
	}
	blend := func(f, b uint8) uint8 {
		return uint8((uint32(f)*uint32(front.A)*255 + uint32(b)*uint32(back.A)*(255-uint32(front.A))) / a)
	}
	return color.NRGBA{R: blend(front.R, back.R), G: blend(front.G, back.G), B: blend(front.B, back.B), A: uint8(a / 255)}
}

func (t *State) deleteGraphics(d *graphics.Deletion) {
	selected := make(map[*ImagePlacement]bool)
	for y, row := range t.lines {
		for x, g := range row {
			if g.Image == nil || g.Image.Placement.ID == 0 {
				continue
			}
			p := g.Image.Placement
			match := false
			switch d.Kind {
			case 'a':
				match = true
			case 'i':
				match = p.ID == d.ID && (d.PlacementID == 0 || p.PlacementID == d.PlacementID)
			case 'c':
				match = x == t.cur.X && y == t.cur.Y
			case 'p':
				match = x+1 == d.X && y+1 == d.Y
			case 'x':
				match = x+1 == d.X
			case 'y':
				match = y+1 == d.Y
			case 'r':
				match = p.ID >= uint32(d.X) && p.ID <= uint32(d.Y)
			}
			if match {
				selected[p] = true
			}
		}
	}
	for y, row := range t.lines {
		for x, g := range row {
			if g.Image != nil && selected[g.Image.Placement] {
				t.eraseCell(x, y)
				t.markDirty(y)
				t.changed |= ChangedScreen
			}
		}
	}
}
