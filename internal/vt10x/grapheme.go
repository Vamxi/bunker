package vt10x

import (
	"strings"

	"github.com/rivo/uniseg"
)

// Text returns the complete grapheme stored in a lead cell.
func (g Glyph) Text() string {
	if g.Width < 0 {
		return ""
	}
	if g.Char == 0 {
		return " "
	}
	return string(g.Char) + g.Combining()
}

// extendGrapheme is incremental across Write calls. Controls close the current
// cluster, so moving the cursor cannot accidentally extend an unrelated cell.
func (t *State) extendGrapheme(c rune) bool {
	if !t.clusterOpen || t.clusterX >= t.cols || t.clusterY >= t.rows {
		return false
	}
	x, y := t.clusterX, t.clusterY
	g := t.lines[y][x]
	// Fast path: an ASCII character after a plain ASCII cell always starts a
	// new cluster (ASCII has no Extend, ZWJ, SpacingMark, or Prepend code
	// points), so skip building the string and running segmentation. This
	// is nearly every character of ordinary output.
	if c < 0x80 && g.Char < 0x80 && g.ext == nil {
		return false
	}
	text := g.Text() + string(c)
	_, rest, width, _ := uniseg.FirstGraphemeClusterInString(text, -1)
	// uniseg 0.4.7 does not widen emoji keycap sequences.
	if strings.ContainsRune(text, '\u20e3') && (g.Char >= '0' && g.Char <= '9' || g.Char == '#' || g.Char == '*') {
		width = 2
	}
	if rest != "" || (t.mode&ModeGrapheme == 0 && uniseg.StringWidth(string(c)) != 0) {
		return false
	}
	// A pathological combining sequence must not grow per-cell storage forever.
	if len(text) > 1024 {
		return true
	}
	width = min(t.cols, max(int(g.Width), width))
	g.SetCombining(g.Combining() + string(c))
	if x+width > t.cols {
		if t.mode&ModeWrap == 0 {
			return true
		}
		t.eraseCell(x, y)
		t.lines[y][x].Width = -2
		t.lines[y][x].Mode |= attrWrap
		t.markDirty(y)
		t.newline(true)
		x, y = t.cur.X, t.cur.Y
	}
	if width == 2 {
		t.eraseWideAt(x+1, y)
	}
	g.Width = int8(width)
	t.lines[y][x] = g
	if width == 2 {
		t.lines[y][x+1] = Glyph{Width: -1, Mode: g.Mode, FG: g.FG, BG: g.BG, UL: g.UL, Link: g.Link}
	}
	t.markDirty(y)
	t.clusterX, t.clusterY = x, y
	t.cur.X, t.cur.Y = min(x+width, t.cols-1), y
	t.cur.State &^= cursorWrapNext
	if x+width == t.cols {
		t.cur.State |= cursorWrapNext
	}
	return true
}
