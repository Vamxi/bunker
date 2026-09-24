// gui_panes.go - drawing bunk's split tree in the GUI.
//
// A tab's App holds bunk's BSP tree in cell coordinates, exactly as in the
// TUI: every pane owns a rectangle (its last column reserved for the
// scrollbar) and one-cell separators sit between siblings. Each frame the
// view copies every visible pane under its own Pane.mu (paneFrame), then
// draws panes clipped to their rectangles, the separators (the segments next
// to the focused pane in the accent colour), per-pane scrollbars, bunk's
// status badges, and the search bar.
package main

/*
#cgo pkg-config: gtk4
#include <gtk/gtk.h>

static void bunker_push_rounded_clip(uintptr_t snapshot, float x, float y, float w, float h, float r) {
	GskRoundedRect rr;
	graphene_rect_t bounds = GRAPHENE_RECT_INIT(x, y, w, h);
	gsk_rounded_rect_init_from_rect(&rr, &bounds, r);
	gtk_snapshot_push_rounded_clip(GTK_SNAPSHOT((gpointer)snapshot), &rr);
}
*/
import "C"

import (
	"fmt"
	"io"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"bunker/internal/vt10x"

	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/gdamore/tcell/v2"
)

// paneFrame is one pane's state copied out for a frame.
type paneFrame struct {
	x, y, w, h int // pane rectangle in cells (w includes the scrollbar column)
	cols, rows int // emulator grid size

	grid        [][]vt10x.Glyph
	cursor      vt10x.Cursor
	cursorOn    bool
	cursorColor tcell.Color
	selActive   bool
	selStart    selPos
	selEnd      selPos
	sbCount     int
	sbOff       int
	firstVRow   int
	searchReg   [][]searchSpan // per visible row
	searchCur   [][]searchSpan
	title       string

	valid bool // holds a complete frame
	seen  bool // pane still visible this frame
}

// guiBorder is one separator segment in cells.
type guiBorder struct {
	vertical bool
	at       int // column (vertical) or row (horizontal) of the separator
	from, to int // extent along the separator, [from, to)
	active   bool
}

// collectBorders walks the tree like the TUI's drawBorders and
// paintActiveBorders: every split contributes its separator, and the part
// adjacent to the active pane is marked. Reports whether active is below n.
func collectBorders(n *Node, active *Pane, out *[]guiBorder) bool {
	if n == nil {
		return false
	}
	if n.isLeaf() {
		return n.pane == active
	}
	leftHas := collectBorders(n.left, active, out)
	rightHas := collectBorders(n.right, active, out)
	b := guiBorder{vertical: n.dir == splitVertical}
	if b.vertical {
		b.at, b.from, b.to = n.left.x+n.left.w, n.y, n.y+n.h
	} else {
		b.at, b.from, b.to = n.left.y+n.left.h, n.x, n.x+n.w
	}
	*out = append(*out, b)
	if (leftHas || rightHas) && active != nil {
		lo, hi := active.y, active.y+active.h
		if !b.vertical {
			lo, hi = active.x, active.x+active.w
		}
		if seg := (guiBorder{b.vertical, b.at, max(b.from, lo), min(b.to, hi), true}); seg.from < seg.to {
			*out = append(*out, seg)
		}
	}
	return leftHas || rightHas
}

// capture copies pane p into f. It keeps the previous frame when the pane is
// mid-update (DEC 2026 or a transient clear), and reports whether f is
// drawable.
func (f *paneFrame) capture(p *Pane) bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	f.x, f.y, f.w, f.h = p.x, p.y, p.w, p.h
	if p.term.Mode()&vt10x.ModeSync != 0 || p.transientLineClear {
		return f.valid
	}
	cols, rows := p.term.Size()
	f.cols, f.rows = cols, rows
	f.sbCount, f.sbOff = p.sb.count, p.sbOff
	f.firstVRow = f.sbCount - f.sbOff
	f.cursor = p.term.Cursor()
	f.cursorOn = !p.dead && p.term.CursorVisible() && f.sbOff == 0 &&
		!p.progressCursorHiddenUntil.After(time.Now())
	f.cursorColor = oscCursorColor(p.themeCursorColor)
	if c, ok := p.term.ColorOverride(vt10x.DefaultCursor); ok {
		f.cursorColor = tcell.NewRGBColor(int32(c>>16&255), int32(c>>8&255), int32(c&255))
	}
	f.selActive = p.selActive
	f.selStart, f.selEnd = p.selNorm()
	f.title = p.term.Title()
	p.term.ConsumeDirty()

	if cap(f.grid) < rows {
		f.grid = make([][]vt10x.Glyph, rows)
		f.searchReg = make([][]searchSpan, rows)
		f.searchCur = make([][]searchSpan, rows)
	}
	f.grid = f.grid[:rows]
	f.searchReg, f.searchCur = f.searchReg[:rows], f.searchCur[:rows]
	for row := range rows {
		line := f.grid[row]
		if cap(line) < cols {
			line = make([]vt10x.Glyph, cols)
		}
		line = line[:cols]
		vRow := f.firstVRow + row
		var ring []vt10x.Glyph
		live := true
		termRow := row
		if f.sbOff > 0 {
			switch {
			case vRow < 0:
				live, ring = false, nil
			case vRow < f.sbCount:
				live, ring = false, p.sb.get(vRow)
			default:
				termRow = vRow - f.sbCount
			}
		}
		for col := range cols {
			g := vt10x.Glyph{FG: vt10x.DefaultFG, BG: vt10x.DefaultBG, UL: vt10x.DefaultUL}
			if live {
				g = p.term.RawCell(col, termRow)
			} else if col < len(ring) {
				g = ring[col]
			}
			if fg, ok := p.term.ColorOverride(g.FG); ok {
				g.FG = fg
			}
			if bg, ok := p.term.ColorOverride(g.BG); ok {
				g.BG = bg
			}
			line[col] = g
		}
		f.grid[row] = line
		f.searchReg[row], f.searchCur[row] = nil, nil
		if p.searchHL != nil { // replaced, never mutated, on search updates
			f.searchReg[row] = p.searchHL.regular[vRow]
			f.searchCur[row] = p.searchHL.current[vRow]
		}
	}
	f.valid = true
	return true
}

// ---------------------------------------------------------------------------
// Snapshot
// ---------------------------------------------------------------------------

func (v *termView) snapshot(s *gtk.Snapshot) {
	defer guiRecover("snapshot")
	w, h := float64(v.Width()), float64(v.Height())
	rt := v.theme
	v.fillRect(s, 0, 0, w, h, rgba(rt.bg, 1))

	app := v.app
	app.mu.Lock()
	active, zoomed := app.active, app.zoomedPane
	var panes []*Pane
	var borders []guiBorder
	switch {
	case zoomed != nil:
		panes = []*Pane{zoomed}
	case app.root != nil:
		for _, leaf := range app.root.leaves() {
			panes = append(panes, leaf.pane)
		}
		collectBorders(app.root, active, &borders)
	}
	searching := app.searchMode && app.searchPane != nil
	searchPane, query := app.searchPane, app.searchQuery
	matchIdx, matchCount := app.searchIdx, len(app.searchMatches)
	app.mu.Unlock()
	app.oscBuf.flush(io.Discard) // host passthrough has no GUI target yet

	for _, f := range v.frames {
		f.seen = false
	}
	for _, p := range panes {
		f := v.frames[p]
		if f == nil {
			f = &paneFrame{}
			v.frames[p] = f
		}
		f.seen = true
		if !f.capture(p) {
			continue
		}
		v.drawPane(s, p, f, p == active, p == zoomed)
		if p == active && f.title != v.title {
			v.title = f.title
			if v.onTitle != nil {
				title := sanitizeTitle(f.title)
				coreglib.IdleAdd(func() { v.onTitle(title) })
			}
		}
	}
	for p, f := range v.frames {
		if !f.seen {
			delete(v.frames, p) // pane closed or hidden by zoom
		}
	}
	if len(v.layouts) > guiLayoutCacheMx {
		clear(v.layouts)
	}

	for _, b := range borders {
		c := rt.inactiveBorder
		if b.active {
			c = rt.activeBorder
		}
		if b.vertical {
			x := v.pad + v.colX(b.at) + math.Floor(v.cellW/2)
			v.fillRect(s, x, v.pad+float64(b.from)*v.cellH, 1, float64(b.to-b.from)*v.cellH, rgba(c, 1))
		} else {
			y := v.pad + float64(b.at)*v.cellH + math.Floor(v.cellH/2)
			x0, x1 := v.pad+v.colX(b.from), v.pad+v.colX(b.to)
			v.fillRect(s, x0, y, x1-x0, 1, rgba(c, 1))
		}
	}

	if searching {
		if f := v.frames[searchPane]; f != nil && f.valid {
			v.drawSearchBar(s, f, query, matchIdx, matchCount)
		}
	}
}

// drawPane paints one pane inside its clip rectangle.
func (v *termView) drawPane(s *gtk.Snapshot, p *Pane, f *paneFrame, isActive, isZoomed bool) {
	ox, oy := v.pad+v.colX(f.x), v.pad+float64(f.y)*v.cellH
	cw := v.colX(f.x+f.w) - v.colX(f.x)
	v.rect.Init(float32(ox), float32(oy), float32(cw), float32(float64(f.h)*v.cellH))
	s.PushClip(v.rect)
	for row := 0; row < f.rows && row < len(f.grid); row++ {
		s.Save()
		s.Translate(v.pt(v.pad, oy+float64(row)*v.cellH))
		v.drawRow(s, f.grid[row], f.x)
		s.Restore()
	}
	v.drawSearchHighlights(s, f)
	v.drawSelection(s, f)
	if isActive {
		if v.ime.preedit != "" {
			v.drawPreeditAtCursor(s, f)
		} else {
			v.drawCursor(s, f)
		}
		v.reportCursor(f)
	}
	v.drawScrollbar(s, f)
	s.Pop()
	v.drawBadges(s, p, f, isZoomed)
}

// ---------------------------------------------------------------------------
// Overlays
// ---------------------------------------------------------------------------

func (v *termView) drawSearchHighlights(s *gtk.Snapshot, f *paneFrame) {
	regular := rgba(tcell.NewRGBColor(0xd0, 0xa0, 0x00), 0.35)
	current := rgba(tcell.NewRGBColor(0xff, 0x8c, 0x00), 0.60)
	for row := 0; row < f.rows && row < len(f.searchReg); row++ {
		y := v.pad + float64(f.y+row)*v.cellH
		for _, spans := range [2]struct {
			list []searchSpan
			c    *gdk.RGBA
		}{{f.searchReg[row], &regular}, {f.searchCur[row], &current}} {
			for _, sp := range spans.list {
				x0, x1 := v.pad+v.colX(f.x+sp.col), v.pad+v.colX(f.x+sp.end)
				v.fillRect(s, x0, y, x1-x0, v.cellH, *spans.c)
			}
		}
	}
}

func (v *termView) drawSelection(s *gtk.Snapshot, f *paneFrame) {
	if !f.selActive {
		return
	}
	c := rgba(v.theme.fg, 0.28)
	for row := 0; row < f.rows; row++ {
		vRow := f.firstVRow + row
		if vRow < f.selStart.row || vRow > f.selEnd.row {
			continue
		}
		c0, c1 := 0, f.cols-1
		if vRow == f.selStart.row {
			c0 = f.selStart.col
		}
		if vRow == f.selEnd.row {
			c1 = f.selEnd.col
		}
		if c1 < c0 {
			continue
		}
		x0, x1 := v.pad+v.colX(f.x+c0), v.pad+v.colX(f.x+c1+1)
		v.fillRect(s, x0, v.pad+float64(f.y+row)*v.cellH, x1-x0, v.cellH, c)
	}
}

func (v *termView) drawCursor(s *gtk.Snapshot, f *paneFrame) {
	cur := f.cursor
	if !f.cursorOn || cur.Y < 0 || cur.Y >= f.rows || cur.X < 0 || cur.X >= f.cols {
		return
	}
	cell := f.grid[cur.Y][cur.X]
	wcols := 1
	if cell.Width == 2 {
		wcols = 2
	}
	col := f.x + cur.X
	x := v.pad + v.colX(col)
	w := v.colX(col+wcols) - v.colX(col)
	y := v.pad + float64(f.y+cur.Y)*v.cellH
	color := f.cursorColor
	if color == tcell.ColorDefault || color == tcell.ColorReset {
		color = v.theme.fg
	}
	cc := rgba(color, 1)
	thick := max(1, math.Round(v.cellH/12))

	if !v.focused {
		v.fillRect(s, x, y, w, 1, cc)
		v.fillRect(s, x, y+v.cellH-1, w, 1, cc)
		v.fillRect(s, x, y, 1, v.cellH, cc)
		v.fillRect(s, x+w-1, y, 1, v.cellH, cc)
		return
	}
	switch cur.Shape {
	case 3, 4:
		v.fillRect(s, x, y+v.cellH-thick, w, thick, cc)
	case 5, 6:
		v.fillRect(s, x, y, max(2, thick), v.cellH, cc)
	default:
		v.fillRect(s, x, y, w, v.cellH, cc)
		ch := cell.Char
		if ch == 0 || ch == ' ' || cell.Mode&vtAttrInvisible != 0 || cell.Image() != nil {
			return
		}
		bg := vtColor(cell.BG, v.theme.bg, v.theme)
		_, variant := v.cellStyle(&cell)
		s.Save()
		s.Translate(v.pt(v.pad, y))
		if !v.drawSpecial(s, ch, col, wcols, bg) {
			v.drawText(s, string(ch)+cell.Combining(), variant, v.colX(col), bg)
		}
		s.Restore()
	}
}

// cursorRect is the active cursor's cell in widget coordinates.
func (v *termView) cursorRect(f *paneFrame) (x, y, w, h float64) {
	col := f.x + min(max(f.cursor.X, 0), max(f.cols-1, 0))
	row := f.y + min(max(f.cursor.Y, 0), max(f.rows-1, 0))
	return v.pad + v.colX(col), v.pad + float64(row)*v.cellH, v.colX(col+1) - v.colX(col), v.cellH
}

func (v *termView) reportCursor(f *paneFrame) {
	x, y, w, h := v.cursorRect(f)
	v.imeReportCursor(x, y, w, h)
}

func (v *termView) drawPreeditAtCursor(s *gtk.Snapshot, f *paneFrame) {
	x, y, _, _ := v.cursorRect(f)
	v.drawPreedit(s, x, y)
}

// drawScrollbar shows the scroll position in the pane's reserved column
// while it is scrolled back.
func (v *termView) drawScrollbar(s *gtk.Snapshot, f *paneFrame) {
	if f.sbOff == 0 || f.sbCount == 0 {
		return
	}
	track := float64(f.h) * v.cellH
	total := float64(f.sbCount + f.rows)
	thumbH := max(12, track*float64(f.rows)/total)
	top := v.pad + float64(f.y)*v.cellH + (track-thumbH)*float64(f.sbCount-f.sbOff)/float64(f.sbCount)
	const thumbW = 4.0
	col := f.x + f.w - 1
	x := v.pad + v.colX(col) + (v.colX(col+1)-v.colX(col)-thumbW)/2
	v.fillRect(s, x, top, thumbW, thumbH, rgba(v.theme.scrollThumb, 0.8))
}

// drawBadges draws bunk's status badges as pills in the pane's top-right.
func (v *termView) drawBadges(s *gtk.Snapshot, p *Pane, f *paneFrame, zoomed bool) {
	badges := paneBadges(p, v.theme, zoomed)
	if len(badges) == 0 {
		return
	}
	const gap, padX = 4.0, 7.0
	h := v.cellH - 4
	right := v.pad + v.colX(f.x+f.w-1) - gap
	top := v.pad + float64(f.y)*v.cellH + 3
	left := v.pad + v.colX(f.x)
	for i := len(badges) - 1; i >= 0; i-- {
		b := badges[i]
		text := strings.TrimSpace(b.text)
		ent := v.layout(text, fontBold)
		_, logical := ent.layout.Extents()
		tw := float64(logical.Width()) / 1024
		w := tw + 2*padX
		x := right - w
		if x < left {
			break
		}
		v.pushRoundedClip(s, x, top, w, h, h/2)
		v.fillRect(s, x, top, w, h, rgba(b.bg, 0.92))
		s.Pop()
		s.Save()
		s.Translate(v.pt(x+padX, top+(h-v.cellH)/2))
		c := rgba(b.fg, 1)
		s.Save()
		s.Translate(v.pt(0, math.Round(v.baseline-ent.baseline)))
		s.AppendLayout(ent.layout, &c)
		s.Restore()
		s.Restore()
		right = x - gap
	}
}

// drawSearchBar draws bunk's Ctrl+F bar over the bottom row of the pane.
func (v *termView) drawSearchBar(s *gtk.Snapshot, f *paneFrame, query string, idx, count int) {
	label := " Search: " + query
	switch {
	case query == "":
	case count == 0:
		label += "  (no matches)"
	default:
		label += fmt.Sprintf("  %d/%d", idx+1, count)
	}
	y := v.pad + float64(f.y+f.h-1)*v.cellH
	x0, x1 := v.pad+v.colX(f.x), v.pad+v.colX(f.x+f.w)
	bg := tcell.NewRGBColor(0x1a, 0x1a, 0x44)
	fg := tcell.ColorWhite
	if count == 0 && query != "" {
		fg = tcell.NewRGBColor(0xff, 0x66, 0x66)
	}
	v.fillRect(s, x0, y, x1-x0, v.cellH, rgba(bg, 0.96))
	s.Save()
	s.Translate(v.pt(0, y))
	v.drawText(s, label+" █", fontRegular, x0, fg)
	kb := &v.app.keys
	hint := fmt.Sprintf("↵ next · %s prev · %s exit", compactKeyName(kb.SearchPrev.raw), compactKeyName(kb.SearchExit.raw))
	labelW := float64(utf8.RuneCountInString(label)+3) * v.cellW
	if hw := float64(utf8.RuneCountInString(hint)) * v.cellW; x1-hw-v.cellW > x0+labelW {
		v.drawText(s, hint, fontRegular, x1-hw-v.cellW, tcell.NewRGBColor(0x80, 0x80, 0xb0))
	}
	s.Restore()
}

func (v *termView) pushRoundedClip(s *gtk.Snapshot, x, y, w, h, r float64) {
	C.bunker_push_rounded_clip(C.uintptr_t(coreglib.BaseObject(s).Native()),
		C.float(x), C.float(y), C.float(w), C.float(h), C.float(r))
}
